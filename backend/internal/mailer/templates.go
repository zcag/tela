package mailer

import (
	"bytes"
	"cmp"
	"fmt"
	"html/template"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Branded transactional templates. Deliberately a LIGHT theme: tela's app is
// dark ("Loom in the dark"), but email clients (Gmail/Outlook) force-INVERT a
// dark-background email in their dark mode, mangling it into a washed-out mess.
// A light email renders consistently everywhere — which is also what Notion /
// Confluence / most SaaS do. Indigo accents carry the brand. Email clients don't
// support OKLCH or CSS vars, so these are inline hex.
//
// This is the one place hex is correct — the frontend token gate does not
// (and cannot) cover server-rendered email.
const (
	clrVoid    = "#f4f5f7" // page canvas behind the card
	clrCard    = "#ffffff" // the card
	clrRule    = "#e6e8ec" // hairline borders
	clrText    = "#15161c" // headings / strong text
	clrMuted   = "#5b6270" // body copy
	clrFaint   = "#8a90a0" // footer / meta
	clrIndigo  = "#4f46e5" // accent fill / links
	clrIndigo2 = "#8b5cf6" // accent-bar gradient end
	clrLink    = "#4f46e5" // links (indigo on white)
	clrQuote   = "#3b4252" // quoted snippet text
	clrPanel   = "#f7f8fa" // footer / header panel, a hair off the card
	clrPill    = "#eef0f4" // event-badge pill fill
)

// emailTagline rides the footer of every email — a one-line product signature.
const emailTagline = "tela — your team's markdown-native, AI-ready knowledge base."

// monogramColors is a small fixed accent palette for actor monogram chips —
// deterministic per name (no avatar storage; renders without an image fetch, so
// it survives image-blocking clients). Hand-picked to read on white text.
var monogramColors = []string{
	"#4f46e5", "#0891b2", "#7c3aed", "#c026d3",
	"#db2777", "#e11d48", "#ca8a04", "#15803d",
}

// NotifLink is one suggested page in the "Related in this wiki" block.
type NotifLink struct {
	Label string
	URL   string
}

// DiffLine is one line of a page_updated "what changed" preview — an addition
// (green +) or a deletion (red −).
type DiffLine struct {
	Add  bool
	Text string
}

// NotifEmail is the content of a notification email. The heading reads as a
// sentence — actor (bold) + action + target (bold), e.g. "Ada mentioned you in
// Roadmap" — so who-did-what-where is obvious at a glance. Snippet and Related
// are optional per event type.
type NotifEmail struct {
	To        string
	Subject   string
	Eyebrow   string     // small uppercase label, e.g. "Mention"
	Actor     string     // display name; drives the monogram chip + bold lead
	Action    string     // "mentioned you in"
	Target    string     // "Page Title" (bold); may be empty
	Context   string     // workspace breadcrumb, e.g. the space name; may be empty
	Snippet   string     // optional quoted excerpt (mention text / reply body)
	Diff      []DiffLine // optional "what changed" preview (page_updated)
	DiffStat  string     // e.g. "12 additions · 3 deletions"
	DiffMore  string     // e.g. "9 more changed lines"
	CTALabel  string
	CTAURL    string
	Related   []NotifLink // optional suggestions
	Footer    string      // why-you-got-this line
	ManageURL string      // link to /settings?tab=notifications
	Brand     Brand       // per-org white-label (zero → tela)
}

// Brand is the per-org email branding resolved at send time, applied only where
// the org has claimed a custom domain (mirrors the app chrome / OG / share
// reader white-label). The zero value renders the default tela brand, so any
// caller can pass mailer.Brand{} to opt out. Accent must be an email-safe CSS
// color (hex or rgb()) — oklch is reduced to hex upstream. LogoURL is absolute.
type Brand struct {
	Name    string // org display name; "" → "tela"
	LogoURL string // absolute org logo URL; "" → the tela icon
	Accent  string // email-safe accent; "" → tela indigo
}

// emailView is the rendering model for the shared layout. Either Heading (a
// plain headline, used by verify/reset/feedback) OR Actor/Action/Target (the
// notification sentence) is set, never both.
type emailView struct {
	LogoOrigin   string
	BrandName    string // wordmark + footer credit; "" → "tela"
	BrandLogoURL string // absolute org logo URL; "" → the tela icon
	Accent       string // email-safe accent override; "" → tela indigo
	Powered      bool   // branded → footer shows "Powered by tela" instead of the product tagline
	Eyebrow      string
	Heading      string
	Actor        string
	Action       string
	Target       string
	Context      string
	Intro        string
	Snippet      string
	Diff         []DiffLine
	DiffStat     string
	DiffMore     string
	Mono         string // monogram initial; "" hides the chip
	MonoColor    string
	CTALabel     string
	CTAURL       string
	Related      []NotifLink
	ShowPaste    bool // show the copy-paste URL fallback (verify/reset want it)
	Footer       string
	ManageURL    string
	Tagline      string // product signature in the footer; set by renderHTML
}

// Color helpers exposed to the template.
func (v emailView) C(name string) string {
	switch name {
	case "void":
		return clrVoid
	case "card":
		return clrCard
	case "rule":
		return clrRule
	case "text":
		return clrText
	case "muted":
		return clrMuted
	case "faint":
		return clrFaint
	case "indigo":
		if v.Accent != "" {
			return v.Accent
		}
		return clrIndigo
	case "link":
		if v.Accent != "" {
			return v.Accent
		}
		return clrLink
	case "quote":
		return clrQuote
	case "indigo2":
		if v.Accent != "" {
			return v.Accent
		}
		return clrIndigo2
	case "panel":
		return clrPanel
	case "pill":
		return clrPill
	}
	return clrText
}

// applyBrand white-labels the view to an org's brand. A blank/"tela" name is a
// no-op (the default tela brand renders), so callers pass a zero Brand to opt
// out. Accent and logo only take effect alongside a real org name.
func (v *emailView) applyBrand(b Brand) {
	n := strings.TrimSpace(b.Name)
	if n == "" || n == "tela" {
		return
	}
	v.BrandName = n
	v.BrandLogoURL = b.LogoURL
	v.Accent = b.Accent
	v.Powered = true
}

// finalize fills the brand/footer defaults shared by the HTML and text renders.
func (v *emailView) finalize() {
	if v.BrandName == "" {
		v.BrandName = "tela"
	}
	if v.Tagline == "" {
		if v.Powered {
			v.Tagline = "Powered by tela."
		} else {
			v.Tagline = emailTagline
		}
	}
}

// emailTmpl is parsed once. html/template gives contextual escaping for every
// interpolated field (actor/title/snippet are user-controlled), so there's no
// hand-rolled EscapeString to forget.
var emailTmpl = template.Must(template.New("email").Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><meta name="color-scheme" content="light"><meta name="supported-color-schemes" content="light"></head>
<body style="margin:0;padding:0;background:{{.C "void"}};">
<table role="presentation" width="100%" cellpadding="0" cellspacing="0" style="background:{{.C "void"}};padding:40px 16px;">
<tr><td align="center">
  <table role="presentation" width="100%" cellpadding="0" cellspacing="0" style="max-width:600px;background:{{.C "card"}};border:1px solid {{.C "rule"}};border-radius:16px;overflow:hidden;font-family:-apple-system,BlinkMacSystemFont,'Segoe UI',Roboto,Helvetica,Arial,sans-serif;">
    <!-- accent bar -->
    <tr><td style="height:4px;line-height:4px;font-size:0;background:{{.C "indigo"}};background:linear-gradient(90deg,{{.C "indigo"}},{{.C "indigo2"}});">&nbsp;</td></tr>
    <!-- header -->
    <tr><td style="padding:22px 36px;border-bottom:1px solid {{.C "rule"}};">
      <table role="presentation" width="100%" cellpadding="0" cellspacing="0"><tr>
        <td style="vertical-align:middle;">
          {{if .BrandLogoURL}}<img src="{{.BrandLogoURL}}" height="26" alt="" style="vertical-align:middle;max-height:26px;width:auto;display:inline-block;"><span style="font-size:18px;font-weight:700;letter-spacing:-0.01em;color:{{.C "text"}};vertical-align:middle;margin-left:9px;">{{.BrandName}}</span>{{else if .LogoOrigin}}<img src="{{.LogoOrigin}}/icon-64.png" width="26" height="26" alt="" style="vertical-align:middle;border-radius:7px;display:inline-block;"><span style="font-size:18px;font-weight:700;letter-spacing:-0.01em;color:{{.C "text"}};vertical-align:middle;margin-left:9px;">{{.BrandName}}</span>{{else}}<span style="font-size:18px;font-weight:700;letter-spacing:-0.01em;color:{{.C "text"}};">{{.BrandName}}</span>{{end}}
        </td>
        {{if .Eyebrow}}<td align="right" style="vertical-align:middle;">
          <span style="display:inline-block;background:{{.C "pill"}};border:1px solid {{.C "rule"}};border-radius:999px;padding:5px 12px;font-size:11px;font-weight:700;letter-spacing:0.06em;text-transform:uppercase;color:{{.C "muted"}};">{{.Eyebrow}}</span>
        </td>{{end}}
      </tr></table>
    </td></tr>
    {{if .Actor}}
    <!-- notification body: who → what → which page -->
    <tr><td style="padding:30px 36px 0 36px;">
      {{if .Context}}<p style="margin:0 0 18px 0;font-size:12px;line-height:1.4;color:{{.C "faint"}};">In <span style="color:{{.C "muted"}};">{{.Context}}</span></p>{{end}}
      <table role="presentation" cellpadding="0" cellspacing="0"><tr>
        {{if .Mono}}<td style="vertical-align:middle;width:44px;">
          <div style="width:44px;height:44px;border-radius:50%;background:{{.MonoColor}};color:#ffffff;font-size:17px;font-weight:700;line-height:44px;text-align:center;">{{.Mono}}</div>
        </td>{{end}}
        <td style="vertical-align:middle;padding-left:14px;">
          <div style="font-size:15px;line-height:1.35;font-weight:700;color:{{.C "text"}};">{{.Actor}}</div>
          <div style="font-size:13px;line-height:1.35;color:{{.C "muted"}};margin-top:2px;">{{.Action}}</div>
        </td>
      </tr></table>
    </td></tr>
    <tr><td style="padding:18px 36px 0 36px;">
      <a href="{{.CTAURL}}" style="display:block;font-size:24px;line-height:1.3;font-weight:680;color:{{.C "text"}};text-decoration:none;">{{.Target}}</a>
    </td></tr>
    {{else}}
    <!-- transactional body: heading + intro -->
    <tr><td style="padding:30px 36px 0 36px;">
      <h1 style="margin:0;font-size:23px;line-height:1.35;font-weight:650;color:{{.C "text"}};">{{.Heading}}</h1>
    </td></tr>
    {{if .Intro}}<tr><td style="padding:13px 36px 0 36px;">
      <p style="margin:0;font-size:15px;line-height:1.6;color:{{.C "muted"}};">{{.Intro}}</p>
    </td></tr>{{end}}
    {{end}}
    {{if .Snippet}}<tr><td style="padding:20px 36px 0 36px;">
      <table role="presentation" width="100%" cellpadding="0" cellspacing="0"><tr>
        <td style="border-left:3px solid {{.C "indigo"}};background:{{.C "void"}};border:1px solid {{.C "rule"}};border-left:3px solid {{.C "indigo"}};border-radius:0 10px 10px 0;padding:15px 18px;">
          <p style="margin:0;font-size:14px;line-height:1.65;color:{{.C "quote"}};">{{.Snippet}}</p>
        </td>
      </tr></table>
    </td></tr>{{end}}
    {{if .Diff}}<tr><td style="padding:20px 36px 0 36px;">
      <table role="presentation" width="100%" cellpadding="0" cellspacing="0" style="border:1px solid {{.C "rule"}};border-radius:10px;overflow:hidden;">
        <tr><td style="padding:10px 16px;background:{{.C "panel"}};border-bottom:1px solid {{.C "rule"}};font-size:11px;font-weight:700;letter-spacing:0.06em;text-transform:uppercase;color:{{.C "faint"}};">What changed{{if .DiffStat}} <span style="color:{{.C "muted"}};font-weight:600;letter-spacing:0;text-transform:none;">· {{.DiffStat}}</span>{{end}}</td></tr>
        {{range .Diff}}<tr><td style="padding:4px 16px;font-family:ui-monospace,SFMono-Regular,Menlo,Consolas,monospace;font-size:13px;line-height:1.5;white-space:pre-wrap;word-break:break-word;background:{{if .Add}}#e7f6ec{{else}}#fdecec{{end}};color:{{if .Add}}#15803d{{else}}#b42318{{end}};">{{if .Add}}+{{else}}−{{end}} {{.Text}}</td></tr>
        {{end}}
        {{if .DiffMore}}<tr><td style="padding:9px 16px;background:{{.C "panel"}};border-top:1px solid {{.C "rule"}};font-size:12px;color:{{.C "faint"}};">{{.DiffMore}}</td></tr>{{end}}
      </table>
    </td></tr>{{end}}
    <tr><td style="padding:26px 36px 2px 36px;">
      <a href="{{.CTAURL}}" style="display:inline-block;background:{{.C "indigo"}};color:#ffffff;text-decoration:none;font-size:15px;font-weight:600;padding:13px 26px;border-radius:9px;">{{.CTALabel}}</a>
    </td></tr>
    {{if .ShowPaste}}<tr><td style="padding:18px 36px 0 36px;">
      <p style="margin:0;font-size:13px;line-height:1.5;color:{{.C "faint"}};">Or paste this link into your browser:<br>
        <a href="{{.CTAURL}}" style="color:{{.C "link"}};word-break:break-all;">{{.CTAURL}}</a></p>
    </td></tr>{{end}}
    {{if .Related}}<tr><td style="padding:30px 36px 0 36px;">
      <p style="margin:0 0 10px 0;font-size:11px;font-weight:700;letter-spacing:0.07em;text-transform:uppercase;color:{{.C "faint"}};">Related in this wiki</p>
      <table role="presentation" width="100%" cellpadding="0" cellspacing="0" style="border:1px solid {{.C "rule"}};border-radius:10px;overflow:hidden;">
        {{range $i, $r := .Related}}<tr><td style="padding:13px 16px;{{if $i}}border-top:1px solid {{$.C "rule"}};{{end}}">
          <a href="{{$r.URL}}" style="font-size:14px;line-height:1.4;font-weight:500;color:{{$.C "link"}};text-decoration:none;">{{$r.Label}}</a>
        </td></tr>{{end}}
      </table>
    </td></tr>{{end}}
    <tr><td style="height:32px;line-height:32px;font-size:0;">&nbsp;</td></tr>
    <!-- footer panel -->
    <tr><td style="padding:24px 36px 28px 36px;background:{{.C "panel"}};border-top:1px solid {{.C "rule"}};">
      <p style="margin:0 0 12px 0;font-size:13px;line-height:1.5;font-weight:600;color:{{.C "muted"}};">{{.Tagline}}</p>
      <p style="margin:0 0 14px 0;font-size:13px;line-height:1.5;">
        {{if .LogoOrigin}}<a href="{{.LogoOrigin}}" style="color:{{.C "link"}};text-decoration:none;">Open {{.BrandName}}</a>{{end}}
        {{if and .LogoOrigin .ManageURL}}<span style="color:{{.C "rule"}};">&nbsp;·&nbsp;</span>{{end}}
        {{if .ManageURL}}<a href="{{.ManageURL}}" style="color:{{.C "link"}};text-decoration:none;">Notification settings</a>{{end}}
      </p>
      <p style="margin:0 0 10px 0;font-size:12px;line-height:1.5;color:{{.C "faint"}};">{{.Footer}}{{if .ManageURL}} <a href="{{.ManageURL}}" style="color:{{.C "link"}};">Manage notification emails</a>.{{end}}</p>
      <p style="margin:0;font-size:11px;line-height:1.5;color:{{.C "faint"}};">© 2026 {{.BrandName}}</p>
    </td></tr>
  </table>
</td></tr>
</table>
</body></html>`))

// renderHTML executes the shared template into a string.
func renderHTML(v emailView) string {
	v.finalize()
	var b bytes.Buffer
	if err := emailTmpl.Execute(&b, v); err != nil {
		// Template is static + fields are strings; an error here is a programming
		// bug, not runtime data. Degrade to plaintext rather than send nothing.
		return v.Intro
	}
	return b.String()
}

// renderText is the plaintext alternative — same content, no markup.
func renderText(v emailView) string {
	v.finalize()
	var b strings.Builder
	b.WriteString(v.BrandName + "\n\n")
	if v.Eyebrow != "" {
		b.WriteString(strings.ToUpper(v.Eyebrow) + "\n")
	}
	switch {
	case v.Heading != "":
		b.WriteString(v.Heading)
	default:
		if v.Context != "" {
			b.WriteString("In " + v.Context + "\n")
		}
		if v.Actor != "" {
			b.WriteString(v.Actor + " ")
		}
		b.WriteString(v.Action)
		if v.Target != "" {
			b.WriteString(" " + v.Target)
		}
	}
	b.WriteString("\n")
	if v.Intro != "" {
		b.WriteString("\n" + v.Intro + "\n")
	}
	if v.Snippet != "" {
		b.WriteString("\n  " + v.Snippet + "\n")
	}
	if len(v.Diff) > 0 {
		b.WriteString("\nWhat changed")
		if v.DiffStat != "" {
			b.WriteString(" (" + v.DiffStat + ")")
		}
		b.WriteString(":\n")
		for _, dl := range v.Diff {
			sign := "−"
			if dl.Add {
				sign = "+"
			}
			b.WriteString("  " + sign + " " + dl.Text + "\n")
		}
		if v.DiffMore != "" {
			b.WriteString("  " + v.DiffMore + "\n")
		}
	}
	b.WriteString("\n" + v.CTALabel + ":\n" + v.CTAURL + "\n")
	if len(v.Related) > 0 {
		b.WriteString("\nRelated in this wiki:\n")
		for _, r := range v.Related {
			b.WriteString("  - " + r.Label + " — " + r.URL + "\n")
		}
	}
	b.WriteString("\n" + v.Footer)
	if v.ManageURL != "" {
		b.WriteString(" Manage notification emails: " + v.ManageURL)
	}
	b.WriteString("\n\n—\n" + v.Tagline + "\n© 2026 " + v.BrandName + "\n")
	return b.String()
}

// monogram returns the uppercase leading rune of name + a deterministic accent
// color. Empty name → no chip.
func monogram(name string) (initial, color string) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", ""
	}
	r := []rune(name)
	initial = strings.ToUpper(string(r[0]))
	var sum int
	for _, c := range name {
		sum += int(c)
	}
	return initial, monogramColors[sum%len(monogramColors)]
}

// VerifyEmail builds the "confirm your email" message addressed to `to`.
// verifyURL is the full link carrying the raw token. brand white-labels the mail
// to the org when it arrived on a custom domain (zero Brand → tela).
func VerifyEmail(to, username, verifyURL string, brand Brand) Message {
	name := cmp.Or(strings.TrimSpace(brand.Name), "tela")
	intro := fmt.Sprintf("Welcome to %s, %s. Confirm this address to activate your account and start writing.", name, username)
	v := emailView{
		LogoOrigin: originOf(verifyURL),
		Heading:    "Confirm your email",
		Intro:      intro,
		CTALabel:   "Confirm email",
		CTAURL:     verifyURL,
		ShowPaste:  true,
		Footer:     "This link expires in 24 hours. If you didn't create a " + name + " account, you can ignore this email.",
	}
	v.applyBrand(brand)
	return Message{To: to, Subject: "Confirm your " + name + " account", HTML: renderHTML(v), Text: renderText(v)}
}

// ResetPassword builds the "reset your password" message addressed to `to`.
// resetURL carries the raw token. brand white-labels the mail to the org.
func ResetPassword(to, username, resetURL string, brand Brand) Message {
	name := cmp.Or(strings.TrimSpace(brand.Name), "tela")
	intro := fmt.Sprintf("We received a request to reset the password for your %s account, %s. Choose a new one below.", name, username)
	v := emailView{
		LogoOrigin: originOf(resetURL),
		Heading:    "Reset your password",
		Intro:      intro,
		CTALabel:   "Reset password",
		CTAURL:     resetURL,
		ShowPaste:  true,
		Footer:     "This link expires in 1 hour. If you didn't request this, your password is unchanged and you can ignore this email.",
	}
	v.applyBrand(brand)
	return Message{To: to, Subject: "Reset your " + name + " password", HTML: renderHTML(v), Text: renderText(v)}
}

// OrgInvite builds the "you've been invited to a team" message addressed to
// `to`. inviteURL carries the raw token to the accept page. inviter is the
// inviting person's label (may be empty). brand white-labels the mail to the org.
func OrgInvite(to, orgName, inviter, inviteURL string, brand Brand) Message {
	name := cmp.Or(strings.TrimSpace(brand.Name), "tela")
	by := strings.TrimSpace(inviter)
	intro := fmt.Sprintf("You've been invited to join the **%s** organization on %s", orgName, name)
	if by != "" {
		intro = fmt.Sprintf("%s invited you to join the **%s** organization on %s", by, orgName, name)
	}
	intro += ". Accept the invitation to collaborate with the team."
	v := emailView{
		LogoOrigin: originOf(inviteURL),
		Heading:    "Join " + orgName,
		Intro:      intro,
		CTALabel:   "Accept invitation",
		CTAURL:     inviteURL,
		ShowPaste:  true,
		Footer:     "This invitation expires in 14 days. If you weren't expecting it, you can ignore this email.",
	}
	v.applyBrand(brand)
	return Message{To: to, Subject: "You're invited to " + orgName + " on " + name, HTML: renderHTML(v), Text: renderText(v)}
}

// SpaceInvite builds the "someone shared a space with you" message for an
// address that has no account yet — the space twin of OrgInvite. inviteURL
// carries the raw token to the same /invite/{token} accept page; following it
// and signing up with this address grants the space automatically.
func SpaceInvite(to, spaceName, inviter, inviteURL string, brand Brand) Message {
	name := cmp.Or(strings.TrimSpace(brand.Name), "tela")
	by := strings.TrimSpace(inviter)
	intro := fmt.Sprintf("You've been invited to the **%s** space on %s", spaceName, name)
	if by != "" {
		intro = fmt.Sprintf("%s invited you to the **%s** space on %s", by, spaceName, name)
	}
	intro += ". Create your account to start reading and writing with the team — the space will be waiting for you."
	v := emailView{
		LogoOrigin: originOf(inviteURL),
		Eyebrow:    "Invitation",
		Heading:    "Join " + spaceName,
		Intro:      intro,
		CTALabel:   "Accept invitation",
		CTAURL:     inviteURL,
		ShowPaste:  true,
		Footer:     "This invitation expires in 14 days. If you weren't expecting it, you can ignore this email.",
	}
	v.applyBrand(brand)
	subject := "You're invited to " + spaceName + " on " + name
	if by != "" {
		subject = by + " invited you to " + spaceName + " on " + name
	}
	return Message{To: to, Subject: subject, HTML: renderHTML(v), Text: renderText(v)}
}

// SelfHostLicense builds the "here's your self-host Enterprise license key"
// delivery email. token is the signed key the buyer pastes into their own
// instance (Settings → License); seats/expires describe it; manageURL links back
// to their licenses in the cloud app so they can re-copy it later.
func SelfHostLicense(to, token string, seats int, expires time.Time, manageURL string) Message {
	intro := "Thanks for subscribing to tela Self-Host Enterprise. Your license key is below — in your self-hosted instance, open **Settings → License** and paste it in to unlock SSO, SCIM, audit, advanced roles and the premium connectors."
	valid := "**Valid until " + expires.UTC().Format("2 January 2006") + "**"
	if seats > 0 {
		valid += " · " + strconv.Itoa(seats) + " seats"
	}
	valid += ". It renews automatically each term. If your instance can reach the internet it **auto-updates its key on renewal** — no action needed. Air-gapped? Each renewal's new key is emailed to you and always in Settings → Self-host licenses; re-install it before this one expires."
	v := emailView{
		LogoOrigin: originOf(manageURL),
		Eyebrow:    "License",
		Heading:    "Your self-host Enterprise key",
		Intro:      intro + "\n\n" + valid,
		Snippet:    token,
		CTALabel:   "Manage your licenses",
		CTAURL:     manageURL,
		Footer:     "You're receiving this because you purchased a tela Self-Host Enterprise license.",
	}
	return Message{To: to, Subject: "Your tela Self-Host Enterprise license key", HTML: renderHTML(v), Text: renderText(v)}
}

// FeedbackNotice tells an instance admin that new feedback landed. `who` is the
// submitter label, `subject`/`body` the feedback content, kind/source/page are
// optional metadata (kind: idea|bug|other|""; source: web|api|mcp; page: page
// context label or ""), and inboxURL the deep link to the admin Feedback tab.
// Body is truncated to keep the email sane.
func FeedbackNotice(to, who, subject, body, kind, source, page, inboxURL string) Message {
	b := strings.TrimSpace(body)
	if len(b) > 600 {
		b = b[:600] + "…"
	}
	// meta condenses provenance into one parenthetical: "bug via web on “Page”".
	meta := source
	if kind != "" {
		meta = kind + " via " + source
	}
	if page != "" {
		meta += " on " + page
	}
	intro := fmt.Sprintf("%s submitted feedback", who)
	if meta != "" {
		intro += " (" + meta + ")"
	}
	intro += fmt.Sprintf(" — “%s”: %s", subject, b)
	v := emailView{
		LogoOrigin: originOf(inboxURL),
		Eyebrow:    "Feedback",
		Heading:    "New feedback",
		Intro:      intro,
		CTALabel:   "Open feedback inbox",
		CTAURL:     inboxURL,
		Footer:     "You're receiving this because you're a tela instance admin.",
	}
	return Message{To: to, Subject: "New tela feedback: " + subject, HTML: renderHTML(v), Text: renderText(v), Important: true}
}

// NotificationMessage builds a notification email from n. The actor anchors the
// email — a monogram chip + a bold lead in the heading sentence.
func NotificationMessage(n NotifEmail) Message {
	mono, color := monogram(n.Actor)
	v := emailView{
		LogoOrigin: originOf(n.CTAURL),
		Eyebrow:    n.Eyebrow,
		Actor:      n.Actor,
		Action:     n.Action,
		Target:     n.Target,
		Context:    n.Context,
		Snippet:    n.Snippet,
		Diff:       n.Diff,
		DiffStat:   n.DiffStat,
		DiffMore:   n.DiffMore,
		Mono:       mono,
		MonoColor:  color,
		CTALabel:   n.CTALabel,
		CTAURL:     n.CTAURL,
		Related:    n.Related,
		Footer:     n.Footer,
		ManageURL:  n.ManageURL,
	}
	v.applyBrand(n.Brand)
	return Message{To: n.To, Subject: n.Subject, HTML: renderHTML(v), Text: renderText(v)}
}

// originOf returns scheme://host for a full URL, or "" when it can't be parsed.
// Used to point the email logo at the deploying instance's own /icon-64.png.
func originOf(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return ""
	}
	return u.Scheme + "://" + u.Host
}
