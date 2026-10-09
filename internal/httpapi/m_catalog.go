package httpapi

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"logaluxe/api/internal/mail"
)

// The menu and the team: services and their prices, who works when, time off,
// and who can sign in.

// ---------- services ----------

// GET /v1/m/services
func (s *Server) mServices(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	m := mc(r)
	out, err := rows(ctx, s.pool, `select sv.id, sv.name, sv.category, sv.description, sv.duration_min, sv.processing_min, sv.buffer_min, sv.price_cents, sv.deposit_cents, sv.online, sv.archived, sv.sort,
		(select coalesce(jsonb_agg(jsonb_build_object('staff_id', ss.staff_id, 'price_cents', ss.price_cents)), '[]'::jsonb) from staff_services ss where ss.service_id = sv.id) as staff,
		(select count(*) from booking_items bi join bookings bk on bk.id = bi.booking_id where bi.service_id = sv.id and bk.created_at > now() - interval '30 days' and bk.status not in ('cancelled_client','cancelled_business')) as booked_30d
		from services sv where sv.business_id=$1 order by sv.archived, sv.sort, sv.name`, m.BusinessID)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	staff, _ := rows(ctx, s.pool, `select id, name, initials, level, tone, bookable from staff where business_id=$1 and not archived order by role='owner' desc, name`, m.BusinessID)
	writeJSON(w, 200, M{"services": out, "staff": staff})
}

type mServiceReq struct {
	serviceReq
	StaffPrices map[string]*int `json:"staff_prices"` // staff id -> their own price, or null for the menu price
}

func (s *Server) saveServiceStaff(r *http.Request, serviceID string, req mServiceReq) error {
	ctx := r.Context()
	m := mc(r)
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err := setServiceStaff(ctx, tx, serviceID, m.BusinessID, req.StaffIDs); err != nil {
		return err
	}
	for staffID, price := range req.StaffPrices {
		if price != nil && *price < 0 {
			continue
		}
		if _, err := tx.Exec(ctx, `update staff_services set price_cents=$3 where service_id=$1 and staff_id::text=$2`, serviceID, staffID, price); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

// POST /v1/m/services
func (s *Server) mServiceCreate(w http.ResponseWriter, r *http.Request) {
	m := mc(r)
	var req mServiceReq
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, "invalid json")
		return
	}
	if msg := req.check(); msg != "" {
		writeErr(w, 400, msg)
		return
	}
	var id string
	err := s.pool.QueryRow(r.Context(), `insert into services (business_id, name, category, description, duration_min, processing_min, buffer_min, price_cents, deposit_cents, online, sort)
		values ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,(select coalesce(max(sort),0)+1 from services where business_id=$1)) returning id::text`,
		m.BusinessID, req.Name, req.Category, req.Description, req.DurationMin, req.ProcessingMin, req.BufferMin, req.PriceCents, req.DepositCents, req.Online == nil || *req.Online).Scan(&id)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if err := s.saveServiceStaff(r, id, req); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 201, M{"ok": true, "id": id})
}

// PUT /v1/m/services/{id}
func (s *Server) mServiceUpdate(w http.ResponseWriter, r *http.Request) {
	m := mc(r)
	id := chi.URLParam(r, "id")
	var req mServiceReq
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, "invalid json")
		return
	}
	if msg := req.check(); msg != "" {
		writeErr(w, 400, msg)
		return
	}
	tag, err := s.pool.Exec(r.Context(), `update services set name=$3, category=$4, description=$5, duration_min=$6, processing_min=$7, buffer_min=$8, price_cents=$9, deposit_cents=$10, online=coalesce($11, online)
		where id=$1 and business_id=$2`, id, m.BusinessID, req.Name, req.Category, req.Description, req.DurationMin, req.ProcessingMin, req.BufferMin, req.PriceCents, req.DepositCents, req.Online)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if tag.RowsAffected() == 0 {
		writeErr(w, 404, "service not found")
		return
	}
	if err := s.saveServiceStaff(r, id, req); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, M{"ok": true})
}

