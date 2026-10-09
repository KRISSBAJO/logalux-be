package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
)

// The lead system. LogaLuxe earns a share of the first visit of a client it
// brought to a business, once, and only when that visit is paid for.
//
//   - A lead opens when someone who has never booked a business before books
//     it after finding it on LogaLuxe (search, a category page, the app).
//     A client who comes through the business's own link is never a lead.
//   - The share is the rate for the business's market and plan, adjusted by
//     kind of business, plus whatever the business bids to be promoted.
//   - It is charged when the visit is checked out, and capped.
//   - A cancelled booking or a no-show voids the lead. Nothing is charged.
//   - The business can dispute a charge for 14 days; the console decides.
//
// Promotion is the dynamic part: a business offers extra percentage points on
// new clients, with a monthly budget, and is listed first in search, marked
// "Promoted", until the budget is spent.

// Sources that mean "LogaLuxe found this client".
var leadSources = map[string]bool{"search": true, "marketplace": true, "app": true, "category": true}

const (
	leadDisputeDays = 14
	maxBoostPct     = 20
)

type leadTerms struct {
	BasePct  float64 // rate for the market and plan, with the adjustment for the kind of business
	BoostPct float64 // what the business bids on top, when promotion is running
	CapCents *int    // the most one lead can cost
	Boosted  bool
	// What the business has set, whether or not it is running now.
	BidPct      float64
	BudgetCents int
	Paused      bool
	SpentCents  int // lead fees charged this month
}

type queryer interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

// leadTermsFor works out what a new lead would cost this business right now.
func leadTermsFor(ctx context.Context, q queryer, businessID string) (leadTerms, error) {
	var t leadTerms
	var raw []byte
	err := q.QueryRow(ctx, `select
		coalesce((select f.new_client_pct from fees f where f.market = b.market and f.plan = b.plan and f.status = 'approved' and f.effective_from <= current_date order by f.effective_from desc limit 1), 0)::float8
		  + coalesce((select r.delta_pct from lead_category_rates r where r.market = b.market and r.category = b.category), 0)::float8,
		(select f.new_client_cap_cents from fees f where f.market = b.market and f.plan = b.plan and f.status = 'approved' and f.effective_from <= current_date order by f.effective_from desc limit 1),
		coalesce(b.settings->'leads', '{}'::jsonb),
		(select coalesce(sum(ld.fee_cents), 0) from leads ld where ld.business_id = b.id and ld.status in ('charged', 'disputed') and ld.charged_at >= date_trunc('month', now() at time zone b.timezone) at time zone b.timezone)::int
		from businesses b where b.id = $1`, businessID).Scan(&t.BasePct, &t.CapCents, &raw, &t.SpentCents)
	if err != nil {
		return t, err
	}
	if t.BasePct < 0 {
		t.BasePct = 0
	}
	var set struct {
		Boost  float64 `json:"boost_pct"`
		Budget int     `json:"monthly_budget_cents"`
		Paused bool    `json:"paused"`
	}
	_ = json.Unmarshal(raw, &set)
	t.BidPct, t.BudgetCents, t.Paused = math.Min(math.Max(set.Boost, 0), maxBoostPct), set.Budget, set.Paused
	t.Boosted = t.BidPct > 0 && !t.Paused && (t.BudgetCents == 0 || t.SpentCents < t.BudgetCents)
	if t.Boosted {
		t.BoostPct = t.BidPct
	}
	return t, nil
}

// The same test in SQL, for ranking search results. It yields the bid when promotion is running, else 0.
const boostSQL = `(case when coalesce((b.settings->'leads'->>'boost_pct')::numeric, 0) > 0
	  and not coalesce((b.settings->'leads'->>'paused')::boolean, false)
	  and (coalesce((b.settings->'leads'->>'monthly_budget_cents')::int, 0) = 0
	    or (select coalesce(sum(ld.fee_cents), 0) from leads ld where ld.business_id = b.id and ld.status in ('charged','disputed') and ld.charged_at >= date_trunc('month', now())) < (b.settings->'leads'->>'monthly_budget_cents')::int)
	then least((b.settings->'leads'->>'boost_pct')::numeric, 20) else 0 end)`

