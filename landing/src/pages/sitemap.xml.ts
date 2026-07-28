import type { APIRoute } from 'astro';
import { competitorSlugs } from '../data/compare';

// Build-time sitemap. Enumerates the static pages under src/pages and emits
// trailing-slash canonical URLs (matching <link rel="canonical">), with a
// build-date <lastmod> — so it never drifts from a hand-maintained file.
// Dynamic routes (e.g. compare/[slug]) are expanded from their data source.
// Served at /sitemap.xml (referenced by robots.txt).
const SITE = 'https://telawiki.com';

// Routes that exist as pages but must never be submitted for indexing —
// 404.astro is Astro's error page and answers with a 404, so listing it earns
// a "Not found (404)" report in Search Console.
const NOINDEX = new Set(['/404/']);

// Per-route crawl hints, keyed by canonical path. Unlisted pages fall back.
const HINTS: Record<string, { changefreq: string; priority: string }> = {
  '/': { changefreq: 'weekly', priority: '1.0' },
  '/mcp/': { changefreq: 'weekly', priority: '0.8' },
  '/pricing/': { changefreq: 'weekly', priority: '0.9' },
  '/compare/': { changefreq: 'monthly', priority: '0.8' },
  '/privacy/': { changefreq: 'yearly', priority: '0.3' },
  '/terms/': { changefreq: 'yearly', priority: '0.3' },
};

export const GET: APIRoute = () => {
  const lastmod = new Date().toISOString().slice(0, 10);
  const staticPaths = Object.keys(import.meta.glob('./**/*.astro'))
    .map((f) => f.replace(/^\.\//, '').replace(/\.astro$/, ''))
    // drop dynamic-route templates (e.g. compare/[slug]); expanded below.
    .filter((p) => !p.includes('['))
    .map((p) => (p === 'index' ? '/' : `/${p.replace(/\/index$/, '')}/`));
  const comparePaths = competitorSlugs.map((s) => `/compare/${s}/`);
  const paths = [...staticPaths, ...comparePaths]
    .filter((p, i, a) => a.indexOf(p) === i && !NOINDEX.has(p))
    .sort();

  const urls = paths.map((p) => {
    const compareLeaf = p.startsWith('/compare/') && p !== '/compare/';
    const h = HINTS[p] ?? (compareLeaf
      ? { changefreq: 'monthly', priority: '0.7' }
      : { changefreq: 'monthly', priority: '0.5' });
    return `  <url>
    <loc>${SITE}${p}</loc>
    <lastmod>${lastmod}</lastmod>
    <changefreq>${h.changefreq}</changefreq>
    <priority>${h.priority}</priority>
  </url>`;
  });

  const body = `<?xml version="1.0" encoding="UTF-8"?>
<urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9">
${urls.join('\n')}
</urlset>
`;
  return new Response(body, { headers: { 'Content-Type': 'application/xml' } });
};
