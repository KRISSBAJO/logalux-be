package httpapi

import (
	"errors"
	"net/http"
	"regexp"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// ---------- health and search ----------

// GET /v1/admin/health
func (s *Server) adminHealth(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	start := time.Now()
	dbErr := s.pool.Ping(ctx)
	dbMs := time.Since(start).Milliseconds()
	state := func(ok bool) string {
		if ok {
			return "ok"
		}
		return "down"
	}
	var failedPayouts, pendingFees, lockedAdmins int
	_ = s.pool.QueryRow(ctx, `select (select count(*) from payouts where status='failed'), (select count(*) from fees where status='pending'),
		(select count(*) from admin_users where locked_until > now())`).Scan(&failedPayouts, &pendingFees, &lockedAdmins)
	checks := []M{
		{"name": "Database", "detail": "Postgres ping", "state": state(dbErr == nil), "value": strconv.FormatInt(dbMs, 10) + " ms"},
		{"name": "Payments", "detail": "Stripe and Paystack keys", "state": map[bool]string{true: "ok", false: "warn"}[s.cfg.PaymentsMode() == "live"], "value": s.cfg.PaymentsMode()},
		{"name": "Messaging", "detail": "WhatsApp, SMS, email", "state": map[bool]string{true: "ok", false: "warn"}[s.cfg.MessagingMode() == "live"], "value": s.cfg.MessagingMode()},
		{"name": "Email", "detail": "Password resets, support replies, gift cards", "state": map[bool]string{true: "ok", false: "warn"}[s.mail.Mode() != "log"], "value": map[string]string{"resend": "Resend", "smtp": "SMTP", "log": "log only"}[s.mail.Mode()]},
		{"name": "Image storage", "detail": "S3 bucket for site images", "state": map[bool]string{true: "ok", false: "warn"}[s.store != nil], "value": map[bool]string{true: "connected", false: "not set up"}[s.store != nil]},
		{"name": "Payout runs", "detail": "Failed transfers waiting", "state": map[bool]string{true: "ok", false: "warn"}[failedPayouts == 0], "value": strconv.Itoa(failedPayouts) + " failed"},
		{"name": "Fee changes", "detail": "Waiting for a second approver", "state": map[bool]string{true: "ok", false: "warn"}[pendingFees == 0], "value": strconv.Itoa(pendingFees) + " pending"},
		{"name": "Admin accounts", "detail": "Locked after failed sign-ins", "state": map[bool]string{true: "ok", false: "warn"}[lockedAdmins == 0], "value": strconv.Itoa(lockedAdmins) + " locked"},
	}
	writeJSON(w, 200, M{"checks": checks, "env": s.cfg.Env})
}

// GET /v1/admin/search?q=
func (s *Server) adminSearch(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	q := r.URL.Query().Get("q")
	if len(q) < 2 {
		writeJSON(w, 200, M{"businesses": []M{}, "bookings": []M{}, "clients": []M{}, "disputes": []M{}, "orders": []M{}})
		return
	}
	biz, _ := rows(ctx, s.pool, `select id, slug, name, owner_name, market, status from businesses where name ilike '%'||$1||'%' or owner_name ilike '%'||$1||'%' or slug ilike $1||'%' or phone like '%'||$1||'%' limit 6`, q)
	bks, _ := rows(ctx, s.pool, `select bk.id, bk.client_name, bk.status, bk.starts_at, b.name as business from bookings bk join businesses b on b.id=bk.business_id
		where bk.client_name ilike '%'||$1||'%' or bk.client_phone like '%'||$1||'%' or bk.id::text ilike $1||'%' order by bk.starts_at desc limit 6`, q)
	cls, _ := rows(ctx, s.pool, `select c.id, c.name, c.phone, b.name as business from clients c join businesses b on b.id=c.business_id where c.name ilike '%'||$1||'%' or c.phone like '%'||$1||'%' limit 6`, q)
	dps, _ := rows(ctx, s.pool, `select d.id, d.ref, d.client_name, d.status, b.name as business from disputes d join businesses b on b.id=d.business_id where d.ref ilike '%'||$1||'%' or d.client_name ilike '%'||$1||'%' limit 6`, q)
	ords, _ := rows(ctx, s.pool, `select id, customer_name, status, total_cents, created_at from orders where customer_name ilike '%'||$1||'%' or customer_phone like '%'||$1||'%' or id::text ilike $1||'%' order by created_at desc limit 6`, q)
	writeJSON(w, 200, M{"businesses": biz, "bookings": bks, "clients": cls, "disputes": dps, "orders": ords})
}

// ---------- business detail and support tools ----------

// GET /v1/admin/businesses/{id}
func (s *Server) adminBusiness(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id := chi.URLParam(r, "id")
	b, err := row(ctx, s.pool, `select * from businesses where id = $1`, id)
	if err != nil {
		writeErr(w, 404, "business not found")
		return
	}
	locs, _ := rows(ctx, s.pool, `select id, name, address, city, region, country, county, timezone, is_primary, hours, arrival_notes, lat, lng, travels, travel_radius_km, position_source from locations where business_id=$1 order by is_primary desc`, id)
	staff, _ := rows(ctx, s.pool, `select id, name, initials, role, level, tone, bookable, rating from staff where business_id=$1 order by role='owner' desc, name`, id)
	svcs, _ := rows(ctx, s.pool, `select sv.id, sv.name, sv.category, sv.description, sv.duration_min, sv.processing_min, sv.buffer_min, sv.price_cents, sv.deposit_cents, sv.online, sv.sort,
		(select coalesce(array_agg(ss.staff_id::text), '{}') from staff_services ss where ss.service_id = sv.id) as staff_ids from services sv where sv.business_id=$1 order by sv.sort, sv.name`, id)
	bks, _ := rows(ctx, s.pool, `select bk.id, bk.status, bk.starts_at, bk.client_name, bk.total_cents, st.name as staff from bookings bk join staff st on st.id=bk.staff_id where bk.business_id=$1 order by bk.starts_at desc limit 15`, id)
	revs, _ := rows(ctx, s.pool, `select id, author_name, rating, body, status, flag_reason, created_at from reviews where business_id=$1 order by created_at desc limit 10`, id)
	dps, _ := rows(ctx, s.pool, `select id, ref, client_name, amount_cents, reason, status, created_at from disputes where business_id=$1 order by created_at desc`, id)
	pos, _ := rows(ctx, s.pool, `select id, amount_cents, currency, status, provider, reference, failure_reason, scheduled_for from payouts where business_id=$1 order by scheduled_for desc limit 10`, id)
	notes, _ := rows(ctx, s.pool, `select id, author, body, created_at from admin_notes where target_type='business' and target_id=$1 order by created_at desc`, id)
	creds, _ := rows(ctx, s.pool, `select id, client_phone, amount_cents, currency, reason, issued_by, created_at from credits where business_id=$1 order by created_at desc limit 10`, id)
	events, _ := rows(ctx, s.pool, `select actor, action, after, created_at from audit_log where target = $1 or target = $2 order by created_at desc limit 15`, b["name"], id)
	stats, _ := row(ctx, s.pool, `select
		(select count(*) from bookings where business_id=$1 and created_at > now() - interval '30 days') as bookings_30d,
		(select coalesce(sum(total_cents),0) from bookings where business_id=$1 and created_at > now() - interval '30 days' and status not in ('cancelled_client','cancelled_business','no_show')) as processed_30d_cents,
		(select count(*) from bookings where business_id=$1 and status='no_show') as no_shows,
		(select count(*) from clients where business_id=$1) as clients`, id)
	writeJSON(w, 200, M{"business": b, "stats": stats, "locations": locs, "staff": staff, "services": svcs, "bookings": bks, "reviews": revs, "disputes": dps, "payouts": pos, "notes": notes, "credits": creds, "events": events})
}

// PATCH /v1/admin/businesses/{id}  {plan}
func (s *Server) adminBusinessUpdate(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var req struct {
		Plan *string `json:"plan"`
	}
	if err := readJSON(r, &req); err != nil || req.Plan == nil || (*req.Plan != "free" && *req.Plan != "pro") {
		writeErr(w, 400, "plan must be free or pro")
		return
	}
	before, err := row(r.Context(), s.pool, `select name, plan from businesses where id=$1`, id)
	if err != nil {
		writeErr(w, 404, "business not found")
		return
	}
	if _, err := s.pool.Exec(r.Context(), `update businesses set plan=$2 where id=$1`, id, *req.Plan); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	s.audit(r, "business.plan", before["name"].(string), before, M{"plan": *req.Plan})
	writeJSON(w, 200, M{"ok": true})
}

// POST /v1/admin/businesses/{id}/payout-hold  {hold, reason}
func (s *Server) adminPayoutHold(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id := chi.URLParam(r, "id")
	var req struct {
		Hold   bool   `json:"hold"`
		Reason string `json:"reason"`
	}
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, "invalid json")
		return
	}
	if req.Hold && req.Reason == "" {
		writeErr(w, 400, "a reason is required to hold payouts")
		return
	}
	before, err := row(ctx, s.pool, `select name, payout_hold, payout_hold_reason from businesses where id=$1`, id)
	if err != nil {
		writeErr(w, 404, "business not found")
		return
	}
	reason := req.Reason
	if !req.Hold {
		reason = ""
	}
	if _, err := s.pool.Exec(ctx, `update businesses set payout_hold=$2, payout_hold_reason=$3 where id=$1`, id, req.Hold, reason); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	// Scheduled payouts follow the business-level hold.
	if req.Hold {
		_, _ = s.pool.Exec(ctx, `update payouts set status='held', failure_reason=$2 where business_id=$1 and status='scheduled'`, id, reason)
	} else {
		_, _ = s.pool.Exec(ctx, `update payouts set status='scheduled', failure_reason='' where business_id=$1 and status='held'`, id)
	}
	s.audit(r, "payout.hold", before["name"].(string), before, M{"hold": req.Hold, "reason": req.Reason})
	writeJSON(w, 200, M{"ok": true})
}

