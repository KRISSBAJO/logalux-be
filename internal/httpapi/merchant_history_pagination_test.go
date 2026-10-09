package httpapi

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"github.com/go-chi/chi/v5"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func historyRequest(s *Server, biz, target string, handler http.HandlerFunc, params map[string]string) *httptest.ResponseRecorder {
	r := httptest.NewRequest("GET", target, nil)
	r = r.WithContext(context.WithValue(r.Context(), merchantKey{}, Merchant{BusinessID: biz, Loc: time.UTC, Timezone: "UTC", Currency: "USD", Market: "US", Slug: "security"}))
	rc := chi.NewRouteContext()
	for key, value := range params {
		rc.URLParams.Add(key, value)
	}
	r = r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, rc))
	w := httptest.NewRecorder()
	handler(w, r)
	return w
}

func TestSecurityHistoryPagination(t *testing.T) {
	s := securityServer(t)
	biz, staff, client, _, _ := securityFixture(t, s)
	foreign := securityID(t, s, `insert into businesses(slug,name,category,market,currency,timezone) values('foreign-history','Foreign','hair','US','USD','UTC') returning id::text`)
	product := securityID(t, s, `insert into products(business_id,seller_name,slug,name,category,price_cents) values($1,'Test','history-product','History product','hair',100) returning id::text`, biz)
	thread := securityID(t, s, `insert into threads(business_id,client_id,client_name) values($1,$2,'Needle thread') returning id::text`, biz, client)
	securityExec(t, s, `insert into ledger(business_id,kind,amount_cents,currency,status,description) select $1,'charge',n,'USD','settled','Needle ledger' from generate_series(1,321) n`, biz)
	securityExec(t, s, `insert into ledger(business_id,kind,amount_cents,currency,status,description) values($1,'charge',999,'USD','settled','Foreign secret')`, foreign)
	securityExec(t, s, `insert into payouts(business_id,amount_cents,currency,provider) select $1,n,'USD','stripe' from generate_series(1,13) n`, biz)
	securityExec(t, s, `insert into purchase_orders(business_id) select $1 from generate_series(1,17)`, biz)
	securityExec(t, s, `insert into stock_movements(business_id,product_id,delta,reason,note) select $1,$2,n,'adjust','Needle movement' from generate_series(1,45) n`, biz, product)
	securityExec(t, s, `insert into threads(business_id,client_name) select $1,'Needle thread' from generate_series(1,104)`, biz)
	securityExec(t, s, `insert into threads(business_id,client_name) values($1,'Foreign secret')`, foreign)
	securityExec(t, s, `insert into thread_messages(thread_id,from_business,author,body) select $1,false,'Client','Needle message '||n from generate_series(1,105) n`, thread)
	securityExec(t, s, `insert into bookings(business_id,staff_id,client_id,client_name,starts_at,ends_at) select $1,$2,$3,'Private',now()-n*interval '1 day',now()-n*interval '1 day'+interval '1 hour' from generate_series(1,14) n`, biz, staff, client)
	cases := []struct {
		key, path string
		handler   http.HandlerFunc
		params    map[string]string
		total     int
	}{
		{"transactions", "/money?from=" + time.Now().UTC().Format("2006-01-02"), s.mMoney, nil, 321},
		{"payouts", "/money", s.mMoney, nil, 13},
		{"orders", "/inventory", s.mInventory, nil, 17},
		{"history", "/products/" + product + "/history", s.mProductHistory, map[string]string{"id": product}, 45},
		{"threads", "/inbox", s.mInbox, nil, 105},
		{"messages", "/inbox/" + thread, s.mThread, map[string]string{"id": thread}, 105},
		{"visits", "/clients/" + client, s.mClient, map[string]string{"id": client}, 15},
	}
	for _, tc := range cases {
		t.Run(tc.key, func(t *testing.T) {
			seen := map[string]bool{}
			for page := 1; ; page++ {
				sep := "?"
				if strings.Contains(tc.path, "?") {
					sep = "&"
				}
				target := tc.path + sep + tc.key + "_per_page=7&" + tc.key + "_page=" + fmt.Sprint(page)
				w := historyRequest(s, biz, target, tc.handler, tc.params)
				if w.Code != 200 {
					t.Fatalf("%d %s", w.Code, w.Body.String())
				}
				var body map[string]json.RawMessage
				if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
					t.Fatal(err)
				}
				var meta struct{ Total, Page, Pages int }
				json.Unmarshal(body[tc.key+"_pagination"], &meta)
				if meta.Total != tc.total || meta.Page != page {
					t.Fatalf("metadata %+v expected total %d page %d", meta, tc.total, page)
				}
				var records []M
				json.Unmarshal(body[tc.key], &records)
				for _, record := range records {
					id := fmt.Sprint(record["id"])
					if seen[id] {
						t.Fatalf("duplicate %s", id)
					}
					seen[id] = true
				}
				if page == meta.Pages {
					break
				}
			}
			if len(seen) != tc.total {
				t.Fatalf("reached %d of %d", len(seen), tc.total)
			}
			sep := "?"
			if strings.Contains(tc.path, "?") {
				sep = "&"
			}
			for _, query := range []string{tc.key + "_q=Foreign", tc.key + "_q=" + url.QueryEscape("' OR true --")} {
				w := historyRequest(s, biz, tc.path+sep+query, tc.handler, tc.params)
				if w.Code != 200 {
					t.Fatal(w.Body.String())
				}
				var body M
				json.Unmarshal(w.Body.Bytes(), &body)
				if body[tc.key+"_pagination"].(map[string]any)["total"] != float64(0) {
					t.Fatal("search leaked or interpolated", w.Body.String())
				}
			}
		})
	}
	// Whitelist rejects executable sort text, and valid sort operates over the full dataset.
	for _, sort := range []string{"amount_cents", "amount_cents; drop table ledger"} {
		w := historyRequest(s, biz, "/money?transactions_sort="+url.QueryEscape(sort)+"&transactions_direction=asc&transactions_per_page=7", s.mMoney, nil)
		if w.Code != 200 {
			t.Fatal(w.Body.String())
		}
		if sort == "amount_cents" {
			var body struct {
				Transactions []struct {
					Amount int `json:"amount_cents"`
				}
			}
			json.Unmarshal(w.Body.Bytes(), &body)
			if body.Transactions[0].Amount != 1 {
				t.Fatal("sorting applied after paging")
			}
		}
	}
	// Ownership is checked before message and visit histories are queried.
	for _, tc := range []struct {
		handler http.HandlerFunc
		id      string
	}{{s.mThread, thread}, {s.mClient, client}} {
		w := historyRequest(s, foreign, "/private", tc.handler, map[string]string{"id": tc.id})
		if w.Code != 404 {
			t.Fatalf("foreign detail: %d", w.Code)
		}
	}
	// Months beyond the former 36-month cap remain reachable.
	securityExec(t, s, `insert into ledger(business_id,kind,amount_cents,currency,status,created_at) select $1,'charge',1,'USD','settled',date_trunc('month',now())-n*interval '1 month' from generate_series(1,40) n`, biz)
	w := historyRequest(s, biz, "/statements?statements_page=5&statements_per_page=10", s.mStatements, nil)
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	var body M
	json.Unmarshal(w.Body.Bytes(), &body)
	if body["statements_pagination"].(map[string]any)["total"] != float64(41) {
		t.Fatal(w.Body.String())
	}
}

