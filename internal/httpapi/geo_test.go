package httpapi

import (
	"net"
	"net/http"
	"testing"
)

func TestPublicIP(t *testing.T) {
	for addr, want := range map[string]bool{
		"8.8.8.8": true, "102.89.23.4": true, "2607:f8b0:4005:805::200e": true,
		"127.0.0.1": false, "::1": false, "10.1.2.3": false, "172.18.0.5": false, "192.168.1.9": false, "169.254.1.1": false,
		"100.64.3.3": false, "203.0.113.7": false, "198.51.100.1": false, "0.0.0.0": false, "fe80::1": false, "fd00::1": false, "2001:db8::1": false, "224.0.0.1": false,
	} {
		if got := publicIP(net.ParseIP(addr)); got != want {
			t.Errorf("publicIP(%s) = %v, want %v", addr, got, want)
		}
	}
	if publicIP(nil) {
		t.Error("no address is not a public address")
	}
}

func TestIPPrefixKeepsOnlyTheNetwork(t *testing.T) {
	for addr, want := range map[string]string{"8.8.8.8": "8.8.8.0/24", "102.89.23.200": "102.89.23.0/24", "2607:f8b0:4005:805::200e": "2607:f8b0:4005::/48"} {
		if got := ipPrefix(net.ParseIP(addr)); got != want {
			t.Errorf("ipPrefix(%s) = %s, want %s", addr, got, want)
		}
	}
	if ipPrefix(net.ParseIP("8.8.8.8")) != ipPrefix(net.ParseIP("8.8.8.250")) {
		t.Error("neighbours should share a prefix")
	}
}

func TestEdgeGuess(t *testing.T) {
	h := http.Header{}
	if _, ok := edgeGuess(h); ok {
		t.Error("no headers is no guess")
	}
	h.Set("X-Vercel-IP-Country", "us")
	h.Set("X-Vercel-IP-Country-Region", "GA")
	h.Set("X-Vercel-IP-City", "Sandy%20Springs")
	h.Set("X-Vercel-IP-Latitude", "33.92")
	h.Set("X-Vercel-IP-Longitude", "-84.37")
	g, ok := edgeGuess(h)
	if !ok || g.Country != "US" || g.Region != "GA" || g.City != "Sandy Springs" || !g.Point || g.Lat != 33.92 {
		t.Errorf("Vercel headers read as %+v %v", g, ok)
	}
	h = http.Header{}
	h.Set("CF-IPCountry", "NG")
	h.Set("CF-IPCity", "Lagos")
	if g, ok := edgeGuess(h); !ok || g.Country != "NG" || g.City != "Lagos" || g.Point {
		t.Errorf("Cloudflare headers read as %+v %v", g, ok)
	}
	h.Set("CF-IPCountry", "XX") // Cloudflare's "unknown"
	if _, ok := edgeGuess(h); ok {
		t.Error("an unknown country is no guess")
	}
}

func TestLocalPhone(t *testing.T) {
	for _, c := range []struct{ country, in, want string }{
		{"US", "(323) 555-0142", "+13235550142"}, {"US", "1 323 555 0142", "+13235550142"}, {"US", "+13235550142", "+13235550142"},
		{"NG", "0803 555 0142", "+2348035550142"}, {"NG", "2348035550142", "+2348035550142"}, {"NG", "+2348035550142", "+2348035550142"},
		{"US", "555", "555"}, {"NG", "not a number", "not a number"}, {"US", "", ""},
	} {
		if got := localPhone(c.country, c.in); got != c.want {
			t.Errorf("localPhone(%s, %q) = %q, want %q", c.country, c.in, got, c.want)
		}
	}
}

func TestSearchWords(t *testing.T) {
	if got := distanceWords(338, "mi"); got != "210 miles" {
		t.Errorf("distanceWords = %q", got)
	}
	if got := distanceWords(3.7, "km"); got != "3.7 kilometres" {
		t.Errorf("distanceWords = %q", got)
	}
	if got := distanceWords(4767.6, "km"); got != "4,768 kilometres" {
		t.Errorf("distanceWords = %q", got)
	}
	if trimNum(25) != "25" || trimNum(2.5) != "2.5" {
		t.Error("trimNum")
	}
	nash, tn, ng := newPlace("Nashville", "TN", "US", 36.16, -86.78), newPlace("", "TN", "US", 0, 0), newPlace("", "", "NG", 0, 0)
	for _, c := range []struct {
		p    place
		text string
		want bool
	}{
		{nash, "nash", true}, {nash, "nashville tn", true}, {nash, "nashville tennessee", true}, {nash, "tenn", false}, {nash, "ville", false},
		{tn, "tenn", true}, {tn, "tn", true}, {tn, "nash", false},
		{ng, "nig", true}, {ng, "all of nigeria", true}, {ng, "lagos", false},
		{newPlace("", "", "US", 0, 0), "usa", true}, {newPlace("", "", "US", 0, 0), "the united", true},
	} {
		if got := matchesText(c.p, foldText(c.text)); got != c.want {
			t.Errorf("matchesText(%s, %q) = %v, want %v", c.p.Label, c.text, got, c.want)
		}
	}
	if ng.Kind != "country" || ng.Slug != "nigeria" || ng.Currency != "NGN" || ng.Lat == 0 || tn.Kind != "state" || nash.Kind != "city" {
		t.Errorf("kinds: %+v", ng)
	}
}

func TestGiftCurrency(t *testing.T) {
	if c, min, max := giftCurrency("NG"); c != "NGN" || min != 500000 || max != 50000000 {
		t.Errorf("NG gift: %s %d %d", c, min, max)
	}
	if c, min, max := giftCurrency("US"); c != "USD" || min != 1000 || max != 50000 {
		t.Errorf("US gift: %s %d %d", c, min, max)
	}
}
