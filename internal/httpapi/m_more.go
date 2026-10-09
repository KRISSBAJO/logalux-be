package httpapi

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
)

// Stock per location, loyalty points, a business's own promo codes, monthly
// statements and the storefront logo.

// ---------- stock per location ----------

// atLocation names the location that stock changes in this transaction belong to.
// The database then keeps that location's count in step with the total.
func atLocation(ctx context.Context, tx pgx.Tx, locationID string) {
	_, _ = tx.Exec(ctx, `select set_config('lx.location', $1, true)`, locationID)
}

// locationFor returns a location of the business: the one asked for when it is theirs, else the main one.
func locationFor(ctx context.Context, q queryer, businessID, wanted string) string {
	var id string
	_ = q.QueryRow(ctx, `select id::text from locations where business_id=$1 order by (id::text = $2) desc, is_primary desc limit 1`, businessID, wanted).Scan(&id)
	return id
}

// POST /v1/m/products/{id}/transfer   {from_location_id, to_location_id, qty, note}
func (s *Server) mProductTransfer(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	m := mc(r)
	id := chi.URLParam(r, "id")
	var req struct {
		From string `json:"from_location_id"`
		To   string `json:"to_location_id"`
		Qty  int    `json:"qty"`
		Note string `json:"note"`
	}
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, "invalid json")
		return
	}
	if req.Qty <= 0 || req.From == req.To {
		writeErr(w, 400, "choose two different locations and how many to move")
		return
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	defer tx.Rollback(ctx)
	var name, fromName, toName string
	if err := tx.QueryRow(ctx, `select p.name, (select name from locations where id::text=$3 and business_id=$2), (select name from locations where id::text=$4 and business_id=$2)
		from products p where p.id=$1 and p.business_id=$2 for update`, id, m.BusinessID, req.From, req.To).Scan(&name, &fromName, &toName); err != nil || fromName == "" || toName == "" {
		writeErr(w, 404, "the product or one of the locations was not found")
		return
	}
	var have int
	_ = tx.QueryRow(ctx, `select qty from location_stock where product_id=$1 and location_id=$2 for update`, id, req.From).Scan(&have)
	if have < req.Qty {
		writeErr(w, 409, "only "+itoa(have)+" of "+name+" at "+fromName)
		return
	}
	if _, err := tx.Exec(ctx, `update location_stock set qty = qty - $3 where product_id=$1 and location_id=$2`, id, req.From, req.Qty); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if _, err := tx.Exec(ctx, `insert into location_stock (product_id, location_id, qty) values ($1,$2,$3) on conflict (product_id, location_id) do update set qty = location_stock.qty + excluded.qty`, id, req.To, req.Qty); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	note := strings.TrimSpace(req.Note)
	_, _ = tx.Exec(ctx, `insert into stock_movements (business_id, product_id, delta, reason, note, actor, location_id) values ($1,$2,$3,'transfer',$4,$5,$6), ($1,$2,$7,'transfer',$8,$5,$9)`,
		m.BusinessID, id, -req.Qty, strings.TrimSpace("To "+toName+". "+note), m.Email, req.From, req.Qty, strings.TrimSpace("From "+fromName+". "+note), req.To)
	if err := tx.Commit(ctx); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, M{"ok": true})
}

// ---------- loyalty points ----------

type loyaltyRules struct {
	Enabled    bool `json:"enabled"`
	EarnPoints int  `json:"earn_points"`       // points given...
	PerCents   int  `json:"per_cents"`         // ...for each of this much spent
	PointValue int  `json:"point_value_cents"` // what one point is worth when it is spent
	MinRedeem  int  `json:"min_redeem"`        // the fewest points that can be spent at once
}

func loyaltyFor(ctx context.Context, q queryer, businessID string) loyaltyRules {
	var raw []byte
	var currency string
	_ = q.QueryRow(ctx, `select coalesce(settings->'loyalty','{}'::jsonb), currency from businesses where id=$1`, businessID).Scan(&raw, &currency)
	// Off until the business switches it on. The defaults are one point per unit of currency spent, and 100 points for 5 units off.
	l := loyaltyRules{EarnPoints: 1, PerCents: 100, PointValue: 5, MinRedeem: 100}
	if currency == "NGN" {
		l = loyaltyRules{EarnPoints: 1, PerCents: 100000, PointValue: 5000, MinRedeem: 100} // a point per ₦1,000; 100 points for ₦5,000
	}
	_ = json.Unmarshal(raw, &l)
	return l
}