// POST /v1/m/services/{id}/action   {action: archive|restore|duplicate|delete}
func (s *Server) mServiceAction(w http.ResponseWriter, r *http.Request) {
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
	var name string
	if err := s.pool.QueryRow(ctx, `select name from services where id=$1 and business_id=$2`, id, m.BusinessID).Scan(&name); err != nil {
		writeErr(w, 404, "service not found")
		return
	}
	switch req.Action {
	case "archive":
		_, _ = s.pool.Exec(ctx, `update services set archived=true, online=false where id=$1`, id)
	case "restore":
		_, _ = s.pool.Exec(ctx, `update services set archived=false, online=true where id=$1`, id)
	case "duplicate":
		var newID string
		if err := s.pool.QueryRow(ctx, `insert into services (business_id, name, category, description, duration_min, processing_min, buffer_min, price_cents, deposit_cents, online, sort)
			select business_id, left(name || ' (copy)', 80), category, description, duration_min, processing_min, buffer_min, price_cents, deposit_cents, false, sort + 1 from services where id=$1 returning id::text`, id).Scan(&newID); err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		_, _ = s.pool.Exec(ctx, `insert into staff_services (staff_id, service_id, price_cents) select staff_id, $2, price_cents from staff_services where service_id=$1`, id, newID)
		writeJSON(w, 201, M{"ok": true, "id": newID})
		return
	case "delete":
		var used bool
		_ = s.pool.QueryRow(ctx, `select exists(select 1 from booking_items where service_id=$1)`, id).Scan(&used)
		if used {
			writeErr(w, 409, "this service has bookings on record, so it is kept for your history; archive it instead")
			return
		}
		_, _ = s.pool.Exec(ctx, `delete from services where id=$1`, id)
	default:
		writeErr(w, 400, "unknown action")
		return
	}
	writeJSON(w, 200, M{"ok": true})
}

// PUT /v1/m/services/order   {ids: [...]}   the order clients see on the booking page
func (s *Server) mServiceOrder(w http.ResponseWriter, r *http.Request) {
	var req struct {
		IDs []string `json:"ids"`
	}
	if err := readJSON(r, &req); err != nil || len(req.IDs) == 0 || len(req.IDs) > 500 {
		writeErr(w, 400, "send the services in their new order")
		return
	}
	if _, err := s.pool.Exec(r.Context(), `update services sv set sort = o.pos from unnest($2::uuid[]) with ordinality as o(id, pos) where sv.id = o.id and sv.business_id = $1`, mc(r).BusinessID, req.IDs); err != nil {
		writeErr(w, 400, "could not save that order")
		return
	}
	writeJSON(w, 200, M{"ok": true})
}

// ---------- team ----------

