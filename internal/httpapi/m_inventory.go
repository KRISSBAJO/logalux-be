package httpapi

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
)

// Inventory: what the business sells and uses, how much is left, and what is on order.

// GET /v1/m/inventory?q=&filter=low|retail|backbar|off
func (s *Server) mInventory(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	m := mc(r)
	q := r.URL.Query()
	now := time.Now().In(m.Loc)
	monthStart := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, m.Loc)
	filter := map[string]string{
		"low":     "p.reorder_at > 0 and p.stock <= p.reorder_at",
		"retail":  "p.kind in ('retail','both')",
		"backbar": "p.kind in ('backbar','both')",
		"off":     "not p.active",
		"online":  "p.active and p.kind in ('retail','both')",
	}[q.Get("filter")]
	if filter == "" {
		filter = "true"
	}
	out, err := rows(ctx, s.pool, `select p.id, p.slug, p.name, p.sku, p.kind, p.category, p.description, p.stock, p.reorder_at, p.cost_cents, p.price_cents, p.active, p.tone, p.supplier_id, su.name as supplier, p.par_level, p.pickup, p.shipping, p.shipping_cents, p.backbar_open::float8 as backbar_open,
		(select coalesce(json_agg(json_build_object('location_id', ls.location_id, 'name', lo.name, 'qty', ls.qty) order by lo.is_primary desc, lo.name), '[]') from location_stock ls join locations lo on lo.id = ls.location_id where ls.product_id = p.id) as by_location,
		(select ls.qty from location_stock ls where ls.product_id = p.id and ls.location_id::text = $3) as here,
		(select sm.id from site_media sm where sm.slot='product' and sm.ref = p.slug and sm.active order by sm.sort, sm.created_at desc limit 1) as photo_id,
		(select coalesce(json_agg(json_build_object('service_id', sp.service_id, 'name', sv.name, 'qty', sp.qty::float8) order by sv.name), '[]') from service_products sp join services sv on sv.id = sp.service_id where sp.product_id = p.id) as used_in,
		(select coalesce(sum(si.qty),0) from sale_items si join sales sa on sa.id = si.sale_id where si.product_id = p.id and sa.created_at > now() - interval '30 days')
		  + (select coalesce(sum(oi.qty),0) from order_items oi join orders o on o.id = oi.order_id where oi.product_id = p.id and o.created_at > now() - interval '30 days' and o.status not in ('cancelled','refunded')) as sold_30d
		from products p left join suppliers su on su.id = p.supplier_id
		where p.business_id=$1 and (`+filter+`) and ($2 = '' or p.name ilike '%'||$2||'%' or p.sku ilike '%'||$2||'%')
		order by (p.reorder_at > 0 and p.stock <= p.reorder_at) desc, p.name`, m.BusinessID, strings.TrimSpace(q.Get("q")), q.Get("location"))
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	kpis, _ := row(ctx, s.pool, `select
		(select coalesce(sum(si.unit_cents * si.qty),0) from sale_items si join sales sa on sa.id = si.sale_id where sa.business_id=$1 and si.kind='product' and sa.created_at >= $2)::int as retail_cents,
		(select coalesce(sum((si.unit_cents - coalesce(p.cost_cents,0)) * si.qty),0) from sale_items si join sales sa on sa.id = si.sale_id left join products p on p.id = si.product_id where sa.business_id=$1 and si.kind='product' and sa.created_at >= $2)::int as profit_cents,
		(select coalesce(sum(stock * cost_cents),0) from products where business_id=$1)::int as stock_value_cents,
		(select count(*) from products where business_id=$1 and reorder_at > 0 and stock <= reorder_at) as low,
		(select count(*) from products where business_id=$1) as products,
		(select coalesce(-sum(sm.delta * p.cost_cents),0) from stock_movements sm join products p on p.id = sm.product_id where sm.business_id=$1 and sm.reason='backbar' and sm.created_at >= $2)::int as backbar_cents,
		(select count(distinct sa.id) from sales sa where sa.business_id=$1 and sa.created_at >= $2) as sales,
		(select count(distinct sa.id) from sales sa join sale_items si on si.sale_id = sa.id where sa.business_id=$1 and sa.created_at >= $2 and si.kind='product') as sales_with_retail`, m.BusinessID, monthStart)
	suppliers, _ := rows(ctx, s.pool, `select su.id, su.name, su.contact, su.email, su.phone, (select count(*) from products p where p.supplier_id = su.id) as products from suppliers su where su.business_id=$1 order by su.name`, m.BusinessID)
	orders, ordersPage, ordersErr := s.historyRows(r, "orders", `select po.id, po.ref, po.status, po.items, po.total_cents, po.expected_on, po.created_at, po.received_at, su.name as supplier
		from purchase_orders po left join suppliers su on su.id = po.supplier_id and su.business_id = po.business_id where po.business_id=$1 order by (po.status = 'draft') desc, po.created_at desc `, "(status = 'draft') desc, created_at desc", "created_at ref status supplier total_cents", m.BusinessID)
	if ordersErr != nil {
		writeErr(w, 500, ordersErr.Error())
		return
	}
	draftOrder, _ := row(ctx, s.pool, `select id, ref, total_cents from purchase_orders where business_id=$1 and status='draft' order by created_at desc, id limit 1`, m.BusinessID)
	services, _ := rows(ctx, s.pool, `select id, name, category from services where business_id=$1 and not archived order by sort, name`, m.BusinessID)
	locations, _ := rows(ctx, s.pool, `select l.id, l.name, l.is_primary, (select coalesce(sum(ls.qty),0) from location_stock ls join products p on p.id = ls.product_id where ls.location_id = l.id and p.business_id = $1)::int as units,
		(select coalesce(sum(ls.qty * p.cost_cents),0) from location_stock ls join products p on p.id = ls.product_id where ls.location_id = l.id and p.business_id = $1)::int as value_cents
		from locations l where l.business_id=$1 order by l.is_primary desc, l.name`, m.BusinessID)
	writeJSON(w, 200, M{"products": out, "kpis": kpis, "suppliers": suppliers, "orders": orders, "draft_order": draftOrder, "orders_pagination": ordersPage, "month_label": monthStart.Format("January"), "services": services, "storage": s.store != nil, "locations": locations, "location": q.Get("location")})
}

