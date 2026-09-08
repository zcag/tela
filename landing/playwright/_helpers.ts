/**
 * Shared helpers for cila production gates.
 *
 * Everything here is project-agnostic: token allow-sets are read at runtime
 * from the resolved CSS custom properties on `:root`, never hardcoded. BASE_URL
 * and the page list come from the environment so a single suite covers any
 * cila-enabled site.
 *
 * Why `evaluate` / `getComputedStyle` instead of Playwright's `toHaveCSS`:
 * `toHaveCSS` (and `getAttribute`/locator CSS reads) cannot resolve CSS custom
 * properties — it returns the raw `var(--x)` token or an empty string for
 * `--custom-prop` names. To build a token allow-set we must run in the page and
 * read *resolved* values via `getComputedStyle(document.documentElement)`.
 */

import type { Page } from '@playwright/test';

// ---------------------------------------------------------------------------
// Shared constants
// ---------------------------------------------------------------------------

/** Viewport matrix the a11y + reduced-motion gates run across. */
export const VIEWPORTS = [
  { name: 'mobile', width: 360, height: 800 },
  { name: 'tablet', width: 768, height: 1024 },
  { name: 'laptop', width: 1024, height: 768 },
  { name: 'desktop', width: 1440, height: 900 },
] as const;

/** Base URL of the dev/preview server under test. Astro default. */
export const BASE_URL = process.env.BASE_URL ?? 'http://localhost:4321';

/**
 * Routes to gate. Override with a comma-separated CILA_ROUTES env var, e.g.
 * `CILA_ROUTES="/,/about,/pricing"`.
 *
 * Default covers the home page plus ONE /compare/<slug> leaf. The compare leaf
 * is not decoration: it is the only template with a horizontally scrollable
 * region, and gating `/` alone let a serious WCAG 2.2 AA failure
 * (scrollable-region-focusable, at 360px) sit on all 21 comparison pages
 * unnoticed. One leaf exercises the shared template for the whole set, and adds
 * only a few seconds to the run.
 */
export const ROUTES = (process.env.CILA_ROUTES ?? '/,/compare/,/compare/notion/')
  .split(',')
  .map((r) => r.trim())
  .filter(Boolean);

// ---------------------------------------------------------------------------
// Color normalization
// ---------------------------------------------------------------------------

/**
 * Normalize a color string to a canonical `r,g,b` or `r,g,b,a` form.
 *
 * Element colors and token allow-set entries are already resolved to sRGB
 * channels in-page (see {@link RESOLVER_SRC} / {@link collectVisibleStyles}),
 * so the common input here is already the bare `r,g,b[,a]` form — which this
 * passes through unchanged. The legacy `rgb()/rgba()` branch is kept so any
 * raw computed string still canonicalizes correctly.
 */
export function canonicalizeColor(input: string): string {
  const v = input.trim().toLowerCase();
  if (!v || v === 'none') return '';
  // Fully transparent — treat as a single bucket regardless of channel noise.
  if (v === 'transparent' || v === 'rgba(0, 0, 0, 0)') return 'transparent';

  const nums = v.match(/-?\d*\.?\d+(?:e-?\d+)?%?/gi);
  if (!nums) return v; // keyword we can't parse (e.g. currentcolor) — keep raw

  // rgb/rgba
  if (v.startsWith('rgb')) {
    const [r, g, b, a] = nums.map((n) => Number(n));
    if (a === undefined || a === 1) return `${r},${g},${b}`;
    return `${r},${g},${b},${round(a, 3)}`;
  }
  return v;
}

/**
 * Drop the alpha channel from a canonicalized color, yielding the base `r,g,b`.
 *
 * Tailwind opacity modifiers (`bg-brand/35`, `text-fg-on-brand/80`) are a
 * sanctioned way to use a token at reduced alpha. They compile to either an
 * `rgba(r, g, b, a)` or a `color-mix(in oklab, var(--token) N%, transparent)`,
 * both of which the engine resolves to the token's base channels with a partial
 * alpha. Membership in the token allow-set is therefore **alpha-agnostic**: a
 * computed color is on-token when its base `r,g,b` matches an allowed token's
 * base — regardless of the alpha applied by an opacity modifier. (Fully
 * transparent stays its own bucket and is handled before this is reached.)
 */
export function baseColor(canonical: string): string {
  if (!canonical || canonical === 'transparent') return canonical;
  const parts = canonical.split(',');
  if (parts.length >= 3) return parts.slice(0, 3).join(',');
  return canonical;
}

/**
 * Per-channel sRGB tolerance for base-color membership. An opacity modifier
 * compiles to `color-mix(in oklab, var(--token) N%, transparent)`, which mixes
 * in oklab and converts back to sRGB — the resulting channels drift a hair (~1–2
 * of 255) from the pure token's sRGB. So base membership is "within N per
 * channel", not exact, to absorb that compositing/rounding drift while still
 * rejecting a genuinely different color.
 */