// GET /v1/m/staff?week=YYYY-MM-DD
func (s *Server) mStaff(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	m := mc(r)
	monday := parseDay(r.URL.Query().Get("week"), m.Loc)
	monday = monday.AddDate(0, 0, -((int(monday.Weekday()) + 6) % 7))
	sunday := monday.AddDate(0, 0, 7)
	staff, err := rows(ctx, s.pool, `select st.id, st.name, st.initials, st.role, st.level, st.tone, st.bookable, st.archived, st.hours, st.email, st.phone, st.commission_pct, st.retail_commission_pct, st.permissions, st.rating, st.created_at,
		st.pay_type, st.hourly_cents, st.salary_cents, st.breaks, st.rent_cents, st.rent_period, st.rent_days, st.trading_name,
		(select count(*) from bookings bk where bk.staff_id = st.id and bk.starts_at >= $2 and bk.starts_at < $3 and bk.status not in ('cancelled_client','cancelled_business','rescheduled','no_show')) as week_bookings,
		(select coalesce(sum(bk.total_cents),0) from bookings bk where bk.staff_id = st.id and bk.starts_at >= $2 and bk.starts_at < $3 and bk.status not in ('cancelled_client','cancelled_business','rescheduled','no_show')) as week_cents,
		(select coalesce(sum(extract(epoch from (bk.ends_at - bk.starts_at))/60),0)::int from bookings bk where bk.staff_id = st.id and bk.starts_at >= $2 and bk.starts_at < $3 and bk.status not in ('cancelled_client','cancelled_business','rescheduled','no_show')) as week_booked_min,
		(select coalesce(array_agg(ss.service_id::text), '{}') from staff_services ss where ss.staff_id = st.id) as service_ids,
		(select mu.email from merchant_members mm join merchant_users mu on mu.id = mm.merchant_id where mm.staff_id = st.id and mm.business_id = st.business_id limit 1) as login_email,
		(select mm.role from merchant_members mm where mm.staff_id = st.id and mm.business_id = st.business_id limit 1) as login_role
		from staff st where st.business_id=$1 order by st.archived, st.role='owner' desc, st.name`, m.BusinessID, monday, sunday)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	private := m.Role == "staff"
	if private {
		for _, st := range staff {
			if fmt.Sprint(st["id"]) == m.StaffID {
				continue
			}
			for _, k := range []string{"email", "phone", "commission_pct", "pay_type", "hourly_cents", "salary_cents", "rent_cents", "rent_period", "rent_days", "week_cents", "login_email", "login_role"} {
				delete(st, k)
			}
		}
	}
	var locHours any
	_ = s.pool.QueryRow(ctx, `select hours from locations where business_id=$1 and is_primary`, m.BusinessID).Scan(&locHours)
	timeOff, _ := rows(ctx, s.pool, `select t.id, t.staff_id, st.name as staff, t.starts_on, t.ends_on, t.reason, t.status, t.created_at,
		(select count(*) from bookings bk where bk.staff_id = t.staff_id and bk.starts_at::date between t.starts_on and t.ends_on and bk.status in ('requested','confirmed')) as bookings_affected
		from time_off t join staff st on st.id = t.staff_id where t.business_id=$1 and t.ends_on >= current_date - 14 order by (t.status = 'requested') desc, t.starts_on`, m.BusinessID)
	services, _ := rows(ctx, s.pool, `select id, name, category from services where business_id=$1 and not archived order by sort, name`, m.BusinessID)
	rent, rentPage, rentErr := s.historyRows(r, "rent", `select rc.id, rc.staff_id, st.name as staff, st.trading_name, rc.period_start, rc.period_end, rc.amount_cents, rc.status, rc.method, rc.note, rc.paid_at, rc.payment_reference, (select p.url from payments p where p.reference=rc.payment_reference and p.status='pending') as payment_url
		from rent_charges rc join staff st on st.id = rc.staff_id and st.business_id = rc.business_id where rc.business_id=$1 order by (rc.status = 'due') desc, rc.period_start desc`, "(status = 'due') desc, period_start desc", "period_start staff status amount_cents", m.BusinessID)
	if rentErr != nil {
		writeErr(w, 500, rentErr.Error())
		return
	}
	rentDue, _ := rows(ctx, s.pool, `select staff_id, sum(amount_cents)::int as cents, count(*) as charges from rent_charges where business_id=$1 and status='due' group by staff_id`, m.BusinessID)
	if private {
		rent = []M{}
		rentPage = nil
		rentDue = []M{}
	}
	writeJSON(w, 200, M{"staff": staff, "location_hours": locHours, "time_off": timeOff, "services": services, "week": monday.Format("2006-01-02"), "rent": rent, "rent_due": rentDue, "rent_pagination": rentPage, "permission_defaults": staffPermDefaults})
}

type mStaffReq struct {
	staffReq
	Email               string              `json:"email"`
	Phone               string              `json:"phone"`
	CommissionPct       float64             `json:"commission_pct"`
	RetailCommissionPct float64             `json:"retail_commission_pct"`
	Hours               map[string][]string `json:"hours"` // their own week; leave out to follow the location
	UseLocationHours    bool                `json:"use_location_hours"`
	ServiceIDs          *[]string           `json:"service_ids"`
	// Everything below is optional: what is left out stays as it is.
	PayType     *string                `json:"pay_type"`     // commission | hourly | salary | owner | renter
	HourlyCents *int                   `json:"hourly_cents"` // with pay_type hourly
	SalaryCents *int                   `json:"salary_cents"` // a month, with pay_type salary
	Breaks      *map[string][][]string `json:"breaks"`       // {mon: [["13:00","13:30"]]}
	RentCents   *int                   `json:"rent_cents"`   // with pay_type renter
	RentPeriod  *string                `json:"rent_period"`  // weekly | monthly
	RentDays    *[]string              `json:"rent_days"`    // the days the chair is theirs
	TradingName *string                `json:"trading_name"` // a renter's own business name
	Permissions *map[string]bool       `json:"permissions"`  // see_all_calendars, take_payments, see_reports
}

