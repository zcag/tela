package api

import (
	"encoding/xml"
	"net/http"
	"strconv"
	"time"
)

// RSS 2.0 feed for a public space — the blog's syndication surface.
// GET /api/public/spaces/{id}/feed.xml. Under /api/public/ so it's on
// auth.IsPublicPath and self-authenticates via requirePublicSpace (GET-only,
// read-only). Items are the space's top-level pages (the "posts"), newest
// published first, mirroring the front-page index.

type rssFeed struct {
	XMLName xml.Name   `xml:"rss"`
	Version string     `xml:"version,attr"`
	AtomNS  string     `xml:"xmlns:atom,attr"`
	Channel rssChannel `xml:"channel"`
}

type rssChannel struct {
	Title       string    `xml:"title"`
	Link        string    `xml:"link"`
	Description string    `xml:"description"`
	AtomLink    atomLink  `xml:"atom:link"`
	Items       []rssItem `xml:"item"`
}

type atomLink struct {
	Href string `xml:"href,attr"`
	Rel  string `xml:"rel,attr"`
	Type string `xml:"type,attr"`
}

type rssItem struct {
	Title       string  `xml:"title"`
	Link        string  `xml:"link"`
	GUID        rssGUID `xml:"guid"`
	PubDate     string  `xml:"pubDate"`
	Description string  `xml:"description"`
}

type rssGUID struct {
	Value       string `xml:",chardata"`
	IsPermaLink bool   `xml:"isPermaLink,attr"`
}

// GetPublicSpaceFeed — GET /api/public/spaces/{id}/feed.xml.
func (s *Server) GetPublicSpaceFeed(w http.ResponseWriter, r *http.Request) {
	id, ok := parseIDParam(w, r, "id")
	if !ok {
		return
	}
	sp, ok := s.requirePublicSpace(w, r, id)
	if !ok {
		return
	}

	rows, err := s.DB.QueryContext(r.Context(),
		`SELECT id, title, body, props, created_at
		   FROM pages
		  WHERE space_id = $1 AND parent_id IS NULL AND deleted_at IS NULL
		  ORDER BY created_at DESC, id DESC
		  LIMIT 50`, id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "load feed failed")
		return
	}
	defer rows.Close()

	base := canonicalBaseURL()
	// One feed = one space, so resolve its pretty prefix once, not per item.
	prefix := s.spacePrettyPrefix(r.Context(), id)
	items := []rssItem{}
	for rows.Next() {
		var (
			pid      int64
			title    string
			body     string
			propsRaw []byte
			created  string
		)
		if err := rows.Scan(&pid, &title, &body, &propsRaw, &created); err != nil {
			writeError(w, http.StatusInternalServerError, "internal", "scan feed row failed")
			return
		}
		link := base + readerPathUnder(prefix, id, pid, title)
		items = append(items, rssItem{
			Title:       title,
			Link:        link,
			GUID:        rssGUID{Value: link, IsPermaLink: true},
			PubDate:     telaTimeToRFC1123Z(created),
			Description: postExcerpt(body, decodeProps(propsRaw), 280),
		})
	}
	if err := rows.Err(); err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "iterate feed failed")
		return
	}

	feedURL := base + "/api/public/spaces/" + strconv.FormatInt(id, 10) + "/feed.xml"
	feed := rssFeed{
		Version: "2.0",
		AtomNS:  "http://www.w3.org/2005/Atom",
		Channel: rssChannel{
			Title:       sp.Name,
			Link:        base + publicSpacePath(id),
			Description: sp.Description,
			AtomLink:    atomLink{Href: feedURL, Rel: "self", Type: "application/rss+xml"},
			Items:       items,
		},
	}

	out, err := xml.MarshalIndent(feed, "", "  ")
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "encode feed failed")
		return
	}
	w.Header().Set("Content-Type", "application/rss+xml; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=300")
	_, _ = w.Write([]byte(xml.Header))
	_, _ = w.Write(out)
}

// telaTimeToRFC1123Z converts a tela TEXT timestamp ('YYYY-MM-DD HH:MM:SS' UTC)
// to the RFC1123Z form RSS/HTTP dates use. Empty string on parse failure.
func telaTimeToRFC1123Z(s string) string {
	t, err := time.Parse("2006-01-02 15:04:05", s)
	if err != nil {
		return ""
	}
	return t.UTC().Format(time.RFC1123Z)
}