type mProductReq struct {
	Name        string `json:"name"`
	SKU         string `json:"sku"`
	Kind        string `json:"kind"`
	Category    string `json:"category"`
	Description string `json:"description"`
	PriceCents  int    `json:"price_cents"`
	CostCents   int    `json:"cost_cents"`
	Stock       *int   `json:"stock"` // only used when creating; later changes go through "adjust stock"
	ReorderAt   int    `json:"reorder_at"`
	SupplierID  string `json:"supplier_id"`
	Online      bool   `json:"online"`    // sold in the LogaLuxe shop and on the booking page
	ParLevel    *int   `json:"par_level"` // a full shelf, for the stock meter; leave out to keep it
	// Whether the shop may send it, and what sending one order of it costs. Pick-up is always offered. Leave out to keep.
	Shipping      *bool `json:"shipping"`
	ShippingCents *int  `json:"shipping_cents"`
}

func (p *mProductReq) check() string {
	p.Name, p.SKU = strings.TrimSpace(p.Name), strings.TrimSpace(p.SKU)
	if p.Kind == "" {
		p.Kind = "retail"
	}
	if p.Category == "" {
		p.Category = "hair"
	}
	switch {
	case len(p.Name) < 2 || len(p.Name) > 100:
		return "the product needs a name of 2 to 100 characters"
	case p.Kind != "retail" && p.Kind != "backbar" && p.Kind != "both":
		return "choose retail, back-bar or both"
	case !productCategories[p.Category]:
		return "choose a category"
	case p.CostCents < 0 || p.ReorderAt < 0 || p.PriceCents < 0:
		return "prices and reorder levels cannot be negative"
	case p.Kind != "backbar" && p.PriceCents <= 0:
		return "a product you sell needs a price above zero"
	case p.Online && p.Kind == "backbar":
		return "a back-bar product is not sold to clients, so it cannot be sold online"
	case len(p.Description) > 2000:
		return "keep the description under 2,000 characters"
	case p.ShippingCents != nil && *p.ShippingCents < 0:
		return "shipping cannot be negative"
	}
	return ""
}

