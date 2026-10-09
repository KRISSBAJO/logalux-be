package httpapi

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func checkoutTestCall(s *Server, m Merchant, body M, mode any) *httptest.ResponseRecorder {
	bytes, _ := json.Marshal(body)
	r := httptest.NewRequest("POST", "/v1/m/checkout", strings.NewReader(string(bytes)))
	deadline, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	r = r.WithContext(deadline)
	ctx := context.WithValue(r.Context(), merchantKey{}, m)
	if mode != nil {
		ctx = context.WithValue(ctx, mode, true)
	}
	w := httptest.NewRecorder()
	s.mCheckoutPay(w, r.WithContext(ctx))
	return w
}

func checkoutTestFixture(t *testing.T) (*Server, Merchant, string, string, M) {
	t.Helper()
	s := securityServer(t)
	biz, staff, client, _, booking := securityFixture(t, s)
	securityID(t, s, `insert into locations(business_id,name,is_primary,country,timezone) values($1,'Main',true,'US','UTC') returning id::text`, biz)
	product := securityID(t, s, `insert into products(business_id,slug,name,price_cents,stock,seller_name,category) values($1,'checkout-product','Checkout product',1000,50,'QA','hair') returning id::text`, biz)
	m := Merchant{ID: "checkout-merchant", BusinessID: biz, StaffID: staff, Role: "owner", Currency: "USD", Market: "US", Email: "checkout@example.test", Loc: time.UTC}
	body := M{"request_id": "checkout_request_123456789", "client_id": client, "staff_id": staff, "method": "cash", "tip_cents": 100, "items": []M{{"kind": "product", "product_id": product, "qty": 2}}}
	return s, m, product, booking, body
}

func checkoutCounts(t *testing.T, s *Server) (sales, requests, stock, sold, movements, ledger, points int) {
	t.Helper()
	err := s.pool.QueryRow(context.Background(), `select (select count(*) from sales), (select count(*) from checkout_requests), (select stock from products limit 1), (select sold from products limit 1), (select count(*) from stock_movements where reason='sale'), (select count(*) from ledger where kind in ('charge','tip')), (select count(*) from loyalty_points where sale_id is not null)`).Scan(&sales, &requests, &stock, &sold, &movements, &ledger, &points)
	if err != nil {
		t.Fatal(err)
	}
	return
}

func TestCheckoutConcurrentDurableReplay(t *testing.T) {
	s, m, _, _, body := checkoutTestFixture(t)
	// Retries exceed available connections: the winner must still finish.
	cfg := s.pool.(*pgxpool.Pool).Config()
	cfg.MaxConns = 2
	pool, err := pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	s.pool = pool
	const n = 12
	replies := make([]*httptest.ResponseRecorder, n)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := range replies {
		wg.Add(1)
		go func(i int) { defer wg.Done(); <-start; replies[i] = checkoutTestCall(s, m, body, nil) }(i)
	}
	close(start)
	wg.Wait()
	fresh := 0
	for _, w := range replies {
		if w.Code != 201 {
			t.Fatalf("retry: %d %s", w.Code, w.Body.String())
		}
		if w.Body.String() != replies[0].Body.String() {
			t.Fatal("responses differ")
		}
		if w.Header().Get("Idempotency-Replayed") == "" {
			fresh++
		}
	}
	if fresh != 1 {
		t.Fatalf("fresh responses=%d", fresh)
	}
	// New server instance has no request cache; replay precedes changed stock/pricing.
	restarted := &Server{pool: s.pool, cfg: s.cfg, mail: s.mail}
	securityExec(t, s, `update products set price_cents=9999`)
	w := checkoutTestCall(restarted, m, body, nil)
	if w.Code != 201 || w.Body.String() != replies[0].Body.String() || w.Header().Get("Idempotency-Replayed") != "true" {
		t.Fatalf("durable replay: %d %s", w.Code, w.Body.String())
	}
	sales, requests, stock, sold, moves, ledger, points := checkoutCounts(t, s)
	if sales != 1 || requests != 1 || stock != 48 || sold != 2 || moves != 1 || ledger != 2 || points != 1 {
		t.Fatalf("effects: %d %d %d %d %d %d %d", sales, requests, stock, sold, moves, ledger, points)
	}
}