func pointsOf(ctx context.Context, q queryer, businessID, clientID string) int {
	var n int
	_ = q.QueryRow(ctx, `select coalesce(sum(points),0) from loyalty_points where business_id=$1 and client_id=$2`, businessID, clientID).Scan(&n)
	return n
}

// GET /v1/m/loyalty
func (s *Server) mLoyalty(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	m := mc(r)
	rules := loyaltyFor(ctx, s.pool, m.BusinessID)
	kpis, _ := row(ctx, s.pool, `with bal as (select client_id, sum(points) as pts from loyalty_points where business_id=$1 group by client_id)
		select (select count(*) from bal where pts > 0) as members, (select coalesce(sum(pts),0) from bal where pts > 0)::int as outstanding,
		(select coalesce(sum(points),0) from loyalty_points where business_id=$1 and reason='earn' and created_at > now() - interval '30 days')::int as earned_30d,
		(select coalesce(-sum(points),0) from loyalty_points where business_id=$1 and reason='redeem' and created_at > now() - interval '30 days')::int as redeemed_30d,
		(select count(distinct sale_id) from loyalty_points where business_id=$1 and reason='redeem' and created_at > now() - interval '30 days') as redemptions_30d`, m.BusinessID)
	top, loyalty_clientsPage, loyalty_clientsErr := s.historyRows(r, "loyalty_clients", `select c.id, c.name, c.phone, sum(lp.points)::int as points,
		coalesce(sum(lp.points) filter (where lp.reason='earn'),0)::int as earned, coalesce(-sum(lp.points) filter (where lp.reason='redeem'),0)::int as redeemed, max(lp.created_at) as last_at
		from loyalty_points lp join clients c on c.id = lp.client_id and c.business_id = lp.business_id where lp.business_id=$1 group by c.id having sum(lp.points) <> 0 or count(*) > 0 order by sum(lp.points) desc`, "points desc", "name points earned redeemed last_at", m.BusinessID)
	if loyalty_clientsErr != nil {
		writeErr(w, 500, loyalty_clientsErr.Error())
		return
	}
	recent, recentPage, recentErr := s.historyRows(r, "recent", `select lp.id, lp.points, lp.reason, lp.note, lp.actor, lp.created_at, c.name as client, lp.client_id from loyalty_points lp join clients c on c.id = lp.client_id and c.business_id = lp.business_id where lp.business_id=$1 order by lp.created_at desc`, "created_at desc", "created_at points reason client actor", m.BusinessID)
	if recentErr != nil {
		writeErr(w, 500, recentErr.Error())
		return
	}
	writeJSON(w, 200, M{"rules": rules, "kpis": kpis, "clients": top, "loyalty_clients_pagination": loyalty_clientsPage, "recent": recent, "recent_pagination": recentPage})
}

// PUT /v1/m/loyalty/settings   {enabled, earn_points, per_cents, point_value_cents, min_redeem}
func (s *Server) mLoyaltySettings(w http.ResponseWriter, r *http.Request) {
	var req loyaltyRules
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, "invalid json")
		return
	}
	switch {
	case req.EarnPoints < 1 || req.EarnPoints > 1000:
		writeErr(w, 400, "points earned must be between 1 and 1,000")
	case req.PerCents < 1:
		writeErr(w, 400, "say how much a client spends to earn those points")
	case req.PointValue < 1:
		writeErr(w, 400, "say what one point is worth")
	case req.MinRedeem < 1 || req.MinRedeem > 100000:
		writeErr(w, 400, "the fewest points that can be spent must be at least 1")
	case float64(req.EarnPoints)*float64(req.PointValue) > float64(req.PerCents)/2:
		// Giving back more than half of every sale is almost certainly a typing slip.
		writeErr(w, 400, "those numbers give back more than half of every sale; check them")
	default:
		raw, _ := json.Marshal(req)
		if _, err := s.pool.Exec(r.Context(), `update businesses set settings = jsonb_set(coalesce(settings,'{}'::jsonb), '{loyalty}', $2::jsonb) where id=$1`, mc(r).BusinessID, string(raw)); err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		writeJSON(w, 200, M{"ok": true})
	}
}

