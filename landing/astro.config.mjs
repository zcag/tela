// @ts-check
import { defineConfig, fontProviders } from 'astro/config';
import tailwindcss from '@tailwindcss/vite';

// geistVariants declares one upright + one italic face for a VARIABLE family, so
// a single file serves every weight in the range. `unicodeRange` matches the
// latin subset these files were cut to — without it the browser would download
// them for text they cannot render.
const LATIN =
  'U+0000-00FF,U+0131,U+0152-0153,U+02BB-02BC,U+02C6,U+02DA,U+02DC,U+0304,U+0308,U+0329,' +
  'U+2000-206F,U+20AC,U+2122,U+2191,U+2193,U+2212,U+2215,U+FEFF,U+FFFD';

const geistVariants = (base) =>
  ['normal', 'italic'].map((style) => ({
    weight: '100 900',
    style,
    unicodeRange: LATIN.split(','),
    src: [`./src/assets/fonts/${base}${style === 'italic' ? '-italic' : ''}.woff2`],
  }));

// tela landing — standalone static marketing site. Built separately from the
// app (backend/ + frontend/), deployed as static files served at the apex.
//
// Tailwind v4 is wired through @tailwindcss/vite; all design tokens live in
// src/styles/tokens.css via the v4 @theme block (single source of truth).
//
// Fonts: Geist (display+body) + Geist Mono, self-hosted via the Astro Fonts API
// with metric-matched fallbacks (size-adjust) — kills FOUT and keeps CLS ~0.
// cssVariables feed tokens.css (@theme --font-* → var(--af-*)).
//
// The files are VENDORED in src/assets/fonts and served by the `local` provider,
// not downloaded from Google at build time. They used to be: `fontProviders
// .google()` fetches each face during the build, and on 2026-08-17 Google's font
// METADATA started handing out a dead URL for Geist Mono regular (the italic
// faces resolved fine, and the CSS endpoint served a different, working URL for
// the same face) — so `astro build` died on a 404 and took `make deploy` with it.
// A marketing page's build must not depend on a third party's URL rotation.
// Geist and Geist Mono are OFL, so self-hosting is exactly what they're for.
//
// Both families are VARIABLE, which is why four files cover every weight: one
// upright + one italic each, with the weight RANGE declared per variant.
// Refresh: download the latin woff2 from Google's CSS endpoint and replace in
// place — no config change, since the variants point at filenames.
export default defineConfig({
  output: 'static',
  site: 'https://telawiki.com',

  // Inline all CSS into the HTML so first paint has no render-blocking
  // stylesheet round-trip (Lighthouse "render-blocking resources"). Styles are
  // unchanged — only where they load — so the design/token/a11y gates still
  // hold. Right tradeoff for a marketing site optimizing first-visit LCP/FCP.
  build: { inlineStylesheets: 'always' },

  // Vanity redirect → the tela Blog (a public tela space, served by the app).
  redirects: {
    '/blog': '/public/spaces/59',
  },

  fonts: [
    {
      provider: fontProviders.local(),
      name: 'Geist',
      cssVariable: '--af-display',
      fallbacks: ['ui-sans-serif', 'system-ui', 'sans-serif'],
      options: { variants: geistVariants('geist-latin') },
    },
    {
      provider: fontProviders.local(),
      name: 'Geist',
      cssVariable: '--af-body',
      fallbacks: ['ui-sans-serif', 'system-ui', 'sans-serif'],
      options: { variants: geistVariants('geist-latin') },
    },
    {
      provider: fontProviders.local(),
      name: 'Geist Mono',
      cssVariable: '--af-mono',
      fallbacks: ['ui-monospace', 'SFMono-Regular', 'monospace'],
      options: { variants: geistVariants('geist-mono-latin') },
    },
  ],

  vite: {
    plugins: [tailwindcss()],
    // View dev/preview over the LAN by hostname: set ASTRO_ALLOWED_HOSTS to a
    // comma-separated host list (empty default → localhost only).
    server: { allowedHosts: (process.env.ASTRO_ALLOWED_HOSTS ?? '').split(',').map((s) => s.trim()).filter(Boolean) },
    preview: { allowedHosts: (process.env.ASTRO_ALLOWED_HOSTS ?? '').split(',').map((s) => s.trim()).filter(Boolean) },
  },
});
