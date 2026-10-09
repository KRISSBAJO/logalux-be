package httpapi

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

func TestSecurityBookingReplayOwnership(t *testing.T) {
	s := securityServer(t)
	_, _, _, user, booking := securityFixture(t, s)
	token := "retry-account-token"
	securityExec(t, s, `insert into user_sessions(user_id,token_hash,expires_at) values($1,$2,now()+interval '1 hour')`, user, hashToken(token))
	req := createBookingReq{RequestID: "safe-request-123456789012345", BusinessSlug: "security", ClientName: "Private name"}
	r := httptest.NewRequest("POST", "/v1/bookings", nil)
	r.Header.Set("Authorization", "Bearer "+token)
	key, body := s.bookingRequestIdentity(r, req)
	securityExec(t, s, `update bookings set request_key_hash=$1,request_body_hash=$2 where id=$3`, key, body, booking)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			w := httptest.NewRecorder()
			if !s.replayBooking(w, r, key, body) || w.Code != 200 || w.Header().Get("Idempotency-Replayed") != "true" {
				t.Errorf("replay failed: %d %s", w.Code, w.Body.String())
			}
			if !strings.Contains(w.Body.String(), booking) {
				t.Error("replay returned another booking")
			}
		}()
	}
	wg.Wait()
	w := httptest.NewRecorder()
	s.replayBooking(w, r, key, "changed")
	if w.Code != 409 {
		t.Fatal("changed payload accepted")
	}
	guest := httptest.NewRequest("POST", "/v1/bookings", nil)
	foreign, _ := s.bookingRequestIdentity(guest, req)
	if key == foreign {
		t.Fatal("account request key not scoped")
	}
	if s.replayBooking(httptest.NewRecorder(), guest, foreign, body) {
		t.Fatal("guest replayed account booking")
	}
	var count int
	s.pool.QueryRow(context.Background(), `select count(*) from bookings where request_key_hash=$1`, key).Scan(&count)
	if count != 1 {
		t.Fatal("duplicate booking")
	}
	// Replays go through the normal booking handler before availability validation.
	data, _ := json.Marshal(req)
	rr := httptest.NewRequest("POST", "/v1/bookings", strings.NewReader(string(data)))
	rr.Header.Set("Authorization", "Bearer "+token)
	out := httptest.NewRecorder()
	s.createBooking(out, rr)
	if out.Code != 200 {
		t.Fatalf("booking retry: %d %s", out.Code, out.Body.String())
	}
}

func TestSecurityMobileBrowserHandoff(t *testing.T) {
	s := securityServer(t)
	_, _, _, user, _ := securityFixture(t, s)
	securityExec(t, s, `insert into user_sessions(user_id,token_hash,expires_at) values($1,$2,now()+interval '1 hour')`, user, hashToken("account-test"))
	c := Customer{ID: user}
	for _, bad := range []string{"https://evil.test", "//evil.test", "/shop/../admin", "/shop/%2e%2e/admin", "/admin", "/cart#bad"} {
		if _, ok := shopReturnPath(bad); ok {
			t.Fatalf("unsafe path accepted %s", bad)
		}
	}
	create := func() string {
		w := accountRequest(c, "POST", "/v1/auth/web-handoff", `{"path":"/cart?country=us"}`, s.authWebHandoff)
		if w.Code != 200 {
			t.Fatal(w.Body.String())
		}
		var out struct{ URL string }
		json.Unmarshal(w.Body.Bytes(), &out)
		parts := strings.Split(out.URL, "#")
		if len(parts) != 2 {
			t.Fatal("missing exchange fragment")
		}
		return parts[1]
	}
	call := func(code string, preview bool) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "/exchange", strings.NewReader(`{"code":"`+code+`"}`))
		w := httptest.NewRecorder()
		if preview {
			s.webHandoffPreview(w, r)
		} else {
			s.webHandoffExchange(w, r)
		}
		return w
	}
	code := create()
	if call(code, true).Code != 200 {
		t.Fatal("preview failed")
	}
	var wg sync.WaitGroup
	results := make(chan int, 6)
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); results <- call(code, false).Code }()
	}
	wg.Wait()
	close(results)
	success := 0
	for status := range results {
		if status == 200 {
			success++
		} else if status != 410 {
			t.Fatalf("unexpected exchange %d", status)
		}
	}
	if success != 1 {
		t.Fatalf("exchange used %d times", success)
	}
	code = create()
	securityExec(t, s, `update customer_web_handoffs set expires_at=now()-interval '1 minute' where code_hash=$1`, hashToken(code))
	if call(code, false).Code != 410 {
		t.Fatal("expired link accepted")
	}
	code = create()
	securityExec(t, s, `delete from user_sessions where token_hash=$1`, hashToken("account-test"))
	if call(code, false).Code != 410 {
		t.Fatal("revoked app session still exchanged")
	}
}

