package httpapi

import (
	"context"
	"errors"
	"fmt"
	"github.com/go-chi/chi/v5"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestSecuritySaleRefundRecovery(t *testing.T) {
	s := securityServer(t)
	ctx := context.Background()
	biz, staff, _, _, _ := securityFixture(t, s)
	sale := securityID(t, s, `insert into sales(business_id,staff_id,client_name,subtotal_cents,total_cents,method,created_by) values($1,$2,'Recovery',1000,1000,'link','QA') returning id::text`, biz, staff)
	pay := securityID(t, s, `insert into payments(reference,provider,purpose,business_id,sale_id,amount_cents,currency,status,payment_intent) values('recovery','stripe','tip',$1,$2,1000,'USD','paid','pi_qa') returning id::text`, biz, sale)
	m := Merchant{BusinessID: biz, StaffID: staff, Role: "owner", Currency: "USD", Market: "US", Email: "qa@example.test", Loc: time.UTC}
	amount := 300
	requestID := ""
	refund := func() int {
		r := httptest.NewRequest("POST", "/", strings.NewReader(fmt.Sprintf(`{"amount_cents":%d,"reason":"QA","request_id":%q}`, amount, requestID)))
		rc := chi.NewRouteContext()
		rc.URLParams.Add("id", sale)
		c := context.WithValue(r.Context(), merchantKey{}, m)
		c = context.WithValue(c, chi.RouteCtxKey, rc)
		w := httptest.NewRecorder()
		s.mSaleRefund(w, r.WithContext(c))
		return w.Code
	}
	if got := refund(); got != 202 {
		t.Fatalf("reservation status %d", got)
	}
	if got := refund(); got != 409 {
		t.Fatalf("pending retry %d", got)
	}
	old := providerHTTP
	t.Cleanup(func() { providerHTTP = old })
	var calls atomic.Int32
	var key string
	providerHTTP = &http.Client{Transport: securityRoundTrip(func(r *http.Request) (*http.Response, error) {
		k := r.Header.Get("Idempotency-Key")
		if key != "" && key != k {
			t.Error("provider retry changed identity")
		}
		key = k
		if calls.Add(1) == 1 {
			return nil, errors.New("lost provider response")
		}
		return securityResponse(`{"id":"re_qa","status":"succeeded"}`), nil
	})}
	// Simulate restart after reservation, followed by a lost provider response.
	restarted := &Server{pool: s.pool, cfg: s.cfg}
	restarted.recoverSaleRefunds(ctx)
	var status string
	if err := s.pool.QueryRow(ctx, `select provider_refund_status from sales where id=$1`, sale).Scan(&status); err != nil || status != "pending" {
		t.Fatalf("pending state %s %v", status, err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); restarted.recoverSaleRefunds(ctx) }()
	}
	wg.Wait()
	var refunded, ledger int
	if err := s.pool.QueryRow(ctx, `select refunded_cents from payments where id=$1`, pay).Scan(&refunded); err != nil {
		t.Fatal(err)
	}
	if err := s.pool.QueryRow(ctx, `select count(*) from ledger where sale_id=$1 and kind='refund'`, sale).Scan(&ledger); err != nil {
		t.Fatal(err)
	}
	if refunded != 300 || ledger != 1 {
		t.Fatalf("duplicate partial refund: cents=%d ledger=%d", refunded, ledger)
	}
	key = ""
	if got := refund(); got != 202 {
		t.Fatalf("second independent refund %d", got)
	}
	restarted.recoverSaleRefunds(ctx)
	if err := s.pool.QueryRow(ctx, `select refunded_cents from payments where id=$1`, pay).Scan(&refunded); err != nil || refunded != 600 {
		t.Fatalf("second partial refund %d %v", refunded, err)
	}
	// Provider acceptance with a pending status must not be shown as completed.
	if got := refund(); got != 202 {
		t.Fatalf("third partial %d", got)
	}
	var pendingCalls atomic.Int32
	providerHTTP = &http.Client{Transport: securityRoundTrip(func(r *http.Request) (*http.Response, error) {
		if pendingCalls.Add(1) == 1 {
			return securityResponse(`{"id":"re_pending","status":"pending"}`), nil
		}
		return securityResponse(`{"id":"re_pending","status":"succeeded"}`), nil
	})}
	restarted.recoverSaleRefunds(ctx)
	if err := s.pool.QueryRow(ctx, `select refunded_cents from payments where id=$1`, pay).Scan(&refunded); err != nil || refunded != 600 {
		t.Fatalf("pending provider prematurely completed: %d %v", refunded, err)
	}
	restarted.recoverSaleRefunds(ctx)
	if err := s.pool.QueryRow(ctx, `select refunded_cents from payments where id=$1`, pay).Scan(&refunded); err != nil || refunded != 900 {
		t.Fatalf("provider confirmation not recovered: %d %v", refunded, err)
	}

	amount = 100
	requestID = "refund-retry-request-00000001"
	if got := refund(); got != 202 {
		t.Fatalf("final refund reserve %d", got)
	}
	restarted.recoverSaleRefunds(ctx)
	if got := refund(); got != 202 {
		t.Fatalf("lost-response replay %d", got)
	}
	if err := s.pool.QueryRow(ctx, `select refunded_cents from payments where id=$1`, pay).Scan(&refunded); err != nil || refunded != 1000 {
		t.Fatalf("request replay changed refund: %d %v", refunded, err)
	}
	amount = 99
	if got := refund(); got != 409 {
		t.Fatalf("changed refund request payload %d", got)
	}

}

