package mail

import (
	"html"
	"regexp"
	"strings"
)

// HTML turns the plain text of a message into the branded LogaLuxe email: the mark and name,
// the subject as a serif heading, the text as paragraphs, the first link as a button and any
// other link kept as a link. The plain text stays the authoritative copy: nothing is added to
// it or taken from it, so a mail client that shows only text sees the same message.
//
// The design follows the product: cream ground, ink text, wine for links, gold for the button,
// Bodoni Moda where the client allows it and Georgia where it does not. Everything is inline,
// because email clients drop stylesheets.
func HTML(subject, text string) string {
	const (
		ink   = "#1A1513"
		cream = "#FBF7F2"
		line  = "#E6DCD2"
		muted = "#6B5F57"
		wine  = "#7A1F2B"
		gold  = "#D4AF5A"
	)
	type block struct {
		kind string // p, list, link
		text string
	}
	// Paragraphs are separated by blank lines; a line that is only a link becomes a button once.
	var blocks []block
	buttonDone := false
	for _, para := range strings.Split(strings.ReplaceAll(strings.TrimSpace(text), "\r\n", "\n"), "\n\n") {
		para = strings.Trim(para, "\n") // keep the indentation that marks a list
		if strings.TrimSpace(para) == "" {
			continue
		}
		lines := strings.Split(para, "\n")
		switch {
		case len(lines) == 1 && urlRe.MatchString(para) && strings.TrimSpace(urlRe.ReplaceAllString(para, "")) == "" && !buttonDone:
			blocks = append(blocks, block{"link", strings.TrimSpace(para)})
			buttonDone = true
		case allIndented(lines):
			blocks = append(blocks, block{"list", para})
		default:
			blocks = append(blocks, block{"p", para})
			// Transactional emails often put the action address after a sentence.
			if !buttonDone {
				if u := urlRe.FindString(para); u != "" {
					blocks = append(blocks, block{"link", strings.TrimRight(u, ".,;:)")})
					buttonDone = true
				}
			}
		}
	}

	var b strings.Builder
	b.WriteString(`<!doctype html><html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width"><title>`)
	b.WriteString(html.EscapeString(subject))
	b.WriteString(`</title><style>@media(max-width:600px){.email-card{padding:24px 20px!important}.email-title{font-size:28px!important}}</style></head><body style="margin:0;padding:0;background:` + cream + `;">`)
	b.WriteString(`<div style="display:none;max-height:0;overflow:hidden;opacity:0;">` + html.EscapeString(subject) + ` · LogaLuxe</div>`)
	b.WriteString(`<table role="presentation" width="100%" cellpadding="0" cellspacing="0" style="background:` + cream + `;"><tr><td align="center" style="padding:32px 16px;">`)
	b.WriteString(`<table role="presentation" width="100%" cellpadding="0" cellspacing="0" style="max-width:560px;">`)
	// The mark and the name.
	b.WriteString(`<tr><td style="padding:24px 28px;background:#120E0D;border-radius:20px 20px 0 0;border-bottom:3px solid ` + gold + `;font-family:Georgia,'Times New Roman',serif;font-size:28px;letter-spacing:.3px;color:` + cream + `;">` +
		`<span aria-hidden="true" style="color:` + gold + `;margin-right:10px;">✦</span>LogaLuxe<div style="margin-top:8px;font:10px Helvetica,Arial,sans-serif;letter-spacing:2px;color:` + gold + `;">BEAUTY, BOOKED.</div></td></tr>`)
	// The card.
	b.WriteString(`<tr><td class="email-card" style="background:#FFFFFF;border:1px solid ` + line + `;border-top:0;border-radius:0 0 20px 20px;padding:32px;">`)
	b.WriteString(`<h1 class="email-title" style="margin:0 0 24px;font-family:'Bodoni Moda',Georgia,'Times New Roman',serif;font-weight:400;font-size:32px;line-height:1.15;color:` + ink + `;">` + html.EscapeString(subject) + `</h1>`)
	for _, bl := range blocks {
		switch bl.kind {
		case "link":
			b.WriteString(`<p style="margin:26px 0;"><a href="` + html.EscapeString(bl.text) + `" style="display:inline-block;background:` + gold + `;color:` + ink + `;text-decoration:none;font-family:Helvetica,Arial,sans-serif;font-weight:700;font-size:15px;padding:14px 26px;border-radius:999px;">` + html.EscapeString(buttonWord(subject, bl.text)) + `</a></p>`)
			b.WriteString(`<p style="margin:0 0 16px;font-family:Helvetica,Arial,sans-serif;font-size:12px;line-height:1.6;color:` + muted + `;word-break:break-all;">Or open this address: <a href="` + html.EscapeString(bl.text) + `" style="color:` + wine + `;">` + html.EscapeString(bl.text) + `</a></p>`)
		case "list":
			b.WriteString(`<table role="presentation" cellpadding="0" cellspacing="0" style="margin:0 0 16px;width:100%;">`)
			for _, l := range strings.Split(bl.text, "\n") {
				b.WriteString(`<tr><td style="padding:6px 0;border-bottom:1px solid ` + line + `;font-family:Helvetica,Arial,sans-serif;font-size:15px;line-height:1.5;color:` + ink + `;">` + linkify(strings.TrimSpace(l), wine) + `</td></tr>`)
			}
			b.WriteString(`</table>`)
		default:
			if code := otpCode(subject, bl.text); code != "" {
				b.WriteString(`<div style="margin:20px 0;padding:24px 12px;border:1px solid ` + line + `;border-radius:14px;background:` + cream + `;text-align:center;"><div style="font:11px Helvetica,Arial,sans-serif;letter-spacing:1.5px;color:` + muted + `;">YOUR VERIFICATION CODE</div><div style="margin-top:12px;font:700 34px 'Courier New',monospace;letter-spacing:6px;color:` + wine + `;">` + html.EscapeString(code) + `</div></div>`)
			}
			b.WriteString(`<p style="margin:0 0 16px;font-family:Helvetica,Arial,sans-serif;font-size:16px;line-height:1.6;color:` + ink + `;">` + strings.ReplaceAll(linkify(bl.text, wine), "\n", "<br>") + `</p>`)
		}
	}
	b.WriteString(`</td></tr>`)
	b.WriteString(`<tr><td style="padding:24px 12px 0;text-align:center;font-family:Helvetica,Arial,sans-serif;font-size:12px;line-height:1.6;color:` + muted + `;">Sent through LogaLuxe · Beauty, booked.<br>Keep verification codes and private booking links to yourself.</td></tr>`)
	b.WriteString(`</table></td></tr></table></body></html>`)
	return b.String()
}

