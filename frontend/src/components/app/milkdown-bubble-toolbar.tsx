import { useEffect, useRef, useState } from 'react'
import { tooltipFactory } from '@milkdown/kit/plugin/tooltip'
import { usePluginViewContext } from '@prosemirror-adapter/react'
import { useInstance } from '@milkdown/react'
import { commandsCtx, editorViewCtx } from '@milkdown/kit/core'
import type { CmdKey } from '@milkdown/kit/core'
import type { Ctx, SliceType } from '@milkdown/ctx'
import { $ctx } from '@milkdown/kit/utils'
import { NodeSelection } from '@milkdown/kit/prose/state'
import type { EditorState } from '@milkdown/kit/prose/state'
import type { EditorView } from '@milkdown/kit/prose/view'
import {
  emphasisKeymap,
  inlineCodeKeymap,
  strongKeymap,
  toggleEmphasisCommand,
  toggleInlineCodeCommand,
  toggleLinkCommand,
  toggleStrongCommand,
  updateLinkCommand,
} from '@milkdown/kit/preset/commonmark'
import { strikethroughKeymap, toggleStrikethroughCommand } from '@milkdown/kit/preset/gfm'
import { toggleHighlightCommand } from './milkdown-highlight'
import {
  Bold,
  Check,
  ChevronDown,
  Code,
  Highlighter,
  Italic,
  Link as LinkIcon,
  MessageSquare,
  Strikethrough,
  Type,
} from 'lucide-react'
import { Input } from '../ui/input'
import { Tooltip, TooltipContent, TooltipTrigger } from '../ui/tooltip'
import { positionFloating, setShow } from './milkdown-floating'
import { TURN_INTO, currentTurnInto, type TurnIntoOption } from './milkdown-turn-into'
import { formatShortcut } from '../../lib/useGlobalShortcut'
import { cn } from '../../lib/utils'

// eslint-disable-next-line react-refresh/only-export-components -- milkdown plugin slice lives with its view
export const bubblePlugin = tooltipFactory('tela-bubble')

// Set by the editor host when comments are on for this page: the bubble's
// Comment button calls it (the host opens the comments panel on the live
// selection). Null hides the button.
// eslint-disable-next-line react-refresh/only-export-components -- milkdown ctx slice lives with its view
export const commentSelectionCtx = $ctx<(() => void) | null, 'commentSelection'>(
  null,
  'commentSelection',
)

// A mark keymap slice (strongKeymap.key etc.): its first shortcut, formatted.
type KeymapConfig = Record<string, { shortcuts: string | string[] }>
function shortcutOf<T extends KeymapConfig, N extends string>(
  ctx: Ctx,
  slice: SliceType<T, N>,
): string | undefined {
  const first = Object.values(ctx.get(slice))[0]
  const key = Array.isArray(first?.shortcuts) ? first.shortcuts[0] : first?.shortcuts
  return key ? formatShortcut(key) : undefined
}

// Selection bubble-toolbar. Appears above a non-empty text selection and lets
// the user change the block type (the shared Turn-into list), apply inline
// marks (bold / italic / code / strikethrough / highlight / link) and start a
// comment, without knowing the markdown syntax: the Medium/Notion gesture.
// Everything is an existing command, so nothing about the canonical markdown
// changes. Tooltips show each mark's shortcut, read from Milkdown's keymap.
//
// Like SlashView, we DON'T use Milkdown's TooltipProvider helper: its internal
// lodash.debounce wedges under our React + Yjs + Vite setup (same failure mode
// documented on the slash menu). Show/hide + positioning is managed directly
// from a no-dependency effect that re-reads the live view on every render.

interface ActiveMarks {
  strong: boolean
  emphasis: boolean
  inlineCode: boolean
  strike: boolean
  highlight: boolean
  link: boolean
}

const NO_MARKS: ActiveMarks = {
  strong: false,
  emphasis: false,
  inlineCode: false,
  strike: false,
  highlight: false,
  link: false,
}