func TestCheckoutFingerprintConflictAndRecovery(t *testing.T) {
	s, m, _, _, body := checkoutTestFixture(t)
	first := checkoutTestCall(s, m, body, nil)
	if first.Code != 201 {
		t.Fatal(first.Body.String())
	}
	for _, field := range []string{"note", "client_name", "location_id", "method", "client_id", "staff_id", "promo_code", "booking_id"} {
		changed := M{}
		for k, v := range body {
			changed[k] = v
		}
		changed[field] = "changed"
		w := checkoutTestCall(s, m, changed, nil)
		if w.Code != 409 {
			t.Fatalf("%s conflict: %d %s", field, w.Code, w.Body.String())
		}
	}
	for _, field := range []string{"tip_cents", "discount_cents", "redeem_points"} {
		changed := M{}
		for k, v := range body {
			changed[k] = v
		}
		changed[field] = 200
		if w := checkoutTestCall(s, m, changed, nil); w.Code != 409 {
			t.Fatalf("%s: %d %s", field, w.Code, w.Body.String())
		}
	}
	changed := M{}
	for k, v := range body {
		changed[k] = v
	}
	changed["items"] = []M{{"kind": "custom", "name": "Different", "qty": 1, "unit_cents": 100}}
	if w := checkoutTestCall(s, m, changed, nil); w.Code != 409 {
		t.Fatal(w.Body.String())
	}
	recovery := M{"request_id": body["request_id"], "replay_only": true}
	if w := checkoutTestCall(s, m, recovery, nil); w.Code != 201 || w.Body.String() != first.Body.String() {
		t.Fatalf("recovery: %d %s", w.Code, w.Body.String())
	}
	other := m
	other.BusinessID = securityID(t, s, `insert into businesses(slug,name,category,market,currency,timezone) values('other','Other','hair','US','USD','UTC') returning id::text`)
	if w := checkoutTestCall(s, other, recovery, nil); w.Code != 404 {
		t.Fatalf("business leak: %d %s", w.Code, w.Body.String())
	}
	other = m
	other.ID = "another-merchant"
	if w := checkoutTestCall(s, other, recovery, nil); w.Code != 409 {
		t.Fatalf("merchant leak: %d", w.Code)
	}
	other = m
	other.Role = "staff"
	other.Permissions = map[string]bool{"take_payments": false}
	if w := checkoutTestCall(s, other, recovery, nil); w.Code != 403 {
		t.Fatalf("permission bypass: %d", w.Code)
	}
	sales, requests, _, _, _, _, _ := checkoutCounts(t, s)
	if sales != 1 || requests != 1 {
		t.Fatal("conflict/recovery wrote a sale")
	}
}

func TestCheckoutAtomicRollbackAndRetry(t *testing.T) {
	s, m, _, _, body := checkoutTestFixture(t)
	// Fail the durable write after sale, inventory and ledger writes have occurred.
	securityExec(t, s, `create function reject_checkout() returns trigger language plpgsql as $$ begin raise exception 'fixture failure'; end $$`)
	securityExec(t, s, `create trigger reject_checkout before insert on checkout_requests for each row execute function reject_checkout()`)
	w := checkoutTestCall(s, m, body, nil)
	if w.Code != 503 {
		t.Fatalf("failure: %d %s", w.Code, w.Body.String())
	}
	sales, requests, stock, sold, moves, ledger, points := checkoutCounts(t, s)
	if sales != 0 || requests != 0 || stock != 50 || sold != 0 || moves != 0 || ledger != 0 || points != 0 {
		t.Fatalf("partial commit: %d %d %d %d %d %d %d", sales, requests, stock, sold, moves, ledger, points)
	}
	securityExec(t, s, `drop trigger reject_checkout on checkout_requests`)
	if w = checkoutTestCall(s, m, body, nil); w.Code != 201 {
		t.Fatalf("retry: %d %s", w.Code, w.Body.String())
	}
}

func TestCheckoutQuoteAndPaidLinkIgnoreRequestIDs(t *testing.T) {
	s, m, _, _, body := checkoutTestFixture(t)
	body["request_id"] = "not-an-id" // even invalid IDs are ignored for quotes.
	for i := 0; i < 2; i++ {
		if w := checkoutTestCall(s, m, body, dryRunKey{}); w.Code != 200 {
			t.Fatalf("quote: %d %s", w.Code, w.Body.String())
		}
	}
	sales, requests, stock, _, _, _, _ := checkoutCounts(t, s)
	if sales != 0 || requests != 0 || stock != 50 {
		t.Fatal("quote persisted effects")
	}
	body["method"] = "link"
	if w := checkoutTestCall(s, m, body, linkPaidKey{}); w.Code != 201 {
		t.Fatalf("paid link: %d %s", w.Code, w.Body.String())
	}
	sales, requests, _, _, _, _, _ = checkoutCounts(t, s)
	if sales != 1 || requests != 0 {
		t.Fatal("paid link shared desk request namespace")
	}
}

