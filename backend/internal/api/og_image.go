package api

import (
	"bytes"
	"database/sql"
	_ "embed"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/jpeg" // encode the share image; its init also registers the JPEG decoder
	"image/png"
	"log/slog"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	xdraw "golang.org/x/image/draw"
	"golang.org/x/image/font"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/math/fixed"
)

// Geist (Vercel, OFL) — tela's brand typeface per DESIGN.md (Geist family only).
// Static 400/700 instances of the variable font, embedded so the renderer stays
// pure-Go with no runtime font dependency. See fonts/OFL.txt.
//
//go:embed fonts/Geist-Regular.ttf
var geistRegularTTF []byte

//go:embed fonts/Geist-Bold.ttf
var geistBoldTTF []byte

// ogShareMaxWidth bounds the deck cover used as a share image. A deck slide
// renders at ~1960×1104 (a ~1.8 MB PNG of a full-bleed, often photographic
// slide) — too heavy for link-preview fetchers that cap the image (WhatsApp
// drops previews over a few hundred KB). Downscaling to 1200-wide + JPEG gets
// it to ~100–150 KB while staying crisp at OG render sizes.
const ogShareMaxWidth = 1200

// OG card layout: a 1200×630 canvas with an 80px inset. Palette is tela's
// DESIGN.md tokens (brand-hue 277 indigo) converted OKLCH→sRGB and hardcoded, so
// the renderer stays pure-Go with no dependency on the FE's tokens.css.
const (
	ogCanvasWidth   = 1200
	ogCanvasHeight  = 630
	ogMargin        = 80
	ogDrawableWidth = ogCanvasWidth - 2*ogMargin
	ogAccentY       = ogMargin
	ogAccentWidth   = 64
	ogAccentHeight  = 5
	ogTitleSize     = 76
	ogTitleLineH    = 90
	ogSubtitleSize  = 34
	ogFooterSize    = 27
	ogMaxTitleLines = 3
	ogLogoMaxWidth  = 440 // org logo bounding box in the branded card header
	ogLogoMaxHeight = 68
	ogWeaveCell     = 44 // woven-grid pitch (tela = loom; the signature device)
	ogKickerSize    = 26 // accent eyebrow (e.g. "ASK YOUR DOCS") above the title

	// Hero composition (root + feature cards): a brand lockup (mark + wordmark)
	// top-left, then a bottom band of pill chips on the left and the domain in
	// accent on the right — echoing the marketing OG, but rendered + branded.
	ogMarkSize     = 60 // rounded accent square holding the brand initial
	ogMarkRadius   = 16
	ogWordmarkSize = 40 // wordmark next to the mark in the lockup
	ogChipH        = 50 // pill height in the bottom band
	ogChipRadius   = 25
	ogChipPadX     = 22 // horizontal text padding inside a chip
	ogChipGap      = 14 // gap between chips
	ogChipTextSize = 23
)

// ogAccentTintWeight is how much the org accent bleeds into the dark ink
// background on a branded card. Kept low so the surface stays a dark void and
// light title/subtitle text remains legible against any accent.
const ogAccentTintWeight = 0.14

var (
	ogBgColor       = color.RGBA{R: 0x0b, G: 0x0d, B: 0x15, A: 0xff} // ink-000 void
	ogBgTop         = color.RGBA{R: 0x13, G: 0x15, B: 0x22, A: 0xff} // subtle top of the gradient
	ogTitleColor    = color.RGBA{R: 0xf1, G: 0xf3, B: 0xfc, A: 0xff} // text-900
	ogSubtitleColor = color.RGBA{R: 0xb4, G: 0xb7, B: 0xc2, A: 0xff} // text-700
	ogFooterColor   = color.RGBA{R: 0x8f, G: 0x91, B: 0x9f, A: 0xff} // text-500
	ogRuleColor     = color.RGBA{R: 0x2b, G: 0x2d, B: 0x38, A: 0xff} // line-rule hairline
	ogWeaveColor    = color.RGBA{R: 0x1b, G: 0x1c, B: 0x24, A: 0xff} // dim woven thread
	ogAccentColor   = color.RGBA{R: 0x52, G: 0x4c, B: 0xe3, A: 0xff} // indigo-fill (tela brand)
	ogWhite         = color.RGBA{R: 0xff, G: 0xff, B: 0xff, A: 0xff} // brand-mark initial
	ogChipBgColor   = color.RGBA{R: 0x1a, G: 0x1c, B: 0x27, A: 0xff} // chip fill (panel over the void)
)

// *opentype.Font is concurrent-safe per its docstring; the per-request
// *opentype.Face wrapper is not, so we keep the parsed fonts here and build
// faces per render in renderOGCard.
var (
	ogBoldFont    *opentype.Font
	ogRegularFont *opentype.Font
)

func init() {
	bold, err := opentype.Parse(geistBoldTTF)
	if err != nil {
		panic("og_image: parse Geist-Bold: " + err.Error())
	}
	regular, err := opentype.Parse(geistRegularTTF)
	if err != nil {
		panic("og_image: parse Geist-Regular: " + err.Error())
	}
	ogBoldFont = bold
	ogRegularFont = regular
}

