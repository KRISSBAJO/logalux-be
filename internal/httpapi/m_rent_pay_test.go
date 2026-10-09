package httpapi

import (
	"context"
	"github.com/go-chi/chi/v5"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSecurityRentPayments(t *testing.T) {
	s := securityServer(t)
	ctx := context.Background()
	biz, staff, _, _, _ := securityFixture(t, s)
	for i, provider := range []string{"stripe", "paystack"} {
		id := securityID(t, s, `insert into rent_charges(business_id,staff_id,period_start,period_end,amount_cents) values($1,$2,current_date+$3::int,current_date+$3::int,20000) returning id::text`, biz, staff, i)
		ref := "lxrent" + provider
		securityExec(t, s, `insert into payments(reference,provider,purpose,business_id,amount_cents,currency,email,description,provider_id,url) values($1,$2,'rent',$3,20000,'USD','fixture@example.test','Rent','fixture','https://example.test')`, ref, provider, biz)
		securityExec(t, s, `update rent_charges set payment_reference=$2 where id=$1`, id, ref)
		r := httptest.NewRequest("POST", "/v1/m/rent/"+id, strings.NewReader(`{"action":"paid","method":"cash"}`))
		route := chi.NewRouteContext()
		route.URLParams.Add("id", id)
		r = r.WithContext(context.WithValue(context.WithValue(ctx, merchantKey{}, Merchant{BusinessID: biz}), chi.RouteCtxKey, route))
		w := httptest.NewRecorder()
		s.mRentAction(w, r)
		if w.Code != 409 {
			t.Fatalf("pending online allows cash: %d %s", w.Code, w.Body.String())
		}
		tx, err := s.pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		atomic := *s
		atomic.pool = &paymentDatabase{database: s.pool, tx: tx}
		if err = atomic.rentPaid(ctx, ref, 20000, "USD"); err != nil {
			t.Fatal(err)
		}
		if err = tx.Rollback(ctx); err != nil {
			t.Fatal(err)
		}
		var status string
		s.pool.QueryRow(ctx, `select status from rent_charges where id=$1`, id).Scan(&status)
		if status != "due" {
			t.Fatal("rent leaked across rollback")
		}
		tx, err = s.pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		atomic.pool = &paymentDatabase{database: s.pool, tx: tx}
		if err = atomic.rentPaid(ctx, ref, 20000, "USD"); err != nil {
			t.Fatal(err)
		}
		if err = tx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
		var method string
		if err = s.pool.QueryRow(ctx, `select status,method from rent_charges where id=$1`, id).Scan(&status, &method); err != nil || status != "paid" || method != provider {
			t.Fatalf("settlement: %s %s %v", status, method, err)
		}
		var n int
		s.pool.QueryRow(ctx, `select count(*) from ledger where rent_charge_id=$1 and in_balance`, id).Scan(&n)
		if n != 1 {
			t.Fatalf("expected one rent balance entry, got %d", n)
		}
	}
}
