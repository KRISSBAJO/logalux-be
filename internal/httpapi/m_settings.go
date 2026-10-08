package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"logaluxe/api/internal/geo"
	"logaluxe/api/internal/mail"
)

// Settings: the business's details, its locations and hours, the rules
// clients book under, and its plan.

// GET /v1/m/settings
func (s *Server) mSettings(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	m := mc(r)
	b, err := row(ctx, s.pool, `select id, slug, name, category, phone, email, about, timezone, market, currency, plan, status, verification_status, sales_tax_bp, payout_schedule, created_at from businesses where id=$1`, m.BusinessID)
	if err != nil {
		writeErr(w, 404, "business not found")
		return
	}
	locations, _ := rows(ctx, s.pool, `select l.id, l.name, l.address, l.city, l.region, l.country, l.timezone, l.is_primary, l.hours, l.arrival_notes, l.lat, l.lng, l.travels, l.travel_radius_km, l.position_source from locations l where l.business_id=$1 order by l.is_primary desc, l.name`, m.BusinessID)
	plans, _ := rows(ctx, s.pool, `select distinct on (plan) plan, plan_price_cents, transaction_pct, transaction_fixed_cents, transaction_cap_cents, new_client_pct, marketplace_pct, instant_payout_pct
		from fees where market=$1 and status='approved' and effective_from <= current_date order by plan, effective_from desc`, m.Market)
	logins, _ := rows(ctx, s.pool, `select mu.id, mu.email, mu.name, mm.role, mu.last_login_at, st.name as staff from merchant_members mm join merchant_users mu on mu.id = mm.merchant_id left join staff st on st.id = mm.staff_id
		where mm.business_id=$1 order by (mm.role = 'owner') desc, mu.name`, m.BusinessID)
	account, _ := row(ctx, s.pool, `select name, email, phone from merchant_users where id=$1`, m.ID)
	billing, _ := row(ctx, s.pool, `select b.plan, b.plan_paid_through, b.plan_due_since, $2::int as grace_days,
		(select f.plan_price_cents from fees f where f.market = b.market and f.plan = 'pro' and f.status = 'approved' and f.effective_from <= current_date order by f.effective_from desc limit 1) as pro_price_cents,
		(select coalesce(sum(-l.amount_cents),0) from ledger l where l.business_id = b.id and l.kind = 'plan_fee')::int as paid_total_cents,
		(select max(l.created_at) from ledger l where l.business_id = b.id and l.kind = 'plan_fee') as last_charged_at
		from businesses b where b.id=$1`, m.BusinessID, planGraceDays)
	writeJSON(w, 200, M{"business": b, "locations": locations, "rules": s.bizSettings(ctx, m.BusinessID), "plans": plans, "logins": logins, "account": account, "mail_mode": s.mail.Mode(), "billing": billing})
}

// PUT /v1/m/settings/profile
func (s *Server) mSettingsProfile(w http.ResponseWriter, r *http.Request) {
	m := mc(r)
	var req struct {
		Name     string  `json:"name"`
		Category string  `json:"category"`
		Phone    string  `json:"phone"`
		Email    string  `json:"email"`
		About    string  `json:"about"`
		Timezone string  `json:"timezone"`
		TaxPct   float64 `json:"sales_tax_pct"`
	}
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, "invalid json")
		return
	}
	req.Name, req.Email = strings.TrimSpace(req.Name), strings.ToLower(strings.TrimSpace(req.Email))
	phone, phoneOK := cleanPhone(req.Phone)
	switch {
	case len(req.Name) < 2 || len(req.Name) > 80:
		writeErr(w, 400, "the business name must be 2 to 80 characters")
		return
	case !businessCategories[req.Category]:
		writeErr(w, 400, "choose a category")
		return
	case !phoneOK:
		writeErr(w, 400, "that phone number does not look right; include the country code")
		return
	case req.Email != "" && !mail.Valid(req.Email):
		writeErr(w, 400, "that email address does not look right")
		return
	case len(req.About) > 2000:
		writeErr(w, 400, "keep the about text under 2,000 characters")
		return
	case req.TaxPct < 0 || req.TaxPct > 30:
		writeErr(w, 400, "sales tax is a percentage between 0 and 30")
		return
	}
	if req.Timezone == "" {
		req.Timezone = m.Timezone
	}
	if _, err := time.LoadLocation(req.Timezone); err != nil {
		writeErr(w, 400, "unknown time zone")
		return
	}
	if _, err := s.pool.Exec(r.Context(), `update businesses set name=$2, category=$3, phone=$4, email=$5, about=$6, timezone=$7, sales_tax_bp=$8 where id=$1`,
		m.BusinessID, req.Name, req.Category, phone, req.Email, strings.TrimSpace(req.About), req.Timezone, int(req.TaxPct*100+0.5)); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	_, _ = s.pool.Exec(r.Context(), `update products set seller_name=$2 where business_id=$1`, m.BusinessID, req.Name)
	writeJSON(w, 200, M{"ok": true})
}

