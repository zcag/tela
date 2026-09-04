import { useEffect, useRef, useState, type MutableRefObject } from 'react'
import * as Y from 'yjs'
import { TelaProvider, type TelaProviderStatus } from './tela-provider'
import { useLeaderElection } from './use-leader-election'
import { useDiagramEditors } from './use-awareness'
import { claimSession, trackUnclaimed } from './session-reaper'

// Owns the live-collab session for one editor: the Y.Doc + provider, the
// connection status, leader election, and per-diagram presence. Extracted from
// milkdown-editor.tsx so the collab machinery is one legible seam instead of
// being smeared across the component as refs + scattered effects — and so the
// provider is INJECTABLE (`createProvider`), which lets tests drive the collab
// path with an offline fake instead of a real /ws WebSocket.

export interface CollabSession {
  doc: Y.Doc
  provider: TelaProvider
}

// Builds the doc + provider for a page. The default talks to the real /ws
// endpoint; tests pass a fake that reports 'connected' offline.
export type CollabProviderFactory = (pageId: number) => CollabSession

export const defaultCollabProviderFactory: CollabProviderFactory = (pageId) => {
  const doc = new Y.Doc()
  const proto = window.location.protocol === 'https:' ? 'wss:' : 'ws:'
  const url = `${proto}//${window.location.host}/ws/pages/${pageId}`
  return { doc, provider: new TelaProvider(url, doc) }
}

export interface UseCollabSessionOptions {
  // Fires once with the provider after init so the page header (presence
  // avatars) and awareness seeding can share this exact instance.
  onReady?: (provider: TelaProvider) => void
  // Dependency-injection seam (tests). Defaults to the real /ws provider.
  createProvider?: CollabProviderFactory
}

export interface CollabSessionState {
  // null in non-collab mode (collabPageId == null) — solo/draft/viewer.
  session: CollabSession | null
  status: TelaProviderStatus
  // Whether we've ever reached 'connected'. The reconnect banner is only for a
  // *dropped* connection, not the normal initial connect on page open.
  hasConnected: boolean
  isLeader: boolean
  // Live mirror of isLeader for ProseMirror listeners wired once at editor build.
  isLeaderRef: MutableRefObject<boolean>
  diagramEditors: ReturnType<typeof useDiagramEditors>
}

export function useCollabSession(
  collabPageId: number | null | undefined,
  opts: UseCollabSessionOptions = {},
): CollabSessionState {
  const { onReady, createProvider = defaultCollabProviderFactory } = opts

  // Lazy-init in a stable ref so the editor factory captures it once. Y.Doc
  // lifecycle pitfall: a re-render with a non-stable doc would trash collab
  // state on every parent update.
  const ref = useRef<CollabSession | null>(null)
  if (collabPageId != null && ref.current == null) {
    const created = createProvider(collabPageId)
    // Building a provider here opens a ws from a render React may still throw
    // away, and only a mounted component ever runs the teardown effect below.
    // Park it with the reaper until the claim effect proves this render
    // committed; see session-reaper.ts for what the orphans do to saves.
    trackUnclaimed(created)
    ref.current = created
  }
  const session = ref.current

  // Claim the render-built session, and only NOW make this peer visible to the
  // room. Seeding awareness at construction time is what let an orphan into the
  // awareness map at all — and a bare `{}` entry with a low clientID wins
  // leader election, so the room's designated saver becomes a provider with no
  // editor behind it and pages.body stops being written. An unclaimed session
  // seeds nothing and therefore can never be elected.
  useEffect(() => {
    if (!session) return
    // A false claim is either StrictMode re-running this effect (provider still
    // alive — seed again, setLocalState is idempotent) or a commit so late the
    // reaper already fired, in which case there is nothing left to seed.
    if (!claimSession(session) && session.provider.isDestroyed()) return
    // Must precede the onReady effect: setLocalStateField MERGES into the
    // existing state and is a silent no-op while it is null, so PageView's
    // `user` seeding would be dropped without this `{}` landing first.
    session.provider.awareness.setLocalState({})
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  const [status, setStatus] = useState<TelaProviderStatus>(
    () => session?.provider.getStatus() ?? 'connected',
  )
  const [hasConnected, setHasConnected] = useState(false)
  useEffect(() => {
    if (!session) return
    // Re-read on attach so we don't miss a transition between useState init and
    // effect mount (race-prone on fast localhost where ws.onopen + sync-init
    // can land before paint).
    const initial = session.provider.getStatus()
    if (initial === 'connected') setHasConnected(true)
    setStatus(initial)
    return session.provider.onStatus((s) => {
      if (s === 'connected') setHasConnected(true)
      setStatus(s)
    })
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  // Hand the provider up once (presence avatars + awareness seeding share it).
  useEffect(() => {
    if (!session || !onReady) return
    onReady(session.provider)
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  const isLeader = useLeaderElection(session?.provider.awareness ?? null)
  const isLeaderRef = useRef(isLeader)
  isLeaderRef.current = isLeader

  const diagramEditors = useDiagramEditors(session?.provider.awareness ?? null)

  // Teardown: close ws + cancel reconnect on unmount, or rooms + reconnect
  // timers leak across page switches. Deliberately do NOT destroy the Y.Doc:
  // @milkdown/react's editor.destroy() is async and tears down across more than
  // a tick, including y-prosemirror's unobserve of the XmlFragment. Destroying
  // the doc while that observer is still attached dispatches into a half-removed
  // ctx → "Context editorState not found" on every page switch. provider.destroy()
  // already stops the ws, reconnect timer, awareness and outbound updates; once
  // the ref is nulled the doc + provider are unreferenced and get GC'd.
  useEffect(() => {
    return () => {
      const s = ref.current
      if (!s) return
      ref.current = null
      s.provider.destroy()
    }
  }, [])

  return { session, status, hasConnected, isLeader, isLeaderRef, diagramEditors }
}
