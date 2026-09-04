import * as Y from 'yjs'
import {
  Awareness,
  applyAwarenessUpdate,
  encodeAwarenessUpdate,
  removeAwarenessStates,
} from 'y-protocols/awareness'
import {
  TAG_AWARENESS,
  TAG_EPHEMERAL,
  TAG_RESET,
  TAG_SNAPSHOT_REQ,
  TAG_SNAPSHOT_RESP,
  TAG_SYNC_INIT,
  TAG_UPDATE,
  decodeSyncInit,
  encodeFrame,
} from './encode'

// Custom Yjs provider over Tela's `/ws/pages/{id}` wire protocol. The server
// is a dumb relay+persister (backend/internal/api/pages_ws.go) speaking a
// 5-tag binary scheme — NOT y-websocket. This shim wires Y.Doc ↔ that
// protocol and exposes a status stream + an Awareness instance for #65.
//
// Status transitions:
//   'connecting'   → ws not yet open OR open but pre-sync-init.
//   'connected'    → sync-init has been applied; editor can promote to
//                    editable. Status only flips after sync-init so the
//                    editor doesn't open editable on a stale empty doc.
//   'disconnected' → ws closed/erred; reconnect timer scheduled.
//
// Wire handling:
//   tag 0x01 update           — peer↔server raw Yjs update blob.
//   tag 0x02 snapshot-request — reply with tag 0x03 carrying full state.
//   tag 0x04 sync-init        — packs snapshot + tail updates; applied
//                                with origin=this so the doc.on('update')
//                                handler skips the echo.

export type TelaProviderStatus = 'connecting' | 'connected' | 'disconnected'

type StatusListener = (status: TelaProviderStatus) => void
type SyncListener = (info: { hadServerState: boolean }) => void
type EphemeralListener = (payload: Uint8Array) => void

const RECONNECT_BASE_MS = 1000
const RECONNECT_MAX_MS = 30_000

export class TelaProvider {
  readonly doc: Y.Doc
  readonly awareness: Awareness
  readonly url: string

  private ws: WebSocket | null = null
  private status: TelaProviderStatus = 'connecting'
  private statusListeners = new Set<StatusListener>()
  // First-sync hook. Fired exactly once, on the first sync-init received
  // for this provider instance — used by the editor to gate empty-room
  // seeding from the canonical markdown body.
  private syncListeners = new Set<SyncListener>()
  private firstSyncFired = false
  // Ephemeral (tag 0x07) channel listeners — the live-Excalidraw relay. These
  // frames ride the page ws but are deliberately kept OFF the Y.Doc: nothing
  // here is persisted or CRDT-merged. Consumers (DiagramSession) own their own
  // convergence (reconcileElements) and writeback.
  private ephemeralListeners = new Set<EphemeralListener>()
  private reconnectAttempts = 0
  private reconnectTimer: number | null = null
  private destroyed = false
  private pageHideHandler: (() => void) | null = null
  private pageShowHandler: (() => void) | null = null
  // Local awareness state as it was just before `pagehide` removed it, so
  // `pageshow` can put it back verbatim (user identity, editingDiagramId, …)
  // rather than reviving a bare {} and dropping the presence avatar.
  private localStateBeforeHide: Record<string, unknown> | null = null

