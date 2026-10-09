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
		}
	}

	var b strings.Builder
	b.WriteString(`<!doctype html><html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width"><title>`)
	b.WriteString(html.EscapeString(subject))
	b.WriteString(`</title></head><body style="margin:0;padding:0;background:` + cream + `;">`)
	b.WriteString(`<table role="presentation" width="100%" cellpadding="0" cellspacing="0" style="background:` + cream + `;"><tr><td align="center" style="padding:32px 16px;">`)
	b.WriteString(`<table role="presentation" width="100%" cellpadding="0" cellspacing="0" style="max-width:560px;">`)
	// The mark and the name.
	b.WriteString(`<tr><td style="padding:0 4px 18px;font-family:Georgia,'Times New Roman',serif;font-size:22px;letter-spacing:.3px;color:` + ink + `;">` +
		`<span style="display:inline-block;width:14px;height:18px;border:1.5px solid ` + ink + `;border-bottom:0;border-radius:9px 9px 0 0;vertical-align:-3px;margin-right:8px;"></span>LogaLuxe</td></tr>`)
	// The card.
	b.WriteString(`<tr><td style="background:#FFFFFF;border:1px solid ` + line + `;border-radius:20px;padding:34px 32px;">`)
	b.WriteString(`<h1 style="margin:0 0 18px;font-family:'Bodoni Moda',Georgia,'Times New Roman',serif;font-weight:500;font-size:28px;line-height:1.15;color:` + ink + `;">` + html.EscapeString(subject) + `</h1>`)
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
			b.WriteString(`<p style="margin:0 0 16px;font-family:Helvetica,Arial,sans-serif;font-size:16px;line-height:1.6;color:` + ink + `;">` + strings.ReplaceAll(linkify(bl.text, wine), "\n", "<br>") + `</p>`)
		}
	}
	b.WriteString(`</td></tr>`)
	b.WriteString(`<tr><td style="padding:18px 4px 0;font-family:Helvetica,Arial,sans-serif;font-size:12px;line-height:1.6;color:` + muted + `;">LogaLuxe · Beauty, booked. This email was sent to you because of an account or a booking on LogaLuxe.</td></tr>`)
	b.WriteString(`</table></td></tr></table></body></html>`)
	return b.String()
}

var urlRe = regexp.MustCompile(`https?://[^\s<>"']+`)

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
