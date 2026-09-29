import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { slashFactory } from '@milkdown/kit/plugin/slash'
import { usePluginViewContext } from '@prosemirror-adapter/react'
import { useInstance } from '@milkdown/react'
import { commandsCtx, editorViewCtx } from '@milkdown/kit/core'
import type { Ctx } from '@milkdown/ctx'
import { insertHrCommand } from '@milkdown/kit/preset/commonmark'
import { insertTableCommand } from '@milkdown/kit/preset/gfm'
import { COLLAPSIBLE_DEFAULT_SUMMARY } from './milkdown-collapsibles'
import { insertCallout } from './milkdown-callouts'
import { insertExcalidraw } from './milkdown-excalidraw'
import { insertTaskList } from './milkdown-task-list'
import { TURN_INTO } from './milkdown-turn-into'
import { insertMathBlock } from './milkdown-math'
import { TEMPLATES, insertTemplate } from './milkdown-templates'
import { positionFloating, setShow } from './milkdown-floating'
import { insertBlock } from '../../lib/milkdown/insert-block'
import { insertMermaid } from './milkdown-mermaid'
import { insertChart } from './milkdown-chart'
import { insertTabs } from './milkdown-tabs'
import { insertKanban } from './milkdown-kanban'
import { insertStatGrid } from './milkdown-stat-grid'
import { insertTimeline } from './milkdown-timeline'
import { insertCalendar } from './milkdown-calendar'
import { insertPoll } from './milkdown-poll'
import { insertPullquote } from './milkdown-pullquote'
import { insertEmbed } from './milkdown-embed'
import { openEmojiPicker } from './milkdown-emoji'
import { SLASH_BLOCKS } from './blocks-manifest'

// eslint-disable-next-line react-refresh/only-export-components -- milkdown plugin slice lives with its view
export const slashPlugin = slashFactory('tela-slash')

interface SlashCommand {
  id: string
  label: string
  hint: string
  keywords: string[]
  run: (ctx: Ctx) => void
}

// Behaviour, keyed by manifest block id. The labels/hints/keywords/ordering all
// live in blocks-manifest.json (the source of truth shared with the agent
// authoring guide); this map only supplies the imperative insert per id, so a
// new block needs an entry in both. The integrity check below fails loudly in
// dev if the two ever drift.
const RUN: Record<string, (ctx: Ctx) => void> = {
  // Block-type transforms (h1-3, lists, quote, code) are shared with the block
  // handle and the bubble toolbar; 'text' isn't an insertable block.
  ...Object.fromEntries(TURN_INTO.filter((o) => o.id !== 'text').map((o) => [o.id, o.run])),
  'task-list': insertTaskList,
  'pull-quote': insertPullquote,
  callout: insertCallout,
  collapsible: insertCollapsible,
  excalidraw: insertExcalidraw,
  divider: (ctx) => ctx.get(commandsCtx).call(insertHrCommand.key),
  table: (ctx) =>
    ctx.get(commandsCtx).call(insertTableCommand.key, { row: 3, col: 2 }),
  equation: insertMathBlock,
  mermaid: insertMermaid,
  chart: insertChart,
  embed: insertEmbed,
  tabs: insertTabs,
  kanban: insertKanban,
  'stat-grid': insertStatGrid,
  timeline: insertTimeline,
  calendar: insertCalendar,
  poll: insertPoll,
  emoji: openEmojiPicker,
  date: (ctx) => {
    const view = ctx.get(editorViewCtx)
    // YYYY-MM-DD, matching the project's date convention.
    const today = new Date().toISOString().slice(0, 10)
    view.dispatch(view.state.tr.insertText(today))
    view.focus()
  },
}

// Fail fast in dev if the manifest's slash blocks and this behaviour map drift:
// every slash block needs a `run`, and no `run` should reference a block the
// manifest doesn't mark slash-insertable.
if (import.meta.env?.DEV) {
  const ids = new Set(SLASH_BLOCKS.map((b) => b.id))
  const missing = SLASH_BLOCKS.filter((b) => !RUN[b.id]).map((b) => b.id)
  const orphan = Object.keys(RUN).filter((id) => !ids.has(id))
  if (missing.length || orphan.length) {
    throw new Error(
      `slash menu / blocks-manifest drift — missing run: [${missing}], orphan run: [${orphan}]`,
    )
  }
}