function rangeHasMark(state: EditorState, markName: string): boolean {
  const type = state.schema.marks[markName]
  if (!type) return false
  const { from, to } = state.selection
  return state.doc.rangeHasMark(from, to, type)
}

function computeActive(state: EditorState): ActiveMarks {
  return {
    strong: rangeHasMark(state, 'strong'),
    emphasis: rangeHasMark(state, 'emphasis'),
    inlineCode: rangeHasMark(state, 'inlineCode'),
    strike: rangeHasMark(state, 'strike_through'),
    highlight: rangeHasMark(state, 'highlight'),
    link: rangeHasMark(state, 'link'),
  }
}

function sameActive(a: ActiveMarks, b: ActiveMarks): boolean {
  return (
    a.strong === b.strong &&
    a.emphasis === b.emphasis &&
    a.inlineCode === b.inlineCode &&
    a.strike === b.strike &&
    a.highlight === b.highlight &&
    a.link === b.link
  )
}

// The href on the first link mark inside the selection (for prefilling the
// edit field when the selection already sits on a link).
function currentLinkHref(state: EditorState): string {
  const type = state.schema.marks.link
  if (!type) return ''
  const { from, to } = state.selection
  let href = ''
  state.doc.nodesBetween(from, to, (node) => {
    const mark = node.marks.find((m) => m.type === type)
    if (mark) href = (mark.attrs.href as string) ?? ''
  })
  return href
}

function shouldShow(view: EditorView): boolean {
  if (!view.editable || !view.hasFocus()) return false
  const sel = view.state.selection
  if (sel.empty) return false
  // A node selection (e.g. an image/excalidraw atom) isn't a text range.
  if (sel instanceof NodeSelection) return false
  // Marks are meaningless inside a code block (`spec.code`); don't offer them.
  // (`$from.parent` is the block at the selection start; for a multi-block /
  // select-all range this still correctly skips a code-block-only selection.)
  if (sel.$from.parent.type.spec.code) return false
  return true
}

