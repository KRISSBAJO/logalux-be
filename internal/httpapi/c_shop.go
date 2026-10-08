package httpapi

import (
	"context"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
)

// The shop: browsing products, reading and writing product reviews, and what
// happens to an order after it is paid. Each seller in an order has its own
// row (a "shipment"): how its items reach the customer and how far along
// they are. A business that sells through the shop is paid its items and
// shipping, less the marketplace fee, through the same ledger as its
// bookings.

// The lowest price a product can be bought at: its smallest size, when it has sizes.
const fromPrice = "least(p.price_cents, coalesce((select min((z->>'price_cents')::int) from jsonb_array_elements(p.sizes) z), p.price_cents))"

// GET /v1/products?q=&category=&seller=&tag=&delivery=pickup|ship&min=&max=&sort=recommended|price_asc|price_desc|rating|new&page=&per=
func (s *Server) listProducts(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	q := r.URL.Query()
	atoi := func(k string) int { n, _ := strconv.Atoi(q.Get(k)); return n }
	page, per := atoi("page"), atoi("per")
	if page < 1 {
		page = 1
	}
	if per < 1 || per > 60 {
		per = 24
	}
	order := map[string]string{"price_asc": fromPrice + ", p.name", "price_desc": fromPrice + " desc, p.name", "rating": "p.rating desc, p.review_count desc", "new": "p.created_at desc"}[q.Get("sort")]
	if order == "" {
		order = "p.sold desc, p.rating desc, p.name"
	}
	// One set of conditions for the list and for the counts beside each filter. $7 lists the handles of
	// businesses the visitor has booked, for "from professionals I've booked".
	const where = `p.active and p.kind in ('retail','both')
		and ($1 = '' or p.name ilike '%'||$1||'%' or p.seller_name ilike '%'||$1||'%' or p.description ilike '%'||$1||'%' or lower($1) = any(p.tags))
		and ($2 = '' or p.category = $2)
		and ($3 = '' or p.seller_name = $3 or b.slug = $3 or ($3 = 'booked' and b.slug = any($7::text[])) or ($3 = 'brands' and p.business_id is null))
		and ($4 = '' or lower($4) = any(p.tags))
		and ($5 = 0 or least(p.price_cents, coalesce((select min((z->>'price_cents')::int) from jsonb_array_elements(p.sizes) z), p.price_cents)) >= $5) and ($6 = 0 or least(p.price_cents, coalesce((select min((z->>'price_cents')::int) from jsonb_array_elements(p.sizes) z), p.price_cents)) <= $6)
		and ($8 = '' or ($8 = 'pickup' and p.pickup and p.business_id is not null) or ($8 = 'ship' and p.shipping))`
	booked := []string{}
	if uid := s.customerID(r); uid != nil {
		rs, _ := rows(ctx, s.pool, `select distinct b.slug from bookings bk join businesses b on b.id = bk.business_id where bk.user_id=$1`, *uid)
		for _, x := range rs {
			booked = append(booked, fmt.Sprint(x["slug"]))
		}
	}
	args := []any{strings.TrimSpace(q.Get("q")), q.Get("category"), q.Get("seller"), strings.TrimSpace(q.Get("tag")), atoi("min"), atoi("max"), booked, q.Get("delivery")}
	out, err := rows(ctx, s.pool, `select p.id, p.slug, p.name, p.seller_name, b.slug as business_slug, (b.verification_status = 'verified') as seller_verified, p.category, p.description, p.price_cents, p.compare_cents,
		p.stock, p.tone, p.tags, p.rating::float8 as rating, p.review_count, p.sold, p.pickup and p.business_id is not null as pickup, p.shipping, p.shipping_cents, p.sizes,
		(select sm.id from site_media sm where sm.slot='product' and sm.ref = p.slug and sm.active order by sm.sort, sm.created_at desc limit 1) as photo_id,
		count(*) over() as total
		from products p left join businesses b on b.id = p.business_id where `+where+` order by (p.stock > 0) desc, `+order+` limit `+strconv.Itoa(per)+` offset `+strconv.Itoa((page-1)*per), args...)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	total := 0
	for _, p := range out {
		total = int(toInt(p["total"]))
		delete(p, "total")
	}
	// What can be chosen, counted across the whole shop so a filter never shows a dead end it created itself.
	categories, _ := rows(ctx, s.pool, `select p.category, count(*) as n from products p where p.active and p.kind in ('retail','both') group by 1 order by 2 desc, 1`)
	sellers, _ := rows(ctx, s.pool, `select p.seller_name, b.slug as business_slug, (b.verification_status = 'verified') as verified, count(*) as n from products p left join businesses b on b.id = p.business_id
		where p.active and p.kind in ('retail','both') group by 1,2,3 order by 4 desc, 1`)
	tags, _ := rows(ctx, s.pool, `select t as tag, count(*) as n from products p, unnest(p.tags) t where p.active and p.kind in ('retail','both') group by 1 order by 2 desc, 1 limit 24`)
	writeJSON(w, 200, M{"products": out, "total": total, "page": page, "per_page": per, "categories": categories, "sellers": sellers, "tags": tags, "booked": booked})
}