// saveStaffExtras stores how a person is paid, their breaks, rental terms and permissions. It returns a message when something is not valid.
func (s *Server) saveStaffExtras(r *http.Request, id string, req mStaffReq) string {
	ctx := r.Context()
	if req.PayType != nil && !contains([]string{"commission", "hourly", "salary", "owner", "renter"}, *req.PayType) {
		return "pay type must be commission, hourly, salary, owner or renter"
	}
	if req.RentPeriod != nil && *req.RentPeriod != "weekly" && *req.RentPeriod != "monthly" {
		return "rent is weekly or monthly"
	}
	for _, n := range []*int{req.HourlyCents, req.SalaryCents, req.RentCents} {
		if n != nil && *n < 0 {
			return "pay and rent cannot be negative"
		}
	}
	var breaks any
	if req.Breaks != nil {
		clean := map[string][][]string{}
		for day, list := range *req.Breaks {
			if !contains(weekDays, day) || len(list) > 4 {
				return "breaks are listed per day, up to four a day"
			}
			for _, b := range list {
				if len(b) != 2 || !clockRe.MatchString(b[0]) || !clockRe.MatchString(b[1]) || b[1] <= b[0] {
					return "a break needs a start and a later end, like 13:00 to 13:30"
				}
				clean[day] = append(clean[day], []string{b[0], b[1]})
			}
		}
		raw, _ := json.Marshal(clean)
		breaks = string(raw)
	}
	var perms any
	if req.Permissions != nil {
		clean := map[string]bool{}
		for k, v := range *req.Permissions {
			if _, ok := staffPermDefaults[k]; ok {
				clean[k] = v
			}
		}
		raw, _ := json.Marshal(clean)
		perms = string(raw)
	}
	var days any
	if req.RentDays != nil {
		list := []string{}
		for _, d := range *req.RentDays {
			if contains(weekDays, d) && !contains(list, d) {
				list = append(list, d)
			}
		}
		days = list
	}
	if _, err := s.pool.Exec(ctx, `update staff set pay_type=coalesce($2, pay_type), hourly_cents=coalesce($3, hourly_cents), salary_cents=coalesce($4, salary_cents), breaks=coalesce($5::jsonb, breaks),
		rent_cents=coalesce($6, rent_cents), rent_period=coalesce($7, rent_period), rent_days=coalesce($8::text[], rent_days), trading_name=coalesce($9, trading_name),
		permissions=coalesce($10::jsonb, permissions) where id=$1`, id, req.PayType, req.HourlyCents, req.SalaryCents, breaks, req.RentCents, req.RentPeriod, days, req.TradingName, perms); err != nil {
		return "could not save pay and permissions"
	}
	// Someone renting a chair runs their own book, so clients do not book them through this business.
	_, _ = s.pool.Exec(ctx, `update staff set bookable=false where id=$1 and pay_type='renter'`, id)
	return ""
}

func (v *mStaffReq) checkAll() (hours any, msg string) {
	if msg := v.check(); msg != "" {
		return nil, msg
	}
	v.Email = strings.ToLower(strings.TrimSpace(v.Email))
	switch {
	case v.Email != "" && !mail.Valid(v.Email):
		return nil, "that email address does not look right"
	case v.CommissionPct < 0 || v.CommissionPct > 100 || v.RetailCommissionPct < 0 || v.RetailCommissionPct > 100:
		return nil, "commission is a percentage between 0 and 100"
	}
	if v.UseLocationHours || v.Hours == nil {
		return nil, ""
	}
	b, m := hoursJSON(v.Hours)
	if m != "" {
		return nil, m
	}
	return string(b), ""
}