export function BubbleToolbarView() {
  const ref = useRef<HTMLDivElement>(null)
  const inputRef = useRef<HTMLInputElement>(null)
  const { view } = usePluginViewContext()
  const [loading, getEditor] = useInstance()

  const [active, setActive] = useState<ActiveMarks>(NO_MARKS)
  // When set, the toolbar shows a URL field instead of the mark buttons. It
  // stays open while the field is focused (the editor blurs, but the PM
  // selection persists, so the applied mark lands on the right range).
  const [linkMode, setLinkMode] = useState(false)
  const [linkValue, setLinkValue] = useState('')
  // Block type of the selection (null = none of the Turn-into options, e.g. a
  // callout or table cell) and whether its dropdown is open.
  const [blockType, setBlockType] = useState<TurnIntoOption | null>(null)
  const [menuOpen, setMenuOpen] = useState(false)

  // Reparent out of the editor DOM so PM doesn't manage the node (mirrors
  // SlashView). Done once per view.
  useEffect(() => {
    const el = ref.current
    if (!el) return
    const parent = view.dom.parentElement
    if (parent && el.parentElement !== parent) parent.appendChild(el)
  }, [view])

  // No-dependency effect: runs after every render, re-reads the live view, and
  // shows + positions the toolbar above the selection (or hides it). While the
  // link field is open we keep it visible regardless of focus/selection.
  useEffect(() => {
    const el = ref.current
    if (!el) return
    if (linkMode) {
      setShow(el, true)
      return
    }
    if (!shouldShow(view)) {
      setShow(el, false)
      // eslint-disable-next-line react-hooks/set-state-in-effect -- closes the dropdown with the bubble; a no-op when already closed
      setMenuOpen(false)
      return
    }
    setShow(el, true)
    // Syncs PM selection state, identity-guarded (no re-render when unchanged).
    setActive((prev) => {
      const next = computeActive(view.state)
      return sameActive(prev, next) ? prev : next
    })
    // TURN_INTO entries are stable objects, so identity is the equality check.
    setBlockType(currentTurnInto(view.state))
    const { from, to } = view.state.selection
    let start
    let end
    try {
      start = view.coordsAtPos(from)
      end = view.coordsAtPos(to)
    } catch {
      return
    }
    const centerLeft = (start.left + end.left) / 2
    // Sit above the selection (centered); flip below when there's no room.
    return positionFloating(
      el,
      { top: start.top, bottom: end.bottom, left: centerLeft },
      { place: 'above', gap: 8, align: 'center' },
    )
  })

  // Focus the URL field when link mode opens.
  useEffect(() => {
    if (linkMode) inputRef.current?.focus()
  }, [linkMode])

  function runAction(fn: (ctx: Ctx) => void) {
    if (loading) return
    getEditor()?.action((ctx) => fn(ctx))
  }

  function toggleMark(key: CmdKey<unknown>) {
    // onMouseDown already prevented default, so the editor keeps focus and the
    // selection survives — the command applies to the live selection.
    runAction((ctx) => ctx.get(commandsCtx).call(key))
  }

  function openLinkMode() {
    runAction((ctx) => setLinkValue(currentLinkHref(ctx.get(editorViewCtx).state)))
    setLinkMode(true)
  }

  function applyLink() {
    const href = linkValue.trim()
    runAction((ctx) => {
      const commands = ctx.get(commandsCtx)
      if (!href) {
        if (active.link) commands.call(toggleLinkCommand.key) // remove
      } else if (active.link) {
        commands.call(updateLinkCommand.key, { href })
      } else {
        commands.call(toggleLinkCommand.key, { href })
      }
      ctx.get(editorViewCtx).focus()
    })
    setLinkMode(false)
    setLinkValue('')
  }

  function cancelLink() {
    setLinkMode(false)
    setLinkValue('')
    runAction((ctx) => ctx.get(editorViewCtx).focus())
  }

  function turnInto(option: TurnIntoOption) {
    runAction(option.run)
    setMenuOpen(false)
  }

  // Read from the editor ctx on render: each mark's live shortcut and the
  // host's comment handler (null until the editor has loaded).
  const editorCtx = loading ? null : (getEditor()?.ctx ?? null)
  const keyOf = <T extends KeymapConfig, N extends string>(slice: SliceType<T, N>) =>
    editorCtx ? shortcutOf(editorCtx, slice) : undefined
  const onComment = editorCtx?.get(commentSelectionCtx.key) ?? null
  const BlockIcon = blockType?.icon ?? Type

  return (
    <div
      ref={ref}
      role="toolbar"
      aria-label="Format selection"
      className="tela-bubble-toolbar"
    >
      {linkMode ? (
        <Input
          ref={inputRef}
          size="sm"
          type="url"
          placeholder="Paste or type a link, then Enter"
          aria-label="Link URL"
          className="tela-bubble-link-input"
          value={linkValue}
          onChange={(e) => setLinkValue(e.target.value)}
          onKeyDown={(e) => {
            if (e.key === 'Enter') {
              e.preventDefault()
              applyLink()
            } else if (e.key === 'Escape') {
              e.preventDefault()
              cancelLink()
            }
          }}
          onBlur={cancelLink}
        />
      ) : (
        <>
          <div className="tela-bubble-menu-anchor">
            <BubbleButton
              label="Turn into"
              wide
              expanded={menuOpen}
              onClick={() => setMenuOpen((open) => !open)}
            >
              <BlockIcon size="1em" strokeWidth={2.5} aria-hidden />
              <span className="tela-bubble-label">{blockType?.label ?? 'Turn into'}</span>
              <ChevronDown className="tela-bubble-chevron" size="1em" aria-hidden />
            </BubbleButton>
            {menuOpen ? (
              <div className="tela-block-menu tela-bubble-menu" role="menu">
                <div className="tela-block-menu-label">Turn into</div>
                {TURN_INTO.map((option) => {
                  const Icon = option.icon
                  const current = option === blockType
                  return (
                    <button
                      key={option.id}
                      type="button"
                      role="menuitemradio"
                      aria-checked={current}
                      className="tela-block-menu-item"
                      onMouseDown={(e) => {
                        e.preventDefault()
                        turnInto(option)
                      }}
                    >
                      <Icon size="1em" aria-hidden />
                      <span>{option.label}</span>
                      {current ? <Check className="tela-bubble-menu-check" size="1em" aria-hidden /> : null}
                    </button>
                  )
                })}
              </div>
            ) : null}
          </div>
          <span className="tela-bubble-sep" aria-hidden />
          <BubbleButton
            label="Bold"
            shortcut={keyOf(strongKeymap.key)}
            active={active.strong}
            onClick={() => toggleMark(toggleStrongCommand.key)}
          >
            <Bold size="1em" strokeWidth={2.5} aria-hidden />
          </BubbleButton>
          <BubbleButton
            label="Italic"
            shortcut={keyOf(emphasisKeymap.key)}
            active={active.emphasis}
            onClick={() => toggleMark(toggleEmphasisCommand.key)}
          >
            <Italic size="1em" strokeWidth={2.5} aria-hidden />
          </BubbleButton>
          <BubbleButton
            label="Strikethrough"
            shortcut={keyOf(strikethroughKeymap.key)}
            active={active.strike}
            onClick={() => toggleMark(toggleStrikethroughCommand.key)}
          >
            <Strikethrough size="1em" strokeWidth={2.5} aria-hidden />
          </BubbleButton>
          <BubbleButton
            label="Inline code"
            shortcut={keyOf(inlineCodeKeymap.key)}
            active={active.inlineCode}
            onClick={() => toggleMark(toggleInlineCodeCommand.key)}
          >
            <Code size="1em" strokeWidth={2.5} aria-hidden />
          </BubbleButton>
          <BubbleButton
            label="Highlight"
            active={active.highlight}
            onClick={() => toggleMark(toggleHighlightCommand.key)}
          >
            <Highlighter size="1em" strokeWidth={2.5} aria-hidden />
          </BubbleButton>
          <span className="tela-bubble-sep" aria-hidden />
          <BubbleButton label="Link" active={active.link} onClick={openLinkMode}>
            <LinkIcon size="1em" strokeWidth={2.5} aria-hidden />
          </BubbleButton>
          {onComment ? (
            <>
              <span className="tela-bubble-sep" aria-hidden />
              <BubbleButton label="Comment" wide onClick={onComment}>
                <MessageSquare size="1em" strokeWidth={2.5} aria-hidden />
                <span className="tela-bubble-label">Comment</span>
              </BubbleButton>
            </>
          ) : null}
        </>
      )}
    </div>
  )
}

