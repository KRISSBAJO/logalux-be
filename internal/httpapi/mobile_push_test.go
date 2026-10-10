package httpapi

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestExpoTokenValidation(t *testing.T) {
	for _, token := range []string{"ExpoPushToken[abc_123-X]", "ExponentPushToken[abc]"} {
		if !expoTokenPattern.MatchString(token) {
			t.Fatal(token)
		}
	}
	for _, token := range []string{"https://example.com", "ExpoPushToken[]", "ExpoPushToken[<script>]"} {
		if expoTokenPattern.MatchString(token) {
			t.Fatal(token)
		}
	}
}

func TestSecurityMobilePushOwnershipAndTransaction(t *testing.T) {
	s := securityServer(t)
	_, _, _, user, booking := securityFixture(t, s)
	other := securityID(t, s, `insert into users(email) values('push-other@example.test') returning id::text`)
	t.Setenv("PUSH_ENABLED", "true")
	save := func(id, token string) {
		r := httptest.NewRequest("POST", "/v1/auth/push", strings.NewReader(`{"token":"ExpoPushToken[fixture]","platform":"ios"}`))
		r.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		s.savePush(w, r, "customer", id)
		if w.Code != 200 {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	save(user, "first-session")
	check := func(id, token string, want bool) {
		r := httptest.NewRequest("GET", "/v1/auth/push", nil)
		r.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		s.savePush(w, r, "customer", id)
		var result struct {
			Enabled bool `json:"enabled"`
		}
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &result) != nil || result.Enabled != want {
			t.Fatalf("notification status: got %d %s; want enabled=%v", w.Code, w.Body.String(), want)
		}
	}
	check(user, "first-session", true)
	check(user, "different-session", false)
	check(other, "first-session", false)
	save(other, "second-session")
	check(user, "first-session", false)
	check(other, "second-session", true)
	var owner string
	if err := s.pool.QueryRow(context.Background(), `select owner_id::text from push_devices where token='ExpoPushToken[fixture]'`).Scan(&owner); err != nil || owner != other {
		t.Fatal("device did not move to its current authenticated owner", err)
	}
	tx, err := s.pool.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.Background())
	if _, err = tx.Exec(context.Background(), `update bookings set status='cancelled_client' where id=$1`, booking); err != nil {
		t.Fatal(err)
	}
	if err = tx.Rollback(context.Background()); err != nil {
		t.Fatal(err)
	}
	var count int
	if err = s.pool.QueryRow(context.Background(), `select count(*) from push_events where owner_id=$1`, user).Scan(&count); err != nil || count != 1 {
		t.Fatal("rolled-back status created a push event", count, err)
	}
}
