package mailer

import (
	"bytes"
	"cmp"
	"html/template"
	"strings"
)

// Digest is tela's weekly recap. It answers one question — "is there anything I
// need to read or do?" — so it leads with what's personal + actionable (For
// you), then a curated slice of notable HUMAN activity (bulk Atlas generation is
// collapsed to one line, not counted as authoring), then quiet housekeeping.
// Data is assembled by internal/api/digest.go; this file only renders.

// DigestForYou is one personal item — a mention, a reply, or a followed page
// that changed. Text is the ready-made sentence; Snippet is optional context.
type DigestForYou struct {
	Text    string
	Snippet string
	URL     string
}

// DigestUpdate is one notable (human-authored) page in "this week".
type DigestUpdate struct {
	Title     string
	SpaceName string
	Actor     string
	When      string
	Summary   string
	URL       string
}

// DigestAttention is one "needs attention" item. Kind is the pill label; Tone is
// "info" or "warn". Title/URL is the primary page; Title2/URL2 is an optional
// second page (a conflict names BOTH pages that disagree), rendered as two links.
type DigestAttention struct {
	Kind    string
	Tone    string
	Title   string
	URL     string
	Title2  string
	URL2    string
	Context string // where it lives, e.g. "Macellan Wiki · services" (space · source)
	Detail  string
}

// DigestData is the full model for one recipient.
type DigestData struct {
	Greeting  string
	DateRange string
	Gist      string            // one-line summary of what MATTERS (not volume)
	ForYou    []DigestForYou    // personal + actionable — the lead
	Updates   []DigestUpdate    // notable human-authored changes
	MoreCount int               // "+N more" human updates
	AtlasLine string            // one-line rollup of bulk Atlas generation ("" hides)
	Attention []DigestAttention // housekeeping: open questions, then stale docs
	AppURL    string
	PrefsURL  string
	UnsubURL  string
	Brand     Brand
}

type digestView struct {
	D                                                   DigestData
	BrandName, LogoURL, Tagline                         string
	Powered                                             bool
	Indigo, Text, Muted, Faint, Panel, Rule, Card, Pill string
}

// Digest renders the weekly digest for one recipient.
func Digest(to, subject string, d DigestData) Message {
	v := digestView{
		D: d, Indigo: clrIndigo, Text: clrText, Muted: clrMuted, Faint: clrFaint,
		Panel: clrPanel, Rule: clrRule, Card: clrCard, Pill: clrPill,
	}
	if name := strings.TrimSpace(d.Brand.Name); name != "" && name != "tela" {
		v.BrandName, v.LogoURL, v.Powered = name, d.Brand.LogoURL, true
		if d.Brand.Accent != "" {
			v.Indigo = d.Brand.Accent
		}
	} else {
		v.BrandName = "tela"
		if d.AppURL != "" {
			v.LogoURL = strings.TrimRight(d.AppURL, "/") + "/icon-64.png"
		}
	}
	if v.Powered {
		v.Tagline = "Powered by tela."
	} else {
		v.Tagline = emailTagline
	}

	var buf bytes.Buffer
	_ = digestTmpl.Execute(&buf, v)
	return Message{To: to, Subject: subject, HTML: buf.String(), Text: renderDigestText(d)}
}

