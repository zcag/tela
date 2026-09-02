export type ThemeName = 'light' | 'dark' | 'warm'
export type ThemeMode = 'system' | 'manual'
export type ThemeSide = 'light' | 'dark'
/** Which theme "follow system" resolves to on each side of the OS switch. */
export type ThemePair = Record<ThemeSide, ThemeName>

export const THEMES: readonly ThemeName[] = ['light', 'dark', 'warm'] as const

// Which side of the OS light/dark switch each theme belongs to. tela ships TWO
// light themes, so "follow system" is under-specified without this: when the OS
// goes light, do you get light or warm? See `setTheme` for how it's answered.
export const THEME_SIDE: Record<ThemeName, ThemeSide> = {
  light: 'light',
  dark: 'dark',
  warm: 'light',
}

export interface ThemeState {
  mode: ThemeMode
  /** The last theme picked by hand — what `manual` mode applies. */
  theme: ThemeName
  pair: ThemePair
  /** What's actually on the document right now. */
  resolved: ThemeName
}

// `tela.theme` keeps holding a bare ThemeName (the manual pick) exactly as it
// did before follow-system existed, so an older bundle in another tab still
// reads it and a returning user is never reset. Mode + pair are additive keys.
const STORAGE_KEY = 'tela.theme'
const MODE_KEY = 'tela.theme.mode'
const PAIR_KEY = 'tela.theme.pair'

const DEFAULT_THEME: ThemeName = 'light'
const DEFAULT_PAIR: ThemePair = { light: 'light', dark: 'dark' }
const THEME_CHANGE_EVENT = 'tela:theme-change'

function isThemeName(value: string | null): value is ThemeName {
  return value !== null && (THEMES as readonly string[]).includes(value)
}

let state: ThemeState = {
  mode: 'manual',
  theme: DEFAULT_THEME,
  pair: DEFAULT_PAIR,
  resolved: DEFAULT_THEME,
}

// Set once `applyPdfThemeParam` pins a theme: on the gotenberg print surfaces
// the URL param is the whole truth, and a stray system-preference change must
// not repaint the page mid-render.
let pinned = false

function read(key: string): string | null {
  try {
    return localStorage.getItem(key)
  } catch {
    // localStorage may be unavailable (private mode, etc.) — non-fatal.
    return null
  }
}

function write(key: string, value: string): void {
  try {
    localStorage.setItem(key, value)
  } catch {
    // Same — a theme that doesn't persist still applies for this session.
  }
}

/** Which side the OS is currently on. Defaults to light where unsupported. */
function systemSide(): ThemeSide {
  if (typeof window === 'undefined' || !window.matchMedia) return 'light'
  return window.matchMedia('(prefers-color-scheme: dark)').matches ? 'dark' : 'light'
}

function resolve(next: Omit<ThemeState, 'resolved'>): ThemeName {
  return next.mode === 'system' ? next.pair[systemSide()] : next.theme
}

// Single writer: puts `state` on the document and tells subscribers. The event
// detail stays the resolved ThemeName — subscribers that only care which
// palette is live (mermaid, the reader, the command host) need no changes.
function apply(): void {
  if (pinned) return
  state = { ...state, resolved: resolve(state) }
  if (typeof document !== 'undefined') {
    document.documentElement.setAttribute('data-theme', state.resolved)
  }
  if (typeof window !== 'undefined') {
    window.dispatchEvent(
      new CustomEvent<ThemeName>(THEME_CHANGE_EVENT, { detail: state.resolved }),
    )
  }
}

/** The palette that's live right now, whether it was picked or followed. */
export function getTheme(): ThemeName {
  if (typeof document === 'undefined') return DEFAULT_THEME
  const attr = document.documentElement.getAttribute('data-theme')
  return isThemeName(attr) ? attr : DEFAULT_THEME
}

/** Mode + pair + resolved, for UI that shows *why* a theme is on. */
export function getThemeState(): ThemeState {
  return { ...state, pair: { ...state.pair } }
}

/**
 * Pick a theme by hand. This also answers the "which light theme?" question
 * implicitly: the theme you just chose becomes the one follow-system uses for
 * that side, so pressing System after picking warm keeps warm in daylight.
 */