// What each rule may hold. Anything else sent is ignored.
var ruleLimits = map[string]map[string]any{
	"booking": {"instant": true, "lead_hours": [2]int{0, 168}, "max_days": [2]int{1, 365}, "anyone": true, "multi_service": true, "waitlist": true, "on_search": true},
	"policy": {"cancel_hours": [2]int{0, 168}, "late_cancel_fee": []string{"none", "deposit", "50", "100"}, "no_show_fee": []string{"none", "deposit", "50", "100"},
		"new_client_deposit_pct": [2]int{0, 100}, "prepay_after_no_show": true},
	"notify": {"new_booking_email": true, "cancellation_email": true, "daily_summary": true, "low_stock_email": true},
}

// PUT /v1/m/settings/rules   {booking: {...}, policy: {...}, notify: {...}}
func (s *Server) mSettingsRules(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	m := mc(r)
	var req map[string]map[string]any
	if err := json.NewDecoder(http.MaxBytesReader(nil, r.Body, 1<<16)).Decode(&req); err != nil {
		writeErr(w, 400, "invalid json")
		return
	}
	for group, limits := range ruleLimits {
		vals, ok := req[group]
		if !ok {
			continue
		}
		clean := map[string]any{}
		for k, limit := range limits {
			v, ok := vals[k]
			if !ok {
				continue
			}
			switch lim := limit.(type) {
			case bool:
				if b, ok := v.(bool); ok {
					clean[k] = b
				}
			case [2]int:
				n, ok := v.(float64)
				if !ok || int(n) < lim[0] || int(n) > lim[1] {
					writeErr(w, 400, strings.ReplaceAll(k, "_", " ")+" must be between "+itoa(lim[0])+" and "+itoa(lim[1]))
					return
				}
				clean[k] = int(n)
			case []string:
				str, _ := v.(string)
				valid := false
				for _, o := range lim {
					valid = valid || o == str
				}
				if !valid {
					writeErr(w, 400, strings.ReplaceAll(k, "_", " ")+" has a value that is not allowed")
					return
				}
				clean[k] = str
			}
		}
		raw, _ := json.Marshal(clean)
		if _, err := s.pool.Exec(ctx, `update businesses set settings = jsonb_set(settings, array[$2], coalesce(settings->$2,'{}'::jsonb) || $3::jsonb) where id=$1`, m.BusinessID, group, string(raw)); err != nil {
			writeErr(w, 500, err.Error())
			return
		}
	}
	writeJSON(w, 200, M{"ok": true, "rules": s.bizSettings(ctx, m.BusinessID)})
}

// POST /v1/m/locations   a second place the business trades from
func (s *Server) mLocationCreate(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	m := mc(r)
	var req locationReq
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, "invalid json")
		return
	}
	if strings.TrimSpace(req.Name) == "" {
		writeErr(w, 400, "the location needs a name")
		return
	}
	if req.Hours == nil {
		req.Hours = map[string][]string{"mon": {"09:00", "18:00"}, "tue": {"09:00", "18:00"}, "wed": {"09:00", "18:00"}, "thu": {"09:00", "18:00"}, "fri": {"09:00", "18:00"}, "sat": {"09:00", "16:00"}}
	}
	hours, msg := hoursJSON(req.Hours)
	if msg != "" {
		writeErr(w, 400, msg)
		return
	}
	if c := strings.TrimSpace(req.Country); c != "" && geo.CleanCountry(c) != m.Market {
		writeErr(w, 400, "a business trades in one country, and this one is in "+geo.InCountry(m.Market))
		return
	}
	_, radius, why := travelRadius(req.TravelRadiusKm)
	if why != "" {
		writeErr(w, 400, why)
		return
	}
	loc, why := s.placeLocation(ctx, req.Address, req.City, req.Region, m.Market, req.Lat, req.Lng)
	if why != "" {
		writeErr(w, 400, why)
		return
	}
	travels := req.Travels != nil && *req.Travels
	if !travels {
		radius = nil
	}
	var id string
	if err := s.pool.QueryRow(ctx, `insert into locations (business_id, name, address, city, region, country, county, timezone, is_primary, hours, arrival_notes, lat, lng, position_source, travels, travel_radius_km)
		values ($1,$2,$3,$4,$5,$6,$7,$8,false,$9::jsonb,$10,$11,$12,$13,$14,$15) returning id::text`, m.BusinessID, strings.TrimSpace(req.Name), loc.Address, loc.City, loc.Region, m.Market, loc.County,
		firstNonEmpty(loc.Timezone, m.Timezone), string(hours), req.ArrivalNotes, loc.Lat, loc.Lng, loc.Source, travels, radius).Scan(&id); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 201, M{"ok": true, "id": id, "position": positionWord(loc.Source, false), "location": loc})
}

