package mail

import (
	"strings"
	"testing"
)

func TestHTMLWrapsTextIntoTheBrandedLayout(t *testing.T) {
	text := "Hello Christopher,\n\nPlease confirm this is your email address for LogaLuxe. Open this link. It works for 48 hours.\n\nhttp://localhost:3100/verify?token=abc123\n\nConfirming lets you leave reviews, and it is where we send your booking and order emails.\n\nLogaLuxe"
	out := HTML("Confirm your email for LogaLuxe", text)
	for _, want := range []string{
		"<h1", "Confirm your email for LogaLuxe",
		`href="http://localhost:3100/verify?token=abc123"`, ">Confirm my email</a>",
		"Hello Christopher,", "Or open this address",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q", want)
		}
	}
	if strings.Count(out, "token=abc123") != 3 { // the button, and the plain address under it as a link and as words
		t.Errorf("the link should appear as a button and as an address, got %d", strings.Count(out, "token=abc123"))
	}
}

func TestHTMLEscapesAndKeepsLists(t *testing.T) {
	out := HTML("Your <b>order</b>", "Items:\n\n  2 x Oil & co, from Ada: $20\n  1 x Gel: $12\n\nSee it: https://logaluxe.com/account?tab=orders.")
	if strings.Contains(out, "<b>order</b>") || !strings.Contains(out, "&lt;b&gt;order&lt;/b&gt;") {
		t.Error("the subject must be escaped")
	}
	if !strings.Contains(out, "Oil &amp; co") {
		t.Error("list text must be escaped")
	}
	if !strings.Contains(out, `href="https://logaluxe.com/account?tab=orders"`) || strings.Contains(out, `orders."`) {
		t.Error("a trailing full stop must not be part of the link")
	}
	if strings.Count(out, "<tr><td style=\"padding:6px 0") != 2 {
		t.Error("two list rows expected")
	}
}

func TestButtonWords(t *testing.T) {
	cases := map[string]string{
		"https://x/verify?token=1":        "Confirm my email",
		"https://x/reset?token=1":         "Open LogaLuxe",
		"https://x/b/ada/book?booking=1":  "See my booking",
		"https://x/business/calendar?d=1": "Open my calendar",
		"https://x/account?tab=orders":    "See my order",
	}
	for u, want := range cases {
		if got := buttonWord("A message", u); got != want {
			t.Errorf("%s: got %q want %q", u, got, want)
		}
	}
	if got := buttonWord("Reset your LogaLuxe password", "https://x/reset?token=1"); got != "Choose a new password" {
		t.Errorf("reset subject: got %q", got)
	}
}