// POST /v1/m/loyalty/adjust   {client_id, points, note}   add or take away points by hand
func (s *Server) mLoyaltyAdjust(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	m := mc(r)
	var req struct {
		ClientID string `json:"client_id"`
		Points   int    `json:"points"`
		Note     string `json:"note"`
	}
	if err := readJSON(r, &req); err != nil || req.Points == 0 || req.Points > 100000 || req.Points < -100000 || len(strings.TrimSpace(req.Note)) < 3 {
		writeErr(w, 400, "enter the points to add or take away, and a short reason")
		return
	}
	var ok bool
	_ = s.pool.QueryRow(ctx, `select exists(select 1 from clients where id=$1 and business_id=$2)`, req.ClientID, m.BusinessID).Scan(&ok)
	if !ok {
		writeErr(w, 404, "client not found")
		return
	}
	if have := pointsOf(ctx, s.pool, m.BusinessID, req.ClientID); have+req.Points < 0 {
		writeErr(w, 409, "that would take the balance below zero; they have "+itoa(have)+" points")
		return
	}
	_, _ = s.pool.Exec(ctx, `insert into loyalty_points (business_id, client_id, points, reason, note, actor) values ($1,$2,$3,'adjust',$4,$5)`, m.BusinessID, req.ClientID, req.Points, strings.TrimSpace(req.Note), m.Email)
	writeJSON(w, 200, M{"ok": true, "points": pointsOf(ctx, s.pool, m.BusinessID, req.ClientID)})
}

// ---------- a business's own promo codes ----------

var promoCodeRe = regexp.MustCompile(`^[A-Z0-9]{4,20}$`)

// GET /v1/m/promos
func (s *Server) mPromos(w http.ResponseWriter, r *http.Request) {
	m := mc(r)
	out, err := rows(r.Context(), s.pool, `select pc.id, pc.code, pc.description, pc.kind, pc.value, pc.currency, pc.min_cents, pc.max_uses, pc.used, pc.starts_at, pc.ends_at, pc.active, pc.created_by, pc.created_at,
		(select coalesce(sum(bk.discount_cents),0) from bookings bk where bk.business_id=$1 and bk.promo_code = pc.code)::int + (select coalesce(sum(sa.discount_cents),0) from sales sa where sa.business_id=$1 and sa.promo_code = pc.code and (sa.booking_id is null or not exists (select 1 from bookings b2 where b2.id = sa.booking_id and b2.promo_code = pc.code)))::int as given_cents,
		(select coalesce(sum(bk.total_cents),0) from bookings bk where bk.business_id=$1 and bk.promo_code = pc.code and bk.status not in ('cancelled_client','cancelled_business','no_show'))::int as booked_cents
		from promo_codes pc where pc.business_id=$1 order by pc.active desc, pc.created_at desc`, m.BusinessID)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, M{"promos": out, "booking_link": strings.TrimRight(s.cfg.WebURL, "/") + "/b/" + m.Slug})
}