// POST /v1/m/staff
func (s *Server) mStaffCreate(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	m := mc(r)
	var req mStaffReq
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, "invalid json")
		return
	}
	hours, msg := req.checkAll()
	if msg != "" {
		writeErr(w, 400, msg)
		return
	}
	if req.Role == "owner" && m.Role != "owner" {
		writeErr(w, 403, "only the owner can add another owner")
		return
	}
	phone, _ := cleanPhone(req.Phone)
	var id string
	if err := s.pool.QueryRow(ctx, `insert into staff (business_id, name, initials, role, level, tone, bookable, email, phone, commission_pct, retail_commission_pct, hours)
		values ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12::jsonb) returning id::text`, m.BusinessID, req.Name, initialsOf(req.Name), req.Role, req.Level, firstNonEmpty(req.Tone, "#7A1F2B"),
		req.Bookable == nil || *req.Bookable, req.Email, phone, req.CommissionPct, req.RetailCommissionPct, hours).Scan(&id); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if req.ServiceIDs != nil {
		_, _ = s.pool.Exec(ctx, `insert into staff_services (staff_id, service_id) select $1, id from services where business_id=$2 and id::text = any($3)`, id, m.BusinessID, *req.ServiceIDs)
	} else {
		_, _ = s.pool.Exec(ctx, `insert into staff_services (staff_id, service_id) select $1, id from services where business_id=$2 and not archived`, id, m.BusinessID)
	}
	if msg := s.saveStaffExtras(r, id, req); msg != "" {
		writeErr(w, 400, msg)
		return
	}
	writeJSON(w, 201, M{"ok": true, "id": id})
}

// PUT /v1/m/staff/{id}
func (s *Server) mStaffUpdate(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	m := mc(r)
	id := chi.URLParam(r, "id")
	var req mStaffReq
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, "invalid json")
		return
	}
	hours, msg := req.checkAll()
	if msg != "" {
		writeErr(w, 400, msg)
		return
	}
	var oldRole string
	if err := s.pool.QueryRow(ctx, `select role from staff where id=$1 and business_id=$2`, id, m.BusinessID).Scan(&oldRole); err != nil {
		writeErr(w, 404, "team member not found")
		return
	}
	if (req.Role == "owner" || oldRole == "owner") && req.Role != oldRole && m.Role != "owner" {
		writeErr(w, 403, "only the owner can change who is an owner")
		return
	}
	phone, _ := cleanPhone(req.Phone)
	// hours: a value replaces their week, use_location_hours clears it, leaving both out keeps what is there.
	keepHours := !req.UseLocationHours && req.Hours == nil
	if _, err := s.pool.Exec(ctx, `update staff set name=$2, initials=$3, role=$4, level=$5, tone=coalesce(nullif($6,''), tone), bookable=coalesce($7, bookable), email=$8, phone=$9,
		commission_pct=$10, retail_commission_pct=$11, hours = case when $13 then hours else $12::jsonb end where id=$1`,
		id, req.Name, initialsOf(req.Name), req.Role, req.Level, req.Tone, req.Bookable, req.Email, phone, req.CommissionPct, req.RetailCommissionPct, hours, keepHours); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if req.ServiceIDs != nil {
		tx, err := s.pool.Begin(ctx)
		if err == nil {
			defer tx.Rollback(ctx)
			// Keep any personal price on services they still do.
			_, _ = tx.Exec(ctx, `delete from staff_services where staff_id=$1 and not (service_id::text = any($2))`, id, *req.ServiceIDs)
			_, _ = tx.Exec(ctx, `insert into staff_services (staff_id, service_id) select $1, id from services where business_id=$2 and id::text = any($3) on conflict do nothing`, id, m.BusinessID, *req.ServiceIDs)
			_ = tx.Commit(ctx)
		}
	}
	if msg := s.saveStaffExtras(r, id, req); msg != "" {
		writeErr(w, 400, msg)
		return
	}
	writeJSON(w, 200, M{"ok": true})
}

// POST /v1/m/staff/{id}/action   {action: archive|restore}
func (s *Server) mStaffAction(w http.ResponseWriter, r *http.Request) {
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
	var role string
	if err := s.pool.QueryRow(ctx, `select role from staff where id=$1 and business_id=$2`, id, m.BusinessID).Scan(&role); err != nil {
		writeErr(w, 404, "team member not found")
		return
	}
	switch req.Action {
	case "archive":
		if role == "owner" {
			writeErr(w, 409, "the owner cannot be removed from the team")
			return
		}
		var upcoming int
		_ = s.pool.QueryRow(ctx, `select count(*) from bookings where staff_id=$1 and starts_at > now() and status in ('requested','confirmed')`, id).Scan(&upcoming)
		if upcoming > 0 {
			writeErr(w, 409, "this person has "+itoa(upcoming)+" upcoming bookings; move or cancel those first")
			return
		}
		_, _ = s.pool.Exec(ctx, `update staff set archived=true, bookable=false where id=$1`, id)
		// Someone who has left should not be able to sign in to this business.
		_, _ = s.pool.Exec(ctx, `delete from merchant_members where staff_id=$1 and business_id=$2 and role <> 'owner'`, id, m.BusinessID)
	case "restore":
		_, _ = s.pool.Exec(ctx, `update staff set archived=false, bookable=true where id=$1`, id)
	default:
		writeErr(w, 400, "unknown action")
		return
	}
	writeJSON(w, 200, M{"ok": true})
}

