package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"logaluxe/api/internal/config"
	"logaluxe/api/internal/db"
	"logaluxe/api/internal/mail"
)

type securityRoundTrip func(*http.Request) (*http.Response, error)

func (f securityRoundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func securityResponse(body string) *http.Response {
	return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}
}

// These tests create a separate disposable database. They never insert into or clean the seeded database.
func securityServer(t *testing.T) *Server {
	t.Helper()
	raw := os.Getenv("SECURITY_TEST_DATABASE_URL")
	if raw == "" {
		t.Skip("set SECURITY_TEST_DATABASE_URL to run isolated database regressions")
	}
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, raw)
	if err != nil {
		t.Fatal(err)
	}
	name := fmt.Sprintf("logaluxe_security_%d", time.Now().UnixNano())
	if _, err = admin.Exec(ctx, "create database "+name); err != nil {
		admin.Close()
		t.Fatal(err)
	}
	cfg, err := pgxpool.ParseConfig(raw)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.Database = name
	cfg.MaxConns = 20
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		pool.Close()
		_, err := admin.Exec(ctx, "drop database "+name+" with (force)")
		admin.Close()
		if err != nil {
			t.Error(err)
		}
	})
	if err = db.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	return &Server{pool: pool, cfg: config.Config{StripeSecret: "sk_test_fixture", PaystackSecret: "sk_test_fixture"}, mail: mail.New(mail.Config{Provider: "log"})}
}
func securityID(t *testing.T, s *Server, q string, args ...any) string {
	t.Helper()
	var id string
	if err := s.pool.QueryRow(context.Background(), q, args...).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}
func securityExec(t *testing.T, s *Server, q string, args ...any) {
	t.Helper()
	if _, err := s.pool.Exec(context.Background(), q, args...); err != nil {
		t.Fatal(err)
	}
}
func securityFixture(t *testing.T, s *Server) (biz, staff, client, user, booking string) {
	biz = securityID(t, s, `insert into businesses(slug,name,category,market,currency,timezone,settings) values ('security','Security Studio','hair','US','USD','America/Chicago','{"loyalty":{"enabled":true}}') returning id::text`)
	staff = securityID(t, s, `insert into staff(business_id,name,initials) values($1,'Test stylist','TS') returning id::text`, biz)
	client = securityID(t, s, `insert into clients(business_id,name,phone) values($1,'Private client','+15550001111') returning id::text`, biz)
	user = securityID(t, s, `insert into users(email,phone) values('security@example.test','+15550001111') returning id::text`)
	booking = securityID(t, s, `insert into bookings(business_id,staff_id,client_id,user_id,client_name,notes,starts_at,ends_at,deposit_cents) values($1,$2,$3,$4,'Private name','Private notes',now()+interval '10 days',now()+interval '10 days 1 hour',100) returning id::text`, biz, staff, client, user)
	return
}