// openLead records a lead when a first-time client books after finding the business on LogaLuxe.
// It is safe to call for every public booking: it does nothing when the booking is not a lead.
func openLead(ctx context.Context, tx pgx.Tx, businessID, bookingID string, clientID *string, clientName, source string, valueCents int) {
	if !leadSources[source] || clientID == nil {
		return
	}
	// New means: no other booking with this business, ever, and never a lead before.
	var known bool
	if err := tx.QueryRow(ctx, `select exists(select 1 from bookings where business_id=$1 and client_id=$2 and id <> $3)
		or exists(select 1 from leads where business_id=$1 and client_id=$2 and status <> 'void')
		or exists(select 1 from sales where business_id=$1 and client_id=$2)`, businessID, *clientID, bookingID).Scan(&known); err != nil || known {
		return
	}
	t, err := leadTermsFor(ctx, tx, businessID)
	if err != nil || t.BasePct+t.BoostPct <= 0 {
		return
	}
	_, _ = tx.Exec(ctx, `insert into leads (business_id, client_id, booking_id, client_name, source, boosted, base_pct, boost_pct, cap_cents, value_cents)
		values ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10) on conflict do nothing`, businessID, *clientID, bookingID, clientName, source, t.Boosted, t.BasePct, t.BoostPct, t.CapCents, valueCents)
}

// settleLead charges the lead for a booking that has just been paid for. The fee is a share of what
// the client paid for services on that first visit (not tips, tax or retail), up to the cap.
func settleLead(ctx context.Context, tx pgx.Tx, businessID, currency, bookingID, saleID, clientName string, serviceCents int, ledgerStatus string) (feeCents int) {
	var id string
	var base, boost float64
	var limit *int
	if err := tx.QueryRow(ctx, `select id::text, base_pct::float8, boost_pct::float8, cap_cents from leads where booking_id=$1 and business_id=$2 and status='pending' for update`, bookingID, businessID).Scan(&id, &base, &boost, &limit); err != nil {
		return 0
	}
	fee := int(math.Round(float64(serviceCents) * (base + boost) / 100))
	if limit != nil && fee > *limit {
		fee = *limit
	}
	if fee <= 0 {
		_, _ = tx.Exec(ctx, `update leads set status='void', void_reason='Nothing was charged for the visit', value_cents=$2 where id=$1`, id, serviceCents)
		return 0
	}
	_, _ = tx.Exec(ctx, `update leads set status='charged', value_cents=$2, fee_cents=$3, sale_id=$4, charged_at=now() where id=$1`, id, serviceCents, fee, saleID)
	// The fee comes out of the payout balance whatever way the client paid, because the client came from LogaLuxe either way.
	_, _ = tx.Exec(ctx, `insert into ledger (business_id, kind, amount_cents, currency, method, status, in_balance, sale_id, booking_id, description, settles_at)
		values ($1,'lead_fee',$2,$3,'logaluxe',$4,true,$5,$6,$7, case when $4 = 'pending' then now() + interval '`+settleAfter+`' end)`,
		businessID, -fee, currency, ledgerStatus, saleID, bookingID, "New client from LogaLuxe · "+clientName)
	return fee
}

// countLeadEvents adds to the day's tally for each business. It never blocks a page.
func (s *Server) countLeadEvents(kind string, businessIDs ...string) {
	if len(businessIDs) == 0 {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, _ = s.pool.Exec(ctx, `insert into lead_events (business_id, kind, day, n) select id::uuid, $2, current_date, 1 from unnest($1::text[]) id
			on conflict (business_id, kind, day) do update set n = lead_events.n + 1`, businessIDs, kind)
	}()
}