// POST /v1/admin/businesses/{id}/notes  {body}
func (s *Server) adminBusinessNote(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var req struct {
		Body string `json:"body"`
	}
	if err := readJSON(r, &req); err != nil || req.Body == "" {
		writeErr(w, 400, "body is required")
		return
	}
	if _, err := s.pool.Exec(r.Context(), `insert into admin_notes (target_type, target_id, author, body) values ('business', $1, $2, $3)`, id, s.actor(r), req.Body); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 201, M{"ok": true})
}

// Credit above this needs a super admin: $100, or ₦150,000.
var creditLimit = map[string]int{"USD": 10000, "NGN": 15000000}

// POST /v1/admin/businesses/{id}/credits  {amount_cents, reason, client_phone}
func (s *Server) adminCredit(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id := chi.URLParam(r, "id")
	var req struct {
		AmountCents int    `json:"amount_cents"`
		Reason      string `json:"reason"`
		ClientPhone string `json:"client_phone"`
	}
	if err := readJSON(r, &req); err != nil || req.AmountCents <= 0 || req.Reason == "" {
		writeErr(w, 400, "amount_cents and reason are required")
		return
	}
	b, err := row(ctx, s.pool, `select name, currency from businesses where id=$1`, id)
	if err != nil {
		writeErr(w, 404, "business not found")
		return
	}
	cur := b["currency"].(string)
	if req.AmountCents > creditLimit[cur] && currentAdmin(r).Role != "super_admin" {
		writeErr(w, 403, "credit above the limit needs a super admin")
		return
	}
	if _, err := s.pool.Exec(ctx, `insert into credits (business_id, client_phone, amount_cents, currency, reason, issued_by) values ($1,$2,$3,$4,$5,$6)`,
		id, req.ClientPhone, req.AmountCents, cur, req.Reason, s.actor(r)); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	s.audit(r, "credit.issue", b["name"].(string), nil, M{"amount_cents": req.AmountCents, "currency": cur, "reason": req.Reason, "client_phone": req.ClientPhone})
	writeJSON(w, 201, M{"ok": true})
}