func TestSecurityHistoryExports(t *testing.T) {
	s := securityServer(t)
	biz, _, _, _, _ := securityFixture(t, s)
	securityExec(t, s, `insert into ledger(business_id,kind,amount_cents,currency,status,description) select $1,'charge',1,'USD','settled','Export row' from generate_series(1,50001)`, biz)
	securityExec(t, s, `insert into clients(business_id,name,phone) select $1,'Export client '||n,'export-'||n from generate_series(1,20001) n`, biz)
	securityExec(t, s, `insert into sales(business_id,subtotal_cents,total_cents,method,created_by) select $1,1,1,'cash','history-test' from generate_series(1,50001)`, biz)
	for _, tc := range []struct {
		handler http.HandlerFunc
		path    string
		params  map[string]string
		want    int
	}{
		{s.mMoneyExport, "/money/export", nil, 50002},
		{s.mStatement, "/statements/month?format=csv", map[string]string{"month": time.Now().UTC().Format("2006-01")}, 50002},
		{s.mClientExport, "/clients/export", nil, 20003},
		{s.mReportsExport, "/reports/export", nil, 50002},
	} {
		w := historyRequest(s, biz, tc.path, tc.handler, tc.params)
		if w.Code != 200 {
			t.Fatal(w.Body.String())
		}
		records, err := csv.NewReader(strings.NewReader(strings.TrimPrefix(w.Body.String(), "\uFEFF"))).ReadAll()
		if err != nil {
			t.Fatal(err)
		}
		if len(records) != tc.want {
			t.Fatalf("%s exported %d expected %d", tc.path, len(records), tc.want)
		}
	}
	// JSON statement lines are bounded, but totals cover the entire month.
	w := historyRequest(s, biz, "/statement?lines_page=1001&lines_per_page=50", s.mStatement, map[string]string{"month": time.Now().UTC().Format("2006-01")})
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	var body M
	json.Unmarshal(w.Body.Bytes(), &body)
	if body["lines_pagination"].(map[string]any)["total"] != float64(50001) || len(body["lines"].([]any)) != 1 {
		t.Fatal("statement tail missing")
	}
}

