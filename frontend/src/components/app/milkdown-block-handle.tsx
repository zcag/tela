import { useEffect, useRef, useState } from 'react'
import { createPortal } from 'react-dom'
import { usePluginViewContext } from '@prosemirror-adapter/react'
import { useInstance } from '@milkdown/react'
import { BlockProvider } from '@milkdown/kit/plugin/block'
import { editorViewCtx } from '@milkdown/kit/core'
import { TextSelection } from '@milkdown/kit/prose/state'
import type { Ctx } from '@milkdown/ctx'
import { Copy, GripVertical, Plus, Trash2 } from 'lucide-react'
import { TURN_INTO, type TurnIntoOption } from './milkdown-turn-into'

// Block drag-handle + add-block gutter (the Notion/GitBook left-gutter pattern).
// Built on Milkdown's `block` plugin, which tracks the block under the cursor
// and turns the handle DOM into a drag source — drag-to-reorder is wired by
// BlockProvider (it calls blockService.addEvent(content) + sets draggable).
// Reordering just rewrites the markdown in the new order; no block table.
//
// The gutter has two controls:
//   +  — insert an empty paragraph below the block and open the slash menu
//        (mirrors Notion's "+"; we seed a "/" so the palette appears).
//   ⋮⋮ — drag to move the block (handled by the block service); click opens a
//        block-action menu (Duplicate / Delete; Turn into is added separately).
//
// The block service self-gates on `view.editable` (block-service mousemove
// callback bails when not editable), so the handle never appears in viewer /
// share / disconnected modes even though the plugin is mounted.

interface BlockMenuState {
  left: number
  top: number
  from: number
  nodeSize: number
}