var urlRe = regexp.MustCompile(`https?://[^\s<>"']+`)
var otpRe = regexp.MustCompile(`\b[0-9]{6}\b`)

func otpCode(subject, paragraph string) string {
	s := strings.ToLower(subject)
	if !strings.Contains(s, "code") && !strings.Contains(s, "otp") {
		return ""
	}
	if !strings.Contains(strings.ToLower(paragraph), "code") && strings.TrimSpace(paragraph) != otpRe.FindString(paragraph) {
		return ""
	}
	return otpRe.FindString(paragraph)
}

// linkify escapes a line of text and turns its addresses into links.
func linkify(s, color string) string {
	var out strings.Builder
	last := 0
	for _, m := range urlRe.FindAllStringIndex(s, -1) {
		out.WriteString(html.EscapeString(s[last:m[0]]))
		u := strings.TrimRight(s[m[0]:m[1]], ".,;:)")
		end := m[0] + len(u)
		out.WriteString(`<a href="` + html.EscapeString(u) + `" style="color:` + color + `;">` + html.EscapeString(u) + `</a>`)
		out.WriteString(html.EscapeString(s[end:m[1]]))
		last = m[1]
	}
	out.WriteString(html.EscapeString(s[last:]))
	return out.String()
}

func allIndented(lines []string) bool {
	for _, l := range lines {
		if !strings.HasPrefix(l, "  ") {
			return false
		}
	}
	return len(lines) > 0
}

// buttonWord names the button from the subject and the address, in plain words.
func buttonWord(subject, u string) string {
	s := strings.ToLower(subject + " " + u)
	switch {
	case strings.Contains(s, "confirm your email") || strings.Contains(u, "/verify"):
		return "Confirm my email"
	case strings.Contains(s, "reset") && strings.Contains(s, "password"):
		return "Choose a new password"
	case strings.Contains(u, "/book?booking="):
		return "See my booking"
	case strings.Contains(u, "/account?tab=orders"):
		return "See my order"
	case strings.Contains(u, "/business/calendar"):
		return "Open my calendar"
	case strings.Contains(s, "gift card"):
		return "See the gift card"
	}
	return "Open LogaLuxe"
}
