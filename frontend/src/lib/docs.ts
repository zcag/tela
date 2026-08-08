// Canonical documentation links, in the canonical URL form
// (/{handle}/{space-slug}/{id}/{title-slug}) — the id resolves the page, so
// renaming a doc never breaks these. They point at the public docs on
// telawiki.com (the "tela Docs" space) so they work the same for self-hosted
// instances — a self-hoster's own server doesn't carry the docs space, but the
// public docs are the one canonical source. One place to maintain these.
const DOCS_BASE = 'https://telawiki.com'

export const DOCS = {
  home: `${DOCS_BASE}/tela/docs`,
  plans: `${DOCS_BASE}/tela/docs/225/plans-billing`,
  tour: `${DOCS_BASE}/tela/docs/325/tela-team-onboarding`,
  webdav: `${DOCS_BASE}/tela/docs/218/sync-your-vault-webdav`,
  rclone: `${DOCS_BASE}/tela/docs/219/sync-with-rclone`,
  mcp: `${DOCS_BASE}/tela/docs/211/agents-mcp`,
  apiTokens: `${DOCS_BASE}/tela/docs/224/api-personal-access-tokens`,
  sso: `${DOCS_BASE}/tela/docs/212/single-sign-on-sso`,
  selfHosting: `${DOCS_BASE}/tela/docs/210/self-hosting`,
} as const