// POST /v1/m/promos   {code, description, kind: percent|fixed, value, min_cents, max_uses, starts_at, ends_at}
func (s *Server) mPromoCreate(w http.ResponseWriter, r *http.Request) {
	m := mc(r)
	var req struct {
		Code        string `json:"code"`
		Description string `json:"description"`
		Kind        string `json:"kind"`
		Value       int    `json:"value"` // a percentage, or minor units when fixed
		MinCents    int    `json:"min_cents"`
		MaxUses     *int   `json:"max_uses"`
		StartsAt    string `json:"starts_at"`
		EndsAt      string `json:"ends_at"`
	}
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, "invalid json")
		return
	}
	req.Code = strings.ToUpper(strings.TrimSpace(req.Code))
	day := func(v string, end bool) (*time.Time, bool) {
		if v == "" {
			return nil, true
		}
		t, err := time.ParseInLocation("2006-01-02", v, m.Loc)
		if err != nil {
			return nil, false
		}
		if end {
			t = t.Add(24*time.Hour - time.Second)
		}
		return &t, true
	}
	starts, ok1 := day(req.StartsAt, false)
	ends, ok2 := day(req.EndsAt, true)
	switch {
	case !promoCodeRe.MatchString(req.Code):
		writeErr(w, 400, "a code is 4 to 20 letters and numbers, with no spaces")
		return
	case req.Kind != "percent" && req.Kind != "fixed":
		writeErr(w, 400, "choose a percentage or a fixed amount")
		return
	case req.Value <= 0 || (req.Kind == "percent" && req.Value > 100):
		writeErr(w, 400, "the discount must be more than zero, and a percentage at most 100")
		return
	case req.MinCents < 0 || (req.MaxUses != nil && *req.MaxUses < 1):
		writeErr(w, 400, "the minimum spend and the number of uses cannot be negative")
		return
	case !ok1 || !ok2 || (starts != nil && ends != nil && ends.Before(*starts)):
		writeErr(w, 400, "check the first and last day")
		return
	}
	var id string
	err := s.pool.QueryRow(r.Context(), `insert into promo_codes (code, description, kind, value, currency, applies_to, min_cents, max_uses, starts_at, ends_at, active, created_by, business_id)
		values ($1,$2,$3,$4,$5,'bookings',$6,$7,$8,$9,true,$10,$11) returning id::text`, req.Code, strings.TrimSpace(req.Description), req.Kind, req.Value, m.Currency, req.MinCents, req.MaxUses, starts, ends, m.Email, m.BusinessID).Scan(&id)
	if err != nil {
		if isCode(err, "23505") {
			writeErr(w, 409, "that code is already taken; choose another")
			return
		}
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 201, M{"ok": true, "id": id})
}

// PUT /v1/m/promos/{id}   any of {active, description, min_cents, max_uses, no_limit, starts_at, ends_at, kind, value}
// The code itself never changes. The discount (kind, value) can change only while nobody has used the code.
// DELETE /v1/m/promos/{id}   only a code nobody has used
func (s *Server) mPromoUpdate(w http.ResponseWriter, r *http.Request) {
	m := mc(r)
	var req struct {
		Active      *bool   `json:"active"`
		Description *string `json:"description"`
		MinCents    *int    `json:"min_cents"`
		MaxUses     *int    `json:"max_uses"`
		NoLimit     bool    `json:"no_limit"`  // take the limit on uses away
		StartsAt    *string `json:"starts_at"` // "" takes the date away
		EndsAt      *string `json:"ends_at"`
		Kind        *string `json:"kind"`
		Value       *int    `json:"value"`
	}
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, "invalid json")
		return
	}
	ctx := r.Context()
	id := chi.URLParam(r, "id")
	var kind string
	var value, used int
	var starts, ends *time.Time
	if err := s.pool.QueryRow(ctx, `select kind, value, used, starts_at, ends_at from promo_codes where id::text=$1 and business_id=$2`, id, m.BusinessID).Scan(&kind, &value, &used, &starts, &ends); err != nil {
		writeErr(w, 404, "code not found")
		return
	}
	day := func(v *string, end bool, was *time.Time) (*time.Time, bool) {
		if v == nil {
			return was, true
		}
		if *v == "" {
			return nil, true
		}
		t, err := time.ParseInLocation("2006-01-02", *v, m.Loc)
		if err != nil {
			return nil, false
		}
		if end {
			t = t.Add(24*time.Hour - time.Second)
		}
		return &t, true
	}
	newStarts, ok1 := day(req.StartsAt, false, starts)
	newEnds, ok2 := day(req.EndsAt, true, ends)
	changesDiscount := (req.Kind != nil && *req.Kind != kind) || (req.Value != nil && *req.Value != value)
	if req.Kind != nil {
		kind = *req.Kind
	}
	if req.Value != nil {
		value = *req.Value
	}
	switch {
	case changesDiscount && used > 0:
		writeErr(w, 409, "this code has been used, so its discount cannot change; switch it off and make a new one")
		return
	case kind != "percent" && kind != "fixed":
		writeErr(w, 400, "choose a percentage or a fixed amount")
		return
	case value <= 0 || (kind == "percent" && value > 100):
		writeErr(w, 400, "the discount must be more than zero, and a percentage at most 100")
		return
	case (req.MinCents != nil && *req.MinCents < 0) || (req.MaxUses != nil && *req.MaxUses < 1):
		writeErr(w, 400, "the minimum spend and the number of uses cannot be negative")
		return
	case req.MaxUses != nil && *req.MaxUses < used:
		writeErr(w, 400, "the code has already been used "+itoa(used)+" times; the limit cannot be lower than that")
		return
	case !ok1 || !ok2 || (newStarts != nil && newEnds != nil && newEnds.Before(*newStarts)):
		writeErr(w, 400, "check the first and last day")
		return
	}
	if req.Description != nil {
		d := strings.TrimSpace(*req.Description)
		req.Description = &d
	}
	if _, err := s.pool.Exec(ctx, `update promo_codes set active = coalesce($3, active), description = coalesce($4, description), min_cents = coalesce($5, min_cents),
		max_uses = case when $7 then null else coalesce($6, max_uses) end, starts_at = $8, ends_at = $9, kind = $10, value = $11 where id::text=$1 and business_id=$2`,
		id, m.BusinessID, req.Active, req.Description, req.MinCents, req.MaxUses, req.NoLimit, newStarts, newEnds, kind, value); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, M{"ok": true})
}

