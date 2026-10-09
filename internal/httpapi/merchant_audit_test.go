package httpapi

import (
	"context"
	"encoding/json"
	"github.com/go-chi/chi/v5"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestSecurityMerchantRefundStockAndRange(t *testing.T) {
	s := securityServer(t)
	biz, staff, _, _, _ := securityFixture(t, s)
	loc := securityID(t, s, `insert into locations(business_id,name,is_primary,country,timezone) values($1,'Main',true,'US','UTC') returning id::text`, biz)
	other := securityID(t, s, `insert into locations(business_id,name,country,timezone) values($1,'Other branch','US','UTC') returning id::text`, biz)
	product := securityID(t, s, `insert into products(business_id,slug,name,price_cents,stock,seller_name,category) values($1,'audit-product','Audit product',1000,5,'Audit','hair') returning id::text`, biz)
	sale := securityID(t, s, `insert into sales(business_id,staff_id,client_name,subtotal_cents,total_cents,method,location_id,created_by) values($1,$2,'Audit client',1000,1000,'cash',$3,'QA') returning id::text`, biz, staff, other)
	securityExec(t, s, `insert into sale_items(sale_id,kind,product_id,name,qty,unit_cents) values($1,'product',$2,'Audit product',1,1000)`, sale, product)
	m := Merchant{BusinessID: biz, StaffID: staff, Role: "owner", Currency: "USD", Market: "US", Email: "audit@example.test", Loc: time.UTC}
	// A restricted team member must not see another person's queue or sales.
	otherStaff := securityID(t, s, `insert into staff(business_id,name,initials) values($1,'Other stylist','OS') returning id::text`, biz)
	foreignBooking := securityID(t, s, `insert into bookings(business_id,staff_id,client_name,starts_at,ends_at,status) values($1,$2,'Other private client',now(),now()+interval '1 hour','checked_in') returning id::text`, biz, otherStaff)
	limited := m
	limited.Role = "staff"
	limited.Permissions = map[string]bool{"see_all_calendars": false}
	rq := httptest.NewRequest("GET", "/checkout", nil)
	rq = rq.WithContext(context.WithValue(rq.Context(), merchantKey{}, limited))
	qw := httptest.NewRecorder()
	s.mCheckout(qw, rq)
	if qw.Code != 200 || strings.Contains(qw.Body.String(), foreignBooking) {
		t.Fatalf("restricted checkout leaked another calendar: %d %s", qw.Code, qw.Body.String())
	}
	refund := func(amount int, restock bool) int {
		body, _ := json.Marshal(M{"amount_cents": amount, "reason": "QA refund", "restock": restock})
		r := httptest.NewRequest("POST", "/", strings.NewReader(string(body)))
		rc := chi.NewRouteContext()
		rc.URLParams.Add("id", sale)
		ctx := context.WithValue(r.Context(), merchantKey{}, m)
		ctx = context.WithValue(ctx, chi.RouteCtxKey, rc)
		w := httptest.NewRecorder()
		s.mSaleRefund(w, r.WithContext(ctx))
		return w.Code
	}
	if code := refund(500, true); code != 400 {
		t.Fatalf("partial stock return accepted: %d", code)
	}
	if code := refund(1000, true); code != 200 {
		t.Fatalf("full return failed: %d", code)
	}
	if code := refund(1000, true); code == 200 {
		t.Fatal("duplicate refund accepted")
	}
	var mainQty, otherQty, total int
	if err := s.pool.QueryRow(context.Background(), `select (select qty from location_stock where product_id=$1 and location_id=$2),(select qty from location_stock where product_id=$1 and location_id=$3),stock from products where id=$1`, product, loc, other).Scan(&mainQty, &otherQty, &total); err != nil {
		t.Fatal(err)
	}
	if mainQty != 5 || otherQty != 1 || total != 6 {
		t.Fatalf("returned to wrong shelf or twice: main=%d other=%d total=%d", mainQty, otherQty, total)
	}
	for _, period := range []string{"7d", "30d", "90d", "month"} {
		r := httptest.NewRequest("GET", "/payroll?range="+period, nil)
		r = r.WithContext(context.WithValue(r.Context(), merchantKey{}, m))
		w := httptest.NewRecorder()
		s.mPayroll(w, r)
		var out struct {
			From string `json:"from"`
			To   string `json:"to"`
		}
		json.Unmarshal(w.Body.Bytes(), &out)
		from, to, _, _ := reportRange(period, time.UTC)
		if w.Code != 200 || out.From != from.Format("2006-01-02") || out.To != to.Add(-time.Minute).Format("2006-01-02") {
			t.Fatalf("payroll/report mismatch %s: %s", period, w.Body.String())
		}
	}
}
