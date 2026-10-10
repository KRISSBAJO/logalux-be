package mail

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRelyKitQueuesAndRefusesSuppression(t *testing.T) {
	for _, tt := range []struct {
		name, body string
		code       int
		fail       bool
	}{
		{"queued", `{"id":"email-fixture","status":"queued"}`, 201, false},
		{"suppressed", `{"id":"email-fixture","status":"cancelled","suppressed":["client@example.com"]}`, 201, true},
		{"invalid", `{}`, 200, true},
		{"forbidden", `{"message":"domain not verified"}`, 403, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/emails" || r.Header.Get("Authorization") != "Bearer fixture-key" {
					t.Error("incorrect RelyKit request")
				}
				var body map[string]any
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
				}
				if body["subject"] != "Hello there" {
					t.Error("subject was not sanitized")
				}
				if body["text"] != "Body" || !strings.Contains(fmtHTML(body["html"]), "LogaLuxe") {
					t.Error("HTML and plain text must both be submitted")
				}
				w.WriteHeader(tt.code)
				w.Write([]byte(tt.body))
			}))
			defer srv.Close()
			m := New(Config{Provider: "relykit", RelyKitKey: "fixture-key", RelyKitURL: srv.URL, From: "LogaLuxe <noreply@logaluxe.com>"})
			status, err := m.Send(context.Background(), "client@example.com", "Hello\nthere", "Body")
			if (err != nil) != tt.fail {
				t.Fatalf("unexpected error: %v", err)
			}
			if !tt.fail && status != "queued" {
				t.Fatalf("status=%s", status)
			}
		})
	}
	if m := New(Config{Provider: "relykit", From: "a@b.co"}); m.Mode() != "log" {
		t.Fatal("missing RelyKit key must log only")
	}
}

func fmtHTML(v any) string { s, _ := v.(string); return s }