// POST /v1/m/products
func (s *Server) mProductCreate(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	m := mc(r)
	var req mProductReq
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, "invalid json")
		return
	}
	if msg := req.check(); msg != "" {
		writeErr(w, 400, msg)
		return
	}
	stock := 0
	if req.Stock != nil && *req.Stock > 0 {
		stock = *req.Stock
	}
	var supplier *string
	if req.SupplierID != "" {
		supplier = &req.SupplierID
	}
	// The shop link must be unique across every seller: name, then name-business, then numbered.
	base := slugify(req.Name)
	if !slugRe.MatchString(base) {
		base = "product"
	}
	slug := base
	for i := 1; i < 40; i++ {
		var taken bool
		_ = s.pool.QueryRow(ctx, `select exists(select 1 from products where slug=$1)`, slug).Scan(&taken)
		if !taken {
			break
		}
		if i == 1 {
			slug = strings.Trim(base+"-"+m.Slug, "-")
		} else {
			slug = base + "-" + itoa(i)
		}
		if len(slug) > 60 {
			slug = slug[:60]
		}
	}
	price := req.PriceCents
	if price == 0 {
		price = 1 // back-bar items are never sold, but the column needs a positive price
	}
	var id string
	if err := s.pool.QueryRow(ctx, `insert into products (business_id, seller_name, slug, name, description, category, price_cents, stock, sku, cost_cents, reorder_at, kind, supplier_id, active, pickup, shipping, par_level, shipping_cents)
		select $1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, (select id from suppliers where id::text = $13 and business_id=$1), $14, true, coalesce($16::bool, false), coalesce($15::int, 0), coalesce($17::int, 0) returning id::text`,
		m.BusinessID, m.Business, slug, req.Name, req.Description, req.Category, price, stock, req.SKU, req.CostCents, req.ReorderAt, req.Kind, supplier, req.Online && req.Kind != "backbar", req.ParLevel, req.Shipping, req.ShippingCents).Scan(&id); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if stock > 0 {
		_, _ = s.pool.Exec(ctx, `insert into stock_movements (business_id, product_id, delta, reason, note, actor) values ($1,$2,$3,'count','Opening stock',$4)`, m.BusinessID, id, stock, m.Email)
	}
	writeJSON(w, 201, M{"ok": true, "id": id})
}

// PUT /v1/m/products/{id}
func (s *Server) mProductUpdate(w http.ResponseWriter, r *http.Request) {
	m := mc(r)
	var req mProductReq
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, "invalid json")
		return
	}
	if msg := req.check(); msg != "" {
		writeErr(w, 400, msg)
		return
	}
	price := req.PriceCents
	if price == 0 {
		price = 1
	}
	tag, err := s.pool.Exec(r.Context(), `update products set name=$3, description=$4, category=$5, price_cents=$6, sku=$7, cost_cents=$8, reorder_at=$9, kind=$10, par_level=coalesce($13::int, par_level), shipping=coalesce($14::bool, shipping), shipping_cents=coalesce($15::int, shipping_cents),
		supplier_id=(select id from suppliers where id::text = $11 and business_id=$2), active=$12 where id=$1 and business_id=$2`,
		chi.URLParam(r, "id"), m.BusinessID, req.Name, req.Description, req.Category, price, req.SKU, req.CostCents, req.ReorderAt, req.Kind, req.SupplierID, req.Online && req.Kind != "backbar", req.ParLevel, req.Shipping, req.ShippingCents)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if tag.RowsAffected() == 0 {
		writeErr(w, 404, "product not found")
		return
	}
	writeJSON(w, 200, M{"ok": true})
}

// POST /v1/m/products/{id}/stock   {delta, reason: restock|adjust|backbar|count, note}
// "count" sets the stock to delta; the others add or take away.
func (s *Server) mProductStock(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	m := mc(r)
	id := chi.URLParam(r, "id")
	var req struct {
		Delta  int    `json:"delta"`
		Reason string `json:"reason"`
		Note   string `json:"note"`
		// Which location's shelf. Left out, it is the main location.
		LocationID string `json:"location_id"`
	}
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, "invalid json")
		return
	}
	if req.Reason != "restock" && req.Reason != "adjust" && req.Reason != "backbar" && req.Reason != "count" {
		writeErr(w, 400, "reason must be restock, adjust, backbar or count")
		return
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	defer tx.Rollback(ctx)
	var stock int
	if err := tx.QueryRow(ctx, `select stock from products where id=$1 and business_id=$2 for update`, id, m.BusinessID).Scan(&stock); err != nil {
		writeErr(w, 404, "product not found")
		return
	}
	// Counts and limits are about one shelf: the location named, or the main one.
	loc := locationFor(ctx, tx, m.BusinessID, req.LocationID)
	atLocation(ctx, tx, loc)
	total := stock
	if loc != "" {
		stock = 0
		_ = tx.QueryRow(ctx, `select qty from location_stock where product_id=$1 and location_id=$2`, id, loc).Scan(&stock)
	}
	delta := req.Delta
	switch req.Reason {
	case "count":
		if req.Delta < 0 {
			writeErr(w, 400, "a count cannot be negative")
			return
		}
		delta = req.Delta - stock
	case "restock":
		if delta <= 0 {
			writeErr(w, 400, "enter how many arrived")
			return
		}
	case "backbar":
		if delta <= 0 {
			writeErr(w, 400, "enter how many were used")
			return
		}
		delta = -delta
	}
	if stock+delta < 0 {
		writeErr(w, 409, "that would take the stock below zero; there are "+itoa(stock)+" left")
		return
	}
	if delta != 0 {
		_, _ = tx.Exec(ctx, `update products set stock = stock + $2 where id=$1`, id, delta)
		_, _ = tx.Exec(ctx, `insert into stock_movements (business_id, product_id, delta, reason, note, actor, location_id) values ($1,$2,$3,$4,$5,$6, nullif($7,'')::uuid)`, m.BusinessID, id, delta, req.Reason, strings.TrimSpace(req.Note), m.Email, loc)
	}
	if err := tx.Commit(ctx); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, M{"ok": true, "stock": total + delta, "here": stock + delta})
}

