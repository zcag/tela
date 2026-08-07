package api

import (
	"database/sql"
	"net/http"

	"github.com/zcag/tela/backend/internal/auth"
)

// Handler builds the canonical Tela HTTP handler: every route registered
// against a single mux, then wrapped with auth.Middleware. Used by both
// cmd/tela/main.go and the integration test suite so the two never drift.
//
// The Server's AuditWriter is threaded into Middleware so bearer-authed
// requests emit api_key_audit rows. Tests that need to assert on the audit
// table flush via srv.AuditWriter().Flush() before reading.
func Handler(d *sql.DB) http.Handler {
	h, _ := HandlerWithServer(d)
	return h
}

// HandlerWithServer is the wired-handler variant that also returns the
// underlying *Server. Used by integration tests so they can reach
// srv.AuditWriter().Flush() between issuing a bearer request and querying
// the api_key_audit table.
//
// Middleware order (outermost→innermost): requestLogger → hostOrgMiddleware →
// auth.Middleware → mux. hostOrgMiddleware runs before auth so the request's
// org context (from a custom-domain host) is set for the login screen, session
// creation, and the session↔org binding check.
func HandlerWithServer(d *sql.DB) (http.Handler, *Server) {
	srv := New(d)
	mux := http.NewServeMux()
	registerRoutes(srv, mux)
	return requestLogger(mux, srv.hostOrgMiddleware(auth.Middleware(d, srv.auditWriter)(mux))), srv
}

