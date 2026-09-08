package api

import (
	"bytes"
	_ "embed"
	"net/http"
)

// openapi.go serves tela's machine-readable API description.
//
// WHY IT LIVES HERE (and not in landing/public/): the spec describes the routes
// registered in router.go and the shapes returned by the handlers in this
// package. Parked in the marketing build it would drift the moment a handler
// changed, with nothing in the repo tying the two together; embedded next to
// the code it documents, a route change and its description are one diff, and
// openapi_test.go can assert the spec only names paths the mux actually
// registers. Same reasoning as blocks_gen.json / the MCP authoring guide.
//
// WHY THE PUBLIC PATH IS /api/public/openapi.json: /api/public/ is already an
// auth.IsPublicPath prefix reserved for GET-only, self-authenticating,
// read-only surfaces — which a static document trivially is. Minting a NEW
// top-level public prefix (/openapi.json) would mean widening IsPublicPath, and
// every future route under a public prefix inherits the bypass (see the
// public-path rule in CLAUDE.md). Reusing the existing prefix costs nothing and
// widens no hole. The pretty, crawler-facing /openapi.json is a Caddy rewrite
// onto this route (deploy/proxy/sites.caddy), the same shape
// /sitemap-public.xml already uses — so it works identically in the standalone
// and split deployment shapes, both of which import sites.caddy.
//
// Regenerating: the spec is hand-maintained. When you add or change a route
// that the spec covers, update openapi.json in the same commit.

//go:embed openapi.json
var openAPISpec []byte

// openAPICanonicalServer is the servers[0].url baked into openapi.json. A
// self-hosted instance is not telawiki.com, so ServeOpenAPI swaps this for the
// instance's own origin. openapi_test.go asserts the literal appears exactly
// once in the embedded document — if the spec is edited so it no longer does,
// the substitution would silently no-op and every self-hoster would advertise
// telawiki.com as their API server.
const openAPICanonicalServer = `"url": "https://telawiki.com"`

// ServeOpenAPI handles GET /api/public/openapi.json (and, via the Caddy
// rewrite, GET /openapi.json). Public and static — no database access, no
// caller data. CORS is wide open because a browser-based API explorer (Scalar,
// Swagger UI, an agent directory's web console) fetches it cross-origin, the
// same reason ServePRM sets it.
func (s *Server) ServeOpenAPI(w http.ResponseWriter, r *http.Request) {
	origin := canonicalBaseURL()
	if origin == "" {
		origin = requestScheme(r) + "://" + r.Host
	}
	body := bytes.Replace(openAPISpec, []byte(openAPICanonicalServer), []byte(`"url": "`+origin+`"`), 1)

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=300")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	_, _ = w.Write(body)
}