// POST /v1/m/staff/{id}/invite   {email, role: manager|staff}
// Gives a team member their own sign-in. They get an email with a link to choose a password.
func (s *Server) mStaffInvite(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	m := mc(r)
	id := chi.URLParam(r, "id")
	var req struct {
		Email string `json:"email"`
		Role  string `json:"role"`
	}
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, "invalid json")
		return
	}
	req.Email = strings.ToLower(strings.TrimSpace(req.Email))
	if !mail.Valid(req.Email) {
		writeErr(w, 400, "that email address does not look right")
		return
	}
	if req.Role != "manager" {
		req.Role = "staff"
	}
	var name string
	if err := s.pool.QueryRow(ctx, `select name from staff where id=$1 and business_id=$2 and not archived`, id, m.BusinessID).Scan(&name); err != nil {
		writeErr(w, 404, "team member not found")
		return
	}
	// Reuse their account if they already work at another LogaLuxe business.
	var merchantID string
	isNew := false
	if err := s.pool.QueryRow(ctx, `select id::text from merchant_users where lower(email)=$1`, req.Email).Scan(&merchantID); err != nil {
		tok, _ := newToken() // a random password nobody knows; they choose their own from the link
		hash, _ := hashPassword(tok)
		if err := s.pool.QueryRow(ctx, `insert into merchant_users (email, name, password_hash) values ($1,$2,$3) returning id::text`, req.Email, name, hash).Scan(&merchantID); err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		isNew = true
	}
	if _, err := s.pool.Exec(ctx, `insert into merchant_members (merchant_id, business_id, role, staff_id) values ($1,$2,$3,$4)
		on conflict (merchant_id, business_id) do update set role = case when merchant_members.role = 'owner' then 'owner' else excluded.role end, staff_id = excluded.staff_id`, merchantID, m.BusinessID, req.Role, id); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	_, _ = s.pool.Exec(ctx, `update staff set email=$2 where id=$1 and email=''`, id, req.Email)
	if isNew {
		s.sendMerchantSetLink(ctx, merchantID, req.Email, name, "You have been added to "+m.Business+" on LogaLuxe",
			m.Name+" added you to "+m.Business+" on LogaLuxe. Open this link within 7 days to choose your password and sign in.", 7*24*time.Hour)
	}
	writeJSON(w, 200, M{"ok": true, "new_account": isNew, "mail_mode": s.mail.Mode()})
}

// DELETE /v1/m/staff/{id}/invite   take away a team member's sign-in
func (s *Server) mStaffUninvite(w http.ResponseWriter, r *http.Request) {
	tag, err := s.pool.Exec(r.Context(), `delete from merchant_members where staff_id=$1 and business_id=$2 and role <> 'owner'`, chi.URLParam(r, "id"), mc(r).BusinessID)
	if err != nil || tag.RowsAffected() == 0 {
		writeErr(w, 404, "that person has no sign-in to remove")
		return
	}
	writeJSON(w, 200, M{"ok": true})
}

// ---------- time off ----------