func (s *Server) mPromoDelete(w http.ResponseWriter, r *http.Request) {
	tag, err := s.pool.Exec(r.Context(), `delete from promo_codes where id=$1 and business_id=$2 and used = 0`, chi.URLParam(r, "id"), mc(r).BusinessID)
	if err != nil || tag.RowsAffected() == 0 {
		writeErr(w, 409, "a code that has been used cannot be deleted; switch it off instead")
		return
	}
	writeJSON(w, 200, M{"ok": true})
}

// ---------- statements ----------

// monthRange turns "2026-10" into the first moment of that month and of the next, in the business's time.
func monthRange(v string, loc *time.Location) (time.Time, time.Time, bool) {
	t, err := time.ParseInLocation("2006-01", v, loc)
	if err != nil {
		return time.Time{}, time.Time{}, false
	}
	return t, t.AddDate(0, 1, 0), true
}

const statementSums = `
	coalesce(sum(amount_cents) filter (where kind = 'charge'),0)::int as charges_cents,
	coalesce(sum(amount_cents) filter (where kind = 'deposit' and status <> 'held'),0)::int as deposits_cents,
	coalesce(sum(amount_cents) filter (where kind = 'tip'),0)::int as tips_cents,
	coalesce(sum(amount_cents) filter (where kind = 'refund'),0)::int as refunds_cents,
	coalesce(sum(amount_cents) filter (where kind = 'fee'),0)::int as fees_cents,
	coalesce(sum(amount_cents) filter (where kind = 'lead_fee'),0)::int as lead_fees_cents,
	coalesce(sum(amount_cents) filter (where kind = 'payout_fee'),0)::int as payout_fees_cents,
	coalesce(sum(amount_cents) filter (where kind = 'plan_fee'),0)::int as plan_fees_cents,
	coalesce(sum(amount_cents) filter (where kind = 'adjustment'),0)::int as adjustments_cents,
	coalesce(sum(amount_cents) filter (where kind = 'payout'),0)::int as payouts_cents,
	coalesce(sum(amount_cents) filter (where not in_balance and kind in ('charge','tip')),0)::int as cash_cents,
	coalesce(sum(amount_cents) filter (where in_balance and status <> 'held' and kind <> 'payout'),0)::int as net_cents,
	count(*) filter (where kind = 'charge') as sales`