// HandleOGImage returns the share image for a page: a deck's first slide
// (downscaled + JPEG via shrinkShareImage), else the server-rendered 1200×630
// PNG title card.
// Public — middleware bypasses /p/* and this route is NOT UA-gated because
// image fetchers (Slack, Twitter, Discord, link-preview proxies) carry
// arbitrary or empty UAs; blocking them would break the OG card path for half
// the real-world traffic.
func (s *Server) HandleOGImage(w http.ResponseWriter, r *http.Request) {
	raw := r.PathValue("id")
	pageID, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || pageID <= 0 {
		writeNotFoundHTML(w)
		return
	}

	var (
		title      string
		spaceName  string
		updatedAt  string
		body       string
		propsRaw   []byte
		spaceID    int64
		ownerOrgID int64 // NULL space.org_id scans as 0 via COALESCE
		visibility string
	)
	err = s.DB.QueryRowContext(r.Context(),
		`SELECT p.title, sp.name, p.updated_at, p.body, p.props, p.space_id, COALESCE(sp.org_id, 0), sp.visibility
		   FROM pages p
		   JOIN spaces sp ON sp.id = p.space_id
		  WHERE p.id = $1 AND p.deleted_at IS NULL`, pageID,
	).Scan(&title, &spaceName, &updatedAt, &body, &propsRaw, &spaceID, &ownerOrgID, &visibility)
	if errors.Is(err, sql.ErrNoRows) {
		writeNotFoundHTML(w)
		return
	}
	if err != nil {
		slog.Error("og_image: load page", "page_id", pageID, "err", err)
		writeInternalHTML(w)
		return
	}

	// A private page's card carries its title (and a deck's first slide), so it
	// needs the sig its OG envelope hands out (ogImageURL); fetched by id alone
	// it's a generic card branded only by the request host, like the envelope.
	if visibility != spaceVisibilityPublic && !s.validOGImageSig(pageID, r.URL.Query().Get("sig")) {
		png, err := renderOGCardOpts(ogCardOpts{title: "A page on " + s.ogSiteName(r, 0), brand: s.resolveOGBrand(r, 0)})
		if err != nil {
			writeInternalHTML(w)
			return
		}
		w.Header().Set("Content-Type", "image/png")
		w.Header().Set("Cache-Control", "public, max-age=3600")
		w.Header().Set("Content-Length", strconv.Itoa(len(png)))
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(png)
		return
	}

	// Weak ETag: rendering is deterministic in principle, but font-library bytes
	// can vary across builds, so the weak form is the honest one. Key on the
	// page's updated_at unix second, which bumps on every body/title edit.
	var updatedUnix int64
	if t, perr := time.Parse(sinceLayout, updatedAt); perr == nil {
		updatedUnix = t.Unix()
	}
	// Resolve the org brand (logo/accent/name) for this card. Folding its
	// signature into the ETag busts caches when an org changes its branding.
	brand := s.resolveOGBrand(r, ownerOrgID)
	// Fold the build commit into the ETag: the card is rendered by this binary, so
	// a deploy that changes the rendering (like blanking sheet data) must produce a
	// new ETag — otherwise a revalidating cache (Cloudflare, social crawlers) gets a
	// 304 on the unchanged page and serves the stale image forever.
	etag := fmt.Sprintf(`W/"og-%d-%d-%s-%s"`, pageID, updatedUnix, brand.sig, Commit)

	if r.Header.Get("If-None-Match") == etag {
		w.Header().Set("ETag", etag)
		w.Header().Set("Cache-Control", "public, max-age=3600")
		w.WriteHeader(http.StatusNotModified)
		return
	}

	// A deck's share image is its first slide (its visual identity), for public
	// decks and for private ones reached through a signed URL. Best-effort + time-bounded — fall back to the generic card
	// if the cover render is slow or unavailable so crawlers always get something.
	if isDeckBag(decodeProps(propsRaw)) {
		if raw, ct, ok := s.deckCoverPNG(r.Context(), body, decodeProps(propsRaw), spaceID); ok {
			img, ict := shrinkShareImage(raw, ct)
			w.Header().Set("Content-Type", ict)
			w.Header().Set("Cache-Control", "public, max-age=3600")
			w.Header().Set("ETag", etag)
			w.Header().Set("Content-Length", strconv.Itoa(len(img)))
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(img)
			return
		}
	}

	// A sheet's share image is a branded card with a generic spreadsheet visual
	// (column letters, row numbers, placeholder cells) — enough to read as a
	// spreadsheet at a glance WITHOUT exposing any of the sheet's actual cell
	// contents (a public OG card must never leak page data). Fall back to the
	// plain card if the render fails.
	if isSheetBag(decodeProps(propsRaw)) {
		if pngBytes, gerr := renderSheetOGCard(title, brand); gerr == nil {
			w.Header().Set("Content-Type", "image/png")
			w.Header().Set("Cache-Control", "public, max-age=3600")
			w.Header().Set("ETag", etag)
			w.Header().Set("Content-Length", strconv.Itoa(len(pngBytes)))
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(pngBytes)
			return
		}
	}

	pngBytes, err := renderOGCard(title, "in "+spaceName, brand)
	if err != nil {
		slog.Error("og_image: render page", "page_id", pageID, "err", err)
		writeInternalHTML(w)
		return
	}

	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "public, max-age=3600")
	w.Header().Set("ETag", etag)
	w.Header().Set("Content-Length", strconv.Itoa(len(pngBytes)))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(pngBytes)
}