func TestSecurityHistoryExtended(t *testing.T) {
	s := securityServer(t)
	biz, staff, client, _, _ := securityFixture(t, s)
	foreign := securityID(t, s, `insert into businesses(slug,name,category,market,currency,timezone) values('history-foreign','Foreign','hair','US','USD','UTC') returning id::text`)
	foreignClient := securityID(t, s, `insert into clients(business_id,name,phone) values($1,'Foreign secret','foreign') returning id::text`, foreign)
	securityExec(t, s, `insert into clients(business_id,name,phone) select $1,'Loyalty client '||n,'loyalty-'||n from generate_series(1,301) n`, biz)
	securityExec(t, s, `insert into loyalty_points(business_id,client_id,points,reason,note) select business_id,id,1,'adjust',name from clients where business_id=$1 and phone like 'loyalty-%'`, biz)
	securityExec(t, s, `insert into loyalty_points(business_id,client_id,points,reason,note) select $1,$2,1,'adjust','Needle movement' from generate_series(1,101)`, biz, client)
	securityExec(t, s, `insert into loyalty_points(business_id,client_id,points,reason,note) values($1,$2,1,'adjust','Foreign secret')`, foreign, foreignClient)
	securityExec(t, s, `insert into leads(business_id,client_name,source,base_pct) select $1,'Needle lead '||n,'search',10 from generate_series(1,501) n`, biz)
	securityExec(t, s, `insert into leads(business_id,client_name,source,base_pct) values($1,'Foreign secret','search',10)`, foreign)
	securityExec(t, s, `insert into reviews(business_id,author_name,rating,body) select $1,'Needle author '||n,5,'Needle review' from generate_series(1,41) n`, biz)
	securityExec(t, s, `insert into reviews(business_id,author_name,rating,body) values($1,'Foreign secret',5,'Foreign secret')`, foreign)
	securityExec(t, s, `insert into rent_charges(business_id,staff_id,period_start,period_end,amount_cents) select $1,$2,current_date-n,current_date-n,1 from generate_series(1,61) n`, biz, staff)
	securityExec(t, s, `insert into client_plans(business_id,client_id,kind,name) select $1,$2,'package','Needle plan '||n from generate_series(1,201) n`, biz, client)
	securityExec(t, s, `insert into client_plans(business_id,client_id,kind,name) values($1,$2,'package','Foreign secret')`, foreign, foreignClient)
	securityExec(t, s, `insert into payments(business_id,reference,provider,purpose,amount_cents,currency,description) select $1,'history-'||n,'stripe','sale',n,'USD','Needle payment' from generate_series(1,301) n`, biz)
	securityExec(t, s, `insert into payments(business_id,reference,provider,purpose,amount_cents,currency,description) values($1,'foreign-history','stripe','sale',1,'USD','Foreign secret')`, foreign)
	cases := []struct {
		key, field, path string
		handler          http.HandlerFunc
		params           map[string]string
		total            int
	}{
		{"loyalty_clients", "clients", "/loyalty", s.mLoyalty, nil, 302},
		{"recent", "recent", "/loyalty", s.mLoyalty, nil, 402},
		{"leads", "leads", "/leads", s.mLeads, nil, 501},
		{"reviews", "reviews", "/storefront", s.mStorefront, nil, 41},
		{"rent", "rent", "/staff", s.mStaff, nil, 61},
		{"holders", "holders", "/menu", s.mMenu, nil, 201},
		{"plans", "plans", "/clients/plans", s.mClientPlans, map[string]string{"id": client}, 201},
		{"payments", "payments", "/payments", s.mPayments, nil, 301},
	}
	for _, tc := range cases {
		t.Run(tc.key, func(t *testing.T) {
			seen := map[string]bool{}
			for page := 1; ; page++ {
				w := historyRequest(s, biz, tc.path+"?"+tc.key+"_per_page=37&"+tc.key+"_page="+fmt.Sprint(page), tc.handler, tc.params)
				if w.Code != 200 {
					t.Fatalf("%d %s", w.Code, w.Body.String())
				}
				var body M
				json.Unmarshal(w.Body.Bytes(), &body)
				meta := body[tc.key+"_pagination"].(map[string]any)
				if meta["total"] != float64(tc.total) {
					t.Fatal("incorrect scoped count", meta)
				}
				for _, record := range body[tc.field].([]any) {
					id := fmt.Sprint(record.(map[string]any)["id"])
					if tc.key == "payments" {
						id = fmt.Sprint(record.(map[string]any)["reference"])
					}
					if seen[id] {
						t.Fatal("duplicate", id)
					}
					seen[id] = true
				}
				if page == int(meta["pages"].(float64)) {
					break
				}
			}
			if len(seen) != tc.total {
				t.Fatalf("reached %d of %d", len(seen), tc.total)
			}
			w := historyRequest(s, biz, tc.path+"?"+tc.key+"_q=Foreign&"+tc.key+"_page=999999999", tc.handler, tc.params)
			if w.Code != 200 {
				t.Fatal(w.Body.String())
			}
			var body M
			json.Unmarshal(w.Body.Bytes(), &body)
			meta := body[tc.key+"_pagination"].(map[string]any)
			if meta["total"] != float64(0) || meta["page"] != float64(1) {
				t.Fatal("empty count or foreign isolation", meta)
			}
		})
	}
	// Loyalty totals and rent balances must not shrink when paging or searching.
	w := historyRequest(s, biz, "/loyalty?recent_per_page=1&loyalty_clients_q=missing", s.mLoyalty, nil)
	var body M
	json.Unmarshal(w.Body.Bytes(), &body)
	if body["kpis"].(map[string]any)["outstanding"] != float64(402) {
		t.Fatal("loyalty KPI based on page")
	}
	w = historyRequest(s, biz, "/staff?rent_per_page=1&rent_page=61", s.mStaff, nil)
	json.Unmarshal(w.Body.Bytes(), &body)
	if body["rent_due"].([]any)[0].(map[string]any)["cents"] != float64(61) {
		t.Fatal("rent balance based on page")
	}
	// Rent totals and metadata remain private from staff who can view rosters.
	r := httptest.NewRequest("GET", "/staff", nil)
	r = r.WithContext(context.WithValue(r.Context(), merchantKey{}, Merchant{BusinessID: biz, StaffID: staff, Role: "staff", Loc: time.UTC, Timezone: "UTC"}))
	private := httptest.NewRecorder()
	s.mStaff(private, r)
	var hidden M
	json.Unmarshal(private.Body.Bytes(), &hidden)
	if private.Code != 200 || len(hidden["rent"].([]any)) != 0 || len(hidden["rent_due"].([]any)) != 0 || hidden["rent_pagination"] != nil {
		t.Fatal("rent metadata leaked to staff", private.Body.String())
	}

	// Legacy customer benefit loader still returns all plans; it has no history cap.
	plans, _ := s.clientPlans(context.Background(), biz, client)
	if len(plans) != 201 {
		t.Fatal("customer benefit loader truncated")
	}
}