// ---------- bookings ----------

// GET /v1/admin/bookings?q=&status=&business=&from=&to=
func (s *Server) adminBookings(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	out, pagination, err := s.adminRows(r, `
		select bk.id, bk.status, bk.starts_at, bk.ends_at, bk.client_name, bk.client_phone, bk.total_cents, bk.deposit_cents, bk.deposit_paid, bk.source, bk.notes, bk.created_at,
		       b.id as business_id, b.name as business, b.slug, b.currency, b.timezone, st.name as staff,
		       (select string_agg(name, ', ') from booking_items where booking_id = bk.id) as services
		from bookings bk join businesses b on b.id = bk.business_id join staff st on st.id = bk.staff_id
		where ($1 = '' or bk.client_name ilike '%'||$1||'%' or bk.client_phone like '%'||$1||'%' or bk.id::text ilike $1||'%' or b.name ilike '%'||$1||'%')
		  and ($2 = '' or bk.status = $2)
		  and ($3 = '' or b.slug = $3)
		  and (nullif($4,'') is null or bk.starts_at >= nullif($4,'')::date)
		  and (nullif($5,'') is null or bk.starts_at < nullif($5,'')::date + 1)
		order by bk.starts_at desc limit 200`, q.Get("q"), q.Get("status"), q.Get("business"), q.Get("from"), q.Get("to"))
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, M{"pagination": pagination, "bookings": out})
}