// GET /v1/m/statements   one line per month that had any money in it
func (s *Server) mStatements(w http.ResponseWriter, r *http.Request) {
	m := mc(r)
	out, statementsPage, err := s.historyRows(r, "statements", `select to_char(date_trunc('month', created_at at time zone $2), 'YYYY-MM') as month,`+statementSums+`
		from ledger where business_id=$1 group by 1 order by 1 desc `, "month desc", "month net_cents sales", m.BusinessID, m.Timezone)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, M{"statements": out, "statements_pagination": statementsPage, "currency": m.Currency})
}

// GET /v1/m/statements/{month}?format=csv   everything that moved in one month, with the balance before and after
func (s *Server) mStatement(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	m := mc(r)
	from, to, ok := monthRange(chi.URLParam(r, "month"), m.Loc)
	if !ok {
		writeErr(w, 400, "a month looks like 2026-10")
		return
	}
	sums, err := row(ctx, s.pool, `select`+statementSums+` from ledger where business_id=$1 and created_at >= $2 and created_at < $3`, m.BusinessID, from, to)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	var opening, closing int
	_ = s.pool.QueryRow(ctx, `select coalesce(sum(amount_cents) filter (where created_at < $2),0), coalesce(sum(amount_cents) filter (where created_at < $3),0) from ledger where business_id=$1 and in_balance and status <> 'held'`, m.BusinessID, from, to).Scan(&opening, &closing)

	if r.URL.Query().Get("format") == "csv" {
		lines, err := s.pool.Query(ctx, `select l.created_at, l.kind, l.description, l.method, l.amount_cents, l.status, l.in_balance, st.name as staff
		from ledger l left join staff st on st.id = l.staff_id and st.business_id = l.business_id where l.business_id=$1 and l.created_at >= $2 and l.created_at < $3 and l.status <> 'held' order by l.created_at, l.id`, m.BusinessID, from, to)
		if err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		defer lines.Close()
		w.Header().Set("Content-Type", "text/csv; charset=utf-8")
		w.Header().Set("Content-Disposition", `attachment; filename="statement-`+m.Slug+`-`+from.Format("2006-01")+`.csv"`)
		_, _ = w.Write([]byte("\xEF\xBB\xBF"))
		cw := csv.NewWriter(w)
		_ = cw.Write([]string{"date", "type", "description", "method", "amount", "currency", "counts_toward_payout", "staff"})
		for lines.Next() {
			l, scanErr := pgx.RowToMap(lines)
			if scanErr != nil {
				panic(http.ErrAbortHandler)
			}
			tidy(l)
			_ = cw.Write([]string{l["created_at"].(time.Time).In(m.Loc).Format("2006-01-02 15:04"), fmt.Sprint(l["kind"]), csvCell(l["description"]), fmt.Sprint(l["method"]),
				fmt.Sprintf("%.2f", float64(toInt(l["amount_cents"]))/100), m.Currency, fmt.Sprint(l["in_balance"]), csvCell(l["staff"])})
		}
		if lines.Err() != nil {
			panic(http.ErrAbortHandler)
		}
		cw.Flush()
		return
	}
	lines, linesPage, linesErr := s.historyRows(r, "lines", `select l.id, l.created_at, l.kind, l.description, l.method, l.amount_cents, l.status, l.in_balance, st.name as staff
		from ledger l left join staff st on st.id = l.staff_id and st.business_id = l.business_id where l.business_id=$1 and l.created_at >= $2 and l.created_at < $3 and l.status <> 'held' order by l.created_at, l.id`, "created_at asc", "created_at kind amount_cents status staff", m.BusinessID, from, to)
	if linesErr != nil {
		writeErr(w, 500, linesErr.Error())
		return
	}
	biz, _ := row(ctx, s.pool, `select b.name, b.slug, b.market, b.plan, l.address, l.city, l.region from businesses b left join locations l on l.business_id = b.id and l.is_primary where b.id=$1`, m.BusinessID)
	payouts, payoutsPage, payoutsErr := s.historyRows(r, "payouts", `select p.id, p.amount_cents, p.fee_cents, p.status, p.kind, p.reference, p.paid_at, p.created_at, a.bank_name, a.account_last4 from payouts p left join payout_accounts a on a.id = p.account_id and a.business_id = p.business_id
		where p.business_id=$1 and p.created_at >= $2 and p.created_at < $3 order by p.created_at`, "created_at asc", "created_at amount_cents status kind", m.BusinessID, from, to)
	if payoutsErr != nil {
		writeErr(w, 500, payoutsErr.Error())
		return
	}
	writeJSON(w, 200, M{"month": from.Format("2006-01"), "label": from.Format("January 2006"), "from": from.Format("2006-01-02"), "to": to.AddDate(0, 0, -1).Format("2006-01-02"),
		"business": biz, "currency": m.Currency, "sums": sums, "opening_cents": opening, "closing_cents": closing, "lines": lines, "lines_pagination": linesPage, "payouts": payouts, "payouts_pagination": payoutsPage, "payments_mode": s.payMode(m.Market)})
}