// GET /v1/products/{slug}
func (s *Server) getProduct(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	p, err := row(ctx, s.pool, `select p.id, p.slug, p.name, p.seller_name, p.category, p.description, p.how_to_use, p.price_cents, p.compare_cents, p.stock, p.sizes, p.tone, p.tags,
		p.rating::float8 as rating, p.review_count, p.sold, p.pickup and p.business_id is not null as pickup, p.shipping, p.shipping_cents,
		b.slug as business_slug, b.name as business, (b.verification_status = 'verified') as seller_verified, b.currency as business_currency,
		(select l.city from locations l where l.business_id = b.id and l.is_primary) as business_city,
		(select min(sv.price_cents) from services sv where sv.business_id = b.id and sv.online and not sv.archived and sv.category <> 'Add-ons') as business_from_cents,
		(select coalesce(json_agg(json_build_object('service_id', sp.service_id, 'name', sv.name) order by sv.name), '[]') from service_products sp join services sv on sv.id = sp.service_id where sp.product_id = p.id and not sv.archived) as used_in
		from products p left join businesses b on b.id = p.business_id where p.slug=$1 and p.active`, chi.URLParam(r, "slug"))
	if err != nil {
		writeErr(w, 404, "product not found")
		return
	}
	photos, _ := rows(ctx, s.pool, `select id, alt from site_media where slot='product' and ref=$1 and active order by sort, created_at desc limit 6`, p["slug"])
	related, _ := rows(ctx, s.pool, `select p.slug, p.name, p.seller_name, p.price_cents, p.tone, p.rating::float8 as rating,
		(select sm.id from site_media sm where sm.slot='product' and sm.ref = p.slug and sm.active order by sm.sort limit 1) as photo_id
		from products p where p.active and p.kind in ('retail','both') and p.slug <> $1 order by (p.seller_name = $3) desc, (p.category = $2) desc, p.sold desc limit 4`, p["slug"], p["category"], p["seller_name"])
	reviews, _ := rows(ctx, s.pool, `select id, author_name, rating, body, verified, created_at from product_reviews where product_id=$1 order by created_at desc limit 30`, p["id"])
	can := M{"review": false, "why": "Sign in to write a review."}
	if uid := s.customerID(r); uid != nil {
		var bought, wrote bool
		_ = s.pool.QueryRow(ctx, `select exists(select 1 from orders o join order_items oi on oi.order_id = o.id where o.user_id=$1 and oi.product_id=$2 and o.status in ('paid','ready','shipped','delivered')),
			exists(select 1 from product_reviews where product_id=$2 and user_id=$1)`, *uid, p["id"]).Scan(&bought, &wrote)
		switch {
		case wrote:
			can = M{"review": false, "why": "You have reviewed this product."}
		case !bought:
			can = M{"review": false, "why": "Reviews are from people who bought it on LogaLuxe."}
		default:
			can = M{"review": true, "why": ""}
		}
	}
	writeJSON(w, 200, M{"product": p, "photos": photos, "related": related, "reviews": reviews, "can": can})
}

