package httpapi

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSecurityMerchantCustomerLink(t *testing.T) {
	s := securityServer(t)
	ctx := context.Background()
	mid := securityID(t, s, `insert into merchant_users(email,name,password_hash) values('merchant-link@example.test','Alex Barber','unused') returning id::text`)
	m := Merchant{ID: mid, Email: "merchant-link@example.test", Name: "Alex Barber"}
	call := func(token string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "/v1/m/customer-profile", strings.NewReader("{}"))
		r.Header.Set("X-Customer-Token", token)
		r = r.WithContext(context.WithValue(r.Context(), merchantKey{}, m))
		w := httptest.NewRecorder()
		s.mCustomer(w, r)
		return w
	}
	if w := call(""); w.Code != 200 {
		t.Fatalf("create: %d %s", w.Code, w.Body.String())
	}
	if w := call(""); w.Code != 200 {
		t.Fatalf("reenter: %d", w.Code)
	}
	var n int
	s.pool.QueryRow(ctx, `select count(*) from users where email=$1`, m.Email).Scan(&n)
	if n != 1 {
		t.Fatal("duplicate profiles")
	}
	other := securityID(t, s, `insert into merchant_users(email,name,password_hash) values('existing-link@example.test','Other Barber','unused') returning id::text`)
	uid := securityID(t, s, `insert into users(email,first_name) values('existing-link@example.test','Existing') returning id::text`)
	m = Merchant{ID: other, Email: "existing-link@example.test", Name: "Other Barber"}
	if w := call(""); w.Code != 409 {
		t.Fatalf("email-only takeover: %d", w.Code)
	}
	tok, err := s.startUserSession(ctx, uid)
	if err != nil {
		t.Fatal(err)
	}
	if w := call(tok); w.Code != 200 {
		t.Fatalf("owned link: %d %s", w.Code, w.Body.String())
	}
	_, err = s.pool.Exec(ctx, `update users set deleted_at=now() where id=$1`, uid)
	if err != nil {
		t.Fatal(err)
	}
	if w := call(""); w.Code != 409 {
		t.Fatalf("deleted profile resurrected: %d", w.Code)
	}
}
