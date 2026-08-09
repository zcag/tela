package api

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/zcag/tela/backend/internal/extract"
)

// The ordinary-page half of the agent authoring loop, mirroring lint_deck /
// preview_deck: lint_page says "here's where the render disagrees with your
// markdown", preview_page says "here's what the page actually looks like".
//
// preview_page exists because a rule list can only catch the divergences we
// thought of. Reading the rendered output back catches the ones we didn't —
// which is the whole reason this feature exists, since every instance of the
// bug class that motivated it was invisible in the source and obvious in the
// render.

// ── lint_page ────────────────────────────────────────────────────────────────

type lintPageIn struct {
	ID int64 `json:"id" jsonschema:"id of the page to check"`
}

func (s *Server) mcpLintPage(ctx context.Context, req *mcp.CallToolRequest, in lintPageIn) (*mcp.CallToolResult, pageLintOut, error) {
	u, k := mcpIdentity(req)
	if u == nil {
		return mcpUnauthErr(), pageLintOut{}, nil
	}
	p, ae := s.getPageCore(ctx, u, k, in.ID)
	if ae != nil {
		return mcpErr(ae), pageLintOut{}, nil
	}
	return nil, s.lintPage(ctx, p, p.Body), nil
}

// ── preview_page ─────────────────────────────────────────────────────────────

type previewPageIn struct {
	ID     int64  `json:"id" jsonschema:"id of the page to render and read back"`
	Format string `json:"format,omitempty" jsonschema:"text (default) returns the rendered page as plain text — enough to catch prose that the renderer swallowed or reordered; image returns a screenshot of the reader; both returns each"`
}

// previewImageCap keeps a long page's screenshot from dominating a tool result.
// Past it the text still comes back with a note, which is more useful than a
// truncated image.
const previewImageCap = 3 << 20

func (s *Server) mcpPreviewPage(ctx context.Context, req *mcp.CallToolRequest, in previewPageIn) (*mcp.CallToolResult, any, error) {
	u, k := mcpIdentity(req)
	if u == nil {
		return mcpUnauthErr(), nil, nil
	}
	p, ae := s.getPageCore(ctx, u, k, in.ID)
	if ae != nil {
		return mcpErr(ae), nil, nil
	}
	// A deck's body is Slidev markdown and a sheet's is a grid; neither reads as
	// prose through this path, and both have a preview of their own.
	if isDeckBag(p.Props) {
		return mcpErr(&apiErr{http.StatusBadRequest, "wrong_tool",
			"this page is a deck — call preview_deck to see its slides"}), nil, nil
	}

	wantText := in.Format != "image"
	wantImage := in.Format == "image" || in.Format == "both"
	url := pdfRenderBaseURL() + "/print/" + s.mintPrintToken(p.ID)

	var content []mcp.Content
	if wantText {
		pdf, err := renderPDF(ctx, url, p.Title)
		if err != nil {
			return mcpErr(previewUnavailable(err)), nil, nil
		}
		text, ok := extract.Text("application/pdf", "page.pdf", pdf)
		if !ok {
			return mcpErr(&apiErr{http.StatusBadGateway, "preview_failed",
				"the page rendered but no text could be read back from it"}), nil, nil
		}
		// The caveat is load-bearing: the text comes out of a PDF layer, so
		// ligatures and line breaks are artifacts of the extraction, not of the
		// page. Without saying so, an agent diffing intent against output chases
		// "ﬁlter" as a rendering defect.
		content = append(content, &mcp.TextContent{Text: fmt.Sprintf(
			"%q as the reader renders it. Compare this against what you wrote — anything MISSING here is missing on the page, and anything reworded or reordered really is reworded or reordered.\n\n"+
				"Ignore typographic noise: this is read back from a rendered document, so ligatures (ﬁ, ﬂ), hyphenation and line breaks come from the layout, not your markdown.\n\n%s",
			p.Title, strings.TrimSpace(text))})
	}
	if wantImage {
		img, err := renderScreenshot(ctx, url)
		switch {
		case err != nil && !wantText:
			return mcpErr(previewUnavailable(err)), nil, nil
		case err != nil:
			content = append(content, &mcp.TextContent{Text: "(the screenshot could not be rendered; the text above is the render)"})
		case len(img) > previewImageCap:
			content = append(content, &mcp.TextContent{Text: fmt.Sprintf(
				"(the screenshot is %d KB — too large to return; use format:\"text\" for this page)", len(img)>>10)})
		default:
			content = append(content, &mcp.ImageContent{Data: img, MIMEType: "image/jpeg"})
		}
	}
	return &mcp.CallToolResult{Content: content}, nil, nil
}

func previewUnavailable(err error) *apiErr {
	return &apiErr{http.StatusBadGateway, "preview_unavailable",
		"could not render the page for preview: " + err.Error()}
}
