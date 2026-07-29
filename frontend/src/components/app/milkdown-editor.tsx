/* eslint-disable react-hooks/refs --
   This file deliberately treats refs as render-readable stable state:
   `callbacks`/`editableRef` are the latest-value sync pattern read from PM
   callbacks wired once at editor build (`isLeaderRef` likewise, surfaced by
   useCollabSession). Restructuring to satisfy the rule would change init/
   teardown timing in the most regression-prone file in the app for no
   behavioral gain. */
import { Suspense, lazy, memo, useEffect, useRef, useState } from 'react'
import {
  Editor,
  defaultValueCtx,
  editorViewCtx,
  editorViewOptionsCtx,
  parserCtx,
  prosePluginsCtx,
  rootCtx,
  serializerCtx,
} from '@milkdown/kit/core'
import { commonmark, imageAttr } from '@milkdown/kit/preset/commonmark'
import { gfm } from '@milkdown/kit/preset/gfm'
import { remarkSafeLinks } from '../../lib/markdown/remark-safe-links'
// gapcursor + dropcursor. Gapcursor lets the caret sit in the gaps around block
// atoms (Excalidraw, images, tables, code) where a text caret can't go — so you
// can click above/below a diagram and start typing. Styling lives in editor.css
// (.ProseMirror-gapcursor); the plugin ships none.
import { cursor } from '@milkdown/kit/plugin/cursor'
import { block } from '@milkdown/kit/plugin/block'
import { history } from '@milkdown/kit/plugin/history'
import { clipboard } from '@milkdown/kit/plugin/clipboard'
import { listener, listenerCtx } from '@milkdown/kit/plugin/listener'
import { Milkdown, MilkdownProvider, useEditor } from '@milkdown/react'
import {
  ProsemirrorAdapterProvider,
  usePluginViewFactory,
} from '@prosemirror-adapter/react'
import { prism, prismConfig } from '@milkdown/plugin-prism'
import { Plugin, Selection } from '@milkdown/kit/prose/state'
import { keymap } from '@milkdown/kit/prose/keymap'
import type { EditorView } from '@milkdown/kit/prose/view'
import * as Y from 'yjs'
import {
  prosemirrorToYXmlFragment,
  redo,
  undo,
  yCursorPlugin,
  ySyncPlugin,
  yUndoPlugin,
} from 'y-prosemirror'
import { createPreserveCellSelectionPlugin } from '../../lib/collab/preserve-cell-selection'
import { cn } from '../../lib/utils'
import { setErrorReportPageId } from '../../lib/client-errors'
import { configureRefractor } from '../../lib/milkdown/refractor-config'
import { emitOpenNewPage } from '../../lib/newPageEvent'
import type { TelaProvider } from '../../lib/collab/tela-provider'
import { DiagramSession } from '../../lib/collab/diagram-session'
import { decodeSyncInit } from '../../lib/collab/encode'
import { cursorBuilder, selectionBuilder } from '../../lib/collab/cursor-builder'
import {
  useCollabSession,
  type CollabProviderFactory,
} from '../../lib/collab/use-collab-session'
import { slashPlugin, SlashView } from './milkdown-slash'
import { bubblePlugin, BubbleToolbarView } from './milkdown-bubble-toolbar'
import { BlockHandleView } from './milkdown-block-handle'
import { taskCheckboxPlugin } from './milkdown-task-list'
import {
  mathBlockInputRule,
  mathBlockSchema,
  mathInlineInputRule,
  mathInlineSchema,
  mathNodeViews,
  mathRemarkPlugin,
} from './milkdown-math'
import {
  highlightInputRule,
  highlightRemarkPlugin,
  highlightSchema,
  toggleHighlightCommand,
} from './milkdown-highlight'
import { mermaidPlugin } from './milkdown-mermaid'
import { chartPlugin } from './milkdown-chart'
import { typographyInputRules } from './milkdown-typography'
import { listIndentKeymap } from './milkdown-list-indent'
import { directiveRemarkPlugin, unknownDirectiveFallbackPlugin } from './milkdown-directives'
import { tabSchema, tabsNodeView, tabsSchema } from './milkdown-tabs'
import {
  kanbanColumnSchema,
  kanbanNodeViews,
  kanbanSchema,
} from './milkdown-kanban'
import {
  statGridNodeViews,
  statGridSchema,
  statTileSchema,
} from './milkdown-stat-grid'
import { timelineSchema } from './milkdown-timeline'
import { calendarNodeView, calendarSchema } from './milkdown-calendar'
import { pollSchema } from './milkdown-poll'
import { tableEnhancePlugin } from './milkdown-table'
import { wikilinkPlugin, WikilinkView } from './milkdown-wikilink'
import {
  emojiInputRule,
  emojiPickerOpenCtx,
  insertEmojiAt,
  type EmojiPickerRequest,
} from './milkdown-emoji'
import {
  emojiAutocompletePlugin,
  EmojiAutocompleteView,
} from './milkdown-emoji-autocomplete'
import { EmojiPicker } from './emoji-picker'
import {
  calloutInputRule,
  calloutSchema,
  calloutsRemarkPlugin,
} from './milkdown-callouts'
import { codeBlockNodeView } from './milkdown-codeblock'
import { pullquoteNodeView, pullquoteSchema } from './milkdown-pullquote'
import { embedSchema } from './milkdown-embed'
import {
  collapsiblesRemarkPlugin,
  detailsNodeView,
  detailsReadOnlyCtx,
  detailsSchema,
  detailsSummarySchema,
} from './milkdown-collapsibles'
import {
  excalidrawClickPlugin,
  excalidrawOpenCtx,
  excalidrawRemarkPlugin,
  excalidrawSchema,
  pageIdCtx,
  type ExcalidrawOpenHandler,
  type ExcalidrawOpenRequest,
} from './milkdown-excalidraw'
import {
  EXCALIDRAW_PRESENCE_META,
  excalidrawPresenceCtx,
  excalidrawPresencePlugin,
} from './milkdown-excalidraw-presence'
import {
  modifierClickEnabledCtx,
  modifierClickPlugin,
  wikilinkNavigateCtx,
  type WikilinkNavigateHandler,
} from './milkdown-modifier-click'
import { createAttachmentDropPlugin } from './milkdown-image-upload'
import { uploadPlaceholderPlugin } from './milkdown-upload-placeholder'
import { placeholderPlugin } from './milkdown-placeholder'
import { linkPopoverPlugin } from './milkdown-link-popover'
import { fileSchema } from './milkdown-file'
import { createUrlUnfurlPlugin } from './milkdown-url-unfurl'
import { createTableEdgeSelectPlugin } from './milkdown-table-select'
import { createPlainPastePlugin } from './milkdown-plain-paste'
import { createTableFixPlugin } from './milkdown-table-fix'
import { useQueryClient } from '@tanstack/react-query'
import { pageKeys } from '../../lib/queries/pages'
import { navigateToPage } from '../../lib/pageHitItem'
import type { PageListItem } from '../../lib/types'