// POST /v1/auth/products/{slug}/review   {rating, body}   only someone who bought it, once
func (s *Server) authProductReview(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	c := currentCustomer(r)
	var req struct {
		Rating int    `json:"rating"`
		Body   string `json:"body"`
	}
	if !needVerified(w, c) {
		return
	}
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, "invalid json")
		return
	}
	req.Body = strings.TrimSpace(req.Body)
	if req.Rating < 1 || req.Rating > 5 || len(req.Body) < 10 || len(req.Body) > 1500 {
		writeErr(w, 400, "choose one to five stars and write at least a sentence")
		return
	}
	var id string
	var bought bool
	if err := s.pool.QueryRow(ctx, `select p.id::text, exists(select 1 from orders o join order_items oi on oi.order_id = o.id where o.user_id=$2 and oi.product_id = p.id and o.status in ('paid','ready','shipped','delivered'))
		from products p where p.slug=$1 and p.active`, chi.URLParam(r, "slug"), c.ID).Scan(&id, &bought); err != nil {
		writeErr(w, 404, "product not found")
		return
	}
	if !bought {
		writeErr(w, 403, "reviews are from people who bought the product on LogaLuxe")
		return
	}
	if _, err := s.pool.Exec(ctx, `insert into product_reviews (product_id, user_id, author_name, rating, body, verified) values ($1,$2,$3,$4,$5,true)`, id, c.ID, strings.TrimSpace(c.FirstName+" "+firstInitial(c.LastName)), req.Rating, req.Body); err != nil {
		if isCode(err, "23505") {
			writeErr(w, 409, "you have already reviewed this product")
			return
		}
		writeErr(w, 500, err.Error())
		return
	}
	_, _ = s.pool.Exec(ctx, `update products p set review_count = x.n, rating = x.avg from (select count(*) as n, round(avg(rating), 1) as avg from product_reviews where product_id=$1) x where p.id=$1`, id)
	writeJSON(w, 201, M{"ok": true})
}

// ---------- after an order is paid ----------

// settleOrder pays each business in an order for what it sold: its items and its shipping, less the
// marketplace fee. It acts once per order, so it is safe to call from every place that learns an order is paid.
func (s *Server) settleOrder(ctx context.Context, orderID string) {
	q := s.pool
	var done bool
	_ = q.QueryRow(ctx, `select exists(select 1 from ledger where order_id=$1)`, orderID).Scan(&done)
	if done {
		return
	}
	_, _ = q.Exec(ctx, `insert into ledger (business_id, kind, amount_cents, currency, method, status, in_balance, order_id, description, settles_at)
		select sh.business_id, 'charge', sh.items_cents + sh.shipping_cents, b.currency, 'card', 'pending', true, sh.order_id, 'Shop order · ' || o.customer_name, now() + interval '`+settleAfter+`'
		from order_shipments sh join businesses b on b.id = sh.business_id join orders o on o.id = sh.order_id where sh.order_id=$1 and sh.items_cents > 0`, orderID)
	_, _ = q.Exec(ctx, `insert into ledger (business_id, kind, amount_cents, currency, method, status, in_balance, order_id, description, settles_at)
		select sh.business_id, 'fee', -round(sh.items_cents * f.marketplace_pct / 100)::int, b.currency, 'card', 'pending', true, sh.order_id, 'Marketplace fee · shop order', now() + interval '`+settleAfter+`'
		from order_shipments sh join businesses b on b.id = sh.business_id
		join lateral (select marketplace_pct from fees where market = b.market and plan = b.plan and status = 'approved' and effective_from <= current_date order by effective_from desc limit 1) f on true
		where sh.order_id=$1 and sh.items_cents > 0 and f.marketplace_pct > 0`, orderID)
}

