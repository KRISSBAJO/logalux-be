package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/go-chi/chi/v5"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func accountRequest(c Customer, method, path, body string, fn http.HandlerFunc) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.Header.Set("Authorization", "Bearer account-test")
	r = r.WithContext(context.WithValue(r.Context(), userKey{}, c))
	w := httptest.NewRecorder()
	fn(w, r)
	return w
}

func TestSecurityCustomerAccountHistoryAndPrivacy(t *testing.T) {
	s := securityServer(t)
	biz, staff, _, user, booking := securityFixture(t, s)
	c := Customer{ID: user, Email: "security@example.test"}
	securityExec(t, s, `update bookings set status='completed',starts_at=now()-interval '90 days',ends_at=now()-interval '89 days' where id=$1`, booking)
	securityExec(t, s, `insert into bookings(business_id,staff_id,user_id,client_name,starts_at,ends_at,status) select $1,$2,$3,'History',now()-(n||' days')::interval,now()-(n||' days')::interval+interval '1 hour','completed' from generate_series(1,72)n`, biz, staff, user)
	seen := map[string]bool{}
	for p := 1; p <= 7; p++ {
		w := accountRequest(c, "GET", fmt.Sprintf("/v1/auth/me?paged=1&page=%d&scope=past", p), "", s.authMe)
		if w.Code != 200 {
			t.Fatal(w.Body.String())
		}
		var d struct {
			Bookings    []struct{ ID string }
			BookingPage struct{ Total int } `json:"booking_page"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &d); err != nil {
			t.Fatal(err)
		}
		if d.BookingPage.Total != 73 {
			t.Fatalf("count %d", d.BookingPage.Total)
		}
		for _, b := range d.Bookings {
			if seen[b.ID] {
				t.Fatal("repeated booking")
			}
			seen[b.ID] = true
		}
	}
	if len(seen) != 73 {
		t.Fatalf("history truncated: %d", len(seen))
	}
	// Mobile detail/review routes must reach an owned booking beyond the history window.
	requestBooking := func(who Customer) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", "/v1/auth/bookings/"+booking, nil)
		rc := chi.NewRouteContext()
		rc.URLParams.Add("id", booking)
		r = r.WithContext(context.WithValue(context.WithValue(r.Context(), userKey{}, who), chi.RouteCtxKey, rc))
		w := httptest.NewRecorder()
		s.authBooking(w, r)
		return w
	}
	owned := requestBooking(c)
	if owned.Code != 200 || !strings.Contains(owned.Body.String(), `"can_review":true`) || !strings.Contains(owned.Body.String(), `"review_photos"`) {
		t.Fatalf("old owned booking is incomplete: %d %s", owned.Code, owned.Body.String())
	}
	other := securityID(t, s, `insert into users(email,first_name) values('other@example.test','Not yours') returning id::text`)
	if foreign := requestBooking(Customer{ID: other}); foreign.Code != 404 {
		t.Fatalf("another customer's booking was exposed: %d", foreign.Code)
	}
	for _, id := range []string{user, other} {
		securityExec(t, s, `insert into user_sessions(user_id,token_hash,expires_at) values($1,$2,now()+interval '1 day')`, id, hashToken(id))
	}
	securityExec(t, s, `insert into user_sessions(user_id,token_hash,expires_at) values($1,$2,now()+interval '1 day')`, user, hashToken("account-test"))
	w := accountRequest(c, "GET", "/v1/auth/export", "", s.authExport)
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	for _, bad := range []string{"token_hash", "password_hash", "Not yours", "stripe_customer_id"} {
		if strings.Contains(w.Body.String(), bad) {
			t.Fatalf("export leaked %s", bad)
		}
	}
	w = accountRequest(c, "GET", "/v1/auth/sessions", "", s.authSessions)
	if w.Code != 200 || strings.Contains(w.Body.String(), "token_hash") {
		t.Fatal(w.Body.String())
	}
	revoke := func(w http.ResponseWriter, r *http.Request) {
		rc := chi.NewRouteContext()
		rc.URLParams.Add("id", "others")
		s.authSessionRevoke(w, r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, rc)))
	}
	w = accountRequest(c, "DELETE", "/v1/auth/sessions/others", "", revoke)
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	var own, foreign int
	_ = s.pool.QueryRow(context.Background(), `select count(*) from user_sessions where user_id=$1`, user).Scan(&own)
	_ = s.pool.QueryRow(context.Background(), `select count(*) from user_sessions where user_id=$1`, other).Scan(&foreign)
	if own != 1 || foreign != 1 {
		t.Fatalf("revocation ownership: %d/%d", own, foreign)
	}
	w = accountRequest(c, "POST", "/v1/auth/delete", `{"confirm":"DELETE"}`, s.authDelete)
	if w.Code != 403 {
		t.Fatal(w.Body.String())
	}
	c.PhoneVerified = true
	securityExec(t, s, `insert into user_credits(user_id,amount_cents,currency,reason) values($1,100,'USD','test')`, user)
	w = accountRequest(c, "POST", "/v1/auth/delete", `{"confirm":"DELETE"}`, s.authDelete)
	if w.Code != 409 {
		t.Fatalf("credit guard: %d %s", w.Code, w.Body.String())
	}
	securityExec(t, s, `delete from user_credits where user_id=$1`, user)
	w = accountRequest(c, "POST", "/v1/auth/delete", `{"confirm":"DELETE"}`, s.authDelete)
	if w.Code != 200 {
		t.Fatalf("delete: %d %s", w.Code, w.Body.String())
	}
	if _, ok := s.customerFrom(context.Background(), "account-test"); ok {
		t.Fatal("deleted session still active")
	}
	var retained int
	_ = s.pool.QueryRow(context.Background(), `select count(*) from bookings where user_id=$1`, user).Scan(&retained)
	if retained != 73 {
		t.Fatal("financial history lost")
	}
}