// GET /v1/m/products/{id}/history
func (s *Server) mProductHistory(w http.ResponseWriter, r *http.Request) {
	out, historyPage, err := s.historyRows(r, "history", `select sm.id, sm.delta, sm.reason, sm.note, sm.actor, sm.created_at, (select lo.name from locations lo where lo.id = sm.location_id) as location from stock_movements sm where sm.product_id=$1 and sm.business_id=$2 order by sm.created_at desc `, "created_at desc", "created_at reason actor delta", chi.URLParam(r, "id"), mc(r).BusinessID)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, M{"history": out, "history_pagination": historyPage})
}

// POST /v1/m/suppliers   {name, contact, email, phone}
func (s *Server) mSupplierCreate(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name    string `json:"name"`
		Contact string `json:"contact"`
		Email   string `json:"email"`
		Phone   string `json:"phone"`
	}
	if err := readJSON(r, &req); err != nil || strings.TrimSpace(req.Name) == "" {
		writeErr(w, 400, "the supplier needs a name")
		return
	}
	var id string
	if err := s.pool.QueryRow(r.Context(), `insert into suppliers (business_id, name, contact, email, phone) values ($1,$2,$3,$4,$5) returning id::text`, mc(r).BusinessID, strings.TrimSpace(req.Name), req.Contact, req.Email, req.Phone).Scan(&id); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 201, M{"ok": true, "id": id})
}

// DELETE /v1/m/suppliers/{id}
func (s *Server) mSupplierDelete(w http.ResponseWriter, r *http.Request) {
	tag, err := s.pool.Exec(r.Context(), `delete from suppliers where id=$1 and business_id=$2`, chi.URLParam(r, "id"), mc(r).BusinessID)
	if err != nil || tag.RowsAffected() == 0 {
		writeErr(w, 404, "supplier not found")
		return
	}
	writeJSON(w, 200, M{"ok": true})
}

type poItem struct {
	ProductID string `json:"product_id"`
	Name      string `json:"name"`
	Qty       int    `json:"qty"`
	CostCents int    `json:"cost_cents"`
}