// unwindOrder puts back the stock of an order that was never paid for.
func (s *Server) unwindOrder(ctx context.Context, orderID string) {
	_, _ = s.pool.Exec(ctx, `update products p set stock = p.stock + x.qty, sold = greatest(p.sold - x.qty, 0) from (select product_id, sum(qty)::int as qty from order_items where order_id=$1 group by 1) x where p.id = x.product_id`, orderID)
	_, _ = s.pool.Exec(ctx, `update order_shipments set status='cancelled', updated_at=now() where order_id=$1`, orderID)
	// A gift card or promo code spent on it is given back, once.
	if tag, err := s.pool.Exec(ctx, `insert into gift_card_txns (gift_card_id, order_id, amount_cents, note, actor)
		select t.gift_card_id, t.order_id, -t.amount_cents, 'Returned: the order was not paid', 'LogaLuxe' from gift_card_txns t
		where t.order_id=$1 and t.amount_cents < 0 and not exists (select 1 from gift_card_txns r where r.order_id=$1 and r.amount_cents > 0)`, orderID); err == nil && tag.RowsAffected() > 0 {
		_, _ = s.pool.Exec(ctx, `update gift_cards g set balance_cents = g.balance_cents + t.amount_cents from gift_card_txns t where t.order_id=$1 and t.amount_cents > 0 and g.id = t.gift_card_id`, orderID)
	}
	_, _ = s.pool.Exec(ctx, `update promo_codes p set used = greatest(p.used - 1, 0) from orders o where o.id=$1 and o.promo_code <> '' and upper(p.code) = upper(o.promo_code)`, orderID)
}

// POST /v1/auth/orders/{id}/cancel   a customer drops an order they have not paid for
func (s *Server) authOrderCancel(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id := chi.URLParam(r, "id")
	// The client may have paid a moment ago, so ask the provider first, then close the payment page.
	pays, _ := rows(ctx, s.pool, `select p.reference, p.provider, p.provider_id from payments p join orders o on o.id = p.order_id where o.id::text=$1 and o.user_id=$2 and p.status='pending'`, id, currentCustomer(r).ID)
	for _, p := range pays {
		s.settlePayment(ctx, fmt.Sprint(p["reference"]))
		if p["provider"] == "stripe" && s.cfg.StripeSecret != "" {
			_ = providerJSON(ctx, http.MethodPost, "https://api.stripe.com/v1/checkout/sessions/"+url.PathEscape(fmt.Sprint(p["provider_id"]))+"/expire", s.cfg.StripeSecret, url.Values{}, nil, nil)
		}
	}
	tag, err := s.pool.Exec(ctx, `update orders set status='cancelled' where id::text=$1 and user_id=$2 and status='pending'`, id, currentCustomer(r).ID)
	if err != nil || tag.RowsAffected() == 0 {
		writeErr(w, 409, "only an order that is not paid yet can be cancelled here; for a paid order, message the seller")
		return
	}
	_, _ = s.pool.Exec(ctx, `update payments set status='expired' where order_id::text=$1 and status='pending'`, id)
	s.unwindOrder(ctx, id)
	writeJSON(w, 200, M{"ok": true})
}

// ---------- a business's online orders ----------