interface BubbleButtonProps {
  label: string
  // Mark state (aria-pressed). Omitted for action buttons.
  active?: boolean
  // Set for a button that opens a menu (aria-expanded instead of pressed).
  expanded?: boolean
  // Carries a visible text label next to the icon (hidden on narrow screens).
  wide?: boolean
  shortcut?: string
  onClick: () => void
  children: React.ReactNode
}

function BubbleButton({ label, active, expanded, wide, shortcut, onClick, children }: BubbleButtonProps) {
  return (
    <Tooltip>
      <TooltipTrigger asChild>
        <button
          type="button"
          className={cn('tela-bubble-btn', wide && 'tela-bubble-btn--wide')}
          // A wide button is named by its visible text; the tooltip says what it does.
          aria-label={wide ? undefined : label}
          aria-pressed={active}
          aria-haspopup={expanded === undefined ? undefined : 'menu'}
          aria-expanded={expanded}
          data-active={active || expanded ? 'true' : 'false'}
          // preventDefault keeps the editor focused + selection intact through the
          // click, so the command lands on the selected range.
          onMouseDown={(e) => {
            e.preventDefault()
            onClick()
          }}
        >
          {children}
        </button>
      </TooltipTrigger>
      <TooltipContent side="top">
        {label}
        {shortcut ? <kbd className="tela-tooltip-kbd">{shortcut}</kbd> : null}
      </TooltipContent>
    </Tooltip>
  )
}
