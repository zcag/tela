import { $inputRule, $nodeSchema, $prose, $remark } from '@milkdown/kit/utils'
import { InputRule } from '@milkdown/kit/prose/inputrules'
import { Plugin } from '@milkdown/kit/prose/state'
import { editorViewCtx } from '@milkdown/kit/core'
import type { Ctx } from '@milkdown/ctx'
import type { Node as ProseNode } from '@milkdown/kit/prose/model'
import type { EditorView } from '@milkdown/kit/prose/view'
import katex from 'katex'
import remarkMath from 'remark-math'
import { insertBlock } from '../../lib/milkdown/insert-block'
import { MATH_OPTIONS } from '../../lib/markdown/math-options'

// Math / LaTeX. `$$inline$$` and `$$block$$` round-trip as canonical markdown:
// remark-math parses them into mdast `inlineMath` / `math` nodes (and its
// mdast-util-math companion serializes back), so the only thing tela adds is
// the PM schema + KaTeX rendering. Single-`$` inline math is OFF — see
// `lib/markdown/math-options.ts` for why, and note the option must stay shared
// with the view stack or read and edit disagree about the same body. (The official @milkdown/plugin-math is
// abandoned at 7.5.x vs our kit 7.21 — this hand-build mirrors how callouts /
// excalidraw are wired.) KaTeX's stylesheet is imported once in main.tsx.
//
// Both math nodes are leaf atoms storing the LaTeX in a `value` attr. toDOM
// renders KaTeX (used for clipboard + read-only/share). The nodeView below
// adds click-to-edit in editable mode: selecting the atom swaps the render for
// a text field; blur/Enter commits via setNodeMarkup.

function renderKatex(value: string, displayMode: boolean): string {
  return katex.renderToString(value || '', {
    displayMode,
    throwOnError: false,
    output: 'html',
  })
}

interface MdastMath {
  type: string
  value?: string
}

export const mathRemarkPlugin = $remark('telaMath', () => remarkMath, MATH_OPTIONS)

export const mathInlineSchema = $nodeSchema('math_inline', () => ({
  group: 'inline',
  inline: true,
  atom: true,
  selectable: true,
  attrs: { value: { default: '' } },
  parseDOM: [
    {
      tag: 'span[data-math-inline]',
      getAttrs: (dom) => ({
        value: dom instanceof HTMLElement ? (dom.dataset.value ?? '') : '',
      }),
    },
  ],
  toDOM: (node) => {
    const span = document.createElement('span')
    span.className = 'tela-math-inline'
    span.setAttribute('data-math-inline', '')
    span.dataset.value = node.attrs.value
    span.innerHTML = renderKatex(node.attrs.value, false)
    return span
  },
  parseMarkdown: {
    match: ({ type }) => type === 'inlineMath',
    runner: (state, node, type) => {
      state.addNode(type, { value: (node as MdastMath).value ?? '' })
    },
  },
  toMarkdown: {
    match: (node) => node.type.name === 'math_inline',
    runner: (state, node) => {
      state.addNode('inlineMath', undefined, node.attrs.value as string)
    },
  },
}))

export const mathBlockSchema = $nodeSchema('math_block', () => ({
  group: 'block',
  atom: true,
  selectable: true,
  attrs: { value: { default: '' } },
  parseDOM: [
    {
      tag: 'div[data-math-block]',
      getAttrs: (dom) => ({
        value: dom instanceof HTMLElement ? (dom.dataset.value ?? '') : '',
      }),
    },
  ],
  toDOM: (node) => {
    const div = document.createElement('div')
    div.className = 'tela-math-block'
    div.setAttribute('data-math-block', '')
    div.dataset.value = node.attrs.value
    div.innerHTML = renderKatex(node.attrs.value, true)
    return div
  },
  parseMarkdown: {
    match: ({ type }) => type === 'math',
    runner: (state, node, type) => {
      state.addNode(type, { value: (node as MdastMath).value ?? '' })
    },
  },
  toMarkdown: {
    match: (node) => node.type.name === 'math_block',
    runner: (state, node) => {
      state.addNode('math', undefined, node.attrs.value as string)
    },
  },
}))