// POST /v1/admin/bookings/{id}/action  {action: cancel|no_show|complete|confirm|reschedule, starts_at, reason}
func (s *Server) adminBookingAction(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id := chi.URLParam(r, "id")
	var req struct {
		Action   string `json:"action"`
		StartsAt string `json:"starts_at"`
		Reason   string `json:"reason"`
	}
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, "invalid json")
		return
	}
	before, err := row(ctx, s.pool, `select bk.status, bk.starts_at, bk.ends_at, bk.client_name, bk.client_id, b.timezone from bookings bk join businesses b on b.id=bk.business_id where bk.id=$1`, id)
	if err != nil {
		writeErr(w, 404, "booking not found")
		return
	}
	switch req.Action {
	case "cancel", "no_show", "complete", "confirm":
		status := map[string]string{"cancel": "cancelled_business", "no_show": "no_show", "complete": "completed", "confirm": "confirmed"}[req.Action]
		if _, err := s.pool.Exec(ctx, `update bookings set status=$2 where id=$1`, id, status); err != nil {
			var pgErr *pgconn.PgError
			if errors.As(err, &pgErr) && pgErr.Code == "23P01" {
				writeErr(w, 409, "that staff member now has another booking at this time")
				return
			}
			writeErr(w, 500, err.Error())
			return
		}
		if req.Action == "no_show" && before["client_id"] != nil {
			_, _ = s.pool.Exec(ctx, `update clients set no_show_count = no_show_count + 1 where id=$1`, before["client_id"])
		}
		s.audit(r, "booking."+req.Action, id, M{"status": before["status"]}, M{"status": status, "reason": req.Reason})
	case "reschedule":
		// Accept RFC3339, or a local "2026-10-12T14:30" read in the business timezone.
		start, err := time.Parse(time.RFC3339, req.StartsAt)
		if err != nil {
			loc, lerr := time.LoadLocation(before["timezone"].(string))
			if lerr != nil {
				loc = time.UTC
			}
			start, err = time.ParseInLocation("2006-01-02T15:04", req.StartsAt, loc)
			if err != nil {
				writeErr(w, 400, "starts_at must be a date and time")
				return
			}
		}
		dur := before["ends_at"].(time.Time).Sub(before["starts_at"].(time.Time))
		// A move keeps the booking and its deposit; it is not a cancel plus rebook.
		_, err = s.pool.Exec(ctx, `update bookings set starts_at=$2, ends_at=$3, status = case when status in ('cancelled_client','cancelled_business','no_show') then 'confirmed' else status end where id=$1`, id, start, start.Add(dur))
		if err != nil {
			var pgErr *pgconn.PgError
			if errors.As(err, &pgErr) && pgErr.Code == "23P01" {
				writeErr(w, 409, "that staff member already has a booking at the new time")
				return
			}
			writeErr(w, 500, err.Error())
			return
		}
		s.audit(r, "booking.reschedule", id, M{"starts_at": before["starts_at"]}, M{"starts_at": start, "reason": req.Reason})
	default:
		writeErr(w, 400, "action must be cancel, no_show, complete, confirm or reschedule")
		return
	}
	writeJSON(w, 200, M{"ok": true})
}

// ---------- clients ----------

// GET /v1/admin/clients?q=&filter=blocked|no_show
func (s *Server) adminClients(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	out, pagination, err := s.adminRows(r, `
		select c.id, c.name, c.phone, c.tags, c.no_show_count, c.notes, c.created_at, b.id as business_id, b.name as business, b.slug, b.currency,
		  (select count(*) from bookings where client_id = c.id) as bookings,
		  (select coalesce(sum(total_cents),0) from bookings where client_id = c.id and status not in ('cancelled_client','cancelled_business','no_show')) as spent_cents,
		  (select max(starts_at) from bookings where client_id = c.id) as last_booking,
		  exists(select 1 from blocked_contacts bc where bc.phone = c.phone and c.phone <> '') as blocked
		from clients c join businesses b on b.id = c.business_id
		where ($1 = '' or c.name ilike '%'||$1||'%' or c.phone like '%'||$1||'%' or b.name ilike '%'||$1||'%')
		  and ($2 <> 'no_show' or c.no_show_count > 0)
		  and ($2 <> 'blocked' or exists(select 1 from blocked_contacts bc where bc.phone = c.phone))
		order by c.created_at desc limit 200`, q.Get("q"), q.Get("filter"))
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	blocked, _ := rows(r.Context(), s.pool, `select phone, reason, blocked_by, created_at from blocked_contacts order by created_at desc`)
	writeJSON(w, 200, M{"pagination": pagination, "clients": out, "blocked": blocked})
}