func TestSecurityReturnRefundAtomicRecovery(t *testing.T) {
	s := securityServer(t)
	ctx := context.Background()
	biz, _, _, user, _ := securityFixture(t, s)
	order := securityID(t, s, `insert into orders(user_id,customer_name,customer_email,fulfilment,status,subtotal_cents,total_cents) values($1,'Test','test@example.test','pickup','paid',1000,1000) returning id::text`, user)
	shipment := securityID(t, s, `insert into order_shipments(order_id,seller_name,business_id,fulfilment,items_cents) values($1,'Security Studio',$2,'pickup',1000) returning id::text`, order, biz)
	ret := securityID(t, s, `insert into order_returns(order_id,shipment_id,business_id,user_id,reason) values($1,$2,$3,$4,'damaged') returning id::text`, order, shipment, biz, user)
	pay := securityID(t, s, `insert into payments(reference,provider,purpose,business_id,order_id,amount_cents,currency,status,payment_intent) values('return-recovery','stripe','order',$1,$2,1000,'USD','paid','pi_return') returning id::text`, biz, order)
	securityExec(t, s, `create function fail_refund_reservation() returns trigger language plpgsql as $$ begin raise exception 'QA failure'; end $$; create trigger fail_refund before insert on order_return_refund_jobs for each row execute function fail_refund_reservation()`)
	code, _ := s.decideReturn(ctx, ret, biz, "QA", returnDecision{Action: "approve"})
	if code != 503 {
		t.Fatalf("injected failure status %d", code)
	}
	var state string
	s.pool.QueryRow(ctx, `select status from order_returns where id=$1`, ret).Scan(&state)
	if state != "requested" {
		t.Fatal("return changed without durable recovery")
	}
	securityExec(t, s, `drop trigger fail_refund on order_return_refund_jobs`)
	code, out := s.decideReturn(ctx, ret, biz, "QA", returnDecision{Action: "approve"})
	if code != 202 || out["provider_refund_status"] != "pending" {
		t.Fatalf("pending approval %d %v", code, out)
	}
	if code, _ = s.decideReturn(ctx, ret, biz, "QA", returnDecision{Action: "approve"}); code != 409 {
		t.Fatal("duplicate return approved")
	}
	old := providerHTTP
	t.Cleanup(func() { providerHTTP = old })
	var calls atomic.Int32
	providerHTTP = &http.Client{Transport: securityRoundTrip(func(r *http.Request) (*http.Response, error) {
		calls.Add(1)
		return securityResponse(`{"id":"re_return","status":"succeeded"}`), nil
	})}
	for i := 0; i < 3; i++ {
		s.recoverReturnRefunds(ctx)
	}
	var refunded, count int
	s.pool.QueryRow(ctx, `select refunded_cents from payments where id=$1`, pay).Scan(&refunded)
	s.pool.QueryRow(ctx, `select count(*) from ledger where order_id=$1 and kind='refund'`, order).Scan(&count)
	if refunded != 1000 || count != 1 || calls.Load() != 1 {
		t.Fatalf("duplicate return recovery %d %d %d", refunded, count, calls.Load())
	}
}