  // Awareness wire-bridge (#65). The Awareness instance is constructed eagerly
  // so consumers can register on('change') listeners before the ws is open
  // (the local clientID is visible immediately). Inbound tag 0x05 frames are
  // applied with origin=this so our own awareness.on('update') listener
  // filters them and doesn't echo to the server.
  constructor(url: string, doc: Y.Doc) {
    this.url = url
    this.doc = doc
    this.awareness = new Awareness(doc)
    this.awareness.on('update', this.onAwarenessUpdate)
    this.doc.on('update', this.onDocUpdate)
    this.connect()

    // Hard tab close / cross-document nav doesn't run React unmount, so
    // destroy() never fires and peers would see our awareness entry linger
    // ~30s (y-protocols outdatedTimeout). pagehide is the modern equivalent of
    // beforeunload, fires reliably on mobile Safari, and runs *before* BFCache
    // freezes the page so the awareness-removal frame still flushes. If the
    // page later restores from BFCache, the existing ws.onclose / reconnect
    // path re-opens and PageView's onStatus('connected') effect re-seeds the
    // local user state — no extra hook needed on the restore side.
    //
    // The removal MUST be paired with a restore. `pagehide` also fires when the
    // page is merely backgrounded (app switch / tab hide on mobile), and if the
    // ws survives that there is no reconnect and therefore no 'connected'
    // transition to re-seed local presence. y-protocols only re-pings a local
    // state that still exists, so our entry would stay gone for the rest of the
    // session — leaving the awareness map empty, the peer permanently
    // non-leader, and its edits never PATCHed back to pages.body.
    this.pageHideHandler = () => {
      this.localStateBeforeHide = this.awareness.getLocalState()
      this.sendAwarenessRemoval()
    }
    this.pageShowHandler = () => {
      if (this.destroyed) return
      if (this.awareness.getLocalState() != null) return
      // Restore only a state we actually captured. A provider that was never
      // seeded has no presence to bring back, and reviving it as a bare `{}`
      // would put a peer with no editor behind it into the awareness map —
      // exactly the orphan leader election must never pick (session-reaper.ts).
      if (this.localStateBeforeHide == null) return
      this.awareness.setLocalState(this.localStateBeforeHide)
      this.localStateBeforeHide = null
    }
    window.addEventListener('pagehide', this.pageHideHandler)
    window.addEventListener('pageshow', this.pageShowHandler)
  }

  destroy(): void {
    if (this.destroyed) return
    this.destroyed = true
    if (this.pageHideHandler) {
      window.removeEventListener('pagehide', this.pageHideHandler)
      this.pageHideHandler = null
    }
    if (this.pageShowHandler) {
      window.removeEventListener('pageshow', this.pageShowHandler)
      this.pageShowHandler = null
    }
    if (this.reconnectTimer != null) {
      window.clearTimeout(this.reconnectTimer)
      this.reconnectTimer = null
    }
    // Detach EVERY awareness observer before mutating awareness below. The
    // editor's y-prosemirror yCursorPlugin observes this awareness and, on any
    // change, schedules a deferred view.dispatch via eventloop.timeout(0)
    // (y-prosemirror's setMeta/updateMetas). During React teardown that timer
    // fires ~tens of ms later — after Milkdown's async editor.destroy() has
    // removed the editorState ctx — so the dispatch throws "Context
    // editorState not found" on every page switch. updateMetas only guards on
    // the ySync binding being destroyed, which hasn't happened yet, so we have
    // to stop the change from being observed at all. Clearing the lib0
    // Observable's listener map means sendAwarenessRemoval()/awareness.destroy()
    // below notify nobody locally, while the removal frame still reaches peers
    // over the ws. (We own this awareness instance; it's being torn down.)
    const obs = (this.awareness as unknown as { _observers?: Map<string, unknown> })
      ._observers
    if (obs && typeof obs.clear === 'function') obs.clear()
    this.sendAwarenessRemoval()
    this.ephemeralListeners.clear()
    this.doc.off('update', this.onDocUpdate)
    this.awareness.destroy()
    if (this.ws) {
      const ws = this.ws
      this.ws = null
      ws.onopen = null
      ws.onclose = null
      ws.onerror = null
      ws.onmessage = null
      try {
        ws.close()
      } catch {
        // best-effort
      }
    }
  }

  // Drop our local awareness entry and (if the ws is still open) push the
  // removal frame so peers see us leave immediately. removeAwarenessStates
  // bumps the clock with origin=this so the outbound listener filters its
  // own fire-back — we send the encoded payload explicitly to make sure the
  // frame goes out exactly once. Safe to call multiple times: a second call
  // re-removes an already-gone clientID (no-op in y-protocols).
  private sendAwarenessRemoval(): void {
    try {
      removeAwarenessStates(this.awareness, [this.awareness.clientID], this)
    } catch {
      // best-effort
    }
    const ws = this.ws
    if (!ws || ws.readyState !== WebSocket.OPEN) return
    try {
      const payload = encodeAwarenessUpdate(this.awareness, [
        this.awareness.clientID,
      ])
      ws.send(encodeFrame(TAG_AWARENESS, payload))
    } catch {
      // best-effort
    }
  }

