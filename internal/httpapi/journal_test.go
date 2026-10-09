package httpapi

import (
	"reflect"
	"strings"
	"testing"
)

func TestArticleSlug(t *testing.T) {
	cases := map[string]string{
		"Knotless braids: what to ask for":        "knotless-braids-what-to-ask-for",
		"  Gel or acrylic nails? Which to book  ": "gel-or-acrylic-nails-which-to-book",
		"Ada's Braid Studio & the 5-hour install": "ada-s-braid-studio-the-5-hour-install",
		"---":                            "",
		"Skin care for humid Lagos (NG)": "skin-care-for-humid-lagos-ng",
		strings.Repeat("a very long title that goes on ", 5): "a-very-long-title-that-goes-on-a-very-long-title-that-goes-o",
	}
	for in, want := range cases {
		if got := articleSlug(in); got != want {
			t.Errorf("articleSlug(%q) = %q, want %q", in, got, want)
		}
	}
	for _, s := range []string{"knotless-braids", "a1", "2026-prices", strings.Repeat("x", 120)} {
		if !articleSlugRe.MatchString(s) {
			t.Errorf("slug %q should be accepted", s)
		}
	}
	for _, s := range []string{"", "-leading", "Caps", "with space", "under_score", strings.Repeat("x", 121)} {
		if articleSlugRe.MatchString(s) {
			t.Errorf("slug %q should be refused", s)
		}
	}
}

func TestReadingMinutes(t *testing.T) {
	words := func(n int) string { return strings.Repeat("word ", n) }
	cases := []struct {
		body string
		want int
	}{
		{"", 1},
		{"## Heading only", 1},
		{words(50), 1},
		{words(220), 1},
		{words(330), 2}, // 329 rounds down, 330 rounds up
		{words(329), 1},
		{words(660), 3},
		{words(700), 3},
		{words(900), 4},
	}
	for _, c := range cases {
		if got := readingMinutes(c.body); got != c.want {
			t.Errorf("readingMinutes(%d words) = %d, want %d", wordCount(c.body), got, c.want)
		}
	}
	t.Run("marks, addresses and bare punctuation do not count as words", func(t *testing.T) {
		body := "## Heading\n\nOne two three, four.\n\n- five\n- six\n\n![a picture](media:fefc4ea0-f5a1-4f33-805c-ca427f437929)\n\n[seven](/search?category=braids) ... eight"
		if got := wordCount(body); got != 11 { // Heading One two three four five six a picture seven eight
			t.Errorf("wordCount = %d, want 11", got)
		}
	})
}

func TestMediaImages(t *testing.T) {
	const id = "fefc4ea0-f5a1-4f33-805c-ca427f437929"
	md := "Intro.\n\n![Braids](media:" + id + ")\n\nA [link](https://example.test/x) and ![again](media:" + id + ") and ![other](media:4a818a12-3f3b-4f7e-a8f5-8489338c807d)."

	t.Run("rewrite to a base address", func(t *testing.T) {
		got := rewriteMediaImages(md, "/v1/media/")
		if !strings.Contains(got, "![Braids](/v1/media/"+id+")") || strings.Contains(got, "media:") {
			t.Errorf("rewrite went wrong: %s", got)
		}
		if !strings.Contains(got, "[link](https://example.test/x)") {
			t.Errorf("an ordinary link must be left alone: %s", got)
		}
		if got := rewriteMediaImages("![x](media:not-an-id)", "/v1/media"); got != "![x](media:not-an-id)" {
			t.Errorf("something that is not an id must be left alone: %s", got)
		}
	})
	t.Run("ids in order of first use, each once", func(t *testing.T) {
		got := mediaImageIDs(md)
		want := []string{id, "4a818a12-3f3b-4f7e-a8f5-8489338c807d"}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("mediaImageIDs = %v, want %v", got, want)
		}
		if n := len(mediaImageIDs("no pictures here")); n != 0 {
			t.Errorf("want no ids, got %d", n)
		}
	})
	t.Run("a pasted address becomes media:<id> on save", func(t *testing.T) {
		for _, in := range []string{"![x](/media/" + id + ")", "![x](/v1/media/" + id + ")", "![x](http://localhost:18080/v1/media/" + id + ")", "![x](https://api.logaluxe.com/v1/media/" + id + ")"} {
			if got := canonicalMediaImages(in); got != "![x](media:"+id+")" {
				t.Errorf("canonicalMediaImages(%q) = %q", in, got)
			}
		}
		if got := canonicalMediaImages("![x](https://elsewhere.test/photo.jpg)"); got != "![x](https://elsewhere.test/photo.jpg)" {
			t.Errorf("an outside picture must be left alone: %s", got)
		}
	})
}
