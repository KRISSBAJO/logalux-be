package httpapi

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/hex"
	"strconv"
	"testing"
	"time"
)

func TestStripeSigned(t *testing.T) {
	secret, body, now := "whsec_test", []byte(`{"type":"checkout.session.completed"}`), time.Unix(1790000000, 0)
	sign := func(ts int64, key string) string {
		mac := hmac.New(sha256.New, []byte(key))
		mac.Write([]byte(strconv.FormatInt(ts, 10) + "."))
		mac.Write(body)
		return "t=" + strconv.FormatInt(ts, 10) + ",v1=" + hex.EncodeToString(mac.Sum(nil))
	}
	if !stripeSigned(body, sign(now.Unix(), secret), secret, now) {
		t.Fatal("a correctly signed event was refused")
	}
	if !stripeSigned(body, "v0=ignored,"+sign(now.Unix()-60, secret), secret, now) {
		t.Fatal("a signed event from a minute ago was refused")
	}
	for name, header := range map[string]string{
		"wrong secret":    sign(now.Unix(), "whsec_other"),
		"too old":         sign(now.Unix()-3600, secret),
		"from the future": sign(now.Unix()+3600, secret),
		"empty":           "",
		"no timestamp":    "v1=abc",
	} {
		if stripeSigned(body, header, secret, now) {
			t.Fatalf("accepted a bad signature: %s", name)
		}
	}
	if stripeSigned([]byte(`{"type":"tampered"}`), sign(now.Unix(), secret), secret, now) {
		t.Fatal("accepted a body that was changed after signing")
	}
}

func TestPaystackSigned(t *testing.T) {
	secret, body := "sk_test_x", []byte(`{"event":"charge.success"}`)
	mac := hmac.New(sha512.New, []byte(secret))
	mac.Write(body)
	good := hex.EncodeToString(mac.Sum(nil))
	if !paystackSigned(body, good, secret) {
		t.Fatal("a correctly signed event was refused")
	}
	if paystackSigned(body, good, "sk_test_y") || paystackSigned([]byte(`{}`), good, secret) || paystackSigned(body, "", secret) {
		t.Fatal("accepted a bad signature")
	}
}

func TestPricingRules(t *testing.T) {
	p := &pricing{
		base:     map[string]int{"braids": 18000, "wash": 2500},
		personal: map[string]map[string]int{"nia": {"braids": 17000}},
		levels:   map[string]string{"ada": "master", "tola": "junior", "nia": "senior"},
		rules: []priceRule{
			{Name: "Saturday peak", ServiceID: "braids", Days: []string{"sat"}, Kind: "amount", Value: 1500},
			{Name: "Junior", Level: "junior", Kind: "percent", Value: -10},
			{Name: "Morning", From: "09:00", To: "12:00", Days: []string{"tue"}, Kind: "percent", Value: -15},
			{Name: "December", StartsOn: "2026-12-01", EndsOn: "2026-12-31", Kind: "amount", Value: 500},
		},
	}
	at := func(v string) time.Time { x, _ := time.Parse("2006-01-02 15:04", v); return x }
	for _, c := range []struct {
		name, service, staff, when string
		want                       int
	}{
		{"a plain weekday", "braids", "ada", "2026-10-07 14:00", 18000},
		{"Saturday costs more", "braids", "ada", "2026-10-10 10:00", 19500},
		{"a personal price replaces the menu price, then rules apply", "braids", "nia", "2026-10-10 10:00", 18500},
		{"a junior on Saturday: +15 then -10%", "braids", "tola", "2026-10-10 10:00", 17550},
		{"the Saturday rule is for braids only", "wash", "ada", "2026-10-10 10:00", 2500},
		{"Tuesday morning", "wash", "ada", "2026-10-06 09:30", 2125},
		{"Tuesday at noon is outside the window", "wash", "ada", "2026-10-06 12:00", 2500},
		{"inside the date range", "wash", "ada", "2026-12-24 15:00", 3000},
	} {
		if got, _ := p.price(c.service, c.staff, at(c.when)); got != c.want {
			t.Errorf("%s: got %d, want %d", c.name, got, c.want)
		}
	}
}