// GET /v1/m/leads?status=   new clients LogaLuxe brought, what they cost and what they have been worth
func (s *Server) mLeads(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	m := mc(r)
	t, err := leadTermsFor(ctx, s.pool, m.BusinessID)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	status := r.URL.Query().Get("status")
	leads, leadsPage, leadsErr := s.historyRows(r, "leads", `select ld.id, ld.client_id, ld.client_name, ld.booking_id, ld.source, ld.status, ld.boosted, ld.base_pct::float8 as base_pct, ld.boost_pct::float8 as boost_pct, ld.cap_cents, ld.value_cents, ld.fee_cents,
		ld.void_reason, ld.dispute_reason, ld.disputed_at, ld.resolved_at, ld.resolution_note, ld.charged_at, ld.created_at, bk.starts_at, bk.status as booking_status,
		(select string_agg(bi.name, ' + ') from booking_items bi where bi.booking_id = ld.booking_id) as services,
		-- What the client has spent here since, first visit included.
		(select coalesce(sum(sa.subtotal_cents - sa.discount_cents - sa.refunded_cents), 0) from sales sa where sa.client_id = ld.client_id and sa.business_id = ld.business_id)::int as lifetime_cents,
		(select count(*) from bookings b2 where b2.client_id = ld.client_id and b2.business_id = ld.business_id and b2.status in ('completed','paid')) as visits,
		(ld.status = 'charged' and ld.charged_at > now() - interval '14 days') as can_dispute
		from leads ld left join bookings bk on bk.id = ld.booking_id and bk.business_id = ld.business_id
		where ld.business_id=$1 and ($2 = '' or ld.status = $2) order by ld.created_at desc`, "created_at desc", "created_at client_name status source fee_cents lifetime_cents", m.BusinessID, status)
	if leadsErr != nil {
		writeErr(w, 500, leadsErr.Error())
		return
	}
	kpis, _ := row(ctx, s.pool, `with mine as (select * from leads where business_id=$1), month as (select * from mine where created_at >= date_trunc('month', now() at time zone $2) at time zone $2)
		select (select count(*) from month) as month_leads,
		(select count(*) from month where status in ('charged','disputed')) as month_charged,
		(select count(*) from month where status = 'pending') as month_pending,
		(select count(*) from month where status = 'void') as month_void,
		(select coalesce(sum(fee_cents),0) from mine where status in ('charged','disputed') and charged_at >= date_trunc('month', now() at time zone $2) at time zone $2)::int as month_fee_cents,
		(select count(*) from mine where status in ('charged','disputed')) as all_charged,
		(select coalesce(sum(fee_cents),0) from mine where status in ('charged','disputed'))::int as all_fee_cents,
		(select coalesce(sum(sa.subtotal_cents - sa.discount_cents - sa.refunded_cents),0) from sales sa where sa.business_id=$1 and sa.client_id in (select client_id from mine where status in ('charged','disputed')))::int as all_revenue_cents,
		(select count(*) from mine ld where ld.status in ('charged','disputed') and (select count(*) from bookings b2 where b2.client_id = ld.client_id and b2.business_id=$1 and b2.status in ('completed','paid')) > 1) as came_back,
		(select coalesce(sum(n),0) from lead_events where business_id=$1 and kind='impression' and day > current_date - 30)::int as impressions_30d,
		(select coalesce(sum(n),0) from lead_events where business_id=$1 and kind='promoted_impression' and day > current_date - 30)::int as promoted_impressions_30d,
		(select coalesce(sum(n),0) from lead_events where business_id=$1 and kind='view' and day > current_date - 30)::int as views_30d,
		(select count(*) from mine where created_at > now() - interval '30 days') as leads_30d`, m.BusinessID, m.Timezone)
	// Where this business stands among the others in its city and kind, so a bid can be judged.
	rank, _ := row(ctx, s.pool, `with peers as (
		  select b.id, `+boostSQL+` as boost, b.rating, b.review_count from businesses b
		  left join locations l on l.business_id = b.id and l.is_primary
		  where b.status = 'live' and b.category = (select category from businesses where id=$1)
		    and coalesce(l.city,'') = coalesce((select l2.city from locations l2 where l2.business_id=$1 and l2.is_primary), ''))
		select (select count(*) from peers) as peers, (select count(*) from peers where boost > 0) as promoted_peers, (select coalesce(max(boost),0)::float8 from peers where id <> $1) as top_bid_pct,
		(select 1 + count(*) from peers p, peers me where me.id = $1 and p.id <> me.id and (p.boost > me.boost or (p.boost = me.boost and (p.rating > me.rating or (p.rating = me.rating and p.review_count > me.review_count))))) as position`, m.BusinessID)
	writeJSON(w, 200, M{
		"leads": leads, "leads_pagination": leadsPage, "kpis": kpis, "rank": rank,
		"rate":     M{"base_pct": t.BasePct, "cap_cents": t.CapCents, "dispute_days": leadDisputeDays, "max_boost_pct": maxBoostPct},
		"settings": M{"boost_pct": t.BidPct, "monthly_budget_cents": t.BudgetCents, "paused": t.Paused},
		"boost":    M{"running": t.Boosted, "spent_cents": t.SpentCents, "total_pct": t.BasePct + t.BoostPct},
	})
}