func TestCheckoutMobileBookingCompatibilityAndQuickSaleRequiresID(t *testing.T) {
	s, m, _, booking, body := checkoutTestFixture(t)
	delete(body, "request_id")
	if w := checkoutTestCall(s, m, body, nil); w.Code != 400 {
		t.Fatalf("missing quick-sale ID accepted: %d %s", w.Code, w.Body.String())
	}
	sales, requests, stock, _, _, _, _ := checkoutCounts(t, s)
	if sales != 0 || requests != 0 || stock != 50 {
		t.Fatal("missing-ID refusal wrote effects")
	}
	body["booking_id"] = booking
	if w := checkoutTestCall(s, m, body, nil); w.Code != 201 {
		t.Fatalf("mobile booking sale: %d %s", w.Code, w.Body.String())
	}
	if w := checkoutTestCall(s, m, body, nil); w.Code != 409 {
		t.Fatalf("mobile duplicate booking: %d", w.Code)
	}
	delete(body, "booking_id")
	body["request_id"] = "mobile_request_1234567890"
	first := checkoutTestCall(s, m, body, nil)
	second := checkoutTestCall(s, m, body, nil)
	if first.Code != 201 || second.Code != 201 || first.Body.String() != second.Body.String() {
		t.Fatalf("mobile quick-sale replay: %d %s / %d %s", first.Code, first.Body.String(), second.Code, second.Body.String())
	}
	sales, requests, _, _, _, _, _ = checkoutCounts(t, s)
	if sales != 2 || requests != 1 {
		t.Fatal("mobile compatibility changed")
	}
}

func TestCheckoutBookingRequestReplaysBeforePaidState(t *testing.T) {
	s, m, _, booking, body := checkoutTestFixture(t)
	body["booking_id"] = booking
	first := checkoutTestCall(s, m, body, nil)
	second := checkoutTestCall(s, m, body, nil)
	if first.Code != 201 || second.Code != 201 || first.Body.String() != second.Body.String() {
		t.Fatalf("booking replay: %d %s / %d %s", first.Code, first.Body.String(), second.Code, second.Body.String())
	}
}

func TestCheckoutSalesPaginationAndReceiptScope(t *testing.T) {
	s, m, _, _, _ := checkoutTestFixture(t)
	securityExec(t, s, `insert into sales(business_id,staff_id,client_name,subtotal_cents,total_cents,method,created_by,created_at)
		select $1,$2,case when i=65 then 'Needle client' else 'History client' end,i,i,'cash','QA',now()-make_interval(secs=>i) from generate_series(1,65) i`, m.BusinessID, m.StaffID)
	otherStaff := securityID(t, s, `insert into staff(business_id,name,initials) values($1,'Other','OT') returning id::text`, m.BusinessID)
	securityExec(t, s, `insert into sales(business_id,staff_id,client_name,subtotal_cents,total_cents,method,created_by) values($1,$2,'Other staff',100,100,'cash','QA')`, m.BusinessID, otherStaff)
	old := securityID(t, s, `insert into sales(business_id,staff_id,client_name,subtotal_cents,total_cents,method,created_by,created_at) values($1,$2,'Previous day',100,100,'cash','QA',now()-interval '2 days') returning id::text`, m.BusinessID, m.StaffID)
	foreignBiz := securityID(t, s, `insert into businesses(slug,name,category,market,currency,timezone) values('pagination-other','Other','hair','US','USD','UTC') returning id::text`)
	foreign := securityID(t, s, `insert into sales(business_id,client_name,subtotal_cents,total_cents,method,created_by) values($1,'Foreign',100,100,'cash','QA') returning id::text`, foreignBiz)
	m.Role = "staff"
	m.Permissions = map[string]bool{"see_all_calendars": false, "take_payments": true}
	get := func(query string) M {
		t.Helper()
		r := httptest.NewRequest("GET", "/v1/m/checkout?"+query, nil)
		r = r.WithContext(context.WithValue(r.Context(), merchantKey{}, m))
		w := httptest.NewRecorder()
		s.mCheckout(w, r)
		if w.Code != 200 {
			t.Fatalf("history: %d %s", w.Code, w.Body.String())
		}
		var out M
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		return out
	}
	seen := map[string]bool{}
	for _, page := range []string{"1", "2", "3"} {
		out := get("sales_per_page=25&sales_page=" + page + "&sales_sort=total_cents&sales_direction=desc")
		meta := out["sales_pagination"].(map[string]any)
		if meta["total"] != float64(65) || meta["pages"] != float64(3) {
			t.Fatalf("metadata: %v", meta)
		}
		for _, value := range out["sales"].([]any) {
			record := value.(map[string]any)
			id := record["id"].(string)
			if seen[id] {
				t.Fatal("duplicate across pages")
			}
			seen[id] = true
			if _, ok := record["refund_problem"]; !ok {
				t.Fatal("refund recovery projection lost")
			}
			if _, ok := record["provider_refund_status"]; !ok {
				t.Fatal("refund status projection lost")
			}
		}
	}
	if len(seen) != 65 {
		t.Fatalf("history truncated: %d", len(seen))
	}
	out := get("sales_q=Needle&receipt=" + old)
	if len(out["sales"].([]any)) != 1 || out["receipt"].(map[string]any)["id"] != old {
		t.Fatalf("search/old receipt: %v", out)
	}
	if out = get("receipt=" + foreign); out["receipt"] != nil {
		t.Fatal("foreign receipt leaked")
	}
	m.Role = "owner"
	if out = get(""); out["sales_pagination"].(map[string]any)["total"] != float64(66) {
		t.Fatal("owner history missing another staff member")
	}
}