export const COLOR_CHANNEL_TOLERANCE = Number(process.env.CILA_COLOR_TOL ?? 3);

/**
 * True if `canonical` (a resolved sRGB `r,g,b[,a]`) is on-token: its base
 * `r,g,b` is within {@link COLOR_CHANNEL_TOLERANCE} per channel of some allowed
 * base in `allowBases`. Alpha is ignored entirely (opacity modifiers permitted).
 */
export function isOnTokenBase(canonical: string, allowBases: Iterable<string>): boolean {
  const base = baseColor(canonical);
  if (base === 'transparent' || !base) return true;
  const c = base.split(',').map(Number);
  if (c.length < 3 || c.some((n) => Number.isNaN(n))) return false;
  for (const ab of allowBases) {
    const a = ab.split(',').map(Number);
    if (a.length < 3) continue;
    if (
      Math.abs(a[0] - c[0]) <= COLOR_CHANNEL_TOLERANCE &&
      Math.abs(a[1] - c[1]) <= COLOR_CHANNEL_TOLERANCE &&
      Math.abs(a[2] - c[2]) <= COLOR_CHANNEL_TOLERANCE
    )
      return true;
  }
  return false;
}

function round(n: number, dp: number): number {
  const f = 10 ** dp;
  return Math.round(n * f) / f;
}

// ---------------------------------------------------------------------------
// In-page evaluation (runs in the browser context)
// ---------------------------------------------------------------------------

/**
 * Source of an in-page sRGB color resolver, injected into every `page.evaluate`
 * that needs to canonicalize colors. It paints `value` onto a 1×1 canvas and
 * reads the pixel back, which forces the engine to convert ANY input form —
 * `oklch()`, `oklab()`, `lab()`, `lch()`, `color(srgb ...)`, `#hex`,
 * `color-mix(...)`, `var()` chains — down to concrete sRGB channels.
 *
 * Why a canvas and not just `getComputedStyle().color`: modern Chromium keeps
 * wide-gamut colors in their authored space (it returns `oklch(...)` /
 * `oklab(...)` verbatim), and a Tailwind opacity modifier compiles to
 * `color-mix(in oklab, …, transparent)` which also reads back as `oklab(…)`.
 * Those never string-match an `rgb()`-shaped allow-set. The canvas read makes
 * both the token allow-set and the collected element colors land in the SAME
 * sRGB space, so comparison is apples-to-apples. Returns `r,g,b,a` (a in 0–1),
 * or '' for a value that doesn't resolve to a paintable color.
 */
const RESOLVER_SRC = `(() => {
  const cv = document.createElement('canvas');
  cv.width = cv.height = 1;
  const cx = cv.getContext('2d', { willReadFrequently: true });
  return (value) => {
    if (!value) return '';
    const v = String(value).trim().toLowerCase();
    if (!v || v === 'none' || v === 'currentcolor') return '';
    // Detect unparseable values: a sentinel that survives the assignment means
    // the engine rejected \`value\`. Pick a sentinel \`value\` itself isn't.
    const sentinel = v.indexOf('#fe00ff') === -1 ? '#fe00ff' : '#00ff01';
    cx.fillStyle = sentinel;
    cx.fillStyle = value;
    if (cx.fillStyle === sentinel) return '';
    cx.clearRect(0, 0, 1, 1);
    cx.fillRect(0, 0, 1, 1);
    const [r, g, b, a255] = cx.getImageData(0, 0, 1, 1).data;
    const a = a255 / 255;
    if (a === 0) return 'transparent';
    if (a === 1) return r + ',' + g + ',' + b;
    return r + ',' + g + ',' + b + ',' + Math.round(a * 1000) / 1000;
  };
})()`;

/**
 * Build the color allow-set by reading every resolved custom property off
 * `:root` (`getComputedStyle(document.documentElement)`), then resolving each
 * to concrete sRGB channels (see {@link RESOLVER_SRC}) so `oklch()`,
 * `color(srgb ...)`, `#hex` and `var()` chains all collapse to the same space.
 *
 * Returns **base `r,g,b`** keys (alpha stripped) so membership is alpha-agnostic
 * — see {@link baseColor}. A token defined with built-in alpha (e.g. an overlay)
 * contributes its base channels, and any opacity-modified use of a token
 * (`bg-brand/35`, `text-fg-on-brand/80`) matches the same base. Runs inside
 * `page.evaluate`.
 */