// PUT /v1/m/leads/settings   {boost_pct, monthly_budget_cents, paused}   the owner's bid to be promoted
func (s *Server) mLeadSettings(w http.ResponseWriter, r *http.Request) {
	m := mc(r)
	var req struct {
		Boost  float64 `json:"boost_pct"`
		Budget int     `json:"monthly_budget_cents"`
		Paused bool    `json:"paused"`
	}
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, "invalid json")
		return
	}
	if req.Boost < 0 || req.Boost > maxBoostPct || math.Round(req.Boost*2) != req.Boost*2 {
		writeErr(w, 400, "the extra share is between 0 and "+itoa(maxBoostPct)+"%, in steps of half a percent")
		return
	}
	if req.Budget < 0 || req.Budget > 100000000 {
		writeErr(w, 400, "the monthly budget cannot be negative")
		return
	}
	raw, _ := json.Marshal(M{"boost_pct": req.Boost, "monthly_budget_cents": req.Budget, "paused": req.Paused})
	if _, err := s.pool.Exec(r.Context(), `update businesses set settings = jsonb_set(coalesce(settings,'{}'::jsonb), '{leads}', $2::jsonb) where id=$1`, m.BusinessID, string(raw)); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	t, _ := leadTermsFor(r.Context(), s.pool, m.BusinessID)
	writeJSON(w, 200, M{"ok": true, "running": t.Boosted})
}

// POST /v1/m/leads/{id}/dispute   {reason}   "this was already my client"
func (s *Server) mLeadDispute(w http.ResponseWriter, r *http.Request) {
	m := mc(r)
	var req struct {
		Reason string `json:"reason"`
	}
	if err := readJSON(r, &req); err != nil || len(strings.TrimSpace(req.Reason)) < 10 || len(req.Reason) > 600 {
		writeErr(w, 400, "say why in a sentence or two, so the team can check")
		return
	}
	tag, err := s.pool.Exec(r.Context(), `update leads set status='disputed', dispute_reason=$3, disputed_at=now() where id=$1 and business_id=$2 and status='charged' and charged_at > now() - make_interval(days => $4)`,
		chi.URLParam(r, "id"), m.BusinessID, strings.TrimSpace(req.Reason), leadDisputeDays)
	if err != nil || tag.RowsAffected() == 0 {
		writeErr(w, 409, "this charge can no longer be disputed; that is possible for "+itoa(leadDisputeDays)+" days after it is made")
		return
	}
	writeJSON(w, 200, M{"ok": true})
}

// ---------- the console ----------

// GET /v1/admin/leads?status=&q=&market=
func (s *Server) adminLeads(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	q := r.URL.Query()
	leads, err := rows(ctx, s.pool, `select ld.id, ld.business_id, b.name as business, b.slug, b.market, b.currency, b.plan, ld.client_name, ld.source, ld.status, ld.boosted, ld.base_pct::float8 as base_pct, ld.boost_pct::float8 as boost_pct,
		ld.value_cents, ld.fee_cents, ld.void_reason, ld.dispute_reason, ld.disputed_at, ld.resolved_by, ld.resolved_at, ld.resolution_note, ld.charged_at, ld.created_at,
		(select count(*) from bookings b2 where b2.business_id = ld.business_id and b2.client_id = ld.client_id and b2.created_at < ld.created_at) as earlier_bookings
		from leads ld join businesses b on b.id = ld.business_id
		where ($1 = '' or ld.status = $1) and ($2 = '' or b.market = $2) and ($3 = '' or b.name ilike '%'||$3||'%' or ld.client_name ilike '%'||$3||'%')
		order by (ld.status = 'disputed') desc, ld.created_at desc limit 500`, q.Get("status"), q.Get("market"), strings.TrimSpace(q.Get("q")))
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	totals, _ := rows(ctx, s.pool, `select b.market, b.currency,
		count(*) filter (where ld.created_at > now() - interval '30 days') as leads_30d,
		count(*) filter (where ld.status in ('charged','disputed') and ld.charged_at > now() - interval '30 days') as charged_30d,
		coalesce(sum(ld.fee_cents) filter (where ld.status in ('charged','disputed') and ld.charged_at > now() - interval '30 days'), 0)::int as fee_cents_30d,
		coalesce(sum(round(ld.value_cents * ld.boost_pct / 100)) filter (where ld.status in ('charged','disputed') and ld.charged_at > now() - interval '30 days'), 0)::int as boost_cents_30d,
		coalesce(sum(ld.fee_cents) filter (where ld.status in ('charged','disputed')), 0)::int as fee_cents_all,
		count(*) filter (where ld.status = 'disputed') as disputed,
		(select count(*) from businesses b2 where b2.market = b.market and b2.status = 'live' and coalesce((b2.settings->'leads'->>'boost_pct')::numeric,0) > 0 and not coalesce((b2.settings->'leads'->>'paused')::boolean,false)) as promoting
		from leads ld join businesses b on b.id = ld.business_id group by b.market, b.currency order by b.market`)
	rates, _ := rows(ctx, s.pool, `select market, category, delta_pct::float8 as delta_pct, updated_by, updated_at from lead_category_rates order by market, category`)
	base, _ := rows(ctx, s.pool, `select distinct on (market, plan) market, plan, new_client_pct::float8 as new_client_pct, new_client_cap_cents from fees where status='approved' and effective_from <= current_date order by market, plan, effective_from desc`)
	writeJSON(w, 200, M{"leads": leads, "totals": totals, "category_rates": rates, "base_rates": base, "categories": keysOf(businessCategories), "max_boost_pct": maxBoostPct})
}