var phoneRe = regexp.MustCompile(`^\+?[0-9]{7,15}$`)

// POST /v1/admin/clients/block  {phone, blocked, reason}
func (s *Server) adminClientBlock(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Phone   string `json:"phone"`
		Blocked bool   `json:"blocked"`
		Reason  string `json:"reason"`
	}
	if err := readJSON(r, &req); err != nil || !phoneRe.MatchString(req.Phone) {
		writeErr(w, 400, "a valid phone number is required")
		return
	}
	if req.Blocked {
		if req.Reason == "" {
			writeErr(w, 400, "a reason is required to block")
			return
		}
		if _, err := s.pool.Exec(r.Context(), `insert into blocked_contacts (phone, reason, blocked_by) values ($1,$2,$3) on conflict (phone) do update set reason=excluded.reason, blocked_by=excluded.blocked_by`, req.Phone, req.Reason, s.actor(r)); err != nil {
			writeErr(w, 500, err.Error())
			return
		}
	} else {
		_, _ = s.pool.Exec(r.Context(), `delete from blocked_contacts where phone=$1`, req.Phone)
	}
	s.audit(r, map[bool]string{true: "client.block", false: "client.unblock"}[req.Blocked], req.Phone, nil, M{"reason": req.Reason})
	writeJSON(w, 200, M{"ok": true})
}

// ---------- orders ----------

// GET /v1/admin/orders?q=&status=
func (s *Server) adminOrders(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	out, pagination, err := s.adminRows(r, `
		select o.id, o.customer_name, o.customer_phone, o.status, o.fulfilment, o.subtotal_cents, o.shipping_cents, o.tax_cents, o.discount_cents, o.promo_code, o.gift_cents, o.gift_code, o.total_cents, o.address, o.created_at,
		  (select string_agg(oi.qty || ' × ' || oi.name || case when oi.size_label <> '' then ' (' || oi.size_label || ')' else '' end, ', ') from order_items oi where oi.order_id = o.id) as items,
		  (select string_agg(distinct oi.seller_name, ', ') from order_items oi where oi.order_id = o.id) as sellers
		from orders o
		where ($1 = '' or o.customer_name ilike '%'||$1||'%' or o.customer_phone like '%'||$1||'%' or o.id::text ilike $1||'%')
		  and ($2 = '' or o.status = $2)
		order by o.created_at desc limit 200`, q.Get("q"), q.Get("status"))
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, M{"pagination": pagination, "orders": out})
}

// POST /v1/admin/orders/{id}/status  {status, reason}
func (s *Server) adminOrderStatus(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id := chi.URLParam(r, "id")
	var req struct {
		Status string `json:"status"`
		Reason string `json:"reason"`
	}
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, "invalid json")
		return
	}
	switch req.Status {
	case "paid", "ready", "shipped", "delivered", "cancelled", "refunded":
	default:
		writeErr(w, 400, "status must be paid, ready, shipped, delivered, cancelled or refunded")
		return
	}
	before, err := row(ctx, s.pool, `select status from orders where id=$1`, id)
	if err != nil {
		writeErr(w, 404, "order not found")
		return
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `update orders set status=$2 where id=$1`, id, req.Status); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	// Cancelling or refunding puts the stock back, once.
	wasOpen := before["status"] != "cancelled" && before["status"] != "refunded"
	if wasOpen && (req.Status == "cancelled" || req.Status == "refunded") {
		if _, err := tx.Exec(ctx, `update products p set stock = p.stock + oi.qty, sold = greatest(p.sold - oi.qty, 0) from order_items oi where oi.order_id=$1 and oi.product_id = p.id`, id); err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		// Money that came off a gift card goes back onto that card.
		if _, err := tx.Exec(ctx, `with spent as (select gift_card_id, -sum(amount_cents) as cents from gift_card_txns where order_id=$1 group by gift_card_id having sum(amount_cents) < 0),
			back as (update gift_cards g set balance_cents = g.balance_cents + spent.cents from spent where g.id = spent.gift_card_id and g.status = 'active' returning g.id, spent.cents)
			insert into gift_card_txns (gift_card_id, order_id, amount_cents, note, actor) select id, $1, cents, 'Order ' || $2 || ', returned to the card', $3 from back`, id, req.Status, s.actor(r)); err != nil {
			writeErr(w, 500, err.Error())
			return
		}
	}
	if err := tx.Commit(ctx); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	s.audit(r, "order.status", id, before, M{"status": req.Status, "reason": req.Reason})
	writeJSON(w, 200, M{"ok": true})
}

