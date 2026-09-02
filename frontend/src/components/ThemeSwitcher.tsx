import { useEffect, useRef, useState } from 'react'
import { Monitor, Moon, Sun, Sunset } from 'lucide-react'
import { ToggleGroup, ToggleGroupItem } from './ui/toggle'
import { Button } from './ui/button'
import { Popover, PopoverContent, PopoverTrigger } from './ui/popover'
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

// Which segments follow-system is currently pointing at. The pairing is
// implicit — whatever you last picked on each side — so without a mark on the
// control there is nothing to tell you the rule exists.
function pairRole(state: ThemeState, name: ThemeName): 'active' | 'other' | null {
  if (state.mode !== 'system') return null
  if (state.pair[THEME_SIDE[name]] !== name) return null
  return state.resolved === name ? 'active' : 'other'
}

// The same fact in words, for the tooltip and for anyone who can't see the band.
function describe(state: ThemeState): string {
  if (state.mode !== 'system') return `Theme — ${THEME_LABEL[state.theme]}`
  return (
    `Follow system — ${THEME_LABEL[state.pair.light]} when light, ` +
    `${THEME_LABEL[state.pair.dark]} when dark. Currently ${THEME_LABEL[state.resolved]}.`
  )
}

function useThemeState(): ThemeState {
  const [state, setState] = useState<ThemeState>(() => getThemeState())
  // Re-sync on changes from anywhere else: the `t` keybind, the palette
  // command, another mounted switcher, or the OS preference itself.
  useEffect(() => subscribeToTheme(() => setState(getThemeState())), [])
  return state
}

// Hover-to-open, without breaking the two input methods that have no hover.
// Pointer enter opens; leaving the trigger OR the panel closes after a grace
// period, so cutting the corner between them doesn't snap it shut. Coarse
// pointers get no hover handlers at all and fall back to tap-to-open.
const CLOSE_DELAY = 140

function useHoverOpen() {
  const [open, setOpen] = useState(false)
  const timer = useRef<number | undefined>(undefined)
  const viaPointer = useRef(false)

  const canHover =
    typeof window !== 'undefined' &&
    !!window.matchMedia?.('(hover: hover) and (pointer: fine)').matches

  useEffect(() => () => window.clearTimeout(timer.current), [])

  if (!canHover) return { open: undefined, setOpen: undefined, hoverProps: {}, viaPointer }

  return {
    open,
    setOpen,
    viaPointer,
    hoverProps: {
      onPointerEnter: () => {
        window.clearTimeout(timer.current)
        viaPointer.current = true
        setOpen(true)
      },
      onPointerLeave: () => {
        window.clearTimeout(timer.current)
        timer.current = window.setTimeout(() => setOpen(false), CLOSE_DELAY)
      },
    },
  }
}

/**
 * The segments: follow-system, then the three themes as icons.
 *
 * While following the system an accent band encloses the two themes it
 * alternates between, and the live one takes the accent colour. The band can be
 * a single box because dark is the middle segment AND the only dark-side theme,
 * so it is in every pair — the two are always neighbours. Adding a second dark
 * theme breaks that, and the band would have to become a per-segment mark.
 */
function ThemeRow({
  state,
  terseSystemLabel = false,
}: {
  state: ThemeState
  // Inside the popover the trigger has already announced the whole sentence,
  // so repeating it on the System segment just makes a reader say it twice.
  terseSystemLabel?: boolean
}) {
  const ref = useRef<HTMLDivElement>(null)
  const [band, setBand] = useState<{ left: number; right: number } | null>(null)

  useEffect(() => {
    const root = ref.current
    if (!root) return
    const measure = () => {
      const members = root.querySelectorAll<HTMLElement>('[data-pair]')
      if (!members.length) return setBand(null)
      const box = root.getBoundingClientRect()
      const first = members[0].getBoundingClientRect()
      const last = members[members.length - 1].getBoundingClientRect()
      setBand({ left: first.left - box.left, right: box.right - last.right })
    }
    measure()
    // The row stretches to fill in the reader's prefs menu, so segment widths
    // change with the container, not only with theme state.
    const ro = new ResizeObserver(measure)
    ro.observe(root)
    return () => ro.disconnect()
  }, [state])

  return (
    <ToggleGroup
      ref={ref}
      type="single"
      value={state.mode === 'system' ? 'system' : state.theme}
      onValueChange={(value) => {
        if (!value) return
        if (value === 'system') followSystem()
        else setTheme(value as ThemeName)
      }}
      aria-label="Theme"
      className="relative"
    >
      {band ? (
        <span
          aria-hidden
          className={cn(
            'pointer-events-none absolute top-[var(--space-1)] bottom-[var(--space-1)]',
            'rounded-[var(--radius-sm)] border border-[var(--accent)]/35 bg-[var(--accent)]/10',
          )}
          style={{ left: band.left, right: band.right }}
        />
      ) : null}

      <ToggleGroupItem
        value="system"
        size="sm"
        title={describe(state)}
        aria-label={terseSystemLabel ? 'Follow system' : describe(state)}
        className="relative z-[1] flex-1 px-[var(--space-2)]"
      >
        <Monitor width={14} height={14} aria-hidden />
      </ToggleGroupItem>

      {/* Follow-system is a mode, not a fourth palette. */}
      <span
        aria-hidden
        className="my-[var(--space-1)] w-px self-stretch bg-[var(--border-subtle)]"
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
            data-pair={role ?? undefined}
            className={cn(
              'relative z-[1] flex-1 px-[var(--space-2)]',
              role === 'active' && 'text-[var(--accent)]',
            )}
          >
            <Icon width={14} height={14} aria-hidden />
          </ToggleGroupItem>
        )
      })}
    </ToggleGroup>
  )
}

/**
 * Theme control.
 *
 * In chrome it rests as a single icon button the size of its neighbours and
 * opens the row on hover — the glyph is the theme you're in, accent-coloured
 * while the system is choosing it. Where a panel is already open (the reader's
 * Display menu) pass `inline` and the row renders directly instead.
 */
export function ThemeSwitcher({ inline = false }: { inline?: boolean }) {
  const state = useThemeState()
  const hover = useHoverOpen()
  const contentRef = useRef<HTMLDivElement>(null)

  if (inline) return <ThemeRow state={state} />

  const Icon = THEME_ICON[state.resolved]
  const label = describe(state)

  return (
    <Popover open={hover.open} onOpenChange={hover.setOpen}>
      <PopoverTrigger asChild>
        <Button
          type="button"
          variant="ghost"
          size="sm"
          aria-label={label}
          title={label}
          className={cn(
            'h-[var(--space-8)] w-[var(--space-8)] p-0',
            state.mode === 'system' && 'text-[var(--accent)]',
          )}
          {...hover.hoverProps}
        >
          <Icon width={16} height={16} aria-hidden />
        </Button>
      </PopoverTrigger>
      <PopoverContent
        ref={contentRef}
        align="end"
        sideOffset={4}
        className="w-auto p-[var(--space-1)]"
        onOpenAutoFocus={(e) => {
          // A panel that opened under the cursor must not take focus — merely
          // passing over the header would yank the caret out of an input.
          // Keyboard opens still land inside, on the current segment.
          e.preventDefault()
          if (!hover.viaPointer.current) {
            const row = contentRef.current
            const target =
              row?.querySelector<HTMLElement>('[data-state="on"]') ??
              row?.querySelector<HTMLElement>('button')
            target?.focus()
          }
          hover.viaPointer.current = false
        }}
        {...hover.hoverProps}
      >
        <ThemeRow state={state} terseSystemLabel />
      </PopoverContent>
    </Popover>
  )
}