func registerRoutes(srv *Server, mux *http.ServeMux) {
	mux.HandleFunc("GET /api/health", srv.Health)

	// Prometheus exposition. NOT on auth.IsPublicPath, so auth.Middleware runs
	// first (session cookie OR bearer PAT); the handler then gates to instance-
	// admins via requireInstanceAdmin. A scraper authenticates with an admin-
	// scoped PAT: `Authorization: Bearer tela_pat_<key>`. Metrics are produced
	// in the request-logging path (reqlog.go), so there is one source for both
	// the access log and the counters. See metrics.go.
	mux.Handle("GET /metrics", srv.metricsHandler())

	// M16.A.1.5 build-metadata probe. Public (see auth.IsPublicPath) — the MCP
	// server (M16.B.1) hits this at startup with no credentials to compat-check
	// itself against the running backend version. Values are injected via
	// ldflags at build time; defaults are version=dev / commit=unknown /
	// built_at=process-start.
	mux.HandleFunc("GET /api/version", srv.Version)

	// WebDAV file-sync surface (sync spec §9). Subtree handler at /dav/ —
	// self-authenticates via PAT-as-Basic and gates scope itself, so it is on
	// auth.IsPublicPath (the session/method Middleware skips it). The x/net/webdav
	// Handler drives the protocol over the davFS; ServeMux redirects the bare
	// /dav → /dav/. Method-less pattern matches every WebDAV verb.
	mux.Handle("/dav/", srv.DAVHandler())

	// MCP Streamable-HTTP transport (single endpoint, POST + GET + DELETE).
	// Self-authenticated via the SDK bearer verifier over tela PATs; on
	// auth.IsPublicPath so the session/method-scope Middleware skips it (a POST
	// transport carries both read and write tools, so per-tool scope is enforced
	// in the handlers, not by HTTP method). Method-less pattern matches all verbs.
	mux.Handle("/api/mcp", srv.MCPHandler())

	// Cloud control plane (api/cloud.go) — managed services the main instance
	// hosts and connected self-hosters reach over HTTP. Self-authenticating
	// bearer-PAT (on IsPublicPath); the embed proxy path mirrors Ollama's
	// /api/embed so the existing rag embedder is a drop-in client.
	mux.HandleFunc("GET /api/cloud/entitlements", srv.CloudEntitlements)
	mux.HandleFunc("POST /api/cloud/ollama/api/embed", srv.CloudEmbed)
	// Managed LLM proxy: mirrors the OpenAI /v1/chat/completions shape so a
	// self-hoster points TELA_LLM_URL at /api/cloud/llm/v1 + a PAT and the
	// in-process llm.OpenAIClient is a drop-in. Gated on the ask_docs feature.
	mux.HandleFunc("POST /api/cloud/llm/v1/chat/completions", srv.CloudChat)

	// OAuth 2.0 Protected Resource Metadata (RFC 9728) for the MCP endpoint.
	// Public + static (see auth.IsPublicPath); 404s when OAuth is unconfigured.
	// Served at BOTH the root well-known and the path-scoped variant — Claude
	// probes `/.well-known/oauth-protected-resource/<mcp-path>` then falls back
	// to the root. Must be routed through Caddy in prod (new top-level path).
	mux.HandleFunc("GET /.well-known/oauth-protected-resource", srv.ServePRM)
	mux.HandleFunc("GET /.well-known/oauth-protected-resource/api/mcp", srv.ServePRM)

	// WorkOS Standalone "OAuth Bridge" Login URI (Phase 5b). WorkOS redirects the
	// user here with ?external_auth_id during the Connect flow; we authenticate
	// against tela's own session, then complete via the WorkOS API. Public (it
	// self-authenticates, bouncing through /login when there's no session). GET
	// renders an explicit consent page; completion is the CSRF-protected POST
	// (prevents account-linking CSRF on a bare GET against an ambient session).
	mux.HandleFunc("GET /oauth/workos/login", srv.WorkOSLogin)
	mux.HandleFunc("POST /oauth/workos/login", srv.WorkOSLoginComplete)

	// First-run setup wizard (setup.go). Public (see auth.IsPublicPath): on a
	// fresh instance the users table is empty and there is no session yet. The
	// POST self-gates by only succeeding while the table is empty.
	mux.HandleFunc("GET /api/setup/status", srv.SetupStatus)
	mux.HandleFunc("POST /api/setup", srv.Setup)

	mux.HandleFunc("POST /api/auth/login", srv.Login)
	mux.HandleFunc("POST /api/auth/logout", srv.Logout)
	mux.HandleFunc("GET /api/auth/me", srv.Me)
	mux.HandleFunc("POST /api/auth/register", srv.Register)
	mux.HandleFunc("POST /api/auth/verify-email", srv.VerifyEmail)
	mux.HandleFunc("POST /api/auth/resend-verification", srv.ResendVerification)
	mux.HandleFunc("POST /api/auth/request-password-reset", srv.RequestPasswordReset)
	mux.HandleFunc("POST /api/auth/reset-password", srv.ResetPassword)

	// Instance-admin self-login to an org custom domain (admin_domain_login.go).
	// Public path (self-authenticates via a short-lived signed token); runs on the
	// org host so hostOrgMiddleware binds the minted session. Mint side is the
	// session-gated POST under /api/orgs/{id}/hostnames/… below.
	mux.HandleFunc("GET /api/auth/admin-login/redeem", srv.AdminDomainLoginRedeem)

	// Federated sign-in (sso.go): social providers (Google/Microsoft/GitHub) +
	// per-org OIDC. Under /api/auth/, so already on auth.IsPublicPath — every
	// handler self-authenticates (state cookie / IdP assertion). The 'org'
	// provider segment is shared by all org connections (org id rides in state).
	mux.HandleFunc("GET /api/auth/sso/providers", srv.SSOProviders)
	mux.HandleFunc("GET /api/auth/sso/{provider}/start", srv.SSOStart)
	// POST variant: the org email-prompt submits the work email in the body so it
	// stays out of the URL (Safe Browsing phishing-shape avoidance; see SSOStart).
	mux.HandleFunc("POST /api/auth/sso/{provider}/start", srv.SSOStart)
	mux.HandleFunc("GET /api/auth/sso/{provider}/callback", srv.SSOCallback)

	mux.HandleFunc("GET /api/spaces", srv.ListSpaces)
	mux.HandleFunc("POST /api/spaces", srv.CreateSpace)
	mux.HandleFunc("GET /api/spaces/{id}", srv.GetSpace)
	mux.HandleFunc("GET /api/spaces/{id}/overview", srv.SpaceOverview)
	mux.HandleFunc("PATCH /api/spaces/{id}", srv.UpdateSpace)
	mux.HandleFunc("DELETE /api/spaces/{id}", srv.DeleteSpace)
	mux.HandleFunc("POST /api/spaces/{id}/transfer", srv.TransferSpace)
	mux.HandleFunc("GET /api/spaces/{id}/index-version", srv.GetSpaceIndexVersion)
	mux.HandleFunc("POST /api/spaces/{id}/import", srv.ImportSpace)
	mux.HandleFunc("GET /api/spaces/{id}/export.zip", srv.ExportSpaceMarkdownZip)

	// atlas: source-grounded, coverage-audited doc generation (docs/atlas.md).
	// First-class projects: owner-scoped reusable credentials, projects (name +
	// owner + output space + schedule), sources under a project, runs per source.
	// Manage = owner user / owner-org admin / instance admin; view = + org members.
	mux.HandleFunc("GET /api/atlas/credentials", srv.ListAtlasCredentials)
	mux.HandleFunc("POST /api/atlas/credentials", srv.CreateAtlasCredential)
	mux.HandleFunc("DELETE /api/atlas/credentials/{id}", srv.DeleteAtlasCredential)

	mux.HandleFunc("GET /api/atlas/projects", srv.ListAtlasProjects)
	mux.HandleFunc("POST /api/atlas/projects", srv.CreateAtlasProject)
	mux.HandleFunc("GET /api/atlas/projects/{id}", srv.GetAtlasProject)
	mux.HandleFunc("PATCH /api/atlas/projects/{id}", srv.PatchAtlasProject)
	mux.HandleFunc("DELETE /api/atlas/projects/{id}", srv.DeleteAtlasProject)
	mux.HandleFunc("POST /api/atlas/projects/{id}/run", srv.RunAtlasProject)

	mux.HandleFunc("POST /api/atlas/projects/{id}/sources", srv.CreateAtlasSource)
	mux.HandleFunc("GET /api/atlas/projects/{id}/sources", srv.ListAtlasSources)
	mux.HandleFunc("PATCH /api/atlas/sources/{id}", srv.PatchAtlasSource)
	mux.HandleFunc("DELETE /api/atlas/sources/{id}", srv.DeleteAtlasSource)
	mux.HandleFunc("POST /api/atlas/sources/{id}/run", srv.RunAtlasSource)
	mux.HandleFunc("POST /api/atlas/sources/{id}/sync", srv.SyncAtlasSource)
	mux.HandleFunc("GET /api/atlas/sources/{id}/runs", srv.ListAtlasSourceRuns)
	mux.HandleFunc("GET /api/atlas/runs/{id}", srv.GetAtlasRun)
	mux.HandleFunc("GET /api/atlas/runs/{id}/stream", srv.StreamAtlasRun)

	mux.HandleFunc("GET /api/pages", srv.ListPages)
	mux.HandleFunc("GET /api/pages/all", srv.ListAllPages)
	mux.HandleFunc("GET /api/pages/bodies", srv.ListPageBodies)
	mux.HandleFunc("POST /api/pages", srv.CreatePage)
	mux.HandleFunc("GET /api/pages/{id}", srv.GetPage)
	mux.HandleFunc("POST /api/pages/{id}/view", srv.RecordPageView)
	mux.HandleFunc("PATCH /api/pages/{id}", srv.UpdatePage)
	mux.HandleFunc("DELETE /api/pages/{id}", srv.DeletePage)
	mux.HandleFunc("POST /api/pages/{id}/move", srv.MovePage)
	mux.HandleFunc("GET /api/pages/{id}/backlinks", srv.Backlinks)
	mux.HandleFunc("GET /api/pages/{id}/provenance", srv.PageProvenance)
	mux.HandleFunc("GET /api/pages/{id}/agreement", srv.PageAgreement)
	mux.HandleFunc("GET /api/graph", srv.GraphData)
	// Persisted Yjs state for instant editor paint (member-gated). The editor
	// applies this on mount so content shows without waiting for the WS sync.
	mux.HandleFunc("GET /api/pages/{id}/yjs", srv.GetPageYjsState)

	// #3 PDF export. Session-authed: renders the caller's own page to PDF via
	// gotenberg (which loads the /print/{token} reader). The public token-data
	// endpoint below is what gotenberg's headless Chromium fetches; it MUST be
	// on auth.IsPublicPath (/api/print/) so the session middleware skips it. The
	// page id lives inside the signed token, not the path, which keeps the
	// public branch a clean HasPrefix.
	mux.HandleFunc("GET /api/pages/{id}/pdf", srv.ExportPagePDF)
	mux.HandleFunc("GET /api/pages/{id}/md", srv.ExportPageMarkdown)
	mux.HandleFunc("GET /api/print/{token}", srv.GetPrintPage)
	// Deck (Slidev) pages. Present is the live interactive SPA (page-scoped +
	// membership-gated via requirePageRead). The PNG asset proxy is public
	// (content-addressed renderId, no notes); the rest session-authed.
	mux.HandleFunc("GET /api/pages/{id}/deck/spa/{path...}", srv.ServePageDeckSPA)
	mux.HandleFunc("GET /api/pages/{id}/deck/cover", srv.ServePageDeckCover)
	mux.HandleFunc("GET /api/pages/{id}/deck/outline", srv.GetPageDeckOutline)
	mux.HandleFunc("POST /api/pages/{id}/deck/parse", srv.PostPageDeckParse)
	mux.HandleFunc("POST /api/pages/{id}/deck/lint", srv.PostPageDeckLint)
	mux.HandleFunc("GET /api/pages/{id}/deck.pdf", srv.ExportPageDeckPDF)
	mux.HandleFunc("GET /api/pages/{id}/deck.pptx", srv.ExportPageDeckPPTX)
	mux.HandleFunc("GET /api/pages/{id}/deck.md", srv.ExportPageDeckMarkdown)
	mux.HandleFunc("GET /api/deck/d/{renderId}/{file}", srv.ServeDeckAsset)
	mux.HandleFunc("GET /api/deck/themes", srv.ServeDeckThemes)

	mux.HandleFunc("GET /api/pages/{id}/comments", srv.ListComments)
	mux.HandleFunc("POST /api/pages/{id}/comments", srv.CreateComment)
	mux.HandleFunc("PATCH /api/comments/{id}", srv.PatchComment)
	mux.HandleFunc("DELETE /api/comments/{id}", srv.DeleteComment)

	mux.HandleFunc("POST /api/pages/{id}/polls/{pollId}/vote", srv.VotePoll)
	mux.HandleFunc("POST /api/users/resolve", srv.ResolveUsers)

	mux.HandleFunc("GET /api/me/digest/preview", srv.DigestPreview)
	mux.HandleFunc("GET /api/me/digest", srv.GetDigestPref)
	mux.HandleFunc("PATCH /api/me/digest", srv.SetDigestPref)
	mux.HandleFunc("GET /api/admin/digest/preview", srv.AdminDigestPreview)
	mux.HandleFunc("GET /api/digest/unsubscribe", srv.DigestUnsubscribe)

	mux.HandleFunc("GET /api/pages/{id}/revisions", srv.ListPageRevisions)
	mux.HandleFunc("GET /api/pages/{id}/revisions/{rev_id}", srv.GetPageRevision)

	// M13.2 RichView Excalidraw PNG sidecar (HYBRID storage). PUT is
	// session-authed editor+ on the page's space; the GET counterpart below
	// is on /api/diagrams/* (public) so it can be served without a session.
	mux.HandleFunc("PUT /api/pages/{id}/diagrams", srv.UploadPageDiagram)

	// Page attachments — the space_files parented to this page (incl. files
	// rclone-synced into its folder). Session-authed read; the bytes are served
	// by the public /api/files/ route below.
	mux.HandleFunc("GET /api/pages/{id}/attachments", srv.ListPageAttachments)
	mux.HandleFunc("POST /api/pages/{id}/attachments", srv.UploadPageAttachment)
	mux.HandleFunc("DELETE /api/pages/{id}/attachments/{file_id}", srv.DeletePageAttachment)

	// Signed-PUT upload handshake (attachment_uploads.go). PUBLIC — the bytes are
	// authorized by the short-lived HMAC token in the path, not a session (on
	// auth.IsPublicPath via /api/uploads/). Lets an MCP agent upload a file
	// out-of-band so the bytes never ride through the model context.
	mux.HandleFunc("PUT /api/uploads/{token}", srv.UploadAttachmentBytes)

	// URL unfurl for paste-as-titled-link. Session-authed (makes an outbound
	// SSRF-guarded request); never public.
	mux.HandleFunc("GET /api/unfurl", srv.Unfurl)

	// M15.0 PublicShare management: session-authed, editor+ on source page's
	// space. Soft-delete via revoked_at so the audit trail survives revocation.
	mux.HandleFunc("POST /api/pages/{id}/shares", srv.CreateShareLink)
	mux.HandleFunc("GET /api/pages/{id}/shares", srv.ListShareLinks)
	// Cross-space audit: every active share link the caller can see, in one
	// place. Distinct pattern from the {share_id} routes below (no wildcard).
	mux.HandleFunc("GET /api/shares", srv.ListAllShares)
	mux.HandleFunc("PATCH /api/shares/{share_id}", srv.PatchShareLink)
	mux.HandleFunc("DELETE /api/shares/{share_id}", srv.DeleteShareLink)

	// M15.0 PublicShare public token API: no session cookie required. MUST be
	// on auth.IsPublicPath (/api/share/) so the session middleware skips it.
	// Password-gated when the share has a password_hash — handlers validate the
	// per-share HMAC cookie themselves.
	mux.HandleFunc("GET /api/share/{token}", srv.GetPublicShare)
	mux.HandleFunc("POST /api/share/{token}/auth", srv.PublicShareAuth)
	mux.HandleFunc("GET /api/share/{token}/page/{page_id}", srv.GetPublicSharePage)
	mux.HandleFunc("GET /api/share/{token}/tree", srv.GetPublicShareTree)
	// #3 ".pdf on a share URL" trick. Caddy rewrites /share/<tok>.pdf →
	// /api/share/{token}/pdf (and the descendant /p/<id>.pdf → ?p=<id>). Public
	// via the /api/share/ prefix; the handler validates the share token + scope.
	mux.HandleFunc("GET /api/share/{token}/pdf", srv.ExportSharePDF)

	// Public-space read API: a space with visibility='public' is readable with
	// no login. MUST be on auth.IsPublicPath (/api/public/) so the session
	// middleware skips it; every handler self-authenticates by requiring the
	// space be public, and is GET/read-only (writes stay on the membership-gated
	// /api/ routes). See public_spaces.go + docs/public-spaces.md.
	// Cross-tenant public-space directory (the publishing "network"); self-
	// authenticates by listing only visibility='public' spaces. See public_discover.go.
	mux.HandleFunc("GET /api/public/discover", srv.GetPublicDiscover)
	mux.HandleFunc("GET /api/public/spaces/{id}", srv.GetPublicSpace)
	mux.HandleFunc("GET /api/public/spaces/{id}/tree", srv.GetPublicSpaceTree)
	mux.HandleFunc("GET /api/public/spaces/{id}/feed.xml", srv.GetPublicSpaceFeed)
	mux.HandleFunc("GET /api/public/spaces/{id}/og.png", srv.HandlePublicSpaceOGImage)
	mux.HandleFunc("GET /api/public/users/{username}/og.png", srv.HandlePublicUserOGImage)
	mux.HandleFunc("GET /api/public/sitemap.xml", srv.HandlePublicSitemap)
	// Crawler-facing OG/JSON-LD surfaces — Caddy bot-gates /public/* and /u/* to
	// these (humans get the SPA); on auth.IsPublicPath, self-authenticating on
	// the space/user being public. See api/public_og.go.
	mux.HandleFunc("GET /public/spaces/{id}", srv.HandlePublicSpaceOG)
	mux.HandleFunc("GET /public/spaces/{id}/pages/{page_id}", srv.HandlePublicReaderOG)
	mux.HandleFunc("GET /public/spaces/{id}/pages/{page_id}/{slug}", srv.HandlePublicReaderOG)
	mux.HandleFunc("GET /u/{username}", srv.HandlePublicUserOG)
	// Unified handle URLs (/{handle}, /{handle}/{space-slug}) — the form the docs
	// tell people to share, so they must unfurl too. Registered under an explicit
	// /api/public/og/ prefix rather than as root wildcards: `GET /{handle}/{slug}`
	// overlaps the /dav/ subtree without either being a subset, which ServeMux
	// rejects at registration (it panics). Caddy bot-gates the pretty URL and
	// REWRITES to these, the same shape @page_bots already uses for /p/{id}.
	mux.HandleFunc("GET /api/public/og/handles/{handle}", srv.HandleHandleHomeOG)
	mux.HandleFunc("GET /api/public/og/handles/{handle}/{spaceSlug}", srv.HandleHandleSpaceOG)
	mux.HandleFunc("GET /api/public/handles/{handle}/og.png", srv.HandleHandleOGImage)
	// Feature routes with a public purpose but no page/space (e.g. /ask): Caddy
	// bot-gates the HTML to the backend (humans get the SPA); the og.png is served
	// to all UAs. On auth.IsPublicPath. See api/og_feature.go.
	mux.HandleFunc("GET /ask", srv.HandleFeatureOG)
	mux.HandleFunc("GET /ask/og.png", srv.HandleFeatureOGImage)
	mux.HandleFunc("GET /graph", srv.HandleFeatureOG)
	mux.HandleFunc("GET /graph/og.png", srv.HandleFeatureOGImage)
	mux.HandleFunc("GET /discover", srv.HandleFeatureOG)
	mux.HandleFunc("GET /discover/og.png", srv.HandleFeatureOGImage)
	mux.HandleFunc("GET /atlas", srv.HandleFeatureOG)
	mux.HandleFunc("GET /atlas/og.png", srv.HandleFeatureOGImage)
	// Root card for an org's white-label apex (e.g. a bot pasting tela.ngss.io/):
	// org-branded card instead of the bare SPA shell. Only the custom-domain Caddy
	// block routes "/" (bot-gated) + "/og.png" here; the canonical apex keeps its
	// marketing-landing OG. {$} matches ONLY "/", not a catch-all. See og_root.go.
	mux.HandleFunc("GET /{$}", srv.HandleRootOG)
	mux.HandleFunc("GET /og.png", srv.HandleRootOGImage)
	// Space overview card (entity, like /p): name-only, branded by the owning org.
	mux.HandleFunc("GET /spaces/{id}", srv.HandleSpaceOG)
	mux.HandleFunc("GET /spaces/{id}/og.png", srv.HandleSpaceOGImage)
	mux.HandleFunc("GET /api/public/spaces/{id}/pages/{page_id}", srv.GetPublicSpacePage)
	mux.HandleFunc("GET /api/public/spaces/{id}/pages/{page_id}/md", srv.ExportPublicSpacePageMarkdown)
	// Public decks: the live Present SPA + the first-slide cover, for public spaces.
	mux.HandleFunc("GET /api/public/spaces/{id}/pages/{page_id}/deck/spa/{path...}", srv.ServePublicDeckSPA)
	mux.HandleFunc("GET /api/public/spaces/{id}/pages/{page_id}/deck/cover", srv.ServePublicDeckCover)
	// Unified GitHub-style handle URLs: ONE namespace where {handle} resolves to a
	// user OR org public home, and {handle}/{slug} to that account's public space.
	// User precedence on a handle collision; public-visibility data only. See
	// public_handles.go.
	mux.HandleFunc("GET /api/public/by-handle/{handle}", srv.GetPublicByHandle)
	mux.HandleFunc("GET /api/public/by-handle/{handle}/spaces/{slug}", srv.GetPublicByHandleSpace)

	// M17.A.1 Feedback submit-only channel. Session OR bearer (any scope —
	// the bearer carve-out lives in auth.scopeAllowsRequest so the MCP
	// `submit_feedback` tool can use a read-scope key). Write-only in v0: no
	// list/get/patch/delete surface.
	mux.HandleFunc("POST /api/feedback", srv.CreateFeedback)

	// Browser-side error beacon (client_errors.go). The frontend's global error
	// reporter + React error boundary POST crashes here so they land in the
	// events feed + a Prometheus counter instead of dying silently in the user's
	// console. Session/bearer-authed (NOT public) — see the handler doc.
	mux.HandleFunc("POST /api/client-errors", srv.CreateClientError)

	mux.HandleFunc("GET /api/search", srv.Search)

	// RAG semantic retrieval (internal/rag). Hybrid chunk search + reindex,
	// scoped through space_access like every other read. 503 when the embedder
	// is unconfigured (TELA_RAG_EMBED_URL unset) so the routes register
	// unconditionally. Session OR bearer-read for search; membership for reindex.
	mux.HandleFunc("GET /api/rag/search", srv.RAGSearch)
	mux.HandleFunc("GET /api/rag/chunk", srv.RAGReadChunk)
	mux.HandleFunc("GET /api/rag/freshness", srv.RAGFreshness)
	mux.HandleFunc("POST /api/rag/reindex", srv.RAGReindex)
	mux.HandleFunc("POST /api/rag/ask", srv.RAGAsk)
	mux.HandleFunc("POST /api/rag/ask/stream", srv.RAGAskStream)
	mux.HandleFunc("GET /api/rag/ask/stream", srv.RAGAskAttach)
	// Knowledge-intelligence surface (built on the same index).
	mux.HandleFunc("GET /api/pages/{id}/related", srv.RAGRelated)
	mux.HandleFunc("POST /api/rag/suggest-links", srv.RAGSuggestLinks)
	mux.HandleFunc("GET /api/rag/overlaps", srv.RAGOverlaps)
	mux.HandleFunc("GET /api/rag/gaps", srv.RAGGaps)
	// Auto-summaries (internal/summarize). Status mirrors /api/rag/freshness
	// (always 200 + enabled flag); the queue action mirrors reindex (membership,
	// 503 when the LLM is unconfigured).
	mux.HandleFunc("GET /api/summaries/status", srv.SummariesStatus)
	mux.HandleFunc("POST /api/spaces/{id}/summarize", srv.SummarizeSpace)
	// Ask-first generative surface.
	mux.HandleFunc("POST /api/rag/draft", srv.RAGDraft)
	mux.HandleFunc("POST /api/rag/answer-to-page", srv.RAGAnswerToPage)
	mux.HandleFunc("GET /api/pages/{id}/questions", srv.RAGPageQuestions)

	mux.HandleFunc("GET /api/admin/users", srv.ListAdminUsers)
	mux.HandleFunc("POST /api/admin/users", srv.CreateAdminUser)
	mux.HandleFunc("PATCH /api/admin/users/{id}", srv.PatchAdminUser)
	mux.HandleFunc("DELETE /api/admin/users/{id}", srv.DeleteAdminUser)
	mux.HandleFunc("GET /api/admin/users/{id}/activity", srv.ListUserActivity)
	mux.HandleFunc("GET /api/admin/events", srv.ListEvents)
	// Client-error "Issues" view: browser error reports grouped by fingerprint
	// (admin_client_errors.go), plus per-issue recent occurrences.
	mux.HandleFunc("GET /api/admin/client-errors", srv.ListClientErrorGroups)
	mux.HandleFunc("GET /api/admin/client-errors/{fingerprint}", srv.ListClientErrorOccurrences)
	// Prometheus http_sd feed of active custom domains so the monitoring stack
	// probes every custom domain dynamically (uptime + cert) with no per-domain
	// config (admin_blackbox_targets.go).
	mux.HandleFunc("GET /api/admin/blackbox-targets", srv.BlackboxTargets)
	mux.HandleFunc("GET /api/admin/usage", srv.AdminUsage)
	// Instance-analytics dashboard (admin_stats.go): activity time-series, growth,
	// leaderboards, AI + error pulse, knowledge health — aggregated from events.
	mux.HandleFunc("GET /api/admin/stats", srv.AdminStats)
	// AI usage log (ai_usage.go): weekly token totals + per-model breakdown, the
	// raw material for cost estimates.
	mux.HandleFunc("GET /api/admin/ai-usage", srv.AdminAIUsage)
	// AI endpoints & reliability (ai_endpoints.go): per-service health, latency,
	// and the relief-proxy topology — the in-app failover breakdown.
	mux.HandleFunc("GET /api/admin/ai-endpoints", srv.AdminAIEndpoints)
	mux.HandleFunc("GET /api/admin/feedback", srv.ListFeedback)
	mux.HandleFunc("POST /api/admin/feedback/seen", srv.MarkFeedbackSeen)
	// Registered after /seen: a literal segment and a wildcard can coexist here
	// because they're distinct methods, but keep the order so the intent is
	// obvious if /seen ever gains a PATCH.
	mux.HandleFunc("PATCH /api/admin/feedback/{id}", srv.UpdateFeedbackStatus)
	mux.HandleFunc("GET /api/admin/settings", srv.GetInstanceSettings)
	mux.HandleFunc("PATCH /api/admin/settings", srv.PatchInstanceSettings)
	mux.HandleFunc("GET /api/admin/license", srv.GetLicense)
	mux.HandleFunc("PUT /api/admin/license", srv.PutLicense)
	mux.HandleFunc("DELETE /api/admin/license", srv.DeleteLicense)

	// Mention directory — active users for the @-mention picker (any member).
	mux.HandleFunc("GET /api/users", srv.ListUsers)
	mux.HandleFunc("PATCH /api/users/me", srv.UpdateMyProfile)
	mux.HandleFunc("DELETE /api/users/me", srv.DeleteMyAccount)
	mux.HandleFunc("POST /api/users/me/password", srv.ChangePassword)
	mux.HandleFunc("POST /api/users/me/quick-notes", srv.QuickNotes)
	mux.HandleFunc("GET /api/users/me/sessions", srv.ListMySessions)
	mux.HandleFunc("DELETE /api/users/me/sessions", srv.DeleteAllMySessionsExceptCurrent)
	mux.HandleFunc("DELETE /api/users/me/sessions/{id}", srv.DeleteMySession)

	// Per-user page favorites (starred pages) + the home-dashboard feeds.
	// Favorites are re-gated through space_access on read; recent-changes is the
	// latest edit per accessible page, built from page_revisions.
	mux.HandleFunc("GET /api/users/me/favorites", srv.ListFavorites)
	mux.HandleFunc("GET /api/pages/{id}/favorite", srv.GetFavoriteStatus)
	mux.HandleFunc("POST /api/pages/{id}/favorite", srv.AddFavorite)
	mux.HandleFunc("DELETE /api/pages/{id}/favorite", srv.DeleteFavorite)
	mux.HandleFunc("GET /api/recent-changes", srv.ListRecentChanges)

	// Per-user pinned spaces (sidebar "Pinned" group). Like favorites, re-gated
	// through space_access on read; pinning requires viewer+ access.
	mux.HandleFunc("GET /api/users/me/pinned-spaces", srv.ListPinnedSpaces)
	mux.HandleFunc("PUT /api/spaces/{id}/pin", srv.AddPinnedSpace)
	mux.HandleFunc("DELETE /api/spaces/{id}/pin", srv.DeletePinnedSpace)

	// Per-user hidden spaces (sidebar declutter — NOT access control; /spaces,
	// search and the palette still list them). Same shape as the pins above.
	mux.HandleFunc("GET /api/users/me/hidden-spaces", srv.ListHiddenSpaces)
	mux.HandleFunc("PUT /api/spaces/{id}/hide", srv.AddHiddenSpace)
	mux.HandleFunc("DELETE /api/spaces/{id}/hide", srv.DeleteHiddenSpace)

	// Notifications inbox (caller-scoped). Emitted best-effort from event sources
	// (today: page-body @-mentions); see docs/notifications.md.
	mux.HandleFunc("GET /api/notifications", srv.ListNotifications)
	mux.HandleFunc("GET /api/notifications/unread-count", srv.UnreadNotificationCount)
	mux.HandleFunc("POST /api/notifications/read-all", srv.MarkAllNotificationsRead)
	mux.HandleFunc("POST /api/notifications/{id}/read", srv.MarkNotificationRead)

	// Follow/subscribe (page + space) → opts into page_updated notifications.
	// Per-user notification preferences (event_type × channel toggles).
	mux.HandleFunc("GET /api/pages/{id}/subscription", srv.GetPageSubscription)
	mux.HandleFunc("POST /api/pages/{id}/subscription", srv.SubscribePage)
	mux.HandleFunc("DELETE /api/pages/{id}/subscription", srv.UnsubscribePage)
	mux.HandleFunc("GET /api/spaces/{id}/subscription", srv.GetSpaceSubscription)
	mux.HandleFunc("POST /api/spaces/{id}/subscription", srv.SubscribeSpace)
	mux.HandleFunc("DELETE /api/spaces/{id}/subscription", srv.UnsubscribeSpace)
	mux.HandleFunc("GET /api/users/me/subscriptions", srv.ListSubscriptions)
	mux.HandleFunc("GET /api/users/me/autowatch", srv.GetAutowatch)
	mux.HandleFunc("PUT /api/users/me/autowatch", srv.SetAutowatch)
	mux.HandleFunc("GET /api/users/me/notification-prefs", srv.GetNotificationPrefs)
	mux.HandleFunc("PUT /api/users/me/notification-prefs", srv.UpdateNotificationPref)

	// Sync delta feed (§4 D10): per-space append-only change_log ranged by a
	// monotonic seq cursor, so a syncing client pulls deltas instead of
	// re-scanning. Session OR bearer-read; membership on space_id required.
	mux.HandleFunc("GET /api/changes", srv.ListChanges)

	// "Connect your vault" (sync §16): user self-service WebDAV sync tokens — a
	// member mints a token for their own access (write or read-only, optionally
	// space-pinned) and gets a ready-to-paste rclone setup. Personal + membership
	// -gated, distinct from the instance-admin /api/api_keys CRUD. Revoke reuses
	// the owner-gated DELETE /api/api_keys/{id}.
	mux.HandleFunc("POST /api/sync/connections", srv.CreateSyncConnection)
	mux.HandleFunc("GET /api/sync/connections", srv.ListSyncConnections)

	// M16.A.1 API keys: bearer-token management. Instance-admin only via the
	// session cookie path, OR a bearer key with admin scope. Keys are issued
	// once on POST and never re-exposed — list/delete operate over key_prefix
	// + opaque id, not the raw token. Owner of a key can DELETE their own
	// (soft-revoke).
	mux.HandleFunc("POST /api/api_keys", srv.CreateAPIKey)
	mux.HandleFunc("GET /api/api_keys", srv.ListAPIKeys)
	mux.HandleFunc("DELETE /api/api_keys/{id}", srv.DeleteAPIKey)

	// M16.A.2 API-key audit log read. Owner-of-key OR instance-admin via the
	// cookie session; bearer-mode callers need admin scope (a read/write key
	// reading its own audit trail would let a stolen token enumerate the
	// surface used to detect it). Writes happen asynchronously in
	// auth.Middleware on the bearer-auth path.
	mux.HandleFunc("GET /api/api_keys/{id}/audit", srv.ListAPIKeyAudit)

	mux.HandleFunc("GET /api/spaces/{id}/members", srv.ListSpaceMembers)
	mux.HandleFunc("POST /api/spaces/{id}/members", srv.AddSpaceMember)
	mux.HandleFunc("PATCH /api/spaces/{id}/members/{user_id}", srv.PatchSpaceMember)
	mux.HandleFunc("DELETE /api/spaces/{id}/members/{user_id}", srv.DeleteSpaceMember)

	// Share a space by email (space_invites.go). POST resolves to an immediate
	// member grant when the address already has an account, otherwise to a pending
	// token invite delivered by mail; the invitee accepts via the shared
	// /invite/{token} flow below (or auto-joins on email verify). Owner-gated.
	mux.HandleFunc("GET /api/spaces/{id}/invites", srv.ListSpaceInvites)
	mux.HandleFunc("POST /api/spaces/{id}/invites", srv.CreateSpaceInvite)
	mux.HandleFunc("DELETE /api/spaces/{id}/invites/{inviteId}", srv.RevokeSpaceInvite)

	// #153 Organizations. A space can be shared with a whole org (grantable
	// principal); access resolves through the space_access view. Org CRUD is
	// instance-admin gated; membership is org-admin gated; auto-join domains are
	// instance-admin gated.
	mux.HandleFunc("GET /api/orgs", srv.ListOrgs)
	mux.HandleFunc("POST /api/orgs", srv.CreateOrg)
	mux.HandleFunc("GET /api/orgs/{id}", srv.GetOrg)
	mux.HandleFunc("PATCH /api/orgs/{id}", srv.UpdateOrg)
	mux.HandleFunc("DELETE /api/orgs/{id}", srv.DeleteOrg)
	// Per-org OIDC SSO connection (org_sso.go). Instance-admin only, like the
	// auto-join domain admin below.
	mux.HandleFunc("GET /api/orgs/{id}/sso", srv.GetOrgSSO)
	mux.HandleFunc("PUT /api/orgs/{id}/sso", srv.PutOrgSSO)
	mux.HandleFunc("DELETE /api/orgs/{id}/sso", srv.DeleteOrgSSO)
	// Per-org custom domains (org_hostnames.go). Org-admin self-serve (instance-
	// admin passes virtually); the verify step proves DNS ownership, instance-
	// admins may force-activate. Distinct from the email auto-join domains below.
	mux.HandleFunc("GET /api/orgs/{id}/hostnames", srv.ListOrgHostnames)
	mux.HandleFunc("POST /api/orgs/{id}/hostnames", srv.AddOrgHostname)
	mux.HandleFunc("POST /api/orgs/{id}/hostnames/{hostname}/verify", srv.VerifyOrgHostname)
	mux.HandleFunc("GET /api/orgs/{id}/hostnames/{hostname}/health", srv.OrgHostnameHealth)
	mux.HandleFunc("POST /api/orgs/{id}/hostnames/{hostname}/admin-login", srv.AdminDomainLoginMint)
	mux.HandleFunc("DELETE /api/orgs/{id}/hostnames/{hostname}", srv.DeleteOrgHostname)
	// Per-org login-method toggles + visual branding (org_login_settings.go /
	// org_branding.go), org-admin.
	mux.HandleFunc("GET /api/orgs/{id}/login-settings", srv.GetOrgLoginSettings)
	mux.HandleFunc("PUT /api/orgs/{id}/login-settings", srv.PutOrgLoginSettings)
	mux.HandleFunc("GET /api/orgs/{id}/branding", srv.GetOrgBranding)
	mux.HandleFunc("PUT /api/orgs/{id}/branding", srv.PutOrgBranding)
	mux.HandleFunc("POST /api/orgs/{id}/branding/logo", srv.UploadOrgLogo)
	mux.HandleFunc("DELETE /api/orgs/{id}/branding/logo", srv.DeleteOrgLogo)
	// The org logo is served from tela's own origin (PUBLIC — shown pre-auth on the
	// white-label login screen, and fetched by the server-side deck renderer). Under
	// /api/public/ so it's on IsPublicPath; content-addressed (?v=<hash>), GET-only.
	mux.HandleFunc("GET /api/public/orgs/{id}/logo", srv.ServeOrgLogo)
	// Host context: org branding + enabled sign-in methods for the request's
	// host. Public (host-derived, pre-login) — see IsPublicPath.
	mux.HandleFunc("GET /api/host-context", srv.HostContext)
	// Caddy on-demand-TLS ask endpoint (tls_check.go). Public (internal-network
	// only — see IsPublicPath and the proxy 404 for /api/internal/ from the WAN).
	mux.HandleFunc("GET /api/internal/tls-check", srv.TLSCheck)
	// Metering & tiers (limits.go / usage.go). Usage is readable by the account's
	// own principals; plans are public to any user; setting a plan is operator-only.
	mux.HandleFunc("GET /api/usage", srv.GetMyUsage)
	mux.HandleFunc("GET /api/plans", srv.ListPlans)
	mux.HandleFunc("GET /api/orgs/{id}/usage", srv.GetOrgUsage)
	mux.HandleFunc("PATCH /api/admin/plan", srv.SetAccountPlan)

	// Self-serve billing (billing.go) via Polar. Checkout + portal are
	// session-authed (org variants gate on org-admin). The webhook is PUBLIC —
	// MUST be on auth.IsPublicPath (/api/billing/webhook) so the session
	// middleware skips it; it self-authenticates by Standard Webhooks signature.
	mux.HandleFunc("POST /api/billing/checkout", srv.CreateCheckout)
	mux.HandleFunc("POST /api/billing/portal", srv.CreateBillingPortal)
	mux.HandleFunc("POST /api/billing/webhook", srv.PolarWebhook)
	// Self-serve self-host Enterprise licensing (selfhost_license.go): buy a key,
	// list your keys. The webhook above mints + emails on purchase.
	mux.HandleFunc("POST /api/billing/selfhost-checkout", srv.CreateSelfHostCheckout)
	mux.HandleFunc("POST /api/billing/selfhost-portal", srv.CreateSelfHostPortal)
	mux.HandleFunc("GET /api/licenses", srv.ListSelfHostLicenses)
	// PUBLIC (under /api/public/, on IsPublicPath): a self-hosted instance polls
	// this with its current key to pull the renewed one — self-authenticates by
	// the key's signature. See selfhost_license.go.
	mux.HandleFunc("GET /api/public/license/refresh", srv.RefreshSelfHostLicense)
	mux.HandleFunc("GET /api/orgs/{id}/members", srv.ListOrgMembers)
	mux.HandleFunc("POST /api/orgs/{id}/members", srv.AddOrgMember)
	mux.HandleFunc("PATCH /api/orgs/{id}/members/{user_id}", srv.PatchOrgMember)
	mux.HandleFunc("DELETE /api/orgs/{id}/members/{user_id}", srv.DeleteOrgMember)

	// Email invitations (self-serve teams). Admin manages pending invites here;
	// the invitee accepts via POST /api/me/accept-invite (session) or auto-joins
	// on email verify. GET /api/invites/{token} is public (self-auth via token,
	// for the logged-out accept page) — see org_invites.go + auth.IsPublicPath.
	// The token lookup + accept serve BOTH org and space invites (invites.go).
	mux.HandleFunc("GET /api/orgs/{id}/invites", srv.ListOrgInvites)
	mux.HandleFunc("POST /api/orgs/{id}/invites", srv.CreateOrgInvite)
	mux.HandleFunc("DELETE /api/orgs/{id}/invites/{inviteId}", srv.RevokeOrgInvite)
	mux.HandleFunc("GET /api/invites/{token}", srv.GetInvite)
	mux.HandleFunc("POST /api/me/accept-invite", srv.AcceptInvite)

	// Org-scoped access audit: a company's own admin sees the membership / group
	// / grant / domain history for THEIR org (not the whole instance). Org-admin
	// gated via requireOrgAdmin — the same primitive future org-admin
	// capabilities (user/space management) will hang off.
	mux.HandleFunc("GET /api/orgs/{id}/audit", srv.ListOrgAudit)

	// Group sub-teams: a third grantable principal nested under an org. Org-admin
	// gated; membership ⊆ org membership (DB-enforced). See docs/access-model.md.
	mux.HandleFunc("GET /api/groups", srv.ListMyGroups)
	mux.HandleFunc("GET /api/orgs/{id}/groups", srv.ListGroups)
	mux.HandleFunc("POST /api/orgs/{id}/groups", srv.CreateGroup)
	mux.HandleFunc("PATCH /api/orgs/{id}/groups/{group_id}", srv.UpdateGroup)
	mux.HandleFunc("DELETE /api/orgs/{id}/groups/{group_id}", srv.DeleteGroup)
	mux.HandleFunc("GET /api/orgs/{id}/groups/{group_id}/members", srv.ListGroupMembers)
	mux.HandleFunc("POST /api/orgs/{id}/groups/{group_id}/members", srv.AddGroupMember)
	mux.HandleFunc("DELETE /api/orgs/{id}/groups/{group_id}/members/{user_id}", srv.DeleteGroupMember)

	// #153 Space↔org grants: share a space with an org at editor/viewer. Owner
	// gated. Keyed by grant id so 'group' principals slot in without a new route.
	mux.HandleFunc("GET /api/spaces/{id}/grants", srv.ListSpaceGrants)
	mux.HandleFunc("POST /api/spaces/{id}/grants", srv.AddSpaceGrant)
	mux.HandleFunc("PATCH /api/spaces/{id}/grants/{grant_id}", srv.PatchSpaceGrant)
	mux.HandleFunc("DELETE /api/spaces/{id}/grants/{grant_id}", srv.DeleteSpaceGrant)

	// Effective access: resolved people + provenance + effective role for a
	// space (any member). The one legible answer to "who can see this, and why".
	mux.HandleFunc("GET /api/spaces/{id}/access", srv.GetSpaceAccess)

	// #153 Auto-join email-domain mappings (instance-admin only).
	mux.HandleFunc("GET /api/admin/org-domains", srv.ListOrgDomains)
	mux.HandleFunc("POST /api/admin/org-domains", srv.CreateOrgDomain)
	mux.HandleFunc("DELETE /api/admin/org-domains/{domain}", srv.DeleteOrgDomain)

	// Access audit trail (instance-admin only).
	mux.HandleFunc("GET /api/admin/access-audit", srv.ListAccessAudit)

	// M7.1 LiveCollab: ws upgrade for Yjs relay. Authed via auth.Middleware
	// on the upgrade request — must NOT be added to auth.IsPublicPath.
	mux.HandleFunc("GET /ws/pages/{id}", srv.WSPage)

	// M13.2 RichView PNG sidecar GET: public, content-addressed by scene_hash.
	// MUST be on auth.IsPublicPath via the /api/diagrams/ HasPrefix branch.
	// Lives outside /api/pages/* so the session-gated PageView prefix doesn't
	// need regex special-casing to carve out a public hole. The .png suffix
	// lives inside the {file} path value (Go 1.22 mux wildcards must end a
	// segment); the handler strips it before validating against the hex regex.
	mux.HandleFunc("GET /api/diagrams/{page_id}/{file}", srv.ServePageDiagramPNG)

	// Legacy public image serve — content-addressed, immutable. Serve-only:
	// uploads moved to the unified space_files attachments store, but this stays
	// for images already embedded in historical page bodies. MUST be on
	// auth.IsPublicPath via the /api/images/ HasPrefix branch.
	mux.HandleFunc("GET /api/images/{page_id}/{file}", srv.ServePageImage)

	// Unified attachment blob serve — content-addressed space_files, public
	// (incl. share/public readers). MUST be on auth.IsPublicPath via the
	// /api/files/ HasPrefix branch. Non-image types are forced to download.
	mux.HandleFunc("GET /api/files/{space_id}/{file}", srv.ServeSpaceFile)

	// M11.0 OG share: public unauthenticated route. Crawler UAs get OG HTML;
	// real browsers get 302'd to the SPA. MUST be on auth.IsPublicPath.
	mux.HandleFunc("GET /p/{id}", srv.HandlePublicShare)
	// M11.1 OG image: public, not UA-gated (image fetchers carry arbitrary
	// UAs). Registered before /p/{id}/{slug} so the more-specific literal
	// pattern wins regardless of mux iteration order.
	mux.HandleFunc("GET /p/{id}/og.png", srv.HandleOGImage)
	mux.HandleFunc("GET /p/{id}/{slug}", srv.HandlePublicShare)

	// M15.5 PublicShare OG bot-gate: Caddy's /share/* block routes bot UAs
	// here so Slack / Twitter / Discord unfurl shared pages with real OG
	// metadata; real browsers fall through to the frontend SPA (M15.1). MUST
	// be on auth.IsPublicPath. Defense-in-depth UA check inside the handler
	// means a misconfigured Caddy block 404s real browsers instead of
	// serving the OG envelope in place of the SPA.
	mux.HandleFunc("GET /share/{token}", srv.HandlePublicShareLink)
	// Cosmetic-slug variant (/share/{token}/{slug}) — the slug is ignored; the
	// token is canonical. Distinct from the 4-segment descendant pattern below.
	mux.HandleFunc("GET /share/{token}/{slug}", srv.HandlePublicShareLink)
	mux.HandleFunc("GET /share/{token}/p/{page_id}", srv.HandlePublicShareLinkPage)
}
