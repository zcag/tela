import { useEffect } from 'react'
import { useBrand } from './queries/host-context'

// Apply the org's white-label accent app-wide. The theme stylesheets define
// --accent inside @layer; an inline style on <html> wins over any @layer value
// and survives runtime theme switches (setTheme only swaps data-theme), so the
// org's accent themes every accent-driven surface (buttons, links, BrandMark)
// regardless of the chosen light/dark/warm palette.
//
// The value is server data (host-context) — the legit runtime exception to the
// "no hardcoded color" rule; nothing is hardcoded here. We leave --accent-fg
// untouched (the per-theme foreground stays correct against the accent).
//
// Mounted once at the app root. Clears the override (removeProperty) when there
// is no org accent, so the canonical host / a cleared override falls straight
// back to the theme stylesheet value.
export function useOrgAccent(): void {
  const accent = useBrand().accent
  useEffect(() => {
    const root = document.documentElement
    if (accent) {
      root.style.setProperty('--accent', accent)
    } else {
      root.style.removeProperty('--accent')
    }
    return () => {
      root.style.removeProperty('--accent')
    }
  }, [accent])
}