// M13.3b — ExcalidrawEditSheet lazy-imports `@excalidraw/excalidraw` (~290 KB
// gz total) on first open. Owned by the editor (not PageView) so the lazy
// declaration, useState, and Suspense JSX all live in this milkdown-editor
// chunk — already lazy-loaded itself, so the cost never lands in main.
const ExcalidrawEditSheet = lazy(() =>
  import('./excalidraw-edit-sheet').then((m) => ({
    default: m.ExcalidrawEditSheet,
  })),
)
import {
  WIKILINK_ALIVE_IDS_META,
  wikilinkAliveIdsCtx,
  wikilinkDecorationPlugin,
  wikilinkModeCtx,
  type WikilinkDecorationMode,
} from './milkdown-wikilink-decoration'
import {
  WIKILINK_RESOLVE_META,
  wikilinkBracketRemarkPlugin,
  wikilinkBracketSchema,
  wikilinkResolveCtx,
  wikilinkResolvePlugin,
} from './milkdown-wikilink-bracket'
import {
  COMMENT_ANCHOR_META,
  commentAnchorCallbacksCtx,
  commentShowResolvedCtx,
  commentThreadsCtx,
  createCommentAnchorPlugin,
  type CommentAnchorCallbacks,
} from '../../lib/comments/anchor-decoration'
import type { CommentThread } from '../../lib/comments/use-comments'

export interface MilkdownEditorProps {
  defaultValue: string
  onChange: (markdown: string) => void
  onBlur?: () => void
  autoFocus?: boolean
  ariaLabel?: string
  className?: string
  // Live snapshot of page ids that still exist. `null` means "don't know yet"
  // (e.g. the `/api/pages/all` query hasn't returned) — the decoration plugin
  // treats every wikilink as alive in that state to avoid a redline flash on
  // first paint. M5.2d.
  aliveWikilinkIds?: Set<number> | null
  // Slug→id map for resolving `[[Name]]` bracket wikilinks within the page's
  // space. `null` means "not loaded yet" (bracket links stay neutral, not
  // redlined). See milkdown-wikilink-bracket.ts.
  wikilinkResolveIndex?: Map<string, number> | null
  // M7.2 LiveCollab. When set, the editor opens a Yjs WebSocket session
  // against /ws/pages/{collabPageId} via our custom 5-tag wire protocol
  // (NOT y-websocket). y-prosemirror's sync + undo plugins are wired in;
  // ws drop flips the editor to read-only with a "Reconnecting…" banner.
  //
  // `collabPageId` is intentionally separate from `defaultValue`: the
  // PageView keys the entire PageEditor subtree on page id, so a page
  // switch unmounts/remounts this component — Y.Doc + provider are owned
  // by this instance and torn down on unmount. Passing the id explicitly
  // (rather than parsing from URL) keeps the component testable.
  //
  // When null/undefined, the editor renders in legacy non-collab mode
  // (used for viewer fallback in PageView).
  collabPageId?: number | null
  // When true, the editor is permanently read-only — used for viewer-role
  // fallback. Takes precedence over the collab connection state.
  readOnly?: boolean
  // M7.4 — fires once with the TelaProvider instance after collab init so the
  // PageView header (PresenceAvatars) and PageView's awareness local-state
  // seeding can hook into the same provider. Not invoked in non-collab mode.
  onCollabReady?: (provider: TelaProvider) => void
  // Dependency-injection seam for the collab provider (tests). Defaults to the
  // real /ws WebSocket provider; tests pass an offline fake so the collab path
  // is editable without a backend. See useCollabSession.
  collabProviderFactory?: CollabProviderFactory
  // M8.3 — selection bridge. `onViewReady` fires once with the EditorView on
  // mount (and once with null on unmount) so CommentsPanel can snapshot
  // anchors at submit time. `onSelectionChange` fires whenever the PM
  // selection transitions and exposes the current selection's plain-text
  // projection (empty when the selection is collapsed). Both flow through
  // a single tiny PM plugin so we don't double-up subscription paths.
  onViewReady?: (view: EditorView | null) => void
  onSelectionChange?: (state: { isEmpty: boolean; text: string }) => void
  // M8.4 — anchor decoration. When `commentThreads` is provided (incl. an
  // empty array meaning "no threads"), the editor mounts the comment-anchor
  // decoration plugin and underlines each thread's resolved passage. Null /
  // undefined disables the plugin entirely (viewer-role fallback).
  //
  // `onAnchorClick` fires when the user clicks any underlined passage —
  // PageView uses it to open the Sheet + scroll the matching panel row into
  // view. `onAnchorsResolved` fires after every decoration rebuild with the
  // map of {threadId → resolved range | null}; PageView reads it to render
  // "Orphaned" tags on threads whose passage no longer matches.
  commentThreads?: CommentThread[] | null
  onAnchorClick?: (threadId: number) => void
  onAnchorsResolved?: (
    resolutions: Map<number, { from: number; to: number } | null>,
  ) => void
  // M8.5 — when true, resolved threads still paint a muted underline
  // (`.tela-comment-anchor.is-resolved`). When false (default), the
  // underline disappears for resolved threads. Mirrors the panel's
  // "Show resolved" filter so the body and panel stay in sync.
  showResolvedAnchors?: boolean
  // M15.1 — wikilink decoration mode. 'edit' (default) shows out-of-scope
  // wikilinks as broken; 'share' shows them as plain text so we don't leak
  // the existence of pages outside the share scope.
  wikilinkMode?: WikilinkDecorationMode
  // M13.3a — page id carried into the excalidraw atom's toDOM so it can build
  // the `/api/diagrams/{pageId}/{sceneHash}.png` URL. 0 = unset (placeholder
  // URL would 404); PageView and ShareReader pass `page.id` so the URL is
  // always real. Stable across the editor's lifetime — PageView keys the
  // editor by page id, so a page switch unmounts/remounts. Also used by the
  // M13.3b Edit Sheet for PUT /api/pages/{pageId}/diagrams.
  pageId?: number
}

// Reconnecting banner copy.
const RECONNECTING_LABEL = 'Reconnecting…'