func renderDigestText(d DigestData) string {
	var b strings.Builder
	if d.Greeting != "" {
		b.WriteString("Hi " + d.Greeting + ",\n\n")
	}
	b.WriteString("Your weekly report on what's happening across your spaces — and anything that needs you.\n")
	b.WriteString("Week of " + d.DateRange + "\n\n")
	if d.Gist != "" {
		b.WriteString(d.Gist + "\n\n")
	}
	if len(d.ForYou) > 0 {
		b.WriteString("FOR YOU\n")
		for _, f := range d.ForYou {
			b.WriteString("- " + f.Text + "\n")
			if f.URL != "" {
				b.WriteString("  " + f.URL + "\n")
			}
		}
		b.WriteString("\n")
	}
	if len(d.Updates) > 0 {
		b.WriteString("NOTABLE THIS WEEK\n")
		for _, u := range d.Updates {
			b.WriteString("- " + u.Title + " (" + u.SpaceName + " · " + u.Actor + " · " + u.When + ")\n")
			if u.Summary != "" {
				b.WriteString("  " + u.Summary + "\n")
			}
		}
		if d.MoreCount > 0 {
			b.WriteString("...and " + itoa(d.MoreCount) + " more.\n")
		}
		if d.AtlasLine != "" {
			b.WriteString(d.AtlasLine + "\n")
		}
		b.WriteString("\n")
	}
	if len(d.Attention) > 0 {
		b.WriteString("NEEDS ATTENTION\n")
		for _, a := range d.Attention {
			b.WriteString("- [" + a.Kind + "] " + a.Title + "\n")
			if a.Detail != "" {
				b.WriteString("  " + a.Detail + "\n")
			}
		}
		b.WriteString("\n")
	}
	b.WriteString("Open " + cmp.Or(strings.TrimSpace(d.Brand.Name), "tela") + ": " + d.AppURL + "\n")
	return b.String()
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

var digestTmpl = template.Must(template.New("digest").Parse(`<!doctype html>
<html><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1.0"></head>
<body style="margin:0;padding:0;background:#f3f3f7;font-family:-apple-system,BlinkMacSystemFont,'Segoe UI',Roboto,Helvetica,Arial,sans-serif;-webkit-font-smoothing:antialiased;">
<table role="presentation" width="100%" cellpadding="0" cellspacing="0" style="background:#f3f3f7;padding:32px 12px;"><tr><td align="center">
<table role="presentation" width="600" cellpadding="0" cellspacing="0" style="width:600px;max-width:600px;background:{{.Card}};border-radius:14px;overflow:hidden;border:1px solid #e6e6ef;">

  <tr><td style="height:4px;background:{{.Indigo}};line-height:4px;font-size:0;">&nbsp;</td></tr>

  <tr><td style="padding:26px 32px 8px 32px;">
    <table role="presentation" width="100%" cellpadding="0" cellspacing="0"><tr>
      <td align="left" style="vertical-align:middle;">
        {{if .LogoURL}}<img src="{{.LogoURL}}" width="26" height="26" alt="" style="vertical-align:middle;border-radius:7px;display:inline-block;"><span style="font-size:17px;font-weight:600;color:{{.Text}};vertical-align:middle;margin-left:9px;letter-spacing:-.01em;">{{.BrandName}}</span>{{else}}<span style="font-size:17px;font-weight:600;color:{{.Text}};letter-spacing:-.01em;">{{.BrandName}}</span>{{end}}
      </td>
      <td align="right" style="vertical-align:middle;font-size:12px;color:{{.Faint}};">Your week · {{.D.DateRange}}</td>
    </tr></table>
  </td></tr>

  <tr><td style="padding:14px 32px 0 32px;">
    <h1 style="margin:0 0 5px 0;font-size:22px;line-height:1.25;font-weight:700;color:{{.Text}};letter-spacing:-.02em;">{{if .D.Greeting}}Hi {{.D.Greeting}},{{else}}Your week{{end}}</h1>
    <p style="margin:0 0 8px 0;font-size:13px;line-height:1.5;color:{{.Faint}};">Your weekly report on what's happening across your spaces — and anything that needs you.</p>
    {{if .D.Gist}}<p style="margin:0;font-size:15px;line-height:1.5;color:{{.Muted}};">{{.D.Gist}}</p>{{end}}
  </td></tr>

  {{if .D.ForYou}}
  <tr><td style="padding:24px 32px 0 32px;">
    <div style="font-size:13px;font-weight:700;color:{{.Indigo}};text-transform:uppercase;letter-spacing:.05em;padding-bottom:6px;">For you</div>
    <table role="presentation" width="100%" cellpadding="0" cellspacing="0" style="background:#f5f6ff;border:1px solid #e2e4ff;border-radius:10px;"><tr><td style="padding:6px 16px;">
      {{range $i, $f := .D.ForYou}}
      <div style="padding:10px 0;{{if $i}}border-top:1px solid #e6e8ff;{{end}}">
        <a href="{{$f.URL}}" style="font-size:14px;color:{{$.Text}};text-decoration:none;line-height:1.4;">{{$f.Text}}</a>
        {{if $f.Snippet}}<div style="font-size:12.5px;color:{{$.Muted}};margin-top:2px;line-height:1.4;">&ldquo;{{$f.Snippet}}&rdquo;</div>{{end}}
      </div>
      {{end}}
    </td></tr></table>
  </td></tr>
  {{end}}

  {{if .D.Updates}}
  <tr><td style="padding:26px 32px 0 32px;">
    <div style="font-size:13px;font-weight:600;color:{{.Text}};text-transform:uppercase;letter-spacing:.05em;border-bottom:1px solid #eee;padding-bottom:8px;">Notable this week</div>
    <table role="presentation" width="100%" cellpadding="0" cellspacing="0">
      {{range .D.Updates}}
      <tr><td style="padding:14px 0 12px 0;border-bottom:1px solid #f2f2f6;">
        <a href="{{.URL}}" style="font-size:15px;font-weight:600;color:{{$.Indigo}};text-decoration:none;">{{.Title}}</a>
        <div style="font-size:12.5px;color:{{$.Faint}};margin-top:3px;">{{.SpaceName}} · {{.Actor}} · {{.When}}</div>
        {{if .Summary}}<div style="font-size:13.5px;color:{{$.Muted}};margin-top:4px;line-height:1.5;">{{.Summary}}</div>{{end}}
      </td></tr>
      {{end}}
    </table>
    {{if gt .D.MoreCount 0}}<a href="{{.D.AppURL}}" style="display:inline-block;margin-top:12px;font-size:13px;color:{{.Indigo}};text-decoration:none;">+ {{.D.MoreCount}} more human edits &rarr;</a>{{end}}
    {{if .D.AtlasLine}}<div style="margin-top:12px;font-size:12.5px;color:{{.Faint}};background:{{.Panel}};border:1px solid #eee;border-radius:8px;padding:9px 12px;">&#10022; {{.D.AtlasLine}}</div>{{end}}
  </td></tr>
  {{end}}

  {{if .D.Attention}}
  <tr><td style="padding:26px 32px 0 32px;">
    <div style="font-size:13px;font-weight:600;color:{{.Text}};text-transform:uppercase;letter-spacing:.05em;padding-bottom:4px;">Needs attention</div>
    <table role="presentation" width="100%" cellpadding="0" cellspacing="0">
      {{range .D.Attention}}
      <tr><td style="padding:8px 0 0 0;">
        {{if eq .Tone "warn"}}
        <table role="presentation" width="100%" cellpadding="0" cellspacing="0" style="background:#fdf9f1;border:1px solid #f0e4cc;border-left:3px solid #d99a2b;border-radius:8px;"><tr><td style="padding:12px 14px;">
          <span style="display:inline-block;font-size:10px;font-weight:700;letter-spacing:.06em;color:#9a6a00;background:#f9ecd2;padding:3px 8px;border-radius:5px;">{{.Kind}}</span>
          <div style="margin-top:7px;font-size:14px;font-weight:600;line-height:1.4;"><a href="{{.URL}}" style="color:{{$.Indigo}};text-decoration:none;">{{.Title}}</a>{{if .Title2}} <span style="color:{{$.Faint}};font-weight:400;">&#8596;</span> <a href="{{.URL2}}" style="color:{{$.Indigo}};text-decoration:none;">{{.Title2}}</a>{{end}}</div>
          {{if .Context}}<div style="font-size:11.5px;color:{{$.Faint}};margin-top:2px;">{{.Context}}</div>{{end}}
          {{if .Detail}}<div style="font-size:12.5px;color:{{$.Muted}};margin-top:3px;line-height:1.45;">{{.Detail}}</div>{{end}}
        </td></tr></table>
        {{else}}
        <table role="presentation" width="100%" cellpadding="0" cellspacing="0" style="background:#f5f6ff;border:1px solid #e2e4ff;border-left:3px solid {{$.Indigo}};border-radius:8px;"><tr><td style="padding:12px 14px;">
          <span style="display:inline-block;font-size:10px;font-weight:700;letter-spacing:.06em;color:{{$.Indigo}};background:#e8eaff;padding:3px 8px;border-radius:5px;">{{.Kind}}</span>
          <div style="margin-top:7px;font-size:14px;font-weight:600;line-height:1.4;"><a href="{{.URL}}" style="color:{{$.Text}};text-decoration:none;">{{.Title}}</a>{{if .Title2}} <span style="color:{{$.Faint}};font-weight:400;">&#8596;</span> <a href="{{.URL2}}" style="color:{{$.Text}};text-decoration:none;">{{.Title2}}</a>{{end}}</div>
          {{if .Context}}<div style="font-size:11.5px;color:{{$.Faint}};margin-top:2px;">{{.Context}}</div>{{end}}
          {{if .Detail}}<div style="font-size:12.5px;color:{{$.Muted}};margin-top:3px;line-height:1.45;">{{.Detail}}</div>{{end}}
        </td></tr></table>
        {{end}}
      </td></tr>
      {{end}}
    </table>
  </td></tr>
  {{end}}

  <tr><td align="center" style="padding:30px 32px 6px 32px;">
    <a href="{{.D.AppURL}}" style="display:inline-block;background:{{.Indigo}};color:#ffffff;font-size:14px;font-weight:600;text-decoration:none;padding:12px 28px;border-radius:8px;">Open {{.BrandName}} &rarr;</a>
  </td></tr>

  <tr><td style="padding:22px 32px 30px 32px;border-top:1px solid #f0f0f4;">
    <p style="margin:0 0 6px 0;font-size:12px;color:{{.Faint}};line-height:1.5;">{{.Tagline}}</p>
    <p style="margin:0;font-size:11px;color:#b4b4c6;line-height:1.6;">You're getting this weekly.{{if .D.PrefsURL}} <a href="{{.D.PrefsURL}}" style="color:{{.Faint}};">Email settings</a>{{end}}{{if .D.UnsubURL}} · <a href="{{.D.UnsubURL}}" style="color:{{.Faint}};">Unsubscribe</a>{{end}}</p>
  </td></tr>

</table>
</td></tr></table>
</body></html>`))