func TestSecurityCreditCurrencyIsolation(t *testing.T) {
	s := securityServer(t)
	_, _, _, user, _ := securityFixture(t, s)
	securityExec(t, s, `insert into user_credits(user_id,amount_cents,currency,reason) values($1,2500,'USD','test'),($1,900000,'NGN','test'),($1,-100000,'NGN','test')`, user)
	if creditBalanceIn(context.Background(), s.pool, user, "USD") != 2500 || creditBalanceIn(context.Background(), s.pool, user, "NGN") != 800000 {
		t.Fatal("currencies mixed or wrong balance")
	}
	w := accountRequest(Customer{ID: user}, "GET", "/auth/me?paged=1", "", s.authMe)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"credit_ngn_cents":800000`) {
		t.Fatalf("naira overview missing: %d %s", w.Code, w.Body.String())
	}
}

func TestSecurityNairaCheckoutCreditAndGift(t *testing.T) {
	s := securityServer(t)
	biz, _, _, user, _ := securityFixture(t, s)
	securityExec(t, s, `update businesses set market='NG',currency='NGN',timezone='Africa/Lagos',sales_tax_bp=0 where id=$1`, biz)
	securityExec(t, s, `insert into user_sessions(user_id,token_hash,expires_at) values($1,$2,now()+interval '1 hour')`, user, hashToken("account-test"))
	securityExec(t, s, `insert into products(business_id,seller_name,slug,name,category,price_cents,stock,shipping_cents) values($1,'Security Studio','naira-oil','Oil','hair',500000,20,0)`, biz)
	securityExec(t, s, `insert into user_credits(user_id,amount_cents,currency,reason) values($1,2500,'USD','test'),($1,800000,'NGN','test')`, user)
	securityExec(t, s, `insert into gift_cards(code,initial_cents,balance_cents,currency,recipient_name,recipient_email,issued_by) values('NGCARDTEST123',200000,200000,'NGN','QA','qa@example.test','test')`)
	body := `{"customer_name":"QA","fulfilment":"pickup","gift_code":"NGCARDTEST123","items":[{"product_slug":"naira-oil","qty":1}]}`
	c := Customer{ID: user}
	w := accountRequest(c, "POST", "/v1/orders/quote", body, s.createOrder)
	if w.Code != 200 {
		t.Fatalf("naira quote: %d %s", w.Code, w.Body.String())
	}
	var out struct {
		Quote struct {
			Currency string
			Gift     int `json:"gift_cents"`
			Credit   int `json:"credit_cents"`
			Total    int `json:"total_cents"`
		}
	}
	json.Unmarshal(w.Body.Bytes(), &out)
	if out.Quote.Currency != "NGN" || out.Quote.Gift != 200000 || out.Quote.Credit != 300000 || out.Quote.Total != 0 {
		t.Fatalf("wrong naira quote %s", w.Body.String())
	}
	if creditBalanceIn(context.Background(), s.pool, user, "NGN") != 800000 {
		t.Fatal("quote spent real credit")
	}
	w = accountRequest(c, "POST", "/v1/orders", body, s.createOrder)
	if w.Code != 201 {
		t.Fatalf("naira order: %d %s", w.Code, w.Body.String())
	}
	if creditBalanceIn(context.Background(), s.pool, user, "NGN") != 500000 || creditBalanceIn(context.Background(), s.pool, user, "USD") != 2500 {
		t.Fatal("naira checkout changed wrong balance")
	}
	var orderID string
	s.pool.QueryRow(context.Background(), `select id::text from orders where user_id=$1 order by created_at desc limit 1`, user).Scan(&orderID)
	s.unwindOrder(context.Background(), orderID)
	if creditBalanceIn(context.Background(), s.pool, user, "NGN") != 800000 || creditBalanceIn(context.Background(), s.pool, user, "USD") != 2500 {
		t.Fatal("credit reversal used wrong currency")
	}
}