// NodeView factory shared by inline + block. Renders KaTeX; in editable mode,
// selecting the atom (click) reveals a text field prefilled with the LaTeX,
// committed on blur / Enter (Shift-Enter allows multi-line in block math).
function mathNodeView(displayMode: boolean) {
  return (node: ProseNode, view: EditorView, getPos: () => number | undefined) => {
    const tag = displayMode ? 'div' : 'span'
    const dom = document.createElement(tag)
    dom.className = displayMode ? 'tela-math-block' : 'tela-math-inline'
    let current = node
    let editing = false

    const renderRendered = () => {
      dom.innerHTML = renderKatex(current.attrs.value, displayMode)
      if (!current.attrs.value) {
        dom.classList.add('tela-math-empty')
        dom.textContent = displayMode ? 'Empty equation' : 'math'
      } else {
        dom.classList.remove('tela-math-empty')
      }
    }

    const commit = (value: string) => {
      editing = false
      const pos = getPos()
      if (pos == null) {
        renderRendered()
        return
      }
      view.dispatch(
        view.state.tr.setNodeMarkup(pos, undefined, {
          ...current.attrs,
          value,
        }),
      )
      view.focus()
    }

    const renderEditing = () => {
      dom.innerHTML = ''
      dom.classList.remove('tela-math-empty')
      const input = document.createElement(displayMode ? 'textarea' : 'input') as
        | HTMLTextAreaElement
        | HTMLInputElement
      input.className = 'tela-math-input'
      input.value = current.attrs.value
      dom.appendChild(input)
      // Defer focus so PM finishes its selection dispatch first.
      requestAnimationFrame(() => input.focus())
      const onKeyDown = (e: KeyboardEvent) => {
        if (e.key === 'Enter' && !(displayMode && e.shiftKey)) {
          e.preventDefault()
          commit(input.value)
        } else if (e.key === 'Escape') {
          e.preventDefault()
          editing = false
          renderRendered()
          view.focus()
        }
      }
      input.addEventListener('blur', () => commit(input.value))
      input.addEventListener('keydown', onKeyDown as EventListener)
    }

    renderRendered()

    return {
      dom,
      // KaTeX injects a large DOM subtree and the edit field replaces it;
      // never let PM's MutationObserver try to reconcile any of it.
      ignoreMutation: () => true,
      // While editing, the text field owns keyboard/mouse events.
      stopEvent: () => editing,
      selectNode: () => {
        if (!view.editable) return
        editing = true
        renderEditing()
      },
      deselectNode: () => {
        if (editing) {
          editing = false
          renderRendered()
        }
      },
      update: (updated: ProseNode) => {
        if (updated.type !== current.type) return false
        current = updated
        if (!editing) renderRendered()
        return true
      },
    }
  }
}

export const mathNodeViews = $prose(() => {
  return new Plugin({
    props: {
      nodeViews: {
        math_inline: mathNodeView(false),
        math_block: mathNodeView(true),
      },
    },
  })
})

// `$x^2$` → inline math, kept as a TYPING convenience only: the node
// serializes as `$$x^2$$`, which is what the parser reads back now that
// single-`$` math is off. Requires a non-space immediately inside each `$`, so
// currency ("$5 and $10") still doesn't convert. Fires on the closing `$`.
export const mathInlineInputRule = $inputRule((ctx) => {
  return new InputRule(
    /\$([^\s$](?:[^$]*[^\s$])?)\$$/,
    (state, match, start, end) => {
      const value = match[1]
      if (!value) return null
      const node = mathInlineSchema.type(ctx).create({ value })
      return state.tr.replaceRangeWith(start, end, node)
    },
  )
})

// `$$ ` at the start of an empty paragraph → block math (then click to edit).
export const mathBlockInputRule = $inputRule((ctx) => {
  return new InputRule(/^\$\$\s$/, (state, _match, start, end) => {
    const node = mathBlockSchema.type(ctx).create({ value: '' })
    return state.tr.replaceRangeWith(start, end, node)
  })
})

// Slash-menu inserter for a block equation.
export function insertMathBlock(ctx: Ctx) {
  const view = ctx.get(editorViewCtx)
  const node = mathBlockSchema.type(ctx).create({ value: '' })
  insertBlock(view, node, { caret: 'none' })
}