export function BlockHandleView() {
  const handleRef = useRef<HTMLDivElement>(null)
  const providerRef = useRef<BlockProvider | null>(null)
  // Subscribe to plugin-view updates: the adapter re-renders this component on
  // every PM view update, which re-runs the reposition effect below. We don't
  // read the value — the subscription (and thus the re-render) is the point.
  usePluginViewContext()
  const [loading, getEditor] = useInstance()
  const [menu, setMenu] = useState<BlockMenuState | null>(null)

  useEffect(() => {
    if (loading || !handleRef.current) return
    const editor = getEditor()
    if (!editor) return
    const provider = new BlockProvider({
      ctx: editor.ctx,
      content: handleRef.current,
      // Fixed strategy → floating-ui writes viewport coords, so the handle
      // doesn't depend on a positioned offsetParent (mirrors the slash menu).
      floatingUIOptions: { strategy: 'fixed' },
      getOffset: () => 4,
      // Anchor the handle to the editor's LEFT content edge — one consistent
      // gutter for every block. Milkdown otherwise anchors to the hovered
      // block's own left, which for indented list items lands on top of the
      // bullet marker. We keep the block's vertical extent, only override left.
      getPosition: ({ active, editorDom }) => {
        const block = (active.el as HTMLElement).getBoundingClientRect()
        const ed = editorDom.getBoundingClientRect()
        const padLeft = parseFloat(getComputedStyle(editorDom).paddingLeft) || 0
        return new DOMRect(ed.left + padLeft, block.top, 0, block.height)
      },
    })
    providerRef.current = provider
    return () => {
      provider.destroy()
      providerRef.current = null
    }
  }, [loading, getEditor])

  // Reposition on every view update (selection / doc / hovered block change).
  // NOTE: must run unconditionally — BlockProvider.update() is also what SHOWS
  // the handle (sets data-show) when the pointer enters a block's gutter, so
  // gating it on visibility would mean a hidden handle never appears.
  useEffect(() => {
    providerRef.current?.update()
  })

  // Hide the handle on scroll. BlockProvider only repositions on its throttled
  // mousemove callback, so a scroll never fires it — the `position: fixed`
  // handle would otherwise freeze at its viewport coords while the content
  // scrolls underneath. We listen on the page-scroll container (the <main
  // data-page-scroll> in router.tsx, which has overflow-y-auto) and hide; the
  // handle re-appears on the next mousemove, which is the correct Notion UX.
  useEffect(() => {
    if (loading || !handleRef.current) return
    const scroller = handleRef.current.closest('[data-page-scroll]')
    if (!scroller) return
    const onScroll = () => providerRef.current?.hide()
    scroller.addEventListener('scroll', onScroll, { passive: true })
    return () => scroller.removeEventListener('scroll', onScroll)
  }, [loading])

  // Dismiss the action menu on outside-click or Escape.
  useEffect(() => {
    if (!menu) return
    const onDown = (e: MouseEvent) => {
      const target = e.target
      if (target instanceof HTMLElement && target.closest('.tela-block-menu')) {
        return
      }
      setMenu(null)
    }
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') setMenu(null)
    }
    document.addEventListener('mousedown', onDown, true)
    document.addEventListener('keydown', onKey, true)
    return () => {
      document.removeEventListener('mousedown', onDown, true)
      document.removeEventListener('keydown', onKey, true)
    }
  }, [menu])

  function run(fn: (ctx: Ctx) => void) {
    if (loading) return
    getEditor()?.action((ctx) => fn(ctx))
  }

  // Active block, as a stable [from, from+nodeSize] range. `$pos.pos` is the
  // position immediately before the node (the block service builds a
  // NodeSelection there).
  function activeRange(): { from: number; nodeSize: number } | null {
    const active = providerRef.current?.active
    if (!active) return null
    return { from: active.$pos.pos, nodeSize: active.node.nodeSize }
  }

  function onAdd() {
    const range = activeRange()
    if (!range) return
    run((ctx) => {
      const editorView = ctx.get(editorViewCtx)
      const { state } = editorView
      const paragraph = state.schema.nodes.paragraph?.create()
      if (!paragraph) return
      const at = range.from + range.nodeSize
      let tr = state.tr.insert(at, paragraph)
      // Caret into the new paragraph, then seed "/" so the slash menu opens.
      tr = tr
        .setSelection(TextSelection.create(tr.doc, at + 1))
        .insertText('/')
      editorView.dispatch(tr.scrollIntoView())
      editorView.focus()
    })
  }

  function openMenu(e: React.MouseEvent) {
    const range = activeRange()
    if (!range) return
    const rect = (e.currentTarget as HTMLElement).getBoundingClientRect()
    setMenu({
      left: rect.right + 4,
      top: rect.top,
      from: range.from,
      nodeSize: range.nodeSize,
    })
  }

  function applyTurnInto(option: TurnIntoOption) {
    if (!menu) return
    const { from } = menu
    run((ctx) => {
      const editorView = ctx.get(editorViewCtx)
      // Move the caret into the block first; the commonmark commands act on
      // the current selection's textblock.
      const pos = Math.min(from + 1, editorView.state.doc.content.size)
      editorView.dispatch(
        editorView.state.tr.setSelection(
          TextSelection.create(editorView.state.doc, pos),
        ),
      )
      option.run(ctx)
      editorView.focus()
    })
    setMenu(null)
  }

  function onDuplicate() {
    if (!menu) return
    run((ctx) => {
      const editorView = ctx.get(editorViewCtx)
      const node = editorView.state.doc.nodeAt(menu.from)
      if (!node) return
      editorView.dispatch(
        editorView.state.tr.insert(menu.from + menu.nodeSize, node),
      )
      editorView.focus()
    })
    setMenu(null)
  }

  function onDelete() {
    if (!menu) return
    run((ctx) => {
      const editorView = ctx.get(editorViewCtx)
      editorView.dispatch(
        editorView.state.tr.delete(menu.from, menu.from + menu.nodeSize),
      )
      editorView.focus()
    })
    setMenu(null)
  }

  return (
    <>
      {/* Single root for the adapter: BlockProvider relocates this node into
          the editor wrapper, so it must have no React-managed siblings here
          (the menu is portaled to body instead, avoiding reconciliation on a
          moved node). */}
      <div ref={handleRef} className="tela-block-handle" data-show="false">
        <button
          type="button"
          className="tela-block-handle-btn"
          aria-label="Add block below"
          title="Add block below"
          draggable={false}
          // Don't let the + initiate a block drag (drag lives on the grip).
          onMouseDown={(e) => e.stopPropagation()}
          onClick={onAdd}
        >
          <Plus size="1em" strokeWidth={2.5} aria-hidden />
        </button>
        <button
          type="button"
          className="tela-block-handle-btn tela-block-handle-grip"
          aria-label="Drag to move; click for actions"
          title="Drag to move · click for actions"
          onClick={openMenu}
        >
          <GripVertical size="1em" strokeWidth={2.5} aria-hidden />
        </button>
      </div>
      {menu
        ? createPortal(
            <div
              className="tela-block-menu"
              role="menu"
              style={{ left: `${menu.left}px`, top: `${menu.top}px` }}
            >
              <div className="tela-block-menu-label">Turn into</div>
              {TURN_INTO.map((option) => {
                const Icon = option.icon
                return (
                  <button
                    key={option.id}
                    type="button"
                    role="menuitem"
                    className="tela-block-menu-item"
                    onClick={() => applyTurnInto(option)}
                  >
                    <Icon size="1em" aria-hidden />
                    <span>{option.label}</span>
                  </button>
                )
              })}
              <div className="tela-block-menu-sep" role="separator" />
              <button
                type="button"
                role="menuitem"
                className="tela-block-menu-item"
                onClick={onDuplicate}
              >
                <Copy size="1em" aria-hidden />
                <span>Duplicate</span>
              </button>
              <button
                type="button"
                role="menuitem"
                className="tela-block-menu-item"
                onClick={onDelete}
              >
                <Trash2 size="1em" aria-hidden />
                <span>Delete</span>
              </button>
            </div>,
            document.body,
          )
        : null}
    </>
  )
}
