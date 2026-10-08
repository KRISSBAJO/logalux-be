package httpapi

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
)

// CSV downloads for finance and operations. Each export lists its columns in
// a fixed order so a spreadsheet built on one download works on the next.

type export struct {
	cols []string
	sql  string
	args func(r *http.Request) []any
}

func qArgs(keys ...string) func(r *http.Request) []any {
	return func(r *http.Request) []any {
		q := r.URL.Query()
		out := make([]any, len(keys))
		for i, k := range keys {
			out[i] = q.Get(k)
		}
		return out
	}
}

const exportLimit = 20000

var exports = map[string]export{
	"bookings": {
		cols: []string{"id", "starts_at", "ends_at", "status", "business", "market", "staff", "client_name", "client_phone", "services", "currency", "total_cents", "discount_cents", "promo_code", "deposit_cents", "deposit_paid", "source", "created_at"},
		sql: `select bk.id, bk.starts_at, bk.ends_at, bk.status, b.name as business, b.market, st.name as staff, bk.client_name, bk.client_phone,
		        (select string_agg(name, '; ') from booking_items where booking_id = bk.id) as services,
		        b.currency, bk.total_cents, bk.discount_cents, bk.promo_code, bk.deposit_cents, bk.deposit_paid, bk.source, bk.created_at
		      from bookings bk join businesses b on b.id = bk.business_id join staff st on st.id = bk.staff_id
		      where ($1 = '' or bk.client_name ilike '%'||$1||'%' or bk.client_phone like '%'||$1||'%' or b.name ilike '%'||$1||'%')
		        and ($2 = '' or bk.status = $2) and ($3 = '' or b.slug = $3)
		        and (nullif($4,'') is null or bk.starts_at >= nullif($4,'')::date)
		        and (nullif($5,'') is null or bk.starts_at < nullif($5,'')::date + 1)
		      order by bk.starts_at desc`,
		args: qArgs("q", "status", "business", "from", "to"),
	},
	"orders": {
		cols: []string{"id", "created_at", "status", "customer_name", "customer_phone", "fulfilment", "items", "sellers", "subtotal_cents", "discount_cents", "promo_code", "shipping_cents", "tax_cents", "gift_cents", "gift_code", "total_cents"},
		sql: `select o.id, o.created_at, o.status, o.customer_name, o.customer_phone, o.fulfilment,
		        (select string_agg(oi.qty || ' x ' || oi.name, '; ') from order_items oi where oi.order_id = o.id) as items,
		        (select string_agg(distinct oi.seller_name, '; ') from order_items oi where oi.order_id = o.id) as sellers,
		        o.subtotal_cents, o.discount_cents, o.promo_code, o.shipping_cents, o.tax_cents, o.gift_cents, o.gift_code, o.total_cents
		      from orders o where ($1 = '' or o.customer_name ilike '%'||$1||'%' or o.customer_phone like '%'||$1||'%') and ($2 = '' or o.status = $2)
		      order by o.created_at desc`,
		args: qArgs("q", "status"),
	},
	"payouts": {
		cols: []string{"id", "business", "market", "currency", "amount_cents", "status", "provider", "reference", "failure_reason", "scheduled_for", "paid_at"},
		sql: `select p.id, b.name as business, b.market, p.currency, p.amount_cents, p.status, p.provider, p.reference, p.failure_reason, p.scheduled_for, p.paid_at
		      from payouts p join businesses b on b.id = p.business_id where ($1 = '' or p.status = $1) order by p.scheduled_for desc`,
		args: qArgs("status"),
	},
	"clients": {
		cols: []string{"id", "name", "phone", "business", "bookings", "spent_cents", "currency", "no_show_count", "last_booking", "created_at"},
		sql: `select c.id, c.name, c.phone, b.name as business,
		        (select count(*) from bookings where client_id = c.id) as bookings,
		        (select coalesce(sum(total_cents),0) from bookings where client_id = c.id and status not in ('cancelled_client','cancelled_business','no_show')) as spent_cents,
		        b.currency, c.no_show_count, (select max(starts_at) from bookings where client_id = c.id) as last_booking, c.created_at
		      from clients c join businesses b on b.id = c.business_id
		      where ($1 = '' or c.name ilike '%'||$1||'%' or c.phone like '%'||$1||'%' or b.name ilike '%'||$1||'%') order by c.created_at desc`,
		args: qArgs("q"),
	},
	"businesses": {
		cols: []string{"id", "slug", "name", "owner_name", "category", "market", "currency", "status", "verification_status", "plan", "phone", "email", "rating", "review_count", "staff_count", "created_at"},
		sql: `select b.id, b.slug, b.name, b.owner_name, b.category, b.market, b.currency, b.status, b.verification_status, b.plan, b.phone, b.email, b.rating, b.review_count,
		        (select count(*) from staff where business_id = b.id) as staff_count, b.created_at
		      from businesses b where ($1 = '' or b.name ilike '%'||$1||'%' or b.owner_name ilike '%'||$1||'%') and ($2 = '' or b.status = $2) and ($3 = '' or b.market = $3) order by b.name`,
		args: qArgs("q", "status", "market"),
	},
	"products": {
		cols: []string{"id", "slug", "name", "seller_name", "category", "price_cents", "compare_cents", "stock", "sold", "active", "rating", "review_count", "created_at"},
		sql:  `select id, slug, name, seller_name, category, price_cents, compare_cents, stock, sold, active, rating, review_count, created_at from products order by name`,
		args: qArgs(),
	},
	"audit": {
		cols: []string{"id", "created_at", "actor", "action", "target", "before", "after"},
		sql: `select id, created_at, actor, action, target, before, after from audit_log
		      where ($1 = '' or target ilike '%'||$1||'%' or action ilike '%'||$1||'%') and ($2 = '' or actor = $2) and ($3 = '' or action like $3||'%') order by created_at desc, id desc`,
		args: qArgs("q", "actor", "action"),
	},
	"gift-cards": {
		cols: []string{"id", "code", "status", "currency", "initial_cents", "balance_cents", "recipient_name", "recipient_email", "expires_on", "issued_by", "created_at"},
		sql:  `select id, code, status, currency, initial_cents, balance_cents, recipient_name, recipient_email, expires_on, issued_by, created_at from gift_cards order by created_at desc`,
		args: qArgs(),
	},
}

