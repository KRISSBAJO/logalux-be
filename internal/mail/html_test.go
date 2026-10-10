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

func TestBookingActionAndCodeSafety(t *testing.T) {
	out := HTML("You are booked at Nia", "Hello Chris,\n\n  Braids\n  Total ₦7000\n\nSee, change or cancel it: https://logaluxe.com/b/nia/book?booking=sample")
	if !strings.Contains(out, ">See my booking</a>") || !strings.Contains(out, "₦7000") {
		t.Fatal("booking details or primary action missing")
	}
	out = HTML("Your sign-in code", "Your code is 093108. It expires in 10 minutes.\n\n<script>alert(1)</script>")
	if !strings.Contains(out, "YOUR VERIFICATION CODE") || !strings.Contains(out, "093108") || strings.Contains(out, "<script>") {
		t.Fatal("code card missing or unsafe content unescaped")
	}
	msg, err := alternativeMessage("a@example.com", "b@example.com", "Your sign-in code", "Your code is 093108")
	if err != nil || !strings.Contains(string(msg), "multipart/alternative") || !strings.Contains(string(msg), "text/plain") || !strings.Contains(string(msg), "text/html") {
		t.Fatal("SMTP requires both alternatives", err)
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