// POST /v1/m/purchase-orders   {supplier_id, expected_on, items} or {suggest: true}
// "suggest" drafts an order for everything at or below its reorder level,
// enough to cover about four weeks at the last 30 days' pace.
func (s *Server) mPurchaseOrderCreate(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	m := mc(r)
	var req struct {
		SupplierID string   `json:"supplier_id"`
		ExpectedOn string   `json:"expected_on"`
		Items      []poItem `json:"items"`
		Suggest    bool     `json:"suggest"`
	}
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, "invalid json")
		return
	}
	var items []poItem
	if req.Suggest {
		low, _ := rows(ctx, s.pool, `select p.id, p.name, p.stock, p.reorder_at, p.cost_cents,
			(select coalesce(sum(si.qty),0) from sale_items si join sales sa on sa.id = si.sale_id where si.product_id = p.id and sa.created_at > now() - interval '30 days') as sold
			from products p where p.business_id=$1 and p.reorder_at > 0 and p.stock <= p.reorder_at and ($2 = '' or p.supplier_id::text = $2) order by p.name`, m.BusinessID, req.SupplierID)
		for _, p := range low {
			sold, stock, reorder := int(toInt(p["sold"])), int(toInt(p["stock"])), int(toInt(p["reorder_at"]))
			qty := sold - stock // four weeks of sales, less what is on the shelf
			if min := reorder*2 - stock; qty < min {
				qty = min
			}
			if qty < 1 {
				qty = 1
			}
			items = append(items, poItem{ProductID: p["id"].(string), Name: p["name"].(string), Qty: qty, CostCents: int(toInt(p["cost_cents"]))})
		}
		if len(items) == 0 {
			writeErr(w, 409, "nothing is at its reorder level right now")
			return
		}
	} else {
		for _, it := range req.Items {
			var name string
			var cost int
			if err := s.pool.QueryRow(ctx, `select name, cost_cents from products where id=$1 and business_id=$2`, it.ProductID, m.BusinessID).Scan(&name, &cost); err != nil || it.Qty <= 0 {
				writeErr(w, 400, "each line needs one of your products and a quantity above zero")
				return
			}
			if it.CostCents <= 0 {
				it.CostCents = cost
			}
			items = append(items, poItem{ProductID: it.ProductID, Name: name, Qty: it.Qty, CostCents: it.CostCents})
		}
		if len(items) == 0 {
			writeErr(w, 400, "add at least one product to the order")
			return
		}
	}
	total := 0
	for _, it := range items {
		total += it.Qty * it.CostCents
	}
	raw, _ := json.Marshal(items)
	var expected *time.Time
	if t, err := time.Parse("2006-01-02", req.ExpectedOn); err == nil {
		expected = &t
	}
	var id, ref string
	if err := s.pool.QueryRow(ctx, `insert into purchase_orders (business_id, supplier_id, items, total_cents, expected_on, created_by)
		values ($1, (select id from suppliers where id::text = $2 and business_id=$1), $3::jsonb, $4, $5, $6) returning id::text, ref`, m.BusinessID, req.SupplierID, string(raw), total, expected, m.Email).Scan(&id, &ref); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 201, M{"ok": true, "id": id, "ref": ref, "items": len(items), "total_cents": total})
}

// POST /v1/m/purchase-orders/{id}   {action: order|receive|cancel, location_id}
// Receiving adds every line to stock, at the location named or the main one.
func (s *Server) mPurchaseOrderAction(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	m := mc(r)
	id := chi.URLParam(r, "id")
	var req struct {
		Action     string `json:"action"`
		LocationID string `json:"location_id"`
	}
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, "invalid json")
		return
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	defer tx.Rollback(ctx)
	loc := locationFor(ctx, tx, m.BusinessID, req.LocationID)
	atLocation(ctx, tx, loc)
	var status, ref string
	var raw []byte
	if err := tx.QueryRow(ctx, `select status, ref, items from purchase_orders where id=$1 and business_id=$2 for update`, id, m.BusinessID).Scan(&status, &ref, &raw); err != nil {
		writeErr(w, 404, "order not found")
		return
	}
	switch req.Action {
	case "order":
		if status != "draft" {
			writeErr(w, 409, "only a draft can be marked as ordered")
			return
		}
		_, _ = tx.Exec(ctx, `update purchase_orders set status='ordered' where id=$1`, id)
	case "cancel":
		if status == "received" {
			writeErr(w, 409, "a received order cannot be cancelled")
			return
		}
		_, _ = tx.Exec(ctx, `update purchase_orders set status='cancelled' where id=$1`, id)
	case "receive":
		if status == "received" || status == "cancelled" {
			writeErr(w, 409, "this order is already closed")
			return
		}
		var items []poItem
		_ = json.Unmarshal(raw, &items)
		for _, it := range items {
			tag, _ := tx.Exec(ctx, `update products set stock = stock + $3, cost_cents = case when $4 > 0 then $4 else cost_cents end where id=$1 and business_id=$2`, it.ProductID, m.BusinessID, it.Qty, it.CostCents)
			if tag.RowsAffected() > 0 {
				_, _ = tx.Exec(ctx, `insert into stock_movements (business_id, product_id, delta, reason, note, actor, location_id) values ($1,$2,$3,'restock',$4,$5, nullif($6,'')::uuid)`, m.BusinessID, it.ProductID, it.Qty, "Received "+ref, m.Email, loc)
			}
		}
		_, _ = tx.Exec(ctx, `update purchase_orders set status='received', received_at=now() where id=$1`, id)
	default:
		writeErr(w, 400, "action must be order, receive or cancel")
		return
	}
	if err := tx.Commit(ctx); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, M{"ok": true})
}