export function setTheme(name: ThemeName): void {
  state = {
    ...state,
    mode: 'manual',
    theme: name,
    pair: { ...state.pair, [THEME_SIDE[name]]: name },
  }
  write(STORAGE_KEY, name)
  write(MODE_KEY, 'manual')
  write(PAIR_KEY, JSON.stringify(state.pair))
  apply()
}

/** Hand the theme back to the OS light/dark preference. */
export function followSystem(): void {
  state = { ...state, mode: 'system' }
  write(MODE_KEY, 'system')
  apply()
}

// Cycle order for the `t` keybind and the palette command: the three themes in
// declaration order, then follow-system, then back. One definition so the two
// callers can't drift.
export function cycleTheme(): void {
  if (state.mode === 'system') {
    setTheme(THEMES[0])
    return
  }
  const i = THEMES.indexOf(state.theme)
  const next = i + 1
  if (next >= THEMES.length) followSystem()
  else setTheme(THEMES[next])
}

// Subscribe to theme changes triggered via setTheme/followSystem (from any
// caller — ThemeSwitcher, the toggle-theme command, the OS itself). Lets
// sibling UI re-sync without lifting theme state to a React context. Callers
// needing mode/pair re-read `getThemeState()` inside the callback.
export function subscribeToTheme(cb: (next: ThemeName) => void): () => void {
  if (typeof window === 'undefined') return () => {}
  function handler(e: Event) {
    cb((e as CustomEvent<ThemeName>).detail)
  }
  window.addEventListener(THEME_CHANGE_EVENT, handler)
  return () => window.removeEventListener(THEME_CHANGE_EVENT, handler)
}

// #3 PDF export — apply a theme passed via ?theme= on the reader URL gotenberg
// loads (/print, /share). Sets data-theme so the chosen palette renders, plus a
// data-pdf-themed marker that opts the page out of the print-CSS forced-light
// default. No-op (keeps the viewer's own theme) when the param is absent/invalid,
// so it's safe to call on the human-facing share view too. Returns the applied
// theme, or null when nothing was applied.
export function applyPdfThemeParam(): ThemeName | null {
  if (typeof window === 'undefined') return null
  try {
    const value = new URLSearchParams(window.location.search).get('theme')
    if (!isThemeName(value)) return null
    document.documentElement.setAttribute('data-theme', value)
    document.documentElement.setAttribute('data-pdf-themed', '')
    pinned = true
    return value
  } catch {
    return null
  }
}

export function initTheme(): void {
  const storedTheme = read(STORAGE_KEY)
  const theme = isThemeName(storedTheme) ? storedTheme : DEFAULT_THEME

  // No stored theme at all = a new user, who gets follow-system. Anyone with a
  // prior pick keeps it (their storage predates the mode key), so an upgrade
  // never silently changes the theme under someone.
  const storedMode = read(MODE_KEY)
  const mode: ThemeMode =
    storedMode === 'system' || (storedMode === null && storedTheme === null)
      ? 'system'
      : 'manual'

  // A pair the user has never configured starts from their existing pick, so a
  // long-time warm user who turns on follow-system gets warm in the daytime.
  let pair: ThemePair = { ...DEFAULT_PAIR, [THEME_SIDE[theme]]: theme }
  const storedPair = read(PAIR_KEY)
  if (storedPair) {
    try {
      const parsed = JSON.parse(storedPair) as Partial<ThemePair>
      if (isThemeName(parsed.light ?? null)) pair.light = parsed.light as ThemeName
      if (isThemeName(parsed.dark ?? null)) pair.dark = parsed.dark as ThemeName
    } catch {
      pair = { ...DEFAULT_PAIR, [THEME_SIDE[theme]]: theme }
    }
  }

  state = { mode, theme, pair, resolved: theme }
  apply()

  // Follow the OS live: with mode=system a preference flip repaints without a
  // reload, through the same broadcast every other subscriber already uses.
  if (typeof window !== 'undefined' && window.matchMedia) {
    window
      .matchMedia('(prefers-color-scheme: dark)')
      .addEventListener('change', () => {
        if (state.mode === 'system') apply()
      })
  }
}