func TestSecurityWalletAndPublicReceipts(t *testing.T) {
	s := securityServer(t)
	biz, _, client, user, booking := securityFixture(t, s)
	securityExec(t, s, `insert into loyalty_points(business_id,client_id,points,reason) values($1,$2,300,'adjust')`, biz, client)
	for _, verified := range []bool{false, true} {
		r := httptest.NewRequest("GET", "/v1/auth/wallet", nil)
		r = r.WithContext(context.WithValue(r.Context(), userKey{}, Customer{ID: user, Phone: "+15550001111", PhoneVerified: verified}))
		w := httptest.NewRecorder()
		s.authWallet(w, r)
		var out struct {
			Wallet []M `json:"wallet"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		if (!verified && len(out.Wallet) != 0) || (verified && len(out.Wallet) != 1) {
			t.Fatalf("wallet access verified=%v: %s", verified, w.Body.String())
		}
	}
	token := "security-fixture-session"
	securityExec(t, s, `insert into user_sessions(user_id,token_hash,expires_at) values($1,$2,now()+interval '1 hour')`, user, hashToken(token))
	for _, owner := range []bool{false, true} {
		r := httptest.NewRequest("GET", "/v1/bookings/"+booking, nil)
		if owner {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		w := httptest.NewRecorder()
		s.getBookingByID(w, r, booking, 200)
		var out struct {
			Booking M `json:"booking"`
		}
		_ = json.Unmarshal(w.Body.Bytes(), &out)
		if w.Code != 200 {
			t.Fatal(w.Body.String())
		}
		_, has := out.Booking["notes"]
		if has != owner {
			t.Fatalf("booking privacy owner=%v: %s", owner, w.Body.String())
		}
	}
	order := securityID(t, s, `insert into orders(user_id,customer_name,customer_email,customer_phone,address,fulfilment,subtotal_cents,total_cents) values($1,'Private buyer','private@example.test','+15550001111','{"street":"Private address"}','ship',100,100) returning id::text`, user)
	for _, owner := range []bool{false, true} {
		r := httptest.NewRequest("GET", "/v1/orders/"+order, nil)
		if owner {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		w := httptest.NewRecorder()
		s.getOrderByID(w, r, order, 200)
		var out struct {
			Order M `json:"order"`
		}
		_ = json.Unmarshal(w.Body.Bytes(), &out)
		if w.Code != 200 {
			t.Fatal(w.Body.String())
		}
		_, has := out.Order["address"]
		if has != owner {
			t.Fatalf("order privacy owner=%v: %s", owner, w.Body.String())
		}
	}
}

func TestSecuritySettlementRollbackAndConcurrency(t *testing.T) {
	s := securityServer(t)
	biz, _, _, _, booking := securityFixture(t, s)
	ctx := context.Background()
	securityExec(t, s, `insert into payments(reference,provider,purpose,business_id,booking_id,amount_cents,currency,email,provider_id) values('security-payment','stripe','deposit',$1,$2,100,'USD','security@example.test','cs_fixture')`, biz, booking)
	old := providerHTTP
	t.Cleanup(func() { providerHTTP = old })
	providerHTTP = &http.Client{Transport: securityRoundTrip(func(r *http.Request) (*http.Response, error) {
		return securityResponse(`{"payment_status":"paid","amount_total":100,"payment_intent":"pi_fixture"}`), nil
	})}
	securityExec(t, s, `create function security_fail_ledger() returns trigger language plpgsql as $$ begin raise exception 'injected storage failure'; end $$; create trigger security_fail before insert on ledger for each row execute function security_fail_ledger()`)
	s.settlePayment(ctx, "security-payment")
	var status string
	var paid bool
	if err := s.pool.QueryRow(ctx, `select p.status,b.deposit_paid from payments p join bookings b on b.id=p.booking_id where p.reference='security-payment'`).Scan(&status, &paid); err != nil {
		t.Fatal(err)
	}
	if status != "pending" || paid {
		t.Fatalf("partial settlement escaped rollback: %s %v", status, paid)
	}
	securityExec(t, s, `drop trigger security_fail on ledger`)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); s.settlePayment(ctx, "security-payment") }()
	}
	wg.Wait()
	var deposits, events int
	if err := s.pool.QueryRow(ctx, `select p.status,b.deposit_paid,(select count(*) from ledger where booking_id=b.id and kind='deposit'),(select count(*) from payment_events where reference=p.reference) from payments p join bookings b on b.id=p.booking_id where p.reference='security-payment'`).Scan(&status, &paid, &deposits, &events); err != nil {
		t.Fatal(err)
	}
	if status != "paid" || !paid || deposits != 1 || events != 1 {
		t.Fatalf("settlement duplicated or missing: %s %v %d %d", status, paid, deposits, events)
	}
}

func TestSecurityRefundRetries(t *testing.T) {
	s := securityServer(t)
	ctx := context.Background()
	old := providerHTTP
	t.Cleanup(func() { providerHTTP = old })
	for _, provider := range []string{"stripe", "paystack"} {
		t.Run(provider, func(t *testing.T) {
			id := securityID(t, s, `insert into payments(reference,provider,purpose,amount_cents,currency,status,payment_intent) values($1,$2,'tip',100,'USD','paid','123') returning id::text`, "refund-"+provider, provider)
			var posts atomic.Int32
			var key, note string
			providerHTTP = &http.Client{Transport: securityRoundTrip(func(r *http.Request) (*http.Response, error) {
				if r.Method == "POST" {
					n := posts.Add(1)
					if provider == "stripe" {
						k := r.Header.Get("Idempotency-Key")
						if k == "" {
							t.Error("missing idempotency key")
						}
						if key != "" && key != k {
							t.Error("retry changed idempotency key")
						}
						key = k
					} else {
						var body M
						_ = json.NewDecoder(r.Body).Decode(&body)
						note = fmt.Sprint(body["merchant_note"])
					}
					if n == 1 {
						return nil, errors.New("response lost after provider accepted refund")
					}
					return securityResponse(`{"id":"re_fixture","status":"succeeded"}`), nil
				}
				return securityResponse(fmt.Sprintf(`{"data":[{"id":123,"amount":100,"merchant_note":%q,"status":"processed"}]}`, note)), nil
			})}
			if err := s.refundPayment(ctx, id, 0); err == nil {
				t.Fatal("ambiguous first response should not report success")
			}
			if err := s.refundPayment(ctx, id, 0); err != nil {
				t.Fatal(err)
			}
			var wg sync.WaitGroup
			for i := 0; i < 4; i++ {
				wg.Add(1)
				go func() {
					defer wg.Done()
					if err := s.refundPayment(ctx, id, 0); err != nil {
						t.Error(err)
					}
				}()
			}
			wg.Wait()
			var refunded int
			if err := s.pool.QueryRow(ctx, `select refunded_cents from payments where id=$1`, id).Scan(&refunded); err != nil {
				t.Fatal(err)
			}
			want := int32(2)
			if provider == "paystack" {
				want = 1
			}
			if refunded != 100 || posts.Load() != want {
				t.Fatalf("duplicate refund: cents=%d requests=%d", refunded, posts.Load())
			}
		})
	}
}

func TestSecurityPaginationBeyond200(t *testing.T) {
	s := securityServer(t)
	securityFixture(t, s)
	securityExec(t, s, `insert into audit_log(actor,action,target) select 'security','test',lpad(n::text,3,'0') from generate_series(1,245) n`)
	req := httptest.NewRequest("GET", "/v1/admin/audit?page=10&per_page=25&sort=target&direction=asc", nil)
	out, meta, err := s.adminRows(req, `select id,actor,action,target,created_at from audit_log order by created_at desc,id desc limit 200`)
	if err != nil {
		t.Fatal(err)
	}
	if toInt(meta["total"]) != 245 || len(out) != 20 || out[0]["target"] != "226" {
		t.Fatalf("older records unreachable: %#v %#v", meta, out)
	}
	req = httptest.NewRequest("GET", "/v1/admin/audit?sort=target%3Bdrop+table+users", nil)
	if _, _, err = s.adminRows(req, `select id,actor,action,target,created_at from audit_log order by created_at desc,id desc limit 200`); err != nil {
		t.Fatal(err)
	}
	for _, check := range []struct {
		path, sort string
		handler    http.HandlerFunc
	}{
		{"bookings", "client_name", s.adminBookings}, {"clients", "spent_cents", s.adminClients}, {"orders", "total_cents", s.adminOrders},
		{"payouts", "amount_cents", s.adminPayouts}, {"audit", "actor", s.adminAudit}, {"support", "priority", s.adminSupport},
	} {
		w := httptest.NewRecorder()
		check.handler(w, httptest.NewRequest("GET", "/v1/admin/"+check.path+"?per_page=1&sort="+check.sort, nil))
		if w.Code != 200 {
			t.Fatalf("%s list failed: %s", check.path, w.Body.String())
		}
		var body M
		_ = json.Unmarshal(w.Body.Bytes(), &body)
		if body["pagination"] == nil {
			t.Fatalf("%s omitted server pagination", check.path)
		}
	}
}

func TestSecurityOrderAndGiftSettlementFailure(t *testing.T) {
	s := securityServer(t)
	ctx := context.Background()
	biz, _, _, _, _ := securityFixture(t, s)
	old := providerHTTP
	t.Cleanup(func() { providerHTTP = old })
	providerHTTP = &http.Client{Transport: securityRoundTrip(func(r *http.Request) (*http.Response, error) {
		return securityResponse(`{"payment_status":"paid","amount_total":100,"payment_intent":"pi_fixture"}`), nil
	})}
	order := securityID(t, s, `insert into orders(customer_name,customer_email,fulfilment,status,subtotal_cents,total_cents) values('Test','test@example.test','pickup','pending',100,100) returning id::text`)
	securityExec(t, s, `insert into order_shipments(order_id,seller_name,business_id,fulfilment,items_cents) values($1,'Security Studio',$2,'pickup',100)`, order, biz)
	securityExec(t, s, `insert into payments(reference,provider,purpose,order_id,amount_cents,currency) values('security-order','stripe','order',$1,100,'USD')`, order)
	securityExec(t, s, `create function security_fail_order_ledger() returns trigger language plpgsql as $$ begin raise exception 'injected order ledger failure'; end $$; create trigger security_order_fail before insert on ledger for each row execute function security_fail_order_ledger()`)
	s.settlePayment(ctx, "security-order")
	var paymentStatus, orderStatus string
	if err := s.pool.QueryRow(ctx, `select p.status,o.status from payments p join orders o on o.id=p.order_id where p.reference='security-order'`).Scan(&paymentStatus, &orderStatus); err != nil {
		t.Fatal(err)
	}
	if paymentStatus != "pending" || orderStatus != "pending" {
		t.Fatalf("order partially settled: %s %s", paymentStatus, orderStatus)
	}
	securityExec(t, s, `drop trigger security_order_fail on ledger`)
	s.settlePayment(ctx, "security-order")
	s.settlePayment(ctx, "security-order")
	var count int
	if err := s.pool.QueryRow(ctx, `select count(*) from ledger where order_id=$1 and kind='charge'`, order).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("order charged %d times", count)
	}
	securityExec(t, s, `insert into payments(reference,provider,purpose,payload,amount_cents,currency) values('security-gift','stripe','gift','[]',100,'USD')`)
	s.settlePayment(ctx, "security-gift")
	if err := s.pool.QueryRow(ctx, `select status from payments where reference='security-gift'`).Scan(&paymentStatus); err != nil {
		t.Fatal(err)
	}
	if paymentStatus != "pending" {
		t.Fatal("invalid gift marked paid without issuing a gift card")
	}
}

func TestSecurityCalendarRejectsPrivateConnections(t *testing.T) {
	for _, raw := range []string{"127.0.0.1", "10.0.0.1", "169.254.169.254", "192.168.0.1", "172.16.0.1", "100.64.0.1", "::1", "fd00::1", "::ffff:127.0.0.1"} {
		if publicCalendarIP(net.ParseIP(raw)) {
			t.Errorf("accepted private address %s", raw)
		}
	}
	if !publicCalendarIP(net.ParseIP("8.8.8.8")) {
		t.Error("rejected public IP")
	}
	tr := calendarTransport()
	defer tr.CloseIdleConnections()
	if tr.Proxy != nil {
		t.Error("calendar transport must not bypass checks through a proxy")
	}
	c, err := tr.DialContext(context.Background(), "tcp", "127.0.0.1:443")
	if c != nil {
		c.Close()
	}
	if err == nil {
		t.Fatal("connected to a private host")
	}
}

func TestSecurityCalendarPinsResolvedIP(t *testing.T) {
	calls := 0
	dialled := ""
	lookup := func(context.Context, string) ([]net.IPAddr, error) {
		calls++
		return []net.IPAddr{{IP: net.ParseIP("8.8.8.8")}}, nil
	}
	dial := func(_ context.Context, _, address string) (net.Conn, error) {
		dialled = address
		return nil, errors.New("fixture: no real connection")
	}
	_, _ = dialCalendar(context.Background(), "tcp", "calendar.example:443", lookup, dial)
	if calls != 1 || dialled != "8.8.8.8:443" {
		t.Fatalf("hostname was resolved again instead of pinned: calls=%d address=%s", calls, dialled)
	}
	dialled = ""
	lookup = func(context.Context, string) ([]net.IPAddr, error) {
		return []net.IPAddr{{IP: net.ParseIP("8.8.8.8")}, {IP: net.ParseIP("127.0.0.1")}}, nil
	}
	_, err := dialCalendar(context.Background(), "tcp", "calendar.example:443", lookup, dial)
	if err == nil || dialled != "" {
		t.Fatal("mixed public/private DNS answer reached the dialer")
	}
}

func TestSecurityClientLinkRequiresVerifiedMatchingNumber(t *testing.T) {
	for _, c := range []Customer{{ID: "a", Phone: "+15550001111"}, {ID: "a", Phone: "+15550002222", PhoneVerified: true}, {Phone: "+15550001111", PhoneVerified: true}} {
		if canLinkClient(c, "+15550001111") {
			t.Fatal("unverified or mismatched account claimed a client")
		}
	}
	if !canLinkClient(Customer{ID: "a", Phone: "+15550001111", PhoneVerified: true}, "+15550001111") {
		t.Fatal("verified owner was refused")
	}
}