// PUT /v1/m/locations/{id}
func (s *Server) mLocationUpdate(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	m := mc(r)
	id := chi.URLParam(r, "id")
	var req locationReq
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, "invalid json")
		return
	}
	if strings.TrimSpace(req.Name) == "" {
		writeErr(w, 400, "the location needs a name")
		return
	}
	hours, msg := hoursJSON(req.Hours)
	if msg != "" {
		writeErr(w, 400, msg)
		return
	}
	old, err := s.loadLocation(ctx, id, m.BusinessID)
	if err != nil {
		writeErr(w, 404, "location not found")
		return
	}
	loc, position, why := s.saveLocation(ctx, id, old, &req, string(hours))
	if why != "" {
		writeErr(w, 400, why)
		return
	}
	writeJSON(w, 200, M{"ok": true, "position": position, "location": loc})
}

// POST /v1/m/locations/{id}/action   {action: primary|delete}
func (s *Server) mLocationAction(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	m := mc(r)
	id := chi.URLParam(r, "id")
	var req struct {
		Action string `json:"action"`
	}
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, "invalid json")
		return
	}
	var primary bool
	if err := s.pool.QueryRow(ctx, `select is_primary from locations where id=$1 and business_id=$2`, id, m.BusinessID).Scan(&primary); err != nil {
		writeErr(w, 404, "location not found")
		return
	}
	switch req.Action {
	case "primary":
		_, _ = s.pool.Exec(ctx, `update locations set is_primary = (id::text = $2) where business_id=$1`, m.BusinessID, id)
		s.syncBusinessZone(ctx, m.BusinessID) // the business keeps the time of its main location
	case "delete":
		if primary {
			writeErr(w, 409, "make another location the main one before deleting this one")
			return
		}
		var used bool
		_ = s.pool.QueryRow(ctx, `select exists(select 1 from bookings where location_id=$1)`, id).Scan(&used)
		if used {
			writeErr(w, 409, "this location has bookings on record, so it cannot be deleted")
			return
		}
		_, _ = s.pool.Exec(ctx, `delete from locations where id=$1`, id)
	default:
		writeErr(w, 400, "unknown action")
		return
	}
	writeJSON(w, 200, M{"ok": true})
}

// POST /v1/m/plan   {plan: free|pro}
// A change takes effect at once. The Pro fee is taken from the payout balance once a month by the worker;
// the first month is taken as soon as the balance can cover it. Moving to Free stops further fees; nothing is refunded.
func (s *Server) mPlan(w http.ResponseWriter, r *http.Request) {
	m := mc(r)
	var req struct {
		Plan string `json:"plan"`
	}
	if err := readJSON(r, &req); err != nil || (req.Plan != "free" && req.Plan != "pro") {
		writeErr(w, 400, "choose the free or the pro plan")
		return
	}
	_, _ = s.pool.Exec(r.Context(), `update businesses set plan=$2, plan_due_since=null where id=$1`, m.BusinessID, req.Plan)
	if req.Plan == "pro" {
		go func() { // take the first month now if the balance can cover it, rather than at the next run
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			s.billPlans(ctx)
		}()
	}
	_, _ = s.pool.Exec(r.Context(), `insert into audit_log (actor, action, target, before, after) values ($1,'merchant.plan',$2,$3,$4)`, m.Email, m.BusinessID, M{"plan": m.Plan}, M{"plan": req.Plan})
	writeJSON(w, 200, M{"ok": true})
}

// POST /v1/m/listing   {paused}   take the business off search for a while, or bring it back
func (s *Server) mListing(w http.ResponseWriter, r *http.Request) {
	m := mc(r)
	var req struct {
		Paused bool `json:"paused"`
	}
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, "invalid json")
		return
	}
	from, to := "paused", "live"
	if req.Paused {
		from, to = "live", "paused"
	}
	tag, err := s.pool.Exec(r.Context(), `update businesses set status=$3 where id=$1 and status=$2`, m.BusinessID, from, to)
	if err != nil || tag.RowsAffected() == 0 {
		writeErr(w, 409, map[bool]string{true: "only a live business can be paused", false: "only a paused business can be brought back; a new or suspended one needs LogaLuxe to approve it"}[req.Paused])
		return
	}
	writeJSON(w, 200, M{"ok": true, "status": to})
}

// PUT /v1/m/account   {name, phone}   the signed-in person's own details
func (s *Server) mAccount(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name  string `json:"name"`
		Phone string `json:"phone"`
	}
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, "invalid json")
		return
	}
	phone, ok := cleanPhone(req.Phone)
	if strings.TrimSpace(req.Name) == "" || !ok {
		writeErr(w, 400, "enter your name, and a phone number with its country code")
		return
	}
	_, _ = s.pool.Exec(r.Context(), `update merchant_users set name=$2, phone=coalesce(nullif($3,''), phone) where id=$1`, mc(r).ID, strings.TrimSpace(req.Name), phone)
	writeJSON(w, 200, M{"ok": true})
}