const ALL_COMMANDS: SlashCommand[] = [
  // Block items projected from the manifest (labels/hints/keywords/order live
  // there); RUN supplies the insert behaviour per id.
  ...SLASH_BLOCKS.map(
    (b): SlashCommand => ({
      id: b.id,
      label: b.label,
      hint: b.hint,
      keywords: b.keywords,
      run: RUN[b.id],
    }),
  ),
  // Built-in templates (markdown snippets). See milkdown-templates.ts.
  ...TEMPLATES.map(
    (t): SlashCommand => ({
      id: t.id,
      label: t.label,
      hint: 'Template',
      keywords: t.keywords,
      run: (ctx) => insertTemplate(ctx, t.markdown),
    }),
  ),
]

interface SlashState {
  visible: boolean
  query: string
}

// Inspect the current selection and return the slash query if the menu should
// be active, or null otherwise.
function readSlashState(view: ReturnType<typeof usePluginViewContext>['view']):
  | { query: string }
  | null {
  const { selection } = view.state
  const { empty, $from } = selection
  if (!empty) return null
  if ($from.parent.type.name !== 'paragraph') return null
  const text = $from.parent.textBetween(0, $from.parentOffset, undefined, '￼')
  const slashIdx = text.lastIndexOf('/')
  if (slashIdx < 0) return null
  // The `/` must be at the start of the block OR preceded by whitespace, so a
  // slash mid-word doesn't pop the menu.
  if (slashIdx > 0) {
    const prev = text[slashIdx - 1]
    if (prev && !/\s/.test(prev)) return null
  }
  const after = text.slice(slashIdx + 1)
  if (/\s/.test(after)) return null
  return { query: after }
}

function filterCommands(query: string): SlashCommand[] {
  const q = query.trim().toLowerCase()
  if (!q) return ALL_COMMANDS
  return ALL_COMMANDS.filter(
    (c) =>
      c.label.toLowerCase().includes(q) ||
      c.keywords.some((k) => k.toLowerCase().includes(q)),
  )
}

// Insert a fresh `details > details_summary("Click to expand") + paragraph("")`
// node at the cursor and land the caret inside the empty body paragraph so the
// user can immediately start typing. `caret: 'inside'` resolves to the details'
// LAST child — the body paragraph after the summary — via insertBlock.
function insertCollapsible(ctx: Ctx) {
  const view = ctx.get(editorViewCtx)
  const { schema } = view.state
  const detailsType = schema.nodes.details
  const summaryType = schema.nodes.details_summary
  const paragraphType = schema.nodes.paragraph
  if (!detailsType || !summaryType || !paragraphType) return
  const summary = summaryType.create(
    null,
    schema.text(COLLAPSIBLE_DEFAULT_SUMMARY),
  )
  const body = paragraphType.create()
  const details = detailsType.create(null, [summary, body])
  insertBlock(view, details)
}

// Delete the `/query` text in the current paragraph before running a command.
function clearSlashTrigger(ctx: Ctx) {
  const view = ctx.get(editorViewCtx)
  const { state } = view
  const { $from } = state.selection
  const text = $from.parent.textBetween(0, $from.parentOffset, undefined, '￼')
  const slashIdx = text.lastIndexOf('/')
  if (slashIdx < 0) return
  const start = $from.start() + slashIdx
  view.dispatch(state.tr.delete(start, $from.pos))
}

