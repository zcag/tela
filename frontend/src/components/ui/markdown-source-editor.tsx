import { useEffect, useRef } from 'react'
import { EditorState } from '@codemirror/state'
import { EditorView, keymap, placeholder as placeholderExt } from '@codemirror/view'
import { defaultKeymap, history, historyKeymap, indentWithTab } from '@codemirror/commands'
import { HighlightStyle, syntaxHighlighting } from '@codemirror/language'
import { markdownKeymap, markdownLanguage } from '@codemirror/lang-markdown'
import { tags as t } from '@lezer/highlight'
import { cn } from '../../lib/utils'

// Plain-text markdown editor with syntax highlighting (CodeMirror 6). The
// surface for editing a page body as raw source: deck bodies (Slidev syntax
// the rich editor would mangle) and the prose editor's markdown source mode.
// Deliberately dumb: text in, text out. It knows nothing about pages, saving
// or collab, and adds no tela-specific syntax, so it only changes when
// CodeMirror does. Colors come from the --syntax-* tokens Prism code blocks use.

const highlight = HighlightStyle.define([
  { tag: t.heading, color: 'var(--text-primary)', fontWeight: '600' },
  { tag: [t.processingInstruction, t.contentSeparator, t.punctuation], color: 'var(--syntax-punctuation)' },
  { tag: t.emphasis, fontStyle: 'italic' },
  { tag: t.strong, fontWeight: '600' },
  { tag: t.strikethrough, textDecoration: 'line-through' },
  { tag: [t.link, t.url], color: 'var(--syntax-function)' },
  { tag: t.monospace, color: 'var(--syntax-string)' },
  { tag: [t.quote, t.comment], color: 'var(--syntax-comment)' },
  { tag: [t.tagName, t.angleBracket], color: 'var(--syntax-tag)' },
  { tag: t.attributeName, color: 'var(--syntax-attr)' },
  { tag: t.meta, color: 'var(--syntax-keyword)' },
])

const theme = EditorView.theme({
  '&': { color: 'var(--text-primary)', backgroundColor: 'transparent', fontSize: 'var(--text-sm)', minHeight: 'inherit' },
  '&.cm-focused': { outline: 'none' },
  '.cm-scroller': { fontFamily: 'var(--font-mono)', lineHeight: 'var(--leading-relaxed)', minHeight: 'inherit' },
  '.cm-content': { padding: 'var(--space-2)', caretColor: 'var(--text-primary)', minHeight: 'inherit' },
  '.cm-line': { padding: '0' },
  '.cm-placeholder': { color: 'var(--text-muted)' },
})

export interface MarkdownSourceEditorProps {
  value: string
  onChange: (next: string) => void
  onBlur?: () => void
  // Mod-s. The browser's save-page dialog is suppressed either way.
  onSave?: () => void
  autoFocus?: boolean
  placeholder?: string
  ariaLabel?: string
  className?: string
}

export function MarkdownSourceEditor({
  value,
  onChange,
  onBlur,
  onSave,
  autoFocus,
  placeholder,
  ariaLabel,
  className,
}: MarkdownSourceEditorProps) {
  const hostRef = useRef<HTMLDivElement>(null)
  const viewRef = useRef<EditorView | null>(null)
  // Callbacks are read through a ref so the view is built once, not per render.
  const cb = useRef({ onChange, onBlur, onSave })
  useEffect(() => {
    cb.current = { onChange, onBlur, onSave }
  })

  useEffect(() => {
    const view = new EditorView({
      parent: hostRef.current!,
      state: EditorState.create({
        doc: value,
        extensions: [
          history(),
          keymap.of([
            { key: 'Mod-s', preventDefault: true, run: () => (cb.current.onSave?.(), true) },
            indentWithTab,
            ...markdownKeymap,
            ...defaultKeymap,
            ...historyKeymap,
          ]),
          // The Language itself, not markdown(): that helper statically pulls in
          // lang-html (plus its CSS/JS grammars) for embedded HTML, 3x the bundle.
          markdownLanguage,
          syntaxHighlighting(highlight),
          EditorView.lineWrapping,
          theme,
          placeholderExt(placeholder ?? ''),
          EditorView.contentAttributes.of({ 'aria-label': ariaLabel ?? 'Markdown source', spellcheck: 'false' }),
          EditorView.updateListener.of((u) => {
            if (u.docChanged) cb.current.onChange(u.state.doc.toString())
          }),
          EditorView.domEventHandlers({ blur: () => void cb.current.onBlur?.() }),
        ],
      }),
    })
    viewRef.current = view
    if (autoFocus) view.focus()
    return () => {
      view.destroy()
      viewRef.current = null
    }
    // Built once per mount; `value` is synced below, the rest is static config.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  // Controlled: adopt a value set from outside (e.g. reloading the saved body).
  // Typing round-trips through onChange, so this is a no-op then.
  useEffect(() => {
    const view = viewRef.current
    if (!view) return
    const current = view.state.doc.toString()
    if (value !== current) view.dispatch({ changes: { from: 0, to: current.length, insert: value } })
  }, [value])

  return <div ref={hostRef} className={cn('min-w-0', className)} />
}

export default MarkdownSourceEditor