// ---------- products ----------

// GET /v1/admin/products
func (s *Server) adminProducts(w http.ResponseWriter, r *http.Request) {
	out, err := rows(r.Context(), s.pool, `select p.id, p.slug, p.name, p.seller_name, p.category, p.description, p.how_to_use, p.price_cents, p.compare_cents, p.stock, p.sold, p.rating, p.review_count, p.active, p.tone, p.tags, p.sizes, p.pickup, p.shipping, p.shipping_cents, b.slug as business_slug
		from products p left join businesses b on b.id = p.business_id order by p.active desc, p.sold desc`)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, M{"products": out})
}

// POST /v1/admin/products/{id}/active  {active, reason}
func (s *Server) adminProductActive(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var req struct {
		Active bool   `json:"active"`
		Reason string `json:"reason"`
	}
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, "invalid json")
		return
	}
	before, err := row(r.Context(), s.pool, `select name, active from products where id=$1`, id)
	if err != nil {
		writeErr(w, 404, "product not found")
		return
	}
	if _, err := s.pool.Exec(r.Context(), `update products set active=$2 where id=$1`, id, req.Active); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	s.audit(r, "product.active", before["name"].(string), before, M{"active": req.Active, "reason": req.Reason})
	writeJSON(w, 200, M{"ok": true})
}

// ---------- payouts and payments ----------

// GET /v1/admin/payouts?status=
func (s *Server) adminPayouts(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	status := r.URL.Query().Get("status")
	out, pagination, err := s.adminRows(r, `select p.id, p.amount_cents, p.currency, p.status, p.provider, p.reference, p.failure_reason, p.scheduled_for, p.paid_at,
		b.id as business_id, b.name as business, b.slug, b.market, b.payout_hold
		from payouts p join businesses b on b.id = p.business_id where ($1 = '' or p.status = $1) order by p.status = 'failed' desc, p.scheduled_for desc limit 200`, status)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	totals, _ := rows(ctx, s.pool, `select currency, status, count(*) as n, coalesce(sum(amount_cents),0) as cents from payouts group by currency, status order by currency, status`)
	events, _ := rows(ctx, s.pool, `select pe.id, pe.provider, pe.kind, pe.amount_cents, pe.currency, pe.status, pe.reference, pe.created_at, pe.booking_id, pe.order_id from payment_events pe order by pe.created_at desc limit 50`)
	writeJSON(w, 200, M{"pagination": pagination, "payouts": out, "totals": totals, "payments": events})
}

