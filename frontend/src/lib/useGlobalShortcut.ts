import { useEffect, useRef } from 'react'

// Modifier-prefixed combo string: 'mod+k', 'mod+shift+p', 'mod+n'. `mod` matches
// either Cmd (macOS) or Ctrl (Windows/Linux). Keys are lowercased.
type Combo = `mod+${string}` | string

export interface ShortcutBindings {
  [combo: string]: (e: KeyboardEvent) => void
}

// True when the keydown target is a form field (<input>/<textarea>/<select>),
// so dialog inputs aren't hijacked by global shortcuts. Contenteditable
// surfaces (Milkdown's ProseMirror editor) are intentionally NOT included —
// global shortcuts fire from inside the editor to match Linear/Notion.
function isEditableTarget(target: EventTarget | null): boolean {
  if (!(target instanceof Element)) return false
  const tag = target.tagName
  if (tag === 'INPUT' || tag === 'TEXTAREA' || tag === 'SELECT') return true
  return false
}

function comboFromEvent(e: KeyboardEvent): Combo {
  const parts: string[] = []
  if (e.metaKey || e.ctrlKey) parts.push('mod')
  if (e.shiftKey) parts.push('shift')
  if (e.altKey) parts.push('alt')
  parts.push(e.key.toLowerCase())
  return parts.join('+')
}

/**
 * Register window-level keyboard shortcuts. Bindings are skipped when focus is
 * inside a form field (input/textarea/select) so dialog inputs aren't
 * hijacked, but DO fire from inside contenteditable surfaces (Milkdown editor)
 * to match Linear/Notion. Matched handlers receive the event and the hook
 * preventDefault()s before invoking, so callers don't need to.
 */
export function useGlobalShortcut(bindings: ShortcutBindings): void {
  const ref = useRef(bindings)
  // Keep the latest map in a ref so callers can pass freshly-constructed
  // objects each render without re-registering the global listener.
  useEffect(() => {
    ref.current = bindings
  })

  useEffect(() => {
    function onKeyDown(e: KeyboardEvent) {
      if (isEditableTarget(e.target)) return
      const combo = comboFromEvent(e)
      const handler = ref.current[combo]
      if (!handler) return
      e.preventDefault()
      handler(e)
    }
    window.addEventListener('keydown', onKeyDown)
    return () => window.removeEventListener('keydown', onKeyDown)
  }, [])
}

export const IS_MAC =
  typeof navigator !== 'undefined' && /Mac|iPod|iPhone|iPad/.test(navigator.platform)

const MODIFIER_LABELS: Record<string, [mac: string, other: string]> = {
  Mod: ['⌘', 'Ctrl'],
  Ctrl: ['⌃', 'Ctrl'],
  Alt: ['⌥', 'Alt'],
  Shift: ['⇧', 'Shift'],
}

// ProseMirror-style key name ('Mod-b', 'Mod-Alt-x') to a display label:
// ⌘B / ⌘⌥X on macOS, Ctrl+B / Ctrl+Alt+X elsewhere.
export function formatShortcut(key: string): string {
  const parts = key.split('-').map((p) => MODIFIER_LABELS[p]?.[IS_MAC ? 0 : 1] ?? p.toUpperCase())
  return parts.join(IS_MAC ? '' : '+')
}