// shrinkShareImage downscales a deck cover to a link-preview-friendly size and
// re-encodes it as JPEG (deck slides are photographic — JPEG is far smaller than
// PNG). Returns the bytes + content-type to serve. Best-effort: if decode or
// re-encode fails, or the source is already small enough, it returns the
// original bytes/ct unchanged so the OG path never breaks on an odd cover.
func shrinkShareImage(raw []byte, ct string) ([]byte, string) {
	src, _, err := image.Decode(bytes.NewReader(raw))
	if err != nil {
		return raw, ct
	}
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	if w <= 0 || h <= 0 {
		return raw, ct
	}
	// Fit within ogShareMaxWidth, preserving aspect; never upscale.
	dw, dh := w, h
	if w > ogShareMaxWidth {
		dw = ogShareMaxWidth
		dh = h * ogShareMaxWidth / w
	}
	dst := image.NewRGBA(image.Rect(0, 0, dw, dh))
	xdraw.CatmullRom.Scale(dst, dst.Bounds(), src, b, xdraw.Over, nil)

	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, dst, &jpeg.Options{Quality: 82}); err != nil {
		return raw, ct
	}
	// Keep the original if our re-encode somehow came out larger (e.g. a tiny
	// source that was already optimally compressed).
	if buf.Len() >= len(raw) {
		return raw, ct
	}
	return buf.Bytes(), "image/jpeg"
}

// renderOGImage paints the unbranded 1200×630 card. Thin wrapper over
// renderOGCard with the zero brand — kept for callers/tests that don't carry an
// org brand. subtitle is rendered verbatim (page cards pass "in <space>").
func renderOGImage(title, subtitle string) ([]byte, error) {
	return renderOGCard(title, subtitle, ogBrand{})
}

// renderOGCard paints a 1200×630 RGBA card and returns PNG-encoded bytes. With
// the zero brand it renders the default dark tela card (blue accent bar, "tela"
// footer); with a brand it tints the background toward the org accent, draws the
// org logo in the header, and footers the org name — the full-brand white-label
// card. Pure function — no DB / no http — so tests can drive it directly.
//
// Builds three opentype.Face values per call because opentype.Face is
// documented as not safe for concurrent use; sharing a Face across goroutines
// races on its internal sfnt.Buffer / vector.Rasterizer / mask. The parsed
// *opentype.Font values are concurrent-safe and live at package scope.
// ogCardOpts configures a rendered card. kicker is an accent eyebrow above the
// title that frames the card as an action (e.g. "ASK YOUR DOCS") rather than an
// article; maxTitleLines caps the title wrap (0 → ogMaxTitleLines); brand carries
// the org logo/accent/name.
type ogCardOpts struct {
	kicker        string
	title         string
	subtitle      string
	maxTitleLines int
	chips         []string // pill labels along the bottom-left (hero cards)
	accentLabel   string   // bottom-right accent text, e.g. the domain (hero cards)
	sheetMock     bool     // when set (a sheet), draw a generic spreadsheet mock instead of the subtitle
	brand         ogBrand
}

// renderOGCard renders a plain card (no kicker, default title wrap).
func renderOGCard(title, subtitle string, brand ogBrand) ([]byte, error) {
	return renderOGCardOpts(ogCardOpts{title: title, subtitle: subtitle, brand: brand})
}

