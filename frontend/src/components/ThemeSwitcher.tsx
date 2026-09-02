import { useEffect, useState } from 'react'
import { Monitor, Moon, Sun, Sunset } from 'lucide-react'
import { ToggleGroup, ToggleGroupItem } from './ui/toggle'
import { cn } from '../lib/utils'
import {
  followSystem,
  getThemeState,
  setTheme,
  subscribeToTheme,
  THEMES,
  THEME_SIDE,
  type ThemeName,
  type ThemeState,
} from '../lib/theme'

const THEME_ICON: Record<ThemeName, typeof Sun> = {
  light: Sun,
  dark: Moon,
  warm: Sunset,
}

const THEME_LABEL: Record<ThemeName, string> = {
  light: 'Light',
  dark: 'Dark',
  warm: 'Warm',
}

// Follow-system pairs a light-side theme with a dark-side one, and that pairing
// is implicit — it's whatever you last picked on each side. The dots make it
// visible: filled under the theme that's live now, hollow under the one waiting
// on the other side of the OS switch. Null in manual mode, where the control
// looks exactly as it always did.
function pairRole(state: ThemeState, name: ThemeName): 'active' | 'other' | null {
  if (state.mode !== 'system') return null
  // Not this side's chosen theme (e.g. light while warm is the light pick).
  if (state.pair[THEME_SIDE[name]] !== name) return null
  return state.resolved === name ? 'active' : 'other'
}

// The same fact in words, for the tooltip and for screen readers — which never
// see the dots.
function describe(state: ThemeState): string {
  if (state.mode !== 'system') {
    return `Follow system — off (${THEME_LABEL[state.theme]} is pinned)`
  }
  return (
    `Follow system — ${THEME_LABEL[state.pair.light]} when light, ` +
    `${THEME_LABEL[state.pair.dark]} when dark. Currently ${THEME_LABEL[state.resolved]}.`
  )
}

/**
 * Theme control: follow-system plus the three themes, as icons.
 *
 * Icon-only on purpose — four labelled segments don't fit a phone header (three
 * already overflowed it), and this one control is narrow enough to be the only
 * one, on every surface, at every width.
 */
export function ThemeSwitcher() {
  const [state, setState] = useState<ThemeState>(() => getThemeState())

  // Re-sync on theme changes that originate elsewhere: the "Toggle theme"
  // command, the `t` keybind, another mounted switcher, or the OS itself.
  useEffect(() => subscribeToTheme(() => setState(getThemeState())), [])

  const systemLabel = describe(state)

  return (
    <ToggleGroup
      type="single"
      value={state.mode === 'system' ? 'system' : state.theme}
      onValueChange={(value) => {
        if (!value) return
        if (value === 'system') followSystem()
        else setTheme(value as ThemeName)
      }}
      aria-label="Theme"
    >
      <ToggleGroupItem
        value="system"
        size="sm"
        title={systemLabel}
        aria-label={systemLabel}
        className="flex-1 px-[var(--space-2)]"
      >
        <Monitor width={14} height={14} aria-hidden />
      </ToggleGroupItem>

      {/* System is a mode, not a peer theme — the rule keeps it from reading as
          a fourth palette. */}
      <div
        aria-hidden
        className="self-stretch w-px my-[var(--space-1)] bg-[var(--border-subtle)]"
      />

      {THEMES.map((name) => {
        const Icon = THEME_ICON[name]
        const role = pairRole(state, name)
        return (
          <ToggleGroupItem
            key={name}
            value={name}
            size="sm"
            title={THEME_LABEL[name]}
            aria-label={THEME_LABEL[name]}
            className={cn(
              'relative flex-1 px-[var(--space-2)]',
              role && 'pb-[var(--space-1)]',
            )}
          >
            <Icon width={14} height={14} aria-hidden />
            {role ? (
              <span
                aria-hidden
                className={cn(
                  'absolute bottom-[calc(var(--space-1)/2)] left-1/2 -translate-x-1/2',
                  'size-[var(--space-1)] rounded-[var(--radius-full)]',
                  role === 'active'
                    ? 'bg-[var(--accent)]'
                    : 'ring-1 ring-[var(--border-strong)]',
                )}
              />
            ) : null}
          </ToggleGroupItem>
        )
      })}
    </ToggleGroup>
  )
}