// POST /v1/admin/payouts/{id}/action  {action: retry|hold|release|mark_paid, reason}
func (s *Server) adminPayoutAction(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id := chi.URLParam(r, "id")
	var req struct {
		Action string `json:"action"`
		Reason string `json:"reason"`
	}
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, "invalid json")
		return
	}
	before, err := row(ctx, s.pool, `select p.status, p.failure_reason, b.name, b.payout_hold from payouts p join businesses b on b.id=p.business_id where p.id=$1`, id)
	if err != nil {
		writeErr(w, 404, "payout not found")
		return
	}
	cur := before["status"].(string)
	var sql string
	switch req.Action {
	case "retry":
		if cur != "failed" {
			writeErr(w, 400, "only a failed payout can be retried")
			return
		}
		if before["payout_hold"] == true {
			writeErr(w, 409, "this business has a payout hold; release it first")
			return
		}
		sql = `update payouts set status='scheduled', failure_reason='', scheduled_for=current_date where id=$1`
	case "hold":
		if cur != "scheduled" && cur != "failed" {
			writeErr(w, 400, "only a scheduled or failed payout can be held")
			return
		}
		if req.Reason == "" {
			writeErr(w, 400, "a reason is required to hold a payout")
			return
		}
		sql = `update payouts set status='held', failure_reason=$2 where id=$1`
	case "release":
		if cur != "held" {
			writeErr(w, 400, "only a held payout can be released")
			return
		}
		sql = `update payouts set status='scheduled', failure_reason='' where id=$1`
	case "mark_paid":
		if cur == "paid" {
			writeErr(w, 400, "already paid")
			return
		}
		sql = `update payouts set status='paid', failure_reason='', paid_at=now(), reference = case when reference='' then 'manual' else reference end where id=$1`
	default:
		writeErr(w, 400, "action must be retry, hold, release or mark_paid")
		return
	}
	if req.Action == "hold" {
		_, err = s.pool.Exec(ctx, sql, id, req.Reason)
	} else {
		_, err = s.pool.Exec(ctx, sql, id)
	}
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	s.audit(r, "payout."+req.Action, before["name"].(string), M{"status": cur}, M{"payout": id, "reason": req.Reason})
	writeJSON(w, 200, M{"ok": true})
}

// ---------- fees: propose, approve, reject ----------

// GET /v1/admin/fees
func (s *Server) adminFees(w http.ResponseWriter, r *http.Request) {
	out, err := rows(r.Context(), s.pool, `select *, (status='approved' and effective_from <= current_date) as in_effect from fees order by market, plan, effective_from desc`)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, M{"fees": out})
}

type feeReq struct {
	Market                string  `json:"market"`
	Plan                  string  `json:"plan"`
	TransactionPct        float64 `json:"transaction_pct"`
	TransactionFixedCents int     `json:"transaction_fixed_cents"`
	TransactionCapCents   *int    `json:"transaction_cap_cents"`
	NewClientPct          float64 `json:"new_client_pct"`
	InstantPayoutPct      float64 `json:"instant_payout_pct"`
	MarketplacePct        float64 `json:"marketplace_pct"`
	ChargebackCents       int     `json:"chargeback_cents"`
	PlanPriceCents        int     `json:"plan_price_cents"`
	EffectiveFrom         string  `json:"effective_from"`
	Note                  string  `json:"note"`
}

// POST /v1/admin/fees — proposes a change. It takes effect only after a different admin approves it.
func (s *Server) adminFeesPropose(w http.ResponseWriter, r *http.Request) {
	var req feeReq
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, "invalid json: "+err.Error())
		return
	}
	if (req.Market != "US" && req.Market != "NG") || (req.Plan != "free" && req.Plan != "pro") {
		writeErr(w, 400, "market must be US or NG and plan must be free or pro")
		return
	}
	day, err := time.Parse("2006-01-02", req.EffectiveFrom)
	if err != nil || day.Before(time.Now().Truncate(24*time.Hour)) {
		writeErr(w, 400, "effective_from must be today or later, as YYYY-MM-DD")
		return
	}
	for _, p := range []float64{req.TransactionPct, req.NewClientPct, req.InstantPayoutPct, req.MarketplacePct} {
		if p < 0 || p > 50 {
			writeErr(w, 400, "percentages must be between 0 and 50")
			return
		}
	}
	_, err = s.pool.Exec(r.Context(), `insert into fees (market, plan, transaction_pct, transaction_fixed_cents, transaction_cap_cents, new_client_pct, instant_payout_pct, marketplace_pct, chargeback_cents, plan_price_cents, effective_from, status, proposed_by, approved_by, note)
		values ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,'pending',$12,null,$13)`,
		req.Market, req.Plan, req.TransactionPct, req.TransactionFixedCents, req.TransactionCapCents, req.NewClientPct, req.InstantPayoutPct, req.MarketplacePct, req.ChargebackCents, req.PlanPriceCents, req.EffectiveFrom, s.actor(r), req.Note)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			writeErr(w, 409, "a fee row for that market, plan and date already exists")
			return
		}
		writeErr(w, 500, err.Error())
		return
	}
	s.audit(r, "fees.propose", req.Market+"/"+req.Plan+"@"+req.EffectiveFrom, nil, req)
	writeJSON(w, 201, M{"ok": true})
}