func TestSecurityHistoryBoundsAndSearch(t *testing.T) {
	s := securityServer(t)
	biz, _, _, _, _ := securityFixture(t, s)
	securityExec(t, s, `insert into ledger(business_id,kind,amount_cents,currency,status,description,created_at) select $1,'charge',n,'USD','settled',case when n=321 then 'Old needle % literal' else 'Recent' end,now()-n*interval '1 second' from generate_series(1,321) n`, biz)
	for _, tc := range []struct {
		query            string
		total, page, per int
	}{
		{"transactions_page=-9&transactions_per_page=-1", 321, 1, 50},
		{"transactions_page=999999999&transactions_per_page=999999", 321, 2, 200},
		{"transactions_q=" + url.QueryEscape("% literal"), 1, 1, 50},
		{"transactions_q=missing&transactions_page=2", 0, 1, 50},
	} {
		w := historyRequest(s, biz, "/money?"+tc.query, s.mMoney, nil)
		if w.Code != 200 {
			t.Fatal(w.Body.String())
		}
		var body M
		json.Unmarshal(w.Body.Bytes(), &body)
		meta := body["transactions_pagination"].(map[string]any)
		if meta["total"] != float64(tc.total) || meta["page"] != float64(tc.page) || meta["per_page"] != float64(tc.per) {
			t.Fatal(meta)
		}
	}
}