// POST /v1/m/time-off   {staff_id, starts_on, ends_on, reason}
// Staff ask for their own; a manager's request is approved as it is made.
func (s *Server) mTimeOffCreate(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	m := mc(r)
	var req struct {
		StaffID  string `json:"staff_id"`
		StartsOn string `json:"starts_on"`
		EndsOn   string `json:"ends_on"`
		Reason   string `json:"reason"`
	}
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, "invalid json")
		return
	}
	a, err1 := time.Parse("2006-01-02", req.StartsOn)
	b, err2 := time.Parse("2006-01-02", firstNonEmpty(req.EndsOn, req.StartsOn))
	if err1 != nil || err2 != nil || b.Before(a) || b.Sub(a) > 120*24*time.Hour {
		writeErr(w, 400, "choose a first and last day, up to 120 days")
		return
	}
	manager := merchantRank[m.Role] >= merchantRank["manager"]
	if !manager {
		if m.StaffID == "" {
			writeErr(w, 403, "your sign-in is not linked to a team member")
			return
		}
		req.StaffID = m.StaffID
	}
	status := "requested"
	if manager {
		status = "approved"
	}
	tag, err := s.pool.Exec(ctx, `insert into time_off (business_id, staff_id, starts_on, ends_on, reason, status, decided_by)
		select $1, id, $3, $4, $5, $6, $7 from staff where id=$2 and business_id=$1`, m.BusinessID, req.StaffID, a, b, strings.TrimSpace(req.Reason), status, map[bool]string{true: m.Email, false: ""}[manager])
	if err != nil || tag.RowsAffected() == 0 {
		writeErr(w, 400, "that person is not on your team")
		return
	}
	writeJSON(w, 201, M{"ok": true, "status": status})
}

// POST /v1/m/time-off/{id}   {decision: approve|decline|cancel}
func (s *Server) mTimeOffDecide(w http.ResponseWriter, r *http.Request) {
	m := mc(r)
	var req struct {
		Decision string `json:"decision"`
		Reassign bool   `json:"reassign"` // with approve: move their bookings to someone else who is free
	}
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, "invalid json")
		return
	}
	id := chi.URLParam(r, "id")
	var err error
	var n int64
	switch req.Decision {
	case "approve", "decline":
		status := map[string]string{"approve": "approved", "decline": "declined"}[req.Decision]
		tag, e := s.pool.Exec(r.Context(), `update time_off set status=$3, decided_by=$4 where id=$1 and business_id=$2`, id, m.BusinessID, status, m.Email)
		err, n = e, tag.RowsAffected()
	case "cancel":
		tag, e := s.pool.Exec(r.Context(), `delete from time_off where id=$1 and business_id=$2`, id, m.BusinessID)
		err, n = e, tag.RowsAffected()
	default:
		writeErr(w, 400, "decision must be approve, decline or cancel")
		return
	}
	if err != nil || n == 0 {
		writeErr(w, 404, "time off request not found")
		return
	}
	if req.Decision == "approve" && req.Reassign {
		moved, left := s.reassign(r.Context(), m, id)
		writeJSON(w, 200, M{"ok": true, "moved": moved, "left": left})
		return
	}
	writeJSON(w, 200, M{"ok": true})
}

// GET /v1/m/payroll?from=&to=&format=csv   what each person earned: wage or salary, commission and tips
func (s *Server) mPayroll(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	m := mc(r)
	q := r.URL.Query()
	now := time.Now().In(m.Loc)
	from := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, m.Loc)
	if q.Get("from") != "" {
		from = parseDay(q.Get("from"), m.Loc)
	}
	to := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, m.Loc).AddDate(0, 0, 1)
	if q.Get("to") != "" {
		to = parseDay(q.Get("to"), m.Loc).AddDate(0, 0, 1)
	}
	if q.Get("range") != "" {
		from, to, _, _ = reportRange(q.Get("range"), m.Loc)
	}
	out, renters, err := s.payroll(ctx, m, from, to)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	last := to.Add(-time.Minute).Format("2006-01-02") // the last day counted
	if q.Get("format") != "csv" {
		writeJSON(w, 200, M{"payroll": out, "renters": renters, "from": from.Format("2006-01-02"), "to": last})
		return
	}
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="payroll-`+from.Format("2006-01-02")+`-to-`+last+`.csv"`)
	_, _ = w.Write([]byte("\xEF\xBB\xBF"))
	cw := csv.NewWriter(w)
	cols := []string{"name", "pay_type", "rostered_min", "wage_cents", "sales", "service_cents", "commission_pct", "service_commission_cents", "retail_cents", "retail_commission_pct", "retail_commission_cents", "tips_cents", "earned_cents"}
	_ = cw.Write(cols)
	rec := make([]string, len(cols))
	for _, p := range out {
		for i, k := range cols {
			rec[i] = csvCell(p[k])
		}
		_ = cw.Write(rec)
	}
	cw.Flush()
}

var _ = fmt.Sprint

var _ = json.Marshal
