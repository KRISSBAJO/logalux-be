package mail

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestFallsBackToLogWithoutAKey(t *testing.T) {
	m := New(Config{Provider: "resend", From: "a@b.co"})
	if m.Mode() != "log" {
		t.Fatalf("mode = %s", m.Mode())
	}
	status, err := m.Send(context.Background(), "someone@example.com", "Hi", "Body")
	if err != nil || status != "logged" {
		t.Fatalf("status=%s err=%v", status, err)
	}
}

func TestResendRequest(t *testing.T) {
	var got map[string]any
	var auth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth = r.Header.Get("Authorization")
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &got)
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{"id":"x"}`))
	}))
	defer srv.Close()

	m := New(Config{Provider: "resend", From: "LogaLuxe <noreply@example.com>", ResendKey: "re_test"})
	m.resendURL = srv.URL
	status, err := m.Send(context.Background(), "someone@example.com", "Reset your\npassword", "Link")
	if err != nil || status != "sent" {
		t.Fatalf("status=%s err=%v", status, err)
	}
	if auth != "Bearer re_test" {
		t.Fatalf("auth = %q", auth)
	}
	if got["subject"] != "Reset your password" {
		t.Fatalf("subject kept a line break: %q", got["subject"])
	}
	if to := got["to"].([]any); len(to) != 1 || to[0] != "someone@example.com" {
		t.Fatalf("to = %v", got["to"])
	}
}

func TestResendErrorIsReported(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(403)
		_, _ = w.Write([]byte(`{"message":"domain not verified"}`))
	}))
	defer srv.Close()
	m := New(Config{Provider: "resend", From: "a@b.co", ResendKey: "k"})
	m.resendURL = srv.URL
	if _, err := m.Send(context.Background(), "someone@example.com", "s", "b"); err == nil {
		t.Fatal("expected an error")
	}
}

func TestRefusesBadAddresses(t *testing.T) {
	m := New(Config{})
	for _, bad := range []string{"", "nope", "a@b", "a@b.co\nBcc: x@y.co", "a b@c.co", "a@b.co, c@d.co"} {
		if _, err := m.Send(context.Background(), bad, "s", "b"); err == nil {
			t.Fatalf("accepted %q", bad)
		}
	}
}