// csvCell renders a value. A cell that a spreadsheet would run as a formula
// gets a leading apostrophe, so a hostile name cannot execute on open.
func csvCell(v any) string {
	var s string
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		s = x
	case time.Time:
		return x.UTC().Format(time.RFC3339)
	case bool:
		if x {
			return "yes"
		}
		return "no"
	case map[string]any, []any:
		b, _ := json.Marshal(x)
		s = string(b)
	default:
		s = fmt.Sprint(x)
	}
	if s != "" && strings.ContainsRune("=+-@\t\r", rune(s[0])) {
		return "'" + s
	}
	return s
}

// GET /v1/admin/export/{kind}   same filters as the matching list
func (s *Server) adminExport(w http.ResponseWriter, r *http.Request) {
	kind := chi.URLParam(r, "kind")
	ex, ok := exports[kind]
	if !ok {
		writeErr(w, 404, "there is no export called "+kind)
		return
	}
	if kind == "audit" && roleRank[currentAdmin(r).Role] < roleRank["super_admin"] {
		writeErr(w, http.StatusForbidden, "only a super admin can export the audit log")
		return
	}
	out, err := rows(r.Context(), s.pool, ex.sql+fmt.Sprintf(" limit %d", exportLimit), ex.args(r)...)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	s.audit(r, "export."+kind, kind, nil, M{"rows": len(out), "filters": r.URL.RawQuery})

	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="logaluxe-%s-%s.csv"`, kind, time.Now().UTC().Format("2006-01-02")))
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write([]byte("\xEF\xBB\xBF")) // so Excel reads it as UTF-8
	cw := csv.NewWriter(w)
	_ = cw.Write(ex.cols)
	rec := make([]string, len(ex.cols))
	for _, row := range out {
		for i, c := range ex.cols {
			rec[i] = csvCell(row[c])
		}
		_ = cw.Write(rec)
	}
	cw.Flush()
}