func TestSecurityHistoryReturns(t *testing.T) {
	s := securityServer(t)
	biz, _, _, user, _ := securityFixture(t, s)
	foreign := securityID(t, s, `insert into businesses(slug,name,category,market,currency,timezone) values('foreign-returns','Foreign','hair','US','USD','UTC') returning id::text`)
	securityExec(t, s, `insert into orders(user_id,customer_name,fulfilment,subtotal_cents,total_cents) select $1,'History return '||n,'ship',100,100 from generate_series(1,301) n`, user)
	securityExec(t, s, `insert into order_shipments(order_id,seller_name,business_id,fulfilment,items_cents,shipping_cents) select id,'History merchant',$1,'ship',100,0 from orders where customer_name like 'History return %'`, biz)
	securityExec(t, s, `insert into order_returns(order_id,shipment_id,business_id,user_id,reason,note,status,provider_refund_status,created_at) select sh.order_id,sh.id,$1,$2,'other','Needle return',case when split_part(o.customer_name,' ',3)::int<=200 then 'approved' else 'requested' end,case when split_part(o.customer_name,' ',3)::int<=20 then 'pending' else '' end,now()-split_part(o.customer_name,' ',3)::int*interval '1 second' from order_shipments sh join orders o on o.id=sh.order_id where sh.business_id=$1`, biz, user)
	for _, tc := range []struct {
		business any
		name     string
	}{{foreign, "Foreign secret"}, {nil, "Brand return"}} {
		order := securityID(t, s, `insert into orders(user_id,customer_name,fulfilment,subtotal_cents,total_cents) values($1,$2,'ship',100,100) returning id::text`, user, tc.name)
		shipment := securityID(t, s, `insert into order_shipments(order_id,seller_name,business_id,fulfilment,items_cents,shipping_cents) values($1,'History seller',$2,'ship',100,0) returning id::text`, order, tc.business)
		securityExec(t, s, `insert into order_returns(order_id,shipment_id,business_id,user_id,reason,note) values($1,$2,$3,$4,'other',$5)`, order, shipment, tc.business, user, tc.name)
	}
	selected := securityID(t, s, `select rt.id::text from order_returns rt join orders o on o.id=rt.order_id where o.customer_name='History return 301'`)
	seen := map[string]bool{}
	for page := 1; page <= 7; page++ {
		w := historyRequest(s, biz, "/returns?returns_per_page=50&returns_page="+fmt.Sprint(page), s.mReturns, nil)
		if w.Code != 200 {
			t.Fatal(w.Body.String())
		}
		var body M
		json.Unmarshal(w.Body.Bytes(), &body)
		if body["returns_pagination"].(map[string]any)["total"] != float64(301) {
			t.Fatal("truncated returns", w.Body.String())
		}
		if body["counts"].(map[string]any)["requested"] != float64(101) || body["counts"].(map[string]any)["approved"] != float64(200) {
			t.Fatal("state totals based on page")
		}
		for _, raw := range body["returns"].([]any) {
			record := raw.(map[string]any)
			id := fmt.Sprint(record["id"])
			if seen[id] {
				t.Fatal("duplicate return")
			}
			seen[id] = true
		}
	}
	if len(seen) != 301 {
		t.Fatal("older returns missing")
	}
	for _, tc := range []struct {
		query string
		want  int
	}{
		{"returns_q=Foreign", 0}, {"returns_status=approved", 200}, {"returns_q=missing&answer=" + selected, 0},
	} {
		w := historyRequest(s, biz, "/returns?"+tc.query, s.mReturns, nil)
		if w.Code != 200 {
			t.Fatal(w.Body.String())
		}
		var body M
		json.Unmarshal(w.Body.Bytes(), &body)
		if body["returns_pagination"].(map[string]any)["total"] != float64(tc.want) {
			t.Fatal("returns filtering count")
		}
		if strings.Contains(tc.query, "answer=") && body["selected_return"].(map[string]any)["id"] != selected {
			t.Fatal("off-page answer missing")
		}
	}
	w := historyRequest(s, foreign, "/returns?answer="+selected, s.mReturns, nil)
	var body M
	json.Unmarshal(w.Body.Bytes(), &body)
	if body["selected_return"] != nil {
		t.Fatal("foreign return answer leaked")
	}
	w = historyRequest(s, "", "/admin/returns", s.adminReturns, nil)
	json.Unmarshal(w.Body.Bytes(), &body)
	if body["returns_pagination"].(map[string]any)["total"] != float64(1) || body["returns"].([]any)[0].(map[string]any)["customer_name"] != "Brand return" {
		t.Fatal("brand returns scope")
	}
}