func keysOf(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	for i := 1; i < len(out); i++ { // a small list: keep it in order without another import
		for j := i; j > 0 && out[j] < out[j-1]; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

// POST /v1/admin/leads/{id}/resolve   {outcome: refund|uphold, note}
func (s *Server) adminLeadResolve(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id := chi.URLParam(r, "id")
	var req struct {
		Outcome string `json:"outcome"`
		Note    string `json:"note"`
	}
	if err := readJSON(r, &req); err != nil || (req.Outcome != "refund" && req.Outcome != "uphold") || len(strings.TrimSpace(req.Note)) < 5 {
		writeErr(w, 400, "choose refund or uphold, and say why")
		return
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	defer tx.Rollback(ctx)
	var bizID, currency, client string
	var fee int
	var saleID, bookingID *string
	if err := tx.QueryRow(ctx, `select ld.business_id::text, b.currency, ld.client_name, ld.fee_cents, ld.sale_id::text, ld.booking_id::text from leads ld join businesses b on b.id = ld.business_id
		where ld.id=$1 and ld.status in ('disputed','charged') for update of ld`, id).Scan(&bizID, &currency, &client, &fee, &saleID, &bookingID); err != nil {
		writeErr(w, 409, "this lead is not charged or disputed, so there is nothing to decide")
		return
	}
	status := "charged"
	if req.Outcome == "refund" {
		status = "refunded"
		if _, err := tx.Exec(ctx, `insert into ledger (business_id, kind, amount_cents, currency, method, status, in_balance, sale_id, booking_id, description) values ($1,'adjustment',$2,$3,'logaluxe','settled',true,$4,$5,$6)`,
			bizID, fee, currency, saleID, bookingID, "Lead fee returned · "+client); err != nil {
			writeErr(w, 500, err.Error())
			return
		}
	}
	if _, err := tx.Exec(ctx, `update leads set status=$2, resolved_by=$3, resolved_at=now(), resolution_note=$4 where id=$1`, id, status, s.actor(r), strings.TrimSpace(req.Note)); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if err := tx.Commit(ctx); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	s.audit(r, "lead."+req.Outcome, id, nil, M{"fee_cents": fee, "note": req.Note})
	writeJSON(w, 200, M{"ok": true, "status": status})
}

// PUT /v1/admin/leads/rates   {market, category, delta_pct}   delta 0 removes the adjustment
func (s *Server) adminLeadRate(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Market   string  `json:"market"`
		Category string  `json:"category"`
		Delta    float64 `json:"delta_pct"`
	}
	if err := readJSON(r, &req); err != nil || (req.Market != "US" && req.Market != "NG") || !businessCategories[req.Category] || req.Delta < -20 || req.Delta > 30 {
		writeErr(w, 400, "choose a market and a category, and an adjustment between -20 and +30 points")
		return
	}
	var err error
	if req.Delta == 0 {
		_, err = s.pool.Exec(r.Context(), `delete from lead_category_rates where market=$1 and category=$2`, req.Market, req.Category)
	} else {
		_, err = s.pool.Exec(r.Context(), `insert into lead_category_rates (market, category, delta_pct, updated_by) values ($1,$2,$3,$4)
			on conflict (market, category) do update set delta_pct = excluded.delta_pct, updated_by = excluded.updated_by, updated_at = now()`, req.Market, req.Category, req.Delta, s.actor(r))
	}
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	s.audit(r, "lead.rate", req.Market+"/"+req.Category, nil, M{"delta_pct": req.Delta})
	writeJSON(w, 200, M{"ok": true})
}

var _ = fmt.Sprint