function MilkdownEditorInner({
  defaultValue,
  onChange,
  onBlur,
  autoFocus,
  ariaLabel,
  className,
  aliveWikilinkIds,
  wikilinkResolveIndex,
  collabPageId,
  readOnly,
  onCollabReady,
  collabProviderFactory,
  onViewReady,
  onSelectionChange,
  commentThreads,
  onAnchorClick,
  onAnchorsResolved,
  showResolvedAnchors = false,
  wikilinkMode = 'edit',
  pageId = 0,
}: MilkdownEditorProps) {
  const pluginViewFactory = usePluginViewFactory()

  // M8.4 — whether to mount the anchor-decoration plugin. `commentThreads`
  // being `undefined` (caller never passed the prop, e.g. viewer fallback)
  // disables; passing an empty array enables (treated as "no threads on
  // this page yet" — the plugin slot still wires so the first POST'd
  // comment can paint without an editor remount). Caller must pass an
  // array (even if `[]`) for non-viewer pages to ensure the plugin mounts
  // at build time; passing `undefined` until the TanStack query returns
  // would miss the build window since the editor builder closure runs
  // once.
  const commentsEnabled = commentThreads !== undefined

  // Keep callbacks fresh without recreating the editor on every render.
  const callbacks = useRef({
    onChange,
    onBlur,
    onViewReady,
    onSelectionChange,
    onAnchorClick,
    onAnchorsResolved,
  })
  callbacks.current = {
    onChange,
    onBlur,
    onViewReady,
    onSelectionChange,
    onAnchorClick,
    onAnchorsResolved,
  }

  // M13.3b — Excalidraw Edit Sheet state. `null` = closed. The click /
  // slash-insert plugin's `openTrampoline` below pushes the open request
  // here; the Sheet renders alongside <Milkdown /> and calls `onSave` on
  // successful save. Owned by the editor (not PageView) so the cost of the
  // lazy import + state + Suspense JSX never lands in the main bundle.
  // Null in share-mode and read-only mode: the editor's click plugin
  // short-circuits on a null `excalidrawOpenCtx`, so the setter is never
  // invoked in those modes.
  const [excalidrawSheet, setExcalidrawSheet] =
    useState<ExcalidrawOpenRequest | null>(null)
  // SPIKE — live multiplayer Excalidraw session for the open diagram. State is
  // declared here with the other sheet state; the effect that builds it lives
  // below the collab provider setup (it reads the collab session).
  const [diagramSession, setDiagramSession] = useState<DiagramSession | null>(
    null,
  )

  // `/`-triggered emoji picker. The slash "Emoji" item fires the
  // `emojiPickerOpenCtx` handler (set below to this setter in editable modes),
  // which captures the caret coords + position; the picker inserts back there.
  const [emojiPicker, setEmojiPicker] = useState<EmojiPickerRequest | null>(
    null,
  )
  // Image-upload paste/drop. Editable, non-share, page-known (the upload route
  // is page-scoped).
  const imageUploadEnabled =
    wikilinkMode !== 'share' && !readOnly && pageId > 0

  // M13.5 (#116) — modifier-click wikilink follow. Resolve pageId → space_id
  // via the cross-space allFlat cache (populated by useAllPages, refreshed
  // through the page-mutation bus). Cache miss = broken / out-of-scope link;
  // bail silently so the user can plain-click to invoke the new-page dialog
  // through the existing broken-wikilink path.
  const queryClient = useQueryClient()
  const wikilinkNavigate: WikilinkNavigateHandler = (pageId) => {
    const pages = queryClient.getQueryData<PageListItem[]>(pageKeys.allFlat())
    const page = pages?.find((p) => p.id === pageId)
    if (!page) return
    navigateToPage(page.space_id, pageId)
  }

  // M8.3 — single-source the selection projection so the PM plugin and any
  // future consumer read selection text via the same '\n' block separator
  // contract that lib/comments/anchor.ts uses. Diverging here would silently
  // misalign captureAnchor/resolveAnchor offsets.
  function emitSelection(view: EditorView) {
    const cb = callbacks.current.onSelectionChange
    if (!cb) return
    const { from, to, empty } = view.state.selection
    const text = empty ? '' : view.state.doc.textBetween(from, to, '\n')
    cb({ isEmpty: empty, text })
  }

  // M7.2 — live-collab session: owns the Y.Doc + provider, connection status,
  // leader election, and per-diagram presence (see useCollabSession). `session`
  // is null in non-collab mode. The provider is injectable for tests.
  const {
    session,
    status: collabStatus,
    hasConnected,
    isLeaderRef,
    diagramEditors,
  } = useCollabSession(collabPageId, {
    onReady: onCollabReady,
    createProvider: collabProviderFactory,
  })

  // Tag global error reports with the page being edited, so a crash here lands
  // in the events feed pointing at the right page (collab is the hotspot we're
  // chasing). Cleared on unmount / page change.
  useEffect(() => {
    if (collabPageId == null) return
    setErrorReportPageId(collabPageId)
    return () => setErrorReportPageId(undefined)
  }, [collabPageId])

  // SPIKE — live multiplayer Excalidraw. While a diagram is open in collab
  // mode, spin up a DiagramSession on the shared provider's ephemeral (tag
  // 0x07) channel so peers editing the SAME diagram see each other's strokes.
  // Keyed by sceneHash (peers opening the same saved diagram share a room);
  // an unsaved diagram falls back to a 'new' room. Torn down on close. Null
  // when there's no collab provider (solo/share/view) → the sheet behaves
  // exactly as before.
  useEffect(() => {
    if (!excalidrawSheet || !session) return
    const provider = session.provider
    const localUser = provider.awareness.getLocalState()?.user as
      | { id: number; username: string; colorIdx: number }
      | undefined
    const self = {
      id: localUser?.id ?? provider.awareness.clientID,
      username: localUser?.username ?? 'You',
      colorIdx: localUser?.colorIdx ?? 0,
      // Per-connection id → distinct cursor per tab.
      clientId: provider.awareness.clientID,
    }
    // Prefer the stable diagram id (survives saves/checkpoints); fall back to
    // sceneHash for legacy diagrams (deterministic across peers until one
    // saves and stamps an id), then 'new' for a never-saved legacy atom.
    const key = excalidrawSheet.diagramId || excalidrawSheet.sceneHash || 'new'
    const sess = new DiagramSession(provider, key, self)
    setDiagramSession(sess)
    // Presence: advertise (page-wide, via awareness) that we're editing THIS
    // diagram, so other editors viewing the page get an "editing" badge on its
    // block. Keyed by the stable diagramId (skip legacy '' ids — they'd collide
    // across diagrams). setLocalStateField merges, leaving `user` intact.
    if (excalidrawSheet.diagramId) {
      provider.awareness.setLocalStateField('editingDiagramId', excalidrawSheet.diagramId)
    }
    return () => {
      sess.destroy()
      setDiagramSession(null)
      provider.awareness.setLocalStateField('editingDiagramId', null)
    }
  }, [excalidrawSheet])

  // Editable predicate. PM evaluates this on every updateState. We use a
  // ref-backed read so the predicate's identity is stable but its result
  // tracks live state. A no-op tx is dispatched below on
  // status/readOnly changes to force PM to re-read.
  const editableRef = useRef(true)
  editableRef.current =
    !readOnly && (session == null || collabStatus === 'connected')

  const { loading, get } = useEditor((root) =>
    Editor.make()
      .config((ctx) => {
        ctx.set(rootCtx, root)
        ctx.set(defaultValueCtx, defaultValue)
        ctx
          .get(listenerCtx)
          .markdownUpdated((_, md, prev) => {
            if (md === prev) return
            // M7.3: in collab mode, only the elected leader (lowest
            // awareness clientID) fires save. Non-leader peers still
            // receive the y-prosemirror sync and update PM locally — but
            // they MUST NOT invoke onChange (which schedules the PATCH).
            // Yjs has already converged the doc across the room, so a save
            // from any one peer carries the canonical body; multi-peer
            // saves would be wasted duplicates. In non-collab mode
            // (session == null) the legacy single-author path is
            // unconditional.
            if (session != null && !isLeaderRef.current) return
            callbacks.current.onChange(md)
          })
          .blur(() => {
            // Same leader gate as markdownUpdated. Blur in PageView cancels
            // the pending debounce and saves immediately; non-leaders have
            // no pending save and shouldn't author one either.
            if (session != null && !isLeaderRef.current) return
            callbacks.current.onBlur?.()
          })
        // M15.1 — slash + wikilink-autocomplete views are editor-surface
        // plugins (they listen for `/` and `[[` triggers and need the live
        // `useAllPages` snapshot for the picker). Skip them in share-mode:
        // the share viewer is read-only and unauthenticated, so mounting
        // these would fire /api/pages/all which 401s for share viewers and
        // trips the global auth-required redirect path.
        if (wikilinkMode !== 'share') {
          ctx.set(slashPlugin.key, {
            view: pluginViewFactory({ component: SlashView }),
          })
          ctx.set(bubblePlugin.key, {
            view: pluginViewFactory({ component: BubbleToolbarView }),
          })
          ctx.set(wikilinkPlugin.key, {
            view: pluginViewFactory({ component: WikilinkView }),
          })
          ctx.set(emojiAutocompletePlugin.key, {
            view: pluginViewFactory({ component: EmojiAutocompleteView }),
          })
          // Block drag-handle + add-block gutter. The `block` plugin (added to
          // the .use() chain) supplies the drag service; BlockHandleView owns
          // the gutter DOM + the BlockProvider. Mounted as its own PM plugin
          // view since `block` has no view slot of its own. Self-gates on
          // view.editable, so it stays inert in viewer mode.
          const blockHandle = new Plugin({
            view: pluginViewFactory({ component: BlockHandleView }),
          })
          ctx.update(prosePluginsCtx, (existing) => [...existing, blockHandle])
        }
        ctx.set(wikilinkModeCtx.key, wikilinkMode)
        ctx.set(wikilinkResolveCtx.key, wikilinkResolveIndex ?? null)
        // M13.4 — share-mode + viewer-mode render collapsibles CLOSED by
        // default (matching native <details> UX); editable mode forces them
        // open so caret-routing into the body works. See detailsReadOnlyCtx
        // for why this isn't read from view.editable.
        ctx.set(detailsReadOnlyCtx.key, wikilinkMode === 'share' || Boolean(readOnly))
        // M13.3a — page id for excalidrawSchema.toDOM's PNG URL. Stable across
        // the editor's lifetime (editor remounts on page change), so a single
        // ctx.set at build time is sufficient — no useEffect needed.
        ctx.set(pageIdCtx.key, pageId)
        // M13.3b — wire the open-Edit-Sheet ctx. Null in share-mode and
        // read-only mode: the click plugin and slash-insert helper both
        // short-circuit on a null handler, so they're safe to mount
        // unconditionally. In editable mode the trampoline pushes into local
        // state; `setExcalidrawSheet` from useState is reference-stable, so
        // capturing it once at editor-build time is correct.
        const openTrampoline: ExcalidrawOpenHandler | null =
          wikilinkMode === 'share' || readOnly
            ? null
            : (req) => setExcalidrawSheet(req)
        ctx.set(excalidrawOpenCtx.key, openTrampoline)
        // Emoji picker open-handler — same gating as the excalidraw sheet:
        // off in share / read-only, where the slash menu (and thus the
        // "Emoji" item) isn't mounted anyway. `setEmojiPicker` is
        // reference-stable, so capturing once at build time is correct.
        ctx.set(
          emojiPickerOpenCtx.key,
          wikilinkMode === 'share' || readOnly
            ? null
            : (req) => setEmojiPicker(req),
        )
        // M13.5 (#116) — modifier-click follow. Off in share and viewer
        // modes (those keep native click behaviour: share has its own
        // ShareReader wrapper, viewer doesn't need to follow links from
        // inside an uneditable surface). The wikilink navigate handler
        // closes over a stable QueryClient instance, so capturing once at
        // build time is correct — each call reads fresh cache state.
        ctx.set(
          modifierClickEnabledCtx.key,
          wikilinkMode !== 'share' && !readOnly,
        )
        ctx.set(wikilinkNavigateCtx.key, wikilinkNavigate)
        ctx.set(imageAttr.key, () => ({ loading: 'lazy' }))
        // M12.1 — register only the curated grammar set (24 langs) on the
        // shared refractor/core singleton. The plugin's static import of
        // `refractor` is aliased to `refractor/core` in vite.config.ts so the
        // plugin's `apply` path uses this same registered singleton.
        ctx.set(prismConfig.key, { configureRefractor })

        // M7.2: wire the editable toggle. Function form is re-evaluated by
        // PM on every updateState; banner-driven readonly + ws-disconnect
        // both flow through editableRef.
        ctx.set(editorViewOptionsCtx, {
          editable: () => editableRef.current,
        })

        // M8.3 — selection bridge: a no-decoration PM plugin that fires
        // onViewReady on mount/unmount and onSelectionChange whenever the
        // selection transitions. The plugin captures the live callback
        // refs (not the props at editor-build time) so the React side can
        // change handlers without rebuilding the editor.
        const selectionBridge = new Plugin({
          view: (view) => {
            callbacks.current.onViewReady?.(view)
            // Initial selection state — fire so consumers don't have to
            // wait for the first selection event to know the editor is
            // ready and the selection is currently collapsed/non-collapsed.
            emitSelection(view)
            return {
              update: (view, prevState) => {
                if (view.state.selection.eq(prevState.selection)) return
                emitSelection(view)
              },
              destroy: () => {
                callbacks.current.onViewReady?.(null)
              },
            }
          },
        })
        ctx.update(prosePluginsCtx, (existing) => [...existing, selectionBridge])

        // Plain-text clipboard serializer. The milkdown clipboard plugin's
        // clipboardTextSerializer serializes copied content as markdown, which
        // causes `<br />` (emitted by the empty-paragraph serializer in
        // remarkPreserveEmptyLinePlugin, part of the commonmark preset) to
        // appear literally in the clipboard text/plain payload. Override it
        // with a ProseMirror plugin that uses textBetween — same approach the
        // clipboard plugin already uses for pure-text slices, extended to all
        // content. Leaf nodes use their schema leafText specs (hardbreak → '\n';
        // image → ''). Prepended so this prop wins over the clipboard plugin's.
        const plainClipboardTextPlugin = new Plugin({
          props: {
            clipboardTextSerializer: (slice) =>
              slice.content.textBetween(0, slice.content.size, '\n\n'),
          },
        })
        ctx.update(prosePluginsCtx, (existing) => [plainClipboardTextPlugin, ...existing])

        // Paste-a-bare-URL → titled link. Prepended to `prosePluginsCtx` so
        // the plugin's `handlePaste` runs ahead of the default clipboard parse.
        // Order matters in collab mode: y-prosemirror's ySync plugin reacts to
        // insertions, so the paste-hook must suppress the default paste BEFORE
        // the collab plugins see it. Gated editable+non-share.
        if (wikilinkMode !== 'share' && !readOnly) {
          const urlUnfurl = createUrlUnfurlPlugin()
          ctx.update(prosePluginsCtx, (existing) => [urlUnfurl, ...existing])
        }

        // Table edge-selection guard. Prepended so its handleKeyDown runs ahead
        // of prosemirror-tables: at a table edge a Shift+Arrow would otherwise
        // collapse the whole CellSelection to a caret (the highlight vanishes).
        // Editable, non-share.
        if (wikilinkMode !== 'share' && !readOnly) {
          const tableEdgeSelect = createTableEdgeSelectPlugin()
          ctx.update(prosePluginsCtx, (existing) => [tableEdgeSelect, ...existing])
        }

        // Ragged-table repair. A GFM row may carry fewer cells than the header,
        // which yields a table whose TableMap describes a grid the document
        // doesn't have — prosemirror-tables then throws RangeError out of its
        // own handlePaste. Runs for every editable surface, before a user can
        // touch a table that arrived ragged from markdown.
        if (wikilinkMode !== 'share' && !readOnly) {
          const tableFix = createTableFixPlugin()
          ctx.update(prosePluginsCtx, (existing) => [...existing, tableFix])
        }

        // Paste-as-plain-text (Cmd/Ctrl+Shift+V). Prepended last so its
        // handlePaste runs ahead of every smart paste handler (table, URL,
        // image, markdown) when the chord armed it. Editable, non-share.
        if (wikilinkMode !== 'share' && !readOnly) {
          const plainPaste = createPlainPastePlugin()
          ctx.update(prosePluginsCtx, (existing) => [plainPaste, ...existing])
        }

        // Image paste/drop → upload → ![](url). Prepended so it intercepts
        // image files before the default clipboard handler turns them into
        // nothing. Image files and URL pastes are disjoint clipboard payloads,
        // so ordering against the URL hook above is immaterial.
        if (imageUploadEnabled) {
          const attachmentDrop = createAttachmentDropPlugin(pageId)
          ctx.update(prosePluginsCtx, (existing) => [
            attachmentDrop,
            uploadPlaceholderPlugin,
            ...existing,
          ])
        }

        // M7.2: bind y-prosemirror's sync + undo plugins when collab is
        // active. ySyncPlugin observes the Y.XmlFragment and pushes the
        // initial PM doc into it iff the fragment is empty — naturally
        // seeding a fresh room from defaultValueCtx (the canonical
        // pages.body markdown) without us doing anything special.
        const collab = session
        if (collab) {
          const fragment = collab.doc.getXmlFragment('milkdown-doc')
          // M7.5: yCursorPlugin renders one widget per remote peer's caret +
          // an inline decoration over each remote selection range. Builders
          // route hue via data-collab-color → --collab-cursor-N tokens so the
          // peer's caret matches their PresenceAvatar pill across every
          // theme. yCursorPlugin slots between ySync and yUndo so the
          // cursor-position relative-position mapping rebuilds after a sync
          // tx without polluting the undo stack.
          ctx.update(prosePluginsCtx, (existing) => [
            ...existing,
            ySyncPlugin(fragment),
            // y-prosemirror's selection restore can't represent a CellSelection
            // and collapses table cell drags to a caret on every sync tx; this
            // re-wraps them back. Must sit after ySyncPlugin to react to its
            // sync transactions.
            createPreserveCellSelectionPlugin(),
            yCursorPlugin(collab.provider.awareness, {
              cursorBuilder,
              selectionBuilder,
            }),
            yUndoPlugin(),
            // yUndoPlugin only wires the Yjs-aware UndoManager — it binds NO
            // keys. Milkdown's `history` plugin (draft path) ships its own
            // Mod-z/Mod-y keymap, but it's gated off here, so without this the
            // collab editor has no keyboard undo/redo. Bind y-prosemirror's
            // own undo/redo commands.
            keymap({
              'Mod-z': undo,
              'Mod-y': redo,
              'Mod-Shift-z': redo,
            }),
          ])
        }

        // M8.4 — anchor decoration. Always-on for non-viewer pages
        // (commentsEnabled). Positioned AFTER yCursorPlugin in the collab
        // branch (so caret widgets still hit-test correctly above the
        // underline) and also wired in non-collab mode. Plugin reads the
        // thread list via commentThreadsCtx and dispatches a meta-flagged
        // rebuild tx — the React useEffect below pushes new threads into
        // the slice + dispatches the tx atomically.
        if (commentsEnabled) {
          // Stable callback bundle — reads live refs each call so prop
          // changes (PageView re-renders) don't require an editor rebuild.
          const stableCallbacks: CommentAnchorCallbacks = {
            onAnchorClick: (id) => callbacks.current.onAnchorClick?.(id),
            onResolved: (resolutions) =>
              callbacks.current.onAnchorsResolved?.(resolutions),
          }
          ctx.set(commentThreadsCtx.key, commentThreads ?? null)
          ctx.set(commentAnchorCallbacksCtx.key, stableCallbacks)
          ctx.set(commentShowResolvedCtx.key, showResolvedAnchors)
          const anchorPlugin = createCommentAnchorPlugin(ctx)
          ctx.update(prosePluginsCtx, (existing) => [...existing, anchorPlugin])
        }
      })
      .use(commonmark)
      .use(gfm)
      .use(remarkSafeLinks)
      // Gapcursor/dropcursor — caret placement in the gaps around block atoms
      // (e.g. clicking above/below an Excalidraw diagram to type). Self-gates on
      // editability, so it never shows in read/share mode.
      .use(cursor)
      // NOTE: do NOT add @milkdown/plugin-trailing here. Its auto-appended
      // trailing paragraph is inserted via appendTransaction OUTSIDE the Yjs
      // document, so y-prosemirror's ySync observer hits an unmapped node and
      // crashes ("Cannot read properties of null (reading 'nodeSize')" in
      // _typeChanged) — but only on docs that END in a block atom (e.g. an
      // Excalidraw diagram), where trailing actually fires. gapcursor already
      // covers placing a caret around/after a trailing atom.
      // Tab / Shift-Tab nest & un-nest list items (markdown-safe; falls through
      // to focus-out when not in a list). See milkdown-list-indent.ts.
      .use(listIndentKeymap)
      // Smart typography: straight quotes → curly, `--` → em-dash, `...` →
      // ellipsis, as you type. Skips code. See milkdown-typography.ts.
      .use(typographyInputRules)
      // GFM task lists: schema + input rule (`[ ] `) ship in the preset above;
      // this adds the click-to-toggle on the CSS checkbox. See
      // milkdown-task-list.ts.
      .use(taskCheckboxPlugin)
      // Block drag-handle service (the gutter view is mounted as a PM plugin
      // view in the config block above). Self-gates on view.editable.
      .use(block)
      // Empty-line "Type / for commands" discoverability hint. See
      // milkdown-placeholder.ts.
      .use(placeholderPlugin)
      // Hover-a-link → open/copy/edit/remove popover. See milkdown-link-popover.ts.
      .use(linkPopoverPlugin)
      // Math / LaTeX: remark-math parse + `$inline$` / `$$block$$` schemas +
      // KaTeX nodeView (click-to-edit) + autoformat input rules. See
      // milkdown-math.ts. KaTeX stylesheet imported in main.tsx.
      .use(mathRemarkPlugin)
      .use(mathInlineSchema)
      .use(mathBlockSchema)
      .use(mathNodeViews)
      .use(mathInlineInputRule)
      .use(mathBlockInputRule)
      // Highlight mark: ==text== ⇄ <mark>. See milkdown-highlight.ts.
      .use(highlightRemarkPlugin)
      .use(highlightSchema)
      .use(highlightInputRule)
      .use(toggleHighlightCommand)
      // Mermaid: renders a diagram below each ```mermaid code block (lazy lib).
      .use(mermaidPlugin)
      // Chart: renders an interactive ECharts chart below each ```chart code
      // block (lazy lib + YAML). Themed from --chart-* tokens. See
      // milkdown-chart.ts.
      .use(chartPlugin)
      // Container directives (:::name) + the tabs block built on them.
      .use(directiveRemarkPlugin)
      // Unwrap any directive without a dedicated schema (below) to its nested
      // content, so a foreign/typo'd `:::name` can't reach the strict parser
      // and crash the editor. Must follow directiveRemarkPlugin (the parser).
      .use(unknownDirectiveFallbackPlugin)
      .use(tabSchema)
      .use(tabsSchema)
      .use(tabsNodeView)
      // Kanban board, also on the directive foundation.
      .use(kanbanColumnSchema)
      .use(kanbanSchema)
      .use(kanbanNodeViews)
      // M19 — data blocks on the directive foundation. stat grid (`:::stats`,
      // `### Label` tiles), timeline (`:::timeline` dated list → rail), and
      // calendar (`:::calendar{month}` list → month grid). Same registration
      // in both editor branches; schemas + nodeViews only, no Yjs dependency.
      .use(statTileSchema)
      .use(statGridSchema)
      .use(statGridNodeViews)
      .use(timelineSchema)
      .use(calendarSchema)
      .use(calendarNodeView)
      // Poll — minimal round-trip schema so `:::poll` survives the strict parser
      // (isn't unwrapped/stripped on save); the rich vote UI is read-view only.
      .use(pollSchema)
      // M19 — GFM table upgrades (glyph cells, featured column, sticky first
      // column, reader-side sort/filter). Enhances the stock table; no new
      // node. See milkdown-table.ts.
      .use(tableEnhancePlugin)
      // Undo/redo: in collab mode y-prosemirror's yUndoPlugin owns history
      // (it's Yjs-aware). prosemirror-history MUST NOT run alongside it — it
      // records and re-applies the remote ySync transactions, fighting the CRDT
      // and corrupting sync. So history is gated to the non-collab (draft) path,
      // which has no yUndo. (collabPageId != null ⇔ collab active, see line ~388.)
      .use(collabPageId != null ? [] : history)
      .use(clipboard)
      .use(listener)
      .use(prism)
      // Code-block chrome (language label + copy button) via a code_block
      // nodeView. After prism so its inline token decorations land on our
      // contentDOM <code>. See milkdown-codeblock.ts.
      .use(codeBlockNodeView)
      .use(slashPlugin)
      .use(bubblePlugin)
      .use(wikilinkPlugin)
      // Emoji shortcodes: `:rocket:` → 🚀 input rule + a caret-anchored
      // `:query` autocomplete picker. The Unicode char is what's stored in the
      // canonical markdown — no schema node, no reader/serialize path. See
      // milkdown-emoji.ts + milkdown-emoji-autocomplete.tsx.
      .use(emojiInputRule)
      .use(emojiAutocompletePlugin)
      .use(wikilinkAliveIdsCtx)
      .use(wikilinkModeCtx)
      .use(wikilinkDecorationPlugin)
      // Obsidian-style `[[Name]]` bracket wikilinks: remark transform + atom
      // node (round-trips the `[[…]]` source) + a resolve plugin that injects
      // `tela://page/{id}` hrefs so the existing nav handlers pick them up.
      .use(wikilinkBracketRemarkPlugin)
      .use(wikilinkBracketSchema)
      .use(wikilinkResolveCtx)
      .use(wikilinkResolvePlugin)
      .use(commentThreadsCtx)
      .use(commentAnchorCallbacksCtx)
      .use(commentShowResolvedCtx)
      // M13.0 — GitHub-style blockquote alert callouts. The remark plugin
      // rewrites qualifying `> [!TYPE]` blockquotes into `callout` mdast
      // nodes during parse; the schema's toMarkdown round-trips back to the
      // same markdown shape on save; the input rule fires live as the user
      // closes the marker so the chrome appears without waiting for a
      // save+reload. Same registration in both collab and non-collab
      // branches — the schema and remark hook live above prosePluginsCtx
      // and don't depend on the Yjs branch.
      .use(calloutsRemarkPlugin)
      .use(calloutSchema)
      .use(calloutInputRule)
      // Pull-quote: `:::quote{cite="…"}` container directive → elevated quote
      // with an optional attribution caption. Built on the directive foundation
      // above. See milkdown-pullquote.ts.
      .use(pullquoteSchema)
      .use(pullquoteNodeView)
      // Web embeds: `:::embed` container directive → responsive sandboxed iframe
      // for allowlisted providers (YouTube/Vimeo/Loom), link card otherwise.
      // See milkdown-embed.ts.
      .use(embedSchema)
      .use(fileSchema)
      // M13.1 — collapsibles via raw `<details><summary>` HTML pass-through.
      // The remark plugin detects post-html-transformer paragraph-wrapped
      // `<details>` / `</details>` brackets and rewrites them into structured
      // `details` + `details_summary` mdast nodes; the schemas materialize
      // them as native browser disclosure widgets in the editor; toMarkdown
      // round-trips back to the same raw HTML form.
      .use(collapsiblesRemarkPlugin)
      .use(detailsSummarySchema)
      .use(detailsSchema)
      .use(detailsReadOnlyCtx)
      .use(detailsNodeView)
      // M13.3a — Excalidraw view-mode renderer. The remark plugin walks the
      // mdast for ```excalidraw fences, parses the JSON, validates the
      // scene_hash, and rewrites the node to `excalidraw`. The schema
      // materializes it as a ProseMirror atom node that renders an <img>
      // pointing at the M13.2 PNG sidecar — zero Excalidraw runtime on the
      // view path. The Edit Sheet (M13.3b / #113) opens on click in
      // edit-mode and lazy-loads the Excalidraw library only then.
      .use(pageIdCtx)
      .use(excalidrawOpenCtx)
      .use(emojiPickerOpenCtx)
      .use(excalidrawRemarkPlugin)
      .use(excalidrawSchema)
      .use(excalidrawClickPlugin)
      .use(excalidrawPresenceCtx)
      .use(excalidrawPresencePlugin)
      // M13.5 (#116) — ctrl/cmd-click to follow wikilinks + external links
      // and to toggle force-open collapsibles. Mounted unconditionally; the
      // handler short-circuits on the modifierClickEnabled flag (off in
      // share-mode and viewer-mode).
      .use(modifierClickEnabledCtx)
      .use(wikilinkNavigateCtx)
      .use(modifierClickPlugin),
  )

  // Autofocus the body ONCE on first ready — never again. `get` from useEditor
  // isn't referentially stable, so without this guard the effect re-ran on
  // every re-render (e.g. each keystroke in the page-title input) and yanked
  // focus from the title back into the editor after a single character.
  const didAutoFocusRef = useRef(false)
  useEffect(() => {
    if (loading || !autoFocus || didAutoFocusRef.current) return
    didAutoFocusRef.current = true
    const editor = get()
    editor?.action((ctx) => {
      const view = ctx.get(editorViewCtx)
      // Anchor the caret to the document start before focusing. ProseMirror's
      // focus() scrolls the current selection into view; on a long page the
      // body-autofocus would otherwise land the view scrolled down with the
      // title off-screen (the recurring "opens scrolled" bug). Forcing the
      // selection to the start means focus scrolls to the top — preserving
      // "open pages scrolled to the top" while still putting the cursor in the
      // body for immediate typing.
      //
      // Use the generic Selection.atStart, NOT TextSelection.atStart: when the
      // first node is a block atom (e.g. a page that opens with an Excalidraw
      // diagram), TextSelection.atStart can't find an inline position and yields
      // an invalid doc-level selection ("TextSelection endpoint not pointing
      // into a node with inline content") — which y-prosemirror's selection sync
      // then crashes on (nodeSize of null in _typeChanged), leaving the editor
      // read-only/empty. Selection.atStart resolves to a valid selection for any
      // first node.
      view.dispatch(view.state.tr.setSelection(Selection.atStart(view.state.doc)))
      view.focus()
    })
  }, [loading, autoFocus, get])

  // Push the alive-ids snapshot into the milkdown ctx and dispatch a no-op
  // transaction tagged with the meta flag so the decoration plugin's `apply`
  // reruns and repaints. Without the meta-tx the plugin would only refresh on
  // the next user keystroke.
  useEffect(() => {
    if (loading) return
    const editor = get()
    editor?.action((ctx) => {
      ctx.set(wikilinkAliveIdsCtx.key, aliveWikilinkIds ?? null)
      const view = ctx.get(editorViewCtx)
      view.dispatch(view.state.tr.setMeta(WIKILINK_ALIVE_IDS_META, true))
    })
  }, [loading, get, aliveWikilinkIds])

  // Push the live diagram-presence snapshot into the ctx slice + repaint the
  // excalidraw "editing" badges — same deferred-snapshot mechanism as alive-ids.
  useEffect(() => {
    if (loading) return
    const editor = get()
    editor?.action((ctx) => {
      ctx.set(excalidrawPresenceCtx.key, diagramEditors)
      const view = ctx.get(editorViewCtx)
      view.dispatch(view.state.tr.setMeta(EXCALIDRAW_PRESENCE_META, true))
    })
  }, [loading, get, diagramEditors])

  // Push the `[[Name]]` resolution index (slug→id) and repaint the bracket
  // wikilink decorations — same deferred-snapshot mechanism as the alive-ids
  // effect above.
  useEffect(() => {
    if (loading) return
    const editor = get()
    editor?.action((ctx) => {
      ctx.set(wikilinkResolveCtx.key, wikilinkResolveIndex ?? null)
      const view = ctx.get(editorViewCtx)
      view.dispatch(view.state.tr.setMeta(WIKILINK_RESOLVE_META, true))
    })
  }, [loading, get, wikilinkResolveIndex])

  // M8.4 — push thread-list updates into the comment-anchor ctx slice and
  // trigger an immediate rebuild via the COMMENT_ANCHOR_META tx. The plugin's
  // internal scheduler handles debounce on doc-change; thread-list changes
  // (new comment, deleted comment, resolve toggle) should paint immediately
  // since they reflect a discrete user action elsewhere in the UI rather
  // than continuous typing. Skipped if comments aren't enabled (viewer).
  // M8.5 — same effect carries the show-resolved-anchors flag so toggling
  // the panel filter repaints in-body decorations without an extra round
  // through React state.
  useEffect(() => {
    if (loading) return
    if (!commentsEnabled) return
    const editor = get()
    editor?.action((ctx) => {
      ctx.set(commentThreadsCtx.key, commentThreads ?? null)
      ctx.set(commentShowResolvedCtx.key, showResolvedAnchors)
      const view = ctx.get(editorViewCtx)
      view.dispatch(view.state.tr.setMeta(COMMENT_ANCHOR_META, true))
    })
  }, [loading, get, commentsEnabled, commentThreads, showResolvedAnchors])

  // M7.2: force PM to re-read the `editable` predicate when collab status
  // or readOnly flips. PM only re-evaluates editable inside updateState,
  // and a disconnect with no pending edits wouldn't otherwise trigger one.
  // A no-op tx dispatch repaints the contenteditable attribute.
  useEffect(() => {
    if (loading) return
    const editor = get()
    editor?.action((ctx) => {
      const view = ctx.get(editorViewCtx)
      view.dispatch(view.state.tr)
    })
  }, [loading, get, collabStatus, readOnly])

  // M7.2: empty-room seed from canonical markdown body. ySyncPlugin's
  // view() pulls fragment → PM on attach — it does NOT push initial PM
  // content into the fragment. So a fresh page (no server-side Yjs state
  // yet) would render empty after the plugin attaches even though
  // pages.body is non-empty. We re-parse `defaultValue` via Milkdown's
  // parser and push it to the Y.XmlFragment via prosemirrorToYXmlFragment
  // after the first sync-init confirms the room is genuinely fresh.
  //
  // Multi-client race: two clients opening a fresh page simultaneously
  // both seed; Yjs CRDT would merge as duplicated content. Acceptable for
  // v0 — extremely rare in practice (a brand-new page open by two users
  // in the same ~100ms before either has typed). #64's leader election
  // doesn't address this; a deterministic seed lock is out of M7 scope.
  useEffect(() => {
    if (loading) return
    const collab = session
    if (!collab) return
    const editor = get()
    if (!editor) return
    if (!defaultValue || defaultValue.trim().length === 0) return

    let cancelled = false
    const unsub = collab.provider.onFirstSync(({ hadServerState }) => {
      if (cancelled || hadServerState) return
      const fragment = collab.doc.getXmlFragment('milkdown-doc')
      if (fragment.length > 0) return
      editor.action((ctx) => {
        const parser = ctx.get(parserCtx)
        const pmNode = parser(defaultValue)
        if (pmNode) {
          prosemirrorToYXmlFragment(pmNode, fragment)
        }
      })
    })
    return () => {
      cancelled = true
      unsub()
    }
  }, [loading, get, defaultValue])

  // Persist sync-arrived content. The leader's body-save (markdownUpdated) only
  // fires for LOCAL edits — content that arrives via Yjs sync (a non-leader
  // peer's keystrokes, or a diagram a peer drew) updates the live doc but never
  // triggers a save, so pages.body silently lags the collab doc and view mode
  // (which renders the body, not the live doc) goes stale. The leader is the
  // single canonical writer, so have it also save when remote content lands.
  //
  // Bounded by design — no write-amplification, no loop:
  //   - origin === provider keeps this to content from the server/peers; our own
  //     local edits use a different Yjs origin and already persist via
  //     markdownUpdated (so no double-save).
  //   - the first-sync guard skips the load-time snapshot (instant-paint REST +
  //     WS sync-init both apply with origin=provider BEFORE first sync), so
  //     opening a page never triggers a save.
  //   - only the elected leader runs it, so N peers don't each save.
  //   - the onChange PATCH is debounced downstream, and a body PATCH never feeds
  //     back into the Yjs doc, so saves can't cascade.
  // It does NOT heal a page that's already diverged (that snapshot arrives
  // pre-first-sync and is skipped) — those recover on the next edit. This only
  // stops NEW divergence.
  useEffect(() => {
    if (loading) return
    const collab = session
    if (!collab) return
    let firstSyncDone = false
    const unsubFirst = collab.provider.onFirstSync(() => {
      firstSyncDone = true
    })
    const onUpdate = (_update: Uint8Array, origin: unknown) => {
      if (origin !== collab.provider) return
      if (!firstSyncDone) return
      if (!isLeaderRef.current) return
      const editor = get()
      if (!editor) return
      try {
        editor.action((ctx) => {
          const view = ctx.get(editorViewCtx)
          callbacks.current.onChange(ctx.get(serializerCtx)(view.state.doc))
        })
      } catch {
        // Editor mid-teardown — drop; the next remote update or a local edit
        // re-saves.
      }
    }
    collab.doc.on('update', onUpdate)
    return () => {
      unsubFirst()
      collab.doc.off('update', onUpdate)
    }
  }, [loading, get])

  // Instant paint: fetch the server's persisted Yjs state over REST and apply
  // it as soon as the editor mounts, so content shows without waiting for the
  // WS sync-init round-trip (the actual cause of the open-page blank on prod).
  // Applied with the provider as the Yjs origin so the doc-update observer
  // skips it (no rebroadcast). The WS then re-delivers the same state — a
  // no-op, since Yjs update application is idempotent.
  useEffect(() => {
    if (collabPageId == null) return
    const collab = session
    if (!collab) return
    let cancelled = false
    void (async () => {
      try {
        const res = await fetch(`/api/pages/${collabPageId}/yjs`, {
          credentials: 'include',
        })
        if (!res.ok || cancelled) return
        const buf = new Uint8Array(await res.arrayBuffer())
        if (cancelled || buf.byteLength === 0) return
        // The fetch can resolve mid-unmount; applying into a destroyed doc
        // dispatches a transaction into a half-disposed editor.
        if (collab.provider.isDestroyed()) return
        const { snapshot, updates } = decodeSyncInit(buf)
        if (snapshot) Y.applyUpdate(collab.doc, snapshot, collab.provider)
        for (const u of updates) Y.applyUpdate(collab.doc, u, collab.provider)
      } catch {
        // Best-effort — the WS sync-init still delivers the state.
      }
    })()
    return () => {
      cancelled = true
    }
  }, [collabPageId])

  // Broken-wikilink click → emit a request to open the new-page dialog with
  // the link text pre-filled. We hang the listener on the editor's
  // contenteditable dom so prevention happens before ProseMirror would try to
  // follow the dead `tela://` URL.
  useEffect(() => {
    if (loading) return
    const editor = get()
    if (!editor) return
    const domBox: { el: HTMLElement | null } = { el: null }
    editor.action((ctx) => {
      domBox.el = ctx.get(editorViewCtx).dom as HTMLElement
    })
    const dom = domBox.el
    if (!dom) return
    const onClick = (e: MouseEvent) => {
      // M13.5 (#116) — leave modifier-clicks to the modifierClickPlugin.
      // ctrl/cmd-click on a broken wikilink is treated as "follow"; the
      // navigate handler bails on cache miss, leaving no surprise dialog.
      // defaultPrevented short-circuit covers the rare case where another
      // PM plugin already handled the click (e.g. an excalidraw atom
      // overlapping a wikilink — unlikely but defensive).
      if (e.ctrlKey || e.metaKey || e.defaultPrevented) return
      const target = e.target as HTMLElement | null
      if (!target) return
      const broken = target.closest('.tela-wikilink--broken')
      if (!broken) return
      e.preventDefault()
      const title = (broken.textContent ?? '').trim()
      emitOpenNewPage({ prefillTitle: title })
    }
    dom.addEventListener('click', onClick)
    return () => dom.removeEventListener('click', onClick)
  }, [loading, get])

  // Only after a real drop (we were connected, now we're not) — never during
  // the initial connect on page open.
  const showReconnectBanner =
    !readOnly &&
    session != null &&
    collabStatus !== 'connected' &&
    hasConnected

  return (
    <div className={cn('tela-milkdown', className)} aria-label={ariaLabel}>
      {showReconnectBanner ? (
        <div
          role="status"
          aria-live="polite"
          className={cn(
            'mb-[var(--space-2)] px-[var(--space-3)] py-[var(--space-2)]',
            'text-[length:var(--text-sm)] text-[var(--text-muted)]',
            'bg-[var(--surface-2)] border border-[var(--border-subtle)]',
            'rounded-[var(--radius-sm)]',
          )}
        >
          {RECONNECTING_LABEL}
        </div>
      ) : null}
      <Milkdown />
      {excalidrawSheet ? (
        <Suspense fallback={null}>
          <ExcalidrawEditSheet
            open
            onOpenChange={(next) => {
              if (!next) setExcalidrawSheet(null)
            }}
            pageId={pageId}
            initialJSON={excalidrawSheet.sceneJSON}
            initialAltText={excalidrawSheet.altText}
            initialDiagramId={excalidrawSheet.diagramId}
            session={diagramSession}
            onSave={(next) => {
              // Persist ONLY (the Sheet owns closing via onOpenChange) — this
              // callback is the single commit path, hit by an explicit Save AND
              // by idle/exit checkpoints.
              //
              // 1. Apply the drawing to the doc (setNodeMarkup on the atom).
              excalidrawSheet.onSave(next)
              // 2. Force-persist `pages.body` immediately. setNodeMarkup fires
              // markdownUpdated, but in collab mode its save is gated to the
              // elected (prose) leader — and the diagram drawer / checkpoint
              // writer may not be that leader, so the diagram would reach the
              // live Yjs doc yet never the body, and view mode (which renders
              // the body) would show nothing. An unconditional save here
              // guarantees the drawing reaches the saved markdown regardless of
              // leadership; the PATCH is debounced downstream so it coalesces
              // with the leader's own save in the common case. Checkpoints are
              // leader-gated + dedup'd in the Sheet, so this isn't per-keystroke.
              const editor = get()
              editor?.action((ctx) => {
                const view = ctx.get(editorViewCtx)
                callbacks.current.onChange(ctx.get(serializerCtx)(view.state.doc))
              })
            }}
          />
        </Suspense>
      ) : null}
      {emojiPicker ? (
        <EmojiPicker
          anchor={emojiPicker.anchor}
          onSelect={(emoji) => {
            get()?.action((ctx) => insertEmojiAt(ctx, emojiPicker.pos, emoji))
            setEmojiPicker(null)
          }}
          onClose={() => setEmojiPicker(null)}
        />
      ) : null}
    </div>
  )
}

// memo so an unrelated parent re-render (e.g. typing the page title) with stable
// props doesn't re-render the editor subtree. The prosemirror-adapter renders
// node views via flushSync; re-rendering it during the title input's update
// cycle fired flushSync mid-lifecycle and stole the title's focus after one
// keystroke. Props from PageView are referentially stable (useCallback/useMemo).
export const MilkdownEditor = memo(function MilkdownEditor(
  props: MilkdownEditorProps,
) {
  return (
    <MilkdownProvider>
      <ProsemirrorAdapterProvider>
        <MilkdownEditorInner {...props} />
      </ProsemirrorAdapterProvider>
    </MilkdownProvider>
  )
})