// GET /v1/m/orders?status=open|new|ready|shipped|done
func (s *Server) mOrders(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	m := mc(r)
	filter := map[string]string{"open": "sh.status in ('new','ready','shipped')", "new": "sh.status = 'new'", "ready": "sh.status = 'ready'", "shipped": "sh.status = 'shipped'", "done": "sh.status in ('delivered','collected','cancelled')"}[r.URL.Query().Get("status")]
	if filter == "" {
		filter = "true"
	}
	out, err := rows(ctx, s.pool, `select sh.id, sh.order_id, sh.fulfilment, sh.status, sh.items_cents, sh.shipping_cents, sh.tracking, sh.updated_at, o.customer_name, o.customer_phone, o.customer_email, o.status as order_status, o.created_at,
		case when sh.fulfilment = 'ship' then o.address end as address,
		(select coalesce(json_agg(json_build_object('name', oi.name, 'size', oi.size_label, 'qty', oi.qty, 'unit_cents', oi.unit_cents) order by oi.name), '[]') from order_items oi where oi.order_id = o.id and oi.seller_name = sh.seller_name) as items,
		(select coalesce(sum(l.amount_cents),0) from ledger l where l.order_id = o.id and l.business_id = sh.business_id)::int as net_cents
		from order_shipments sh join orders o on o.id = sh.order_id
		where sh.business_id=$1 and o.status <> 'pending' and (`+filter+`) order by (sh.status in ('new','ready','shipped')) desc, o.created_at desc limit 300`, m.BusinessID)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	counts, _ := row(ctx, s.pool, `select count(*) filter (where sh.status = 'new') as new, count(*) filter (where sh.status = 'ready') as ready, count(*) filter (where sh.status = 'shipped') as shipped,
		count(*) filter (where sh.status in ('delivered','collected','cancelled')) as done, count(*) as all
		from order_shipments sh join orders o on o.id = sh.order_id where sh.business_id=$1 and o.status <> 'pending'`, m.BusinessID)
	var pct float64
	_ = s.pool.QueryRow(ctx, `select coalesce((select marketplace_pct from fees where market=$1 and plan=$2 and status='approved' and effective_from <= current_date order by effective_from desc limit 1),0)::float8`, m.Market, m.Plan).Scan(&pct)
	writeJSON(w, 200, M{"orders": out, "counts": counts, "marketplace_pct": pct})
}

// POST /v1/m/orders/{id}   {action: ready|shipped|delivered|collected, tracking}
func (s *Server) mOrderAction(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	m := mc(r)
	var req struct {
		Action   string `json:"action"`
		Tracking string `json:"tracking"`
	}
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, "invalid json")
		return
	}
	var fulfilment, status, orderID string
	if err := s.pool.QueryRow(ctx, `select fulfilment, status, order_id::text from order_shipments where id=$1 and business_id=$2`, chi.URLParam(r, "id"), m.BusinessID).Scan(&fulfilment, &status, &orderID); err != nil {
		writeErr(w, 404, "order not found")
		return
	}
	// The steps an order can take, by how it reaches the customer.
	next := map[string]map[string]string{
		"pickup": {"new": "ready", "ready": "collected"},
		"ship":   {"new": "shipped", "ready": "shipped", "shipped": "delivered"},
	}[fulfilment]
	if req.Action == "ready" && fulfilment == "ship" && status == "new" {
		next = map[string]string{"new": "ready"} // packed, waiting for the courier
	}
	if next[status] != req.Action {
		writeErr(w, 409, "this order cannot be marked "+req.Action+" from where it is")
		return
	}
	if _, err := s.pool.Exec(ctx, `update order_shipments set status=$2, tracking = case when $3 <> '' then $3 else tracking end, updated_at=now() where id=$1`, chi.URLParam(r, "id"), req.Action, strings.TrimSpace(req.Tracking)); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	// The order as a whole follows its slowest seller.
	_, _ = s.pool.Exec(ctx, `update orders o set status = case
		when not exists (select 1 from order_shipments sh where sh.order_id = o.id and sh.status not in ('delivered','collected','cancelled')) then 'delivered'
		when not exists (select 1 from order_shipments sh where sh.order_id = o.id and sh.status = 'new') and exists (select 1 from order_shipments sh where sh.order_id = o.id and sh.status = 'shipped') then 'shipped'
		when not exists (select 1 from order_shipments sh where sh.order_id = o.id and sh.status = 'new') then 'ready'
		else o.status end where o.id=$1 and o.status in ('paid','ready','shipped')`, orderID)
	writeJSON(w, 200, M{"ok": true, "status": req.Action})
}

var _ = math.Round