func TestSecurityMerchantOrderAndProblemHistories(t *testing.T) {
	s := securityServer(t)
	biz, _, _, user, _ := securityFixture(t, s)
	securityExec(t, s, `insert into orders(user_id,customer_name,fulfilment,status,subtotal_cents,total_cents) select $1,'Order needle '||n,'ship','paid',100,100 from generate_series(1,301) n`, user)
	securityExec(t, s, `insert into order_shipments(order_id,seller_name,business_id,fulfilment,items_cents) select id,'Security Studio',$1,'ship',100 from orders where customer_name like 'Order needle %'`, biz)
	securityExec(t, s, `insert into disputes(ref,business_id,client_name,amount_cents,currency,reason) select 'problem-'||n,$1,'Problem needle '||n,100,'USD','quality' from generate_series(1,201) n`, biz)
	for _, tc := range []struct {
		key, path string
		handler   http.HandlerFunc
		total     int
	}{{"orders", "/orders", s.mOrders, 301}, {"problems", "/problems", s.mProblems, 201}} {
		for page := 1; page <= 4; page++ {
			w := historyRequest(s, biz, tc.path+"?"+tc.key+"_per_page=100&"+tc.key+"_page="+fmt.Sprint(page), tc.handler, nil)
			if w.Code != 200 {
				t.Fatalf("%s %s", tc.key, w.Body.String())
			}
			var out M
			json.Unmarshal(w.Body.Bytes(), &out)
			meta := out[tc.key+"_pagination"].(map[string]any)
			if meta["total"] != float64(tc.total) {
				t.Fatalf("history count %s %v", tc.key, meta)
			}
		}
	}
	w := historyRequest(s, biz, "/problems?problems_q=needle%20201", s.mProblems, nil)
	var out M
	json.Unmarshal(w.Body.Bytes(), &out)
	if w.Code != 200 || len(out["problems"].([]any)) != 1 || out["waiting"] != float64(201) {
		t.Fatalf("old problem search/count: %s", w.Body.String())
	}
}