  getStatus(): TelaProviderStatus {
    return this.status
  }

  // True once destroy() has run. Callers that apply Yjs updates from async
  // paths (the instant-paint REST fetch) check this before touching the doc:
  // applying an update fires the y-prosemirror ySync observer, which dispatches
  // a transaction into the ProseMirror view — and during navigation teardown
  // the editor's Milkdown ctx may already be disposed ("Context editorState not
  // found"). Bailing here keeps stray updates out of a dying editor.
  isDestroyed(): boolean {
    return this.destroyed
  }

  onStatus(fn: StatusListener): () => void {
    this.statusListeners.add(fn)
    return () => {
      this.statusListeners.delete(fn)
    }
  }

  // Subscribe to the one-shot first-sync signal. If the first sync has
  // already fired by the time you subscribe, the listener is invoked
  // synchronously — keeps the seeder useEffect race-free.
  onFirstSync(fn: SyncListener, payload?: { hadServerState: boolean }): () => void {
    if (this.firstSyncFired) {
      fn(payload ?? { hadServerState: false })
      return () => {}
    }
    this.syncListeners.add(fn)
    return () => {
      this.syncListeners.delete(fn)
    }
  }

  // Broadcast a raw payload on the ephemeral diagram channel (tag 0x07). The
  // server fans it out to every other peer in the page room and never persists
  // it. No-op if the ws isn't open — the channel is best-effort by design (a
  // dropped mid-draw delta is corrected by the next frame / final reconcile).
  sendEphemeral(payload: Uint8Array): void {
    const ws = this.ws
    if (!ws || ws.readyState !== WebSocket.OPEN) return
    try {
      ws.send(encodeFrame(TAG_EPHEMERAL, payload))
    } catch {
      // Connection likely dying; onclose handles reconnect.
    }
  }

  // Subscribe to inbound ephemeral frames. Returns an unsubscribe fn.
  onEphemeral(fn: EphemeralListener): () => void {
    this.ephemeralListeners.add(fn)
    return () => {
      this.ephemeralListeners.delete(fn)
    }
  }

  private setStatus(next: TelaProviderStatus): void {
    if (this.status === next) return
    this.status = next
    for (const fn of this.statusListeners) fn(next)
  }

  private connect(): void {
    if (this.destroyed) return
    this.setStatus('connecting')
    let ws: WebSocket
    try {
      ws = new WebSocket(this.url)
    } catch {
      this.scheduleReconnect()
      return
    }
    ws.binaryType = 'arraybuffer'
    this.ws = ws

    ws.onopen = () => {
      if (this.destroyed) {
        try {
          ws.close()
        } catch {
          // best-effort
        }
        return
      }
      this.reconnectAttempts = 0
      // Status stays 'connecting' until sync-init lands.
    }
    ws.onmessage = (ev) => this.onMessage(ev)
    ws.onerror = () => {
      // onclose fires after onerror; do reconnect bookkeeping there only.
    }
    ws.onclose = () => {
      if (this.ws === ws) this.ws = null
      this.setStatus('disconnected')
      this.scheduleReconnect()
    }
  }

  private scheduleReconnect(): void {
    if (this.destroyed) return
    if (this.reconnectTimer != null) return
    const delay = Math.min(
      RECONNECT_BASE_MS * 2 ** this.reconnectAttempts,
      RECONNECT_MAX_MS,
    )
    this.reconnectAttempts += 1
    this.reconnectTimer = window.setTimeout(() => {
      this.reconnectTimer = null
      this.connect()
    }, delay)
  }