// POST /v1/admin/fees/decide  {market, plan, effective_from, decision: approve|reject}
func (s *Server) adminFeesDecide(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var req struct {
		Market        string `json:"market"`
		Plan          string `json:"plan"`
		EffectiveFrom string `json:"effective_from"`
		Decision      string `json:"decision"`
	}
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, "invalid json")
		return
	}
	before, err := row(ctx, s.pool, `select status, proposed_by, transaction_pct, new_client_pct from fees where market=$1 and plan=$2 and effective_from=$3::date`, req.Market, req.Plan, req.EffectiveFrom)
	if err != nil {
		writeErr(w, 404, "fee change not found")
		return
	}
	if before["status"] != "pending" {
		writeErr(w, 400, "that fee change is not pending")
		return
	}
	target := req.Market + "/" + req.Plan + "@" + req.EffectiveFrom
	switch req.Decision {
	case "approve":
		if before["proposed_by"] == s.actor(r) {
			writeErr(w, 403, "a fee change must be approved by a different admin than the one who proposed it")
			return
		}
		if _, err := s.pool.Exec(ctx, `update fees set status='approved', approved_by=$4 where market=$1 and plan=$2 and effective_from=$3::date`, req.Market, req.Plan, req.EffectiveFrom, s.actor(r)); err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		s.audit(r, "fees.approve", target, before, M{"status": "approved"})
	case "reject":
		if _, err := s.pool.Exec(ctx, `delete from fees where market=$1 and plan=$2 and effective_from=$3::date and status='pending'`, req.Market, req.Plan, req.EffectiveFrom); err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		s.audit(r, "fees.reject", target, before, nil)
	default:
		writeErr(w, 400, "decision must be approve or reject")
		return
	}
	writeJSON(w, 200, M{"ok": true})
}

// ---------- flags: create and delete ----------

var flagKeyRe = regexp.MustCompile(`^[a-z][a-z0-9_]{2,48}$`)

// POST /v1/admin/flags
func (s *Server) adminFlagCreate(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Key         string  `json:"key"`
		Name        string  `json:"name"`
		Description string  `json:"description"`
		Market      *string `json:"market"`
		Plan        *string `json:"plan"`
	}
	if err := readJSON(r, &req); err != nil || req.Name == "" || !flagKeyRe.MatchString(req.Key) {
		writeErr(w, 400, "name is required and key must be lowercase letters, digits and underscores")
		return
	}
	if req.Market != nil && *req.Market == "" {
		req.Market = nil
	}
	if req.Plan != nil && *req.Plan == "" {
		req.Plan = nil
	}
	// New flags start off at 0%. Turning one on is a separate, audited step.
	if _, err := s.pool.Exec(r.Context(), `insert into feature_flags (key, name, description, market, plan, rollout_pct, enabled) values ($1,$2,$3,$4,$5,0,false)`, req.Key, req.Name, req.Description, req.Market, req.Plan); err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			writeErr(w, 409, "a flag with that key already exists")
			return
		}
		writeErr(w, 500, err.Error())
		return
	}
	s.audit(r, "flag.create", req.Key, nil, req)
	writeJSON(w, 201, M{"ok": true})
}

// DELETE /v1/admin/flags/{key}
func (s *Server) adminFlagDelete(w http.ResponseWriter, r *http.Request) {
	key := chi.URLParam(r, "key")
	before, err := row(r.Context(), s.pool, `select name, enabled, rollout_pct from feature_flags where key=$1`, key)
	if err != nil {
		writeErr(w, 404, "flag not found")
		return
	}
	if before["enabled"] == true {
		writeErr(w, 400, "turn the flag off before deleting it")
		return
	}
	_, _ = s.pool.Exec(r.Context(), `delete from feature_flags where key=$1`, key)
	s.audit(r, "flag.delete", key, before, nil)
	writeJSON(w, 200, M{"ok": true})
}

// ---------- audit ----------

// GET /v1/admin/audit?q=&actor=&action=
func (s *Server) adminAudit(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	out, pagination, err := s.adminRows(r, `select id, actor, action, target, before, after, created_at from audit_log
		where ($1 = '' or target ilike '%'||$1||'%' or action ilike '%'||$1||'%')
		  and ($2 = '' or actor = $2)
		  and ($3 = '' or action like $3||'%')
		order by created_at desc, id desc limit 200`, q.Get("q"), q.Get("actor"), q.Get("action"))
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	actors, _ := rows(r.Context(), s.pool, `select actor, count(*) as n from audit_log group by actor order by n desc`)
	writeJSON(w, 200, M{"pagination": pagination, "events": out, "actors": actors})
}