// ---------- the storefront logo ----------

// POST /v1/m/storefront/logo   multipart: file   one logo per business; a new one replaces the old
func (s *Server) mLogoUpload(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	m := mc(r)
	if s.store == nil {
		writeErr(w, 503, "photo storage is not set up yet; contact LogaLuxe support")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxImageBytes+(1<<20))
	if err := r.ParseMultipartForm(maxImageBytes + (1 << 20)); err != nil {
		writeErr(w, 413, "the image is too large; the limit is 8 MB")
		return
	}
	file, _, err := r.FormFile("file")
	if err != nil {
		writeErr(w, 400, "choose an image to upload")
		return
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxImageBytes+1))
	if err != nil || len(data) == 0 || len(data) > maxImageBytes {
		writeErr(w, 413, "the image could not be read, or is over 8 MB")
		return
	}
	contentType := http.DetectContentType(data) // trust the bytes, not the file name
	ext, ok := imageExt[contentType]
	if !ok {
		writeErr(w, 415, "use a JPEG, PNG or WebP image")
		return
	}
	var id string
	_ = s.pool.QueryRow(ctx, `select gen_random_uuid()::text`).Scan(&id)
	key := "site/logo/" + m.Slug + "/" + id + ext
	if err := s.store.Put(ctx, key, contentType, data); err != nil {
		writeErr(w, 502, "the upload failed; try again")
		return
	}
	old, _ := rows(ctx, s.pool, `select id, storage_key from site_media where slot='logo' and ref=$1`, m.Slug)
	if _, err := s.pool.Exec(ctx, `insert into site_media (id, slot, ref, storage_key, content_type, size_bytes, alt, uploaded_by, sort) values ($1,'logo',$2,$3,$4,$5,$6,$7,0)`,
		id, m.Slug, key, contentType, len(data), m.Business+" logo", m.Email); err != nil {
		_ = s.store.Delete(ctx, key)
		writeErr(w, 500, err.Error())
		return
	}
	for _, o := range old {
		_, _ = s.pool.Exec(ctx, `delete from site_media where id=$1`, o["id"])
		_ = s.store.Delete(ctx, fmt.Sprint(o["storage_key"]))
	}
	writeJSON(w, 201, M{"ok": true, "id": id})
}

// DELETE /v1/m/storefront/logo
func (s *Server) mLogoDelete(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	old, _ := rows(ctx, s.pool, `select id, storage_key from site_media where slot='logo' and ref=$1`, mc(r).Slug)
	for _, o := range old {
		_, _ = s.pool.Exec(ctx, `delete from site_media where id=$1`, o["id"])
		if s.store != nil {
			_ = s.store.Delete(ctx, fmt.Sprint(o["storage_key"]))
		}
	}
	writeJSON(w, 200, M{"ok": true, "removed": len(old)})
}

// payMode says whether money really moves for a business in this market: the United States needs a
// Stripe key, Nigeria a Paystack key. Without the key, payments and payouts there are simulated.
func (s *Server) payMode(market string) string {
	if (market == "NG" && s.cfg.PaystackSecret != "") || (market != "NG" && s.cfg.StripeSecret != "") {
		return "live"
	}
	return "simulation"
}

var _ = context.Background