  // Y.Doc → wire. Skip the echo on inbound 0x01 frames: applyUpdate from the
  // ws-receive path sets origin=this, and we filter that out so we don't
  // bounce the same bytes back to the server. Local typing fires
  // origin=undefined (or some non-this origin) and goes out.
  private onDocUpdate = (update: Uint8Array, origin: unknown): void => {
    if (origin === this) return
    const ws = this.ws
    if (!ws || ws.readyState !== WebSocket.OPEN) return
    try {
      ws.send(encodeFrame(TAG_UPDATE, update))
    } catch {
      // Connection likely dying; onclose will handle reconnect.
    }
  }

  // Awareness → wire. Mirror of onDocUpdate: skip echoes (inbound 0x05
  // frames apply with origin=this), batch the changed clientIDs into a
  // single y-protocols/awareness blob, send as tag 0x05.
  private onAwarenessUpdate = (
    changes: { added: number[]; updated: number[]; removed: number[] },
    origin: unknown,
  ): void => {
    if (origin === this) return
    const ws = this.ws
    if (!ws || ws.readyState !== WebSocket.OPEN) return
    const clients = [...changes.added, ...changes.updated, ...changes.removed]
    if (clients.length === 0) return
    try {
      const payload = encodeAwarenessUpdate(this.awareness, clients)
      ws.send(encodeFrame(TAG_AWARENESS, payload))
    } catch {
      // Connection likely dying; onclose will handle reconnect.
    }
  }

  private onMessage(ev: MessageEvent): void {
    // A frame already queued on the event loop can still reach us after
    // destroy() nulled the handler; never apply Yjs updates into a torn-down
    // doc (see isDestroyed for why that throws in the editor).
    if (this.destroyed) return
    if (!(ev.data instanceof ArrayBuffer)) return
    const frame = new Uint8Array(ev.data)
    if (frame.byteLength < 1) return
    const tag = frame[0]
    const payload = frame.subarray(1)
    switch (tag) {
      case TAG_RESET:
        // The body was rewritten out-of-band (an agent MCP write) and the
        // server dropped the Yjs overlay; this Y.Doc is now stale. Reload to
        // re-seed from pages.body — DB-wins, per the agent-backend sync design.
        if (!this.destroyed) window.location.reload()
        return
      case TAG_UPDATE:
        Y.applyUpdate(this.doc, payload, this)
        return
      case TAG_SNAPSHOT_REQ: {
        const ws = this.ws
        if (!ws || ws.readyState !== WebSocket.OPEN) return
        const state = Y.encodeStateAsUpdate(this.doc)
        try {
          ws.send(encodeFrame(TAG_SNAPSHOT_RESP, state))
        } catch {
          // Drop — server will re-request on the next threshold.
        }
        return
      }
      case TAG_SYNC_INIT: {
        let unpacked
        try {
          unpacked = decodeSyncInit(payload)
        } catch {
          return
        }
        const hadServerState =
          (unpacked.snapshot != null && unpacked.snapshot.byteLength > 0) ||
          unpacked.updates.length > 0
        if (unpacked.snapshot && unpacked.snapshot.byteLength > 0) {
          Y.applyUpdate(this.doc, unpacked.snapshot, this)
        }
        for (const upd of unpacked.updates) {
          Y.applyUpdate(this.doc, upd, this)
        }
        // First sync-init signals the editor it's safe to promote to
        // editable + potentially seed-from-markdown when the room is fresh.
        if (!this.firstSyncFired) {
          this.firstSyncFired = true
          for (const fn of this.syncListeners) fn({ hadServerState })
          this.syncListeners.clear()
        }
        this.setStatus('connected')
        return
      }
      case TAG_AWARENESS:
        // Apply with origin=this so our awareness 'update' listener filters
        // the change and doesn't bounce it back to the server.
        applyAwarenessUpdate(this.awareness, payload, this)
        return
      case TAG_EPHEMERAL:
        // Live-Excalidraw relay: hand the opaque payload to subscribers. Copy
        // out of the ws receive buffer (payload is a subarray view) so async
        // consumers can't read torn bytes. Never touches the Y.Doc.
        for (const fn of this.ephemeralListeners) fn(payload.slice())
        return
      default:
        // Unknown / future tag — ignore.
        return
    }
  }
}