export function SlashView() {
  const ref = useRef<HTMLDivElement>(null)
  const { view } = usePluginViewContext()
  const [loading, getEditor] = useInstance()

  const [{ visible, query }, setSlashState] = useState<SlashState>({
    visible: false,
    query: '',
  })
  const [activeIdx, setActiveIdx] = useState(0)

  const items = useMemo(() => filterCommands(query), [query])

  // Move the menu out of the editor DOM so PM doesn't manage it, mirroring
  // what SlashProvider would do on its first update() call. We don't use
  // SlashProvider here because its internal lodash.debounce reliably gets
  // wedged in our React + Yjs + Vite setup (the 200ms timer keeps resetting
  // and #onUpdate never fires past the initial appendChild), leaving the
  // menu hidden after every `/` press. Positioning + show/hide is simple
  // enough to manage directly via view.coordsAtPos.
  useEffect(() => {
    const el = ref.current
    if (!el) return
    const parent = view.dom.parentElement
    if (parent && el.parentElement !== parent) {
      parent.appendChild(el)
    }
  }, [view])

  // Per-render: read slash state from the live view, then show + position
  // the menu (or hide it). Position is computed from view.coordsAtPos so
  // the menu lands directly below the cursor; a follow-up rAF re-measures
  // and flips upward if the menu would overflow the bottom of the viewport.
  useEffect(() => {
    const el = ref.current
    if (!el) return
    const next = readSlashState(view)
    const shouldShow = next != null && view.hasFocus() && view.editable
    if (!shouldShow) {
      setShow(el, false)
      // eslint-disable-next-line react-hooks/set-state-in-effect -- syncs PM slash state, identity-guarded
      setSlashState((prev) =>
        prev.visible ? { visible: false, query: '' } : prev,
      )
      return
    }
    const { from } = view.state.selection
    const coords = view.coordsAtPos(from)
    setShow(el, true)
    setSlashState((prev) =>
      prev.visible && prev.query === next.query
        ? prev
        : { visible: true, query: next.query },
    )
    // Place below the caret; flip up + clamp into the viewport after measuring.
    return positionFloating(
      el,
      { top: coords.top, bottom: coords.bottom, left: coords.left },
      { place: 'below', align: 'start', clampVertical: true },
    )
  })

  useEffect(() => {
    // eslint-disable-next-line react-hooks/set-state-in-effect -- resets the active row on list change
    setActiveIdx(0)
  }, [query, items.length])

  // Keep the active item in view when arrow-nav moves the selection past
  // the menu's max-height scroll region. 'instant' so rapid arrow-down
  // doesn't visually lag the selection.
  useEffect(() => {
    if (!visible) return
    const container = ref.current
    if (!container) return
    const active = container.querySelector(
      '[aria-selected="true"]',
    ) as HTMLElement | null
    active?.scrollIntoView({ block: 'nearest', behavior: 'instant' })
  }, [activeIdx, visible])

  const runCommand = useCallback(
    (cmd: SlashCommand) => {
      if (loading) return
      // Hide the menu immediately so it doesn't visibly reposition through the
      // multi-step insert transaction before the trigger-clear hides it.
      const el = ref.current
      if (el) setShow(el, false)
      setSlashState((prev) => (prev.visible ? { visible: false, query: '' } : prev))
      const editor = getEditor()
      editor?.action((ctx) => {
        clearSlashTrigger(ctx)
        cmd.run(ctx)
      })
    },
    [loading, getEditor],
  )

  // Capture-phase keydown so we beat ProseMirror's handler when the menu is
  // open. Arrow keys nav within the list; Enter selects.
  useEffect(() => {
    if (!visible) return
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'ArrowDown') {
        e.preventDefault()
        e.stopPropagation()
        setActiveIdx((i) => (items.length === 0 ? 0 : (i + 1) % items.length))
      } else if (e.key === 'ArrowUp') {
        e.preventDefault()
        e.stopPropagation()
        setActiveIdx((i) =>
          items.length === 0 ? 0 : (i - 1 + items.length) % items.length,
        )
      } else if (e.key === 'Enter') {
        const cmd = items[activeIdx]
        if (!cmd) return
        e.preventDefault()
        e.stopPropagation()
        runCommand(cmd)
      }
    }
    document.addEventListener('keydown', onKey, true)
    return () => document.removeEventListener('keydown', onKey, true)
  }, [visible, items, activeIdx, runCommand])

  return (
    <div
      ref={ref}
      role="listbox"
      aria-label="Insert block"
      className="tela-slash-menu"
    >
      {items.length === 0 ? (
        <div className="tela-slash-empty">No matches</div>
      ) : (
        items.map((cmd, idx) => (
          <button
            key={cmd.id}
            type="button"
            role="option"
            aria-selected={idx === activeIdx}
            data-active={idx === activeIdx ? 'true' : 'false'}
            className="tela-slash-item"
            onMouseEnter={() => setActiveIdx(idx)}
            onMouseDown={(e) => {
              e.preventDefault()
              runCommand(cmd)
            }}
          >
            <span className="tela-slash-item-label">{cmd.label}</span>
            <span className="tela-slash-item-hint">{cmd.hint}</span>
          </button>
        ))
      )}
    </div>
  )
}