export async function buildColorAllowSet(page: Page): Promise<string[]> {
  const raw = await page.evaluate((resolverSrc) => {
    // eslint-disable-next-line no-eval
    const resolveSrgb: (v: string) => string = eval(resolverSrc);
    const root = document.documentElement;
    const cs = getComputedStyle(root);
    const probe = document.createElement('span');
    probe.style.display = 'none';
    document.body.appendChild(probe);

    const resolved = new Set<string>();
    // Resolve a token value through a real element first (collapses var()
    // chains + computes against the cascade), then to sRGB via the canvas.
    const resolve = (value: string): string => {
      probe.style.color = '';
      probe.style.color = value;
      const computed = getComputedStyle(probe).color;
      return resolveSrgb(computed || value);
    };

    // Iterate declared custom properties on :root.
    for (let i = 0; i < cs.length; i++) {
      const name = cs[i];
      if (!name.startsWith('--')) continue;
      const val = cs.getPropertyValue(name).trim();
      if (!val) continue;
      const out = resolve(val);
      if (out && out !== 'transparent') resolved.add(out);
      // Also resolve `var(--name)` so token aliases land in the set.
      const viaVar = resolve(`var(${name})`);
      if (viaVar && viaVar !== 'transparent') resolved.add(viaVar);
    }

    // Always-allowed structural colors: pure black/white text fallbacks the
    // engine emits for inherited/unset values.
    ['0,0,0', '255,255,255'].forEach((c) => resolved.add(c));

    probe.remove();
    return [...resolved];
  }, RESOLVER_SRC);

  const set = new Set<string>();
  for (const c of raw) set.add(baseColor(c));
  return [...set];
}

// ---------------------------------------------------------------------------
// Visible-element walking
// ---------------------------------------------------------------------------

/**
 * Collect computed style facts for every *visible* element, in the page.
 * We do the whole walk inside one `evaluate` for speed and to use the real
 * `getComputedStyle`. An element is "visible" if it has layout boxes and is
 * not `display:none` / `visibility:hidden` / `opacity:0` / zero-area.
 *
 * Color fields are normalized to canonical sRGB `r,g,b[,a]` via the shared
 * resolver (see {@link RESOLVER_SRC}) so they compare directly against the
 * allow-set built by {@link buildColorAllowSet} — both live in the same space,
 * regardless of whether the engine reported the color as `oklch()`/`oklab()`
 * (wide-gamut tokens, opacity-modified tokens) or `rgb()`.
 */
export interface ElementStyle {
  selector: string;
  color: string;
  backgroundColor: string;
  borderTopColor: string;
  borderRightColor: string;
  borderBottomColor: string;
  borderLeftColor: string;
  fontSize: number;
  margins: number[];
  paddings: number[];
  gap: number[];
  hasVisibleBorder: boolean;
}

export async function collectVisibleStyles(page: Page): Promise<ElementStyle[]> {
  return page.evaluate((resolverSrc) => {
    // eslint-disable-next-line no-eval
    const resolveSrgb: (v: string) => string = eval(resolverSrc);
    const describe = (el: Element): string => {
      const tag = el.tagName.toLowerCase();
      const id = (el as HTMLElement).id ? `#${(el as HTMLElement).id}` : '';
      const cls =
        typeof (el as HTMLElement).className === 'string' &&
        (el as HTMLElement).className.trim()
          ? '.' + (el as HTMLElement).className.trim().split(/\s+/).slice(0, 2).join('.')
          : '';
      return `${tag}${id}${cls}`;
    };

    const num = (v: string) => parseFloat(v) || 0;
    const out: any[] = [];

    for (const el of Array.from(document.querySelectorAll('body *'))) {
      const rect = el.getBoundingClientRect();
      const s = getComputedStyle(el);
      if (
        s.display === 'none' ||
        s.visibility === 'hidden' ||
        s.opacity === '0' ||
        rect.width === 0 ||
        rect.height === 0
      )
        continue;

      const borderW = [
        s.borderTopWidth,
        s.borderRightWidth,
        s.borderBottomWidth,
        s.borderLeftWidth,
      ].map(num);
      const hasVisibleBorder = borderW.some((w) => w > 0);

      out.push({
        selector: describe(el),
        color: resolveSrgb(s.color),
        backgroundColor: resolveSrgb(s.backgroundColor),
        borderTopColor: resolveSrgb(s.borderTopColor),
        borderRightColor: resolveSrgb(s.borderRightColor),
        borderBottomColor: resolveSrgb(s.borderBottomColor),
        borderLeftColor: resolveSrgb(s.borderLeftColor),
        fontSize: num(s.fontSize),
        margins: [s.marginTop, s.marginRight, s.marginBottom, s.marginLeft].map(num),
        paddings: [s.paddingTop, s.paddingRight, s.paddingBottom, s.paddingLeft].map(num),
        gap: [s.rowGap, s.columnGap].map(num),
        hasVisibleBorder,
      });
    }
    return out;
  }, RESOLVER_SRC);
}

/** Wait for the page to be visually settled (fonts + a render frame). */
export async function settle(page: Page): Promise<void> {
  await page.waitForLoadState('networkidle');
  await page.evaluate(async () => {
    // @ts-ignore — document.fonts exists in browsers.
    if (document.fonts?.ready) await document.fonts.ready;
    await new Promise((r) => requestAnimationFrame(() => r(null)));
  });
}