func renderOGCardOpts(o ogCardOpts) ([]byte, error) {
	titleFace, err := opentype.NewFace(ogBoldFont, &opentype.FaceOptions{
		Size: ogTitleSize, DPI: 72, Hinting: font.HintingFull,
	})
	if err != nil {
		return nil, fmt.Errorf("og: title face: %w", err)
	}
	defer titleFace.Close()

	subtitleFace, err := opentype.NewFace(ogRegularFont, &opentype.FaceOptions{
		Size: ogSubtitleSize, DPI: 72, Hinting: font.HintingFull,
	})
	if err != nil {
		return nil, fmt.Errorf("og: subtitle face: %w", err)
	}
	defer subtitleFace.Close()

	footerFace, err := opentype.NewFace(ogBoldFont, &opentype.FaceOptions{
		Size: ogFooterSize, DPI: 72, Hinting: font.HintingFull,
	})
	if err != nil {
		return nil, fmt.Errorf("og: footer face: %w", err)
	}
	defer footerFace.Close()

	img := image.NewRGBA(image.Rect(0, 0, ogCanvasWidth, ogCanvasHeight))

	// The accent is tela's indigo by default, or the org's accent when branded —
	// the single earned color across the whole card (woven threads, light sweep,
	// the header mark/rule).
	accent := ogAccentColor
	if o.brand.hasAccent {
		accent = o.brand.accent
	}
	paintOGBackground(img, accent, o.brand.hasAccent)

	// "Hero" cards (root + feature) carry chips and/or a domain label: they get a
	// brand lockup up top and a bottom band of chips + domain, echoing the
	// marketing OG. Entity cards (page/space) have neither → the plain footer.
	hero := len(o.chips) > 0 || o.accentLabel != ""

	// Header cursor: org logo if present; else, on a hero card, a mark+wordmark
	// brand lockup; else a short accent rule (the plain page-card look). The
	// kicker eyebrow (when set) and the title sit below.
	top := ogMargin
	headerH := 0
	switch {
	case o.brand.logo != nil:
		headerH = drawLogoFit(img, o.brand.logo, ogMargin, ogMargin, ogLogoMaxWidth, ogLogoMaxHeight)
	case hero:
		lh, lerr := drawBrandLockup(img, accent, brandName(o.brand))
		if lerr != nil {
			return nil, lerr
		}
		headerH = lh
	}
	top += headerH

	var titleY int
	switch {
	case o.kicker != "":
		kickerFace, kerr := opentype.NewFace(ogBoldFont, &opentype.FaceOptions{
			Size: ogKickerSize, DPI: 72, Hinting: font.HintingFull,
		})
		if kerr != nil {
			return nil, fmt.Errorf("og: kicker face: %w", kerr)
		}
		defer kickerFace.Close()
		ky := ogMargin + ogKickerSize
		if headerH > 0 {
			ky = top + 26 + ogKickerSize
		}
		mk := ogKickerSize - 7
		draw.Draw(img, image.Rect(ogMargin, ky-mk, ogMargin+mk, ky), &image.Uniform{C: accent}, image.Point{}, draw.Src)
		kd := &font.Drawer{Dst: img, Src: &image.Uniform{C: accent}, Face: kickerFace, Dot: fixed.P(ogMargin+mk+14, ky)}
		kd.DrawString(strings.ToUpper(o.kicker))
		titleY = ky + 30 + ogTitleSize
	case headerH > 0:
		titleY = top + 44 + ogTitleSize
	default:
		draw.Draw(img, image.Rect(ogMargin, ogAccentY, ogMargin+ogAccentWidth, ogAccentY+ogAccentHeight),
			&image.Uniform{C: accent}, image.Point{}, draw.Src)
		titleY = ogAccentY + ogAccentHeight + 34 + ogTitleSize
	}

	maxLines := o.maxTitleLines
	if maxLines <= 0 {
		maxLines = ogMaxTitleLines
	}
	titleLines := wrapLines(titleFace, o.title, ogDrawableWidth, maxLines)
	titleDrawer := &font.Drawer{
		Dst:  img,
		Src:  &image.Uniform{C: ogTitleColor},
		Face: titleFace,
	}
	for i, line := range titleLines {
		titleDrawer.Dot = fixed.P(ogMargin, titleY+i*ogTitleLineH)
		titleDrawer.DrawString(line)
	}

	// The bottom band sits at the same baseline whichever mode: hero = chips +
	// domain; entity = a hairline rule + accent mark + wordmark. clearanceY is the
	// top of that band — the subtitle is dropped rather than colliding into it.
	var clearanceY int
	if hero {
		clearanceY = drawHeroBand(img, footerFace, accent, o.chips, o.accentLabel)
	} else {
		clearanceY = drawEntityFooter(img, footerFace, accent, brandName(o.brand))
	}

	subtitleY := titleY + (len(titleLines)-1)*ogTitleLineH + 24 + ogSubtitleSize
	if o.sheetMock {
		// A sheet: draw a generic spreadsheet mock in the space between the title
		// and the footer band — reads as a spreadsheet without exposing any cells.
		gridTop := titleY + (len(titleLines)-1)*ogTitleLineH + 30
		if gridTop < clearanceY-90 {
			drawSheetMock(img, gridTop, clearanceY-16)
		}
	} else if o.subtitle != "" && subtitleY <= clearanceY-12 {
		sub := truncateToWidth(subtitleFace, o.subtitle, ogDrawableWidth)
		subtitleDrawer := &font.Drawer{
			Dst:  img,
			Src:  &image.Uniform{C: ogSubtitleColor},
			Face: subtitleFace,
			Dot:  fixed.P(ogMargin, subtitleY),
		}
		subtitleDrawer.DrawString(sub)
	}

	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// renderSheetOGCard is the share card for a sheet: the branded card with a "sheet"
// type label and a generic spreadsheet mock (drawn by drawSheetMock) in place of a
// subtitle. It carries NO cell content — the card must not leak the page's data.
func renderSheetOGCard(title string, brand ogBrand) ([]byte, error) {
	return renderOGCardOpts(ogCardOpts{kicker: "sheet", title: title, sheetMock: true, brand: brand, maxTitleLines: 2})
}

// drawSheetMock paints a generic spreadsheet panel on the dark OG card between top
// and bottom: a column-letter header (A, B, C…), a row-number gutter (1, 2, 3…),
// grid hairlines, and neutral placeholder bars in the cells. It deliberately shows
// NO real cell text — the sheet's identity is conveyed by the spreadsheet form
// alone, so a public share card never exposes page contents.
func drawSheetMock(img *image.RGBA, top, bottom int) {
	x0, x1 := ogMargin, ogMargin+ogDrawableWidth
	const gutter = 56 // row-number column width
	const headerH = 44
	const cols = 5
	cellW := (x1 - x0 - gutter) / cols

	// Fit as many data rows as the space allows (3–6), ~46px each.
	rowH := 46
	avail := bottom - top - headerH
	rows := avail / rowH
	if rows > 6 {
		rows = 6
	}
	if rows < 3 {
		rows = 3
		if avail > rows {
			rowH = avail / rows
		}
	}
	if rowH < 28 {
		rowH = 28
	}
	y0 := top
	yHeader := y0 + headerH
	y1 := yHeader + rows*rowH

	panelBG := color.RGBA{R: 0xfa, G: 0xfa, B: 0xfc, A: 0xff}
	headerBG := color.RGBA{R: 0xe7, G: 0xea, B: 0xf6, A: 0xff}
	gutterBG := color.RGBA{R: 0xef, G: 0xf1, B: 0xf9, A: 0xff}
	lineC := color.RGBA{R: 0xd8, G: 0xdb, B: 0xe4, A: 0xff}
	headText := color.RGBA{R: 0x5b, G: 0x61, B: 0x82, A: 0xff}
	barC := color.RGBA{R: 0xdd, G: 0xe0, B: 0xea, A: 0xff}

	fillRoundRect(img, image.Rect(x0, y0, x1, y1), 14, panelBG)
	draw.Draw(img, image.Rect(x0+1, y0+1, x1-1, yHeader), &image.Uniform{C: headerBG}, image.Point{}, draw.Src)
	draw.Draw(img, image.Rect(x0+1, yHeader, x0+gutter, y1-1), &image.Uniform{C: gutterBG}, image.Point{}, draw.Src)

	// Vertical hairlines (gutter edge + each column), horizontal hairlines (rows).
	draw.Draw(img, image.Rect(x0+gutter, y0, x0+gutter+1, y1), &image.Uniform{C: lineC}, image.Point{}, draw.Src)
	for c := 1; c < cols; c++ {
		x := x0 + gutter + c*cellW
		draw.Draw(img, image.Rect(x, y0, x+1, y1), &image.Uniform{C: lineC}, image.Point{}, draw.Src)
	}
	for r := 0; r <= rows; r++ {
		y := yHeader + r*rowH
		draw.Draw(img, image.Rect(x0, y, x1, y+1), &image.Uniform{C: lineC}, image.Point{}, draw.Src)
	}

	hf, err := opentype.NewFace(ogBoldFont, &opentype.FaceOptions{Size: 22, DPI: 72, Hinting: font.HintingFull})
	if err != nil {
		return
	}
	defer hf.Close()
	hm := hf.Metrics()

	// Column letters, centered in each header cell.
	for c := 0; c < cols; c++ {
		letter := string(rune('A' + c))
		lw := font.MeasureString(hf, letter)
		cx := fixed.I(x0+gutter+c*cellW) + (fixed.I(cellW)-lw)/2
		cy := fixed.I(y0) + (fixed.I(headerH)+hm.Ascent-hm.Descent)/2
		d := &font.Drawer{Dst: img, Src: &image.Uniform{C: headText}, Face: hf, Dot: fixed.Point26_6{X: cx, Y: cy}}
		d.DrawString(letter)
	}
	// Row numbers, centered in the gutter.
	for r := 0; r < rows; r++ {
		num := strconv.Itoa(r + 1)
		nw := font.MeasureString(hf, num)
		nx := fixed.I(x0) + (fixed.I(gutter)-nw)/2
		ny := fixed.I(yHeader+r*rowH) + (fixed.I(rowH)+hm.Ascent-hm.Descent)/2
		d := &font.Drawer{Dst: img, Src: &image.Uniform{C: headText}, Face: hf, Dot: fixed.Point26_6{X: nx, Y: ny}}
		d.DrawString(num)
	}

	// Neutral placeholder bars — a deterministic width/skip pattern per (row,col)
	// so cells look populated without carrying any real value. Column A reads
	// "label-like" (wider bars); some cells are left blank for an organic look.
	const pad, barH = 16, 10
	for r := 0; r < rows; r++ {
		for c := 0; c < cols; c++ {
			if (r+2*c)%3 == 2 {
				continue
			}
			maxBar := cellW - 2*pad
			w := maxBar * (5 + (r*7+c*3)%5) / 10
			if c == 0 {
				w = maxBar * (7 + r%3) / 10
			}
			if w < 12 {
				continue
			}
			bx := x0 + gutter + c*cellW + pad
			by := yHeader + r*rowH + (rowH-barH)/2
			fillRoundRect(img, image.Rect(bx, by, bx+w, by+barH), barH/2, barC)
		}
	}
}

// brandName is the org name when branded, else "tela".
func brandName(b ogBrand) string {
	if b.name != "" {
		return b.name
	}
	return "tela"
}

// drawBrandLockup paints a mark+wordmark lockup at the top-left for hero cards
// with no org logo: an accent rounded square holding the name's initial, then the
// wordmark. Returns the lockup height so the caller can place the title below it.
func drawBrandLockup(img *image.RGBA, accent color.RGBA, name string) (int, error) {
	fillRoundRect(img, image.Rect(ogMargin, ogMargin, ogMargin+ogMarkSize, ogMargin+ogMarkSize), ogMarkRadius, accent)

	glyphFace, err := opentype.NewFace(ogBoldFont, &opentype.FaceOptions{Size: 34, DPI: 72, Hinting: font.HintingFull})
	if err != nil {
		return 0, fmt.Errorf("og: mark face: %w", err)
	}
	defer glyphFace.Close()
	initial := strings.ToUpper(string([]rune(name)[0]))
	gm := glyphFace.Metrics()
	gw := font.MeasureString(glyphFace, initial)
	gx := fixed.I(ogMargin) + (fixed.I(ogMarkSize)-gw)/2
	gy := fixed.I(ogMargin) + (fixed.I(ogMarkSize)-(gm.Ascent+gm.Descent))/2 + gm.Ascent
	gd := &font.Drawer{Dst: img, Src: &image.Uniform{C: ogWhite}, Face: glyphFace, Dot: fixed.Point26_6{X: gx, Y: gy}}
	gd.DrawString(initial)

	wmFace, err := opentype.NewFace(ogBoldFont, &opentype.FaceOptions{Size: ogWordmarkSize, DPI: 72, Hinting: font.HintingFull})
	if err != nil {
		return 0, fmt.Errorf("og: wordmark face: %w", err)
	}
	defer wmFace.Close()
	wm := truncateToWidth(wmFace, name, ogDrawableWidth-ogMarkSize-22)
	wmMetrics := wmFace.Metrics()
	wy := fixed.I(ogMargin) + (fixed.I(ogMarkSize)-(wmMetrics.Ascent+wmMetrics.Descent))/2 + wmMetrics.Ascent
	wd := &font.Drawer{Dst: img, Src: &image.Uniform{C: ogTitleColor}, Face: wmFace, Dot: fixed.Point26_6{X: fixed.I(ogMargin + ogMarkSize + 22), Y: wy}}
	wd.DrawString(wm)
	return ogMarkSize, nil
}

// drawHeroBand paints the bottom band of a hero card: pill chips from the left
// and an accent domain label on the right, vertically centered on the chip row.
// Returns the band's top Y (the subtitle clearance).
func drawHeroBand(img *image.RGBA, footerFace font.Face, accent color.RGBA, chips []string, accentLabel string) int {
	bandTop := ogCanvasHeight - ogMargin - ogChipH
	centerY := bandTop + ogChipH/2

	chipFace, err := opentype.NewFace(ogRegularFont, &opentype.FaceOptions{Size: ogChipTextSize, DPI: 72, Hinting: font.HintingFull})
	if err == nil {
		defer chipFace.Close()
		cm := chipFace.Metrics()
		baseline := fixed.I(centerY) + (cm.Ascent-cm.Descent)/2
		// Reserve room on the right for the domain so chips never overlap it.
		rightLimit := ogCanvasWidth - ogMargin
		if accentLabel != "" {
			rightLimit -= font.MeasureString(footerFace, accentLabel).Round() + 28
		}
		x := ogMargin
		for _, label := range chips {
			label = strings.TrimSpace(label)
			if label == "" {
				continue
			}
			w := font.MeasureString(chipFace, label).Round() + 2*ogChipPadX
			if x+w > rightLimit {
				break
			}
			rect := image.Rect(x, bandTop, x+w, bandTop+ogChipH)
			fillRoundRect(img, rect, ogChipRadius, ogRuleColor)
			fillRoundRect(img, rect.Inset(1), ogChipRadius-1, ogChipBgColor)
			cd := &font.Drawer{Dst: img, Src: &image.Uniform{C: ogSubtitleColor}, Face: chipFace, Dot: fixed.Point26_6{X: fixed.I(x + ogChipPadX), Y: baseline}}
			cd.DrawString(label)
			x += w + ogChipGap
		}
	}

	if accentLabel != "" {
		fm := footerFace.Metrics()
		baseline := fixed.I(centerY) + (fm.Ascent-fm.Descent)/2
		lw := font.MeasureString(footerFace, accentLabel)
		ld := &font.Drawer{Dst: img, Src: &image.Uniform{C: accent}, Face: footerFace, Dot: fixed.Point26_6{X: fixed.I(ogCanvasWidth-ogMargin) - lw, Y: baseline}}
		ld.DrawString(accentLabel)
	}
	return bandTop
}

// drawEntityFooter paints the plain footer for page/space cards: a hairline rule,
// a small accent mark, and the wordmark. Returns the rule Y (the subtitle
// clearance) so a long title's subtitle is dropped instead of colliding.
func drawEntityFooter(img *image.RGBA, footerFace font.Face, accent color.RGBA, name string) int {
	ruleY := ogCanvasHeight - ogMargin - ogFooterSize - 20
	footerY := ogCanvasHeight - ogMargin
	draw.Draw(img, image.Rect(ogMargin, ruleY, ogCanvasWidth-ogMargin, ruleY+2),
		&image.Uniform{C: ogRuleColor}, image.Point{}, draw.Src)

	const mark = 22
	draw.Draw(img, image.Rect(ogMargin, footerY-mark, ogMargin+mark, footerY),
		&image.Uniform{C: accent}, image.Point{}, draw.Src)

	footerX := ogMargin + mark + 18
	footer := truncateToWidth(footerFace, name, ogDrawableWidth-mark-18)
	footerDrawer := &font.Drawer{
		Dst:  img,
		Src:  &image.Uniform{C: ogFooterColor},
		Face: footerFace,
		Dot:  fixed.P(footerX, footerY),
	}
	footerDrawer.DrawString(footer)
	return ruleY
}

// fillRoundRect fills a rounded rectangle with col (opaque). Uses the clamped-
// center test: a pixel is inside when its distance to the inner rect (inset by
// rad) is ≤ rad — straight zones clamp to 0 distance, corners round off.
func fillRoundRect(img *image.RGBA, rect image.Rectangle, rad int, col color.RGBA) {
	if rad < 0 {
		rad = 0
	}
	if rad*2 > rect.Dx() {
		rad = rect.Dx() / 2
	}
	if rad*2 > rect.Dy() {
		rad = rect.Dy() / 2
	}
	r2 := rad * rad
	for y := rect.Min.Y; y < rect.Max.Y; y++ {
		for x := rect.Min.X; x < rect.Max.X; x++ {
			nx := x
			if nx < rect.Min.X+rad {
				nx = rect.Min.X + rad
			} else if nx > rect.Max.X-1-rad {
				nx = rect.Max.X - 1 - rad
			}
			ny := y
			if ny < rect.Min.Y+rad {
				ny = rect.Min.Y + rad
			} else if ny > rect.Max.Y-1-rad {
				ny = rect.Max.Y - 1 - rad
			}
			dx, dy := x-nx, y-ny
			if dx*dx+dy*dy <= r2 {
				img.SetRGBA(x, y, col)
			}
		}
	}
}

// paintOGBackground draws tela's signature atmosphere: a near-black ink gradient,
// the faint woven grid (tela = loom), and one diagonal light sweep tinted by the
// accent. On a branded card the ink is tinted faintly toward the org accent. This
// is what turns the card from a flat void into something deliberate.
func paintOGBackground(img *image.RGBA, accent color.RGBA, branded bool) {
	top, bot := ogBgTop, ogBgColor
	if branded {
		top = mixRGBA(accent, ogBgTop, ogAccentTintWeight)
		bot = mixRGBA(accent, ogBgColor, ogAccentTintWeight)
	}

	// Vertical ink gradient (lighter at the top).
	for y := 0; y < ogCanvasHeight; y++ {
		t := float64(y) / float64(ogCanvasHeight-1)
		c := mixRGBA(bot, top, t) // t=0 → top, t=1 → bot
		draw.Draw(img, image.Rect(0, y, ogCanvasWidth, y+1), &image.Uniform{C: c}, image.Point{}, draw.Src)
	}

	// Woven grid — warp (vertical) + weft (horizontal) threads, faint and uniform.
	for x := ogWeaveCell; x < ogCanvasWidth; x += ogWeaveCell {
		draw.Draw(img, image.Rect(x, 0, x+1, ogCanvasHeight), &image.Uniform{C: ogWeaveColor}, image.Point{}, draw.Src)
	}
	for y := ogWeaveCell; y < ogCanvasHeight; y += ogWeaveCell {
		draw.Draw(img, image.Rect(0, y, ogCanvasWidth, y+1), &image.Uniform{C: ogWeaveColor}, image.Point{}, draw.Src)
	}

	// One diagonal light sweep — a single luminance gesture, near-white with a
	// faint accent tint, lighting the upper-left through the title (and catching
	// the threads it crosses). Subtle peak so it reads as atmosphere, not a blob.
	light := mixRGBA(color.RGBA{R: 0xff, G: 0xff, B: 0xff, A: 0xff}, accent, 0.62)
	const center, sigma, peak = 360.0, 300.0, 0.13
	for y := 0; y < ogCanvasHeight; y++ {
		for x := 0; x < ogCanvasWidth; x++ {
			d := (float64(x)*0.8 + float64(y)*0.55) - center
			inten := peak * math.Exp(-(d*d)/(2*sigma*sigma))
			if inten < 0.003 {
				continue
			}
			i := img.PixOffset(x, y)
			img.Pix[i+0] = mixU8(img.Pix[i+0], light.R, inten)
			img.Pix[i+1] = mixU8(img.Pix[i+1], light.G, inten)
			img.Pix[i+2] = mixU8(img.Pix[i+2], light.B, inten)
		}
	}
}

// mixRGBA returns base shifted t of the way toward over (t in [0,1]), opaque.
func mixRGBA(over, base color.RGBA, t float64) color.RGBA {
	return color.RGBA{
		R: mixU8(base.R, over.R, t),
		G: mixU8(base.G, over.G, t),
		B: mixU8(base.B, over.B, t),
		A: 0xff,
	}
}

func mixU8(base, over uint8, t float64) uint8 {
	v := float64(base)*(1-t) + float64(over)*t
	if v > 255 {
		return 255
	}
	if v < 0 {
		return 0
	}
	return uint8(v + 0.5)
}

// drawLogoFit composites a logo into dst at (x,y), scaled to fit within
// maxW×maxH while preserving aspect ratio (never upscaled past the box). Returns
// the drawn height so the caller can place the title below it. Drawn with Over
// so a transparent logo blends onto the tinted background.
func drawLogoFit(dst *image.RGBA, logo image.Image, x, y, maxW, maxH int) int {
	b := logo.Bounds()
	lw, lh := b.Dx(), b.Dy()
	if lw <= 0 || lh <= 0 {
		return 0
	}
	dw, dh := lw, lh
	// Scale down to fit the height, then clamp the width.
	if dh > maxH {
		dw = dw * maxH / dh
		dh = maxH
	}
	if dw > maxW {
		dh = dh * maxW / dw
		dw = maxW
	}
	rect := image.Rect(x, y, x+dw, y+dh)
	xdraw.CatmullRom.Scale(dst, rect, logo, b, xdraw.Over, nil)
	return dh
}

// wrapLines greedily wraps text into at most maxLines lines that fit within
// maxWidth pixels when drawn with face. The final line is suffixed with "…" if
// remaining text would overflow. Whitespace-separated; existing newlines inside
// the input are flattened to single spaces by the upstream caller (page title is
// a single TEXT column with no newline convention).
//
// A word too long to fit is broken at a punctuation boundary (-, _, ., /) rather
// than truncated on the spot — that is what a filename card ("attachment-sharing
// -spec.pdf") or a URL needs, and those are exactly the titles with no spaces to
// wrap at. Only when a word has no such boundary does it fall back to truncation.
func wrapLines(face font.Face, text string, maxWidth, maxLines int) []string {
	text = strings.TrimSpace(text)
	if text == "" {
		return []string{""}
	}
	if maxLines <= 0 {
		return nil
	}

	toks := wrapTokens(face, strings.Fields(text), maxWidth)
	maxFixed := fixed.I(maxWidth)

	lines := make([]string, 0, maxLines)
	cur := ""
	i := 0
	for i < len(toks) && len(lines) < maxLines {
		candidate := cur
		if candidate == "" {
			candidate = toks[i].text
		} else {
			candidate = cur + toks[i].sep() + toks[i].text
		}
		if font.MeasureString(face, candidate) <= maxFixed {
			cur = candidate
			i++
			continue
		}
		// Adding this token overflowed.
		if cur == "" {
			// One token longer than the line even after boundary splitting;
			// force it onto its own line, truncated. Avoid getting stuck.
			lines = append(lines, truncateToWidth(face, toks[i].text, maxWidth))
			i++
			continue
		}
		lines = append(lines, cur)
		cur = ""
	}
	if cur != "" && len(lines) < maxLines {
		lines = append(lines, cur)
		cur = ""
	}

	// Tokens left over: the final line must collapse them with an ellipsis.
	if i < len(toks) {
		if len(lines) == 0 {
			// maxLines was 0 or the very first token didn't fit even truncated.
			return []string{truncateToWidth(face, joinTokens(toks[i:]), maxWidth)}
		}
		last := lines[len(lines)-1]
		remainder := last + toks[i].sep() + joinTokens(toks[i:])
		lines[len(lines)-1] = truncateToWidth(face, remainder, maxWidth)
	}

	return lines
}

// wrapTok is one unit the wrapper may put on a line. `glued` tokens rejoin the
// previous one with no space — the pieces a long word was broken into.
type wrapTok struct {
	text  string
	glued bool
}

func (t wrapTok) sep() string {
	if t.glued {
		return ""
	}
	return " "
}

func joinTokens(toks []wrapTok) string {
	var b strings.Builder
	for i, t := range toks {
		if i > 0 {
			b.WriteString(t.sep())
		}
		b.WriteString(t.text)
	}
	return b.String()
}

// wrapBreakChars are where an over-long word may be broken. The break char stays
// on the left piece, so "spec.pdf" reads as "spec." / "pdf" and never loses a
// character across the line break.
const wrapBreakChars = "-_./"

// wrapTokens expands words that cannot fit on a line into glued pieces split at
// wrapBreakChars. Words that fit (the overwhelming majority) pass through as one
// token, so ordinary prose wraps exactly as before.
func wrapTokens(face font.Face, words []string, maxWidth int) []wrapTok {
	maxFixed := fixed.I(maxWidth)
	out := make([]wrapTok, 0, len(words))
	for _, w := range words {
		if font.MeasureString(face, w) <= maxFixed {
			out = append(out, wrapTok{text: w})
			continue
		}
		start, first := 0, true
		for i, r := range w {
			if !strings.ContainsRune(wrapBreakChars, r) {
				continue
			}
			out = append(out, wrapTok{text: w[start : i+1], glued: !first})
			start, first = i+1, false
		}
		if start < len(w) {
			out = append(out, wrapTok{text: w[start:], glued: !first})
		}
	}
	return out
}

// truncateToWidth returns s if it fits within maxWidth, else the longest
// rune-prefix that fits with a trailing "…" appended.
func truncateToWidth(face font.Face, s string, maxWidth int) string {
	maxFixed := fixed.I(maxWidth)
	if font.MeasureString(face, s) <= maxFixed {
		return s
	}
	runes := []rune(s)
	ellipsis := "…"
	for n := len(runes) - 1; n >= 0; n-- {
		candidate := strings.TrimRight(string(runes[:n]), " ") + ellipsis
		if font.MeasureString(face, candidate) <= maxFixed {
			return candidate
		}
	}
	return ellipsis
}
