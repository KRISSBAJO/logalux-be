package httpapi

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// The calendar: what is booked, who is working, and everything that happens
// to a booking from the moment it is made to the moment the client leaves.

func parseDay(v string, loc *time.Location) time.Time {
	if t, err := time.ParseInLocation("2006-01-02", v, loc); err == nil {
		return t
	}
	n := time.Now().In(loc)
	return time.Date(n.Year(), n.Month(), n.Day(), 0, 0, 0, 0, loc)
}

// parseLocal accepts a full timestamp, or a local "2006-01-02T15:04" in the business's time zone.
func parseLocal(v string, loc *time.Location) (time.Time, bool) {
	if t, err := time.Parse(time.RFC3339, v); err == nil {
		return t, true
	}
	if t, err := time.ParseInLocation("2006-01-02T15:04", v, loc); err == nil {
		return t, true
	}
	return time.Time{}, false
}

const bookingCols = `bk.id, bk.status, bk.starts_at, bk.ends_at, bk.client_id, bk.client_name, bk.client_phone, bk.staff_id, bk.source, bk.notes,
	bk.total_cents, bk.discount_cents, bk.deposit_cents, bk.deposit_paid, bk.tip_cents, bk.paid_at, bk.checked_in_at, bk.promo_code, bk.guest_name, bk.series_id,
	st.name as staff, st.initials as staff_initials, st.tone as staff_tone,
	(select string_agg(name, ' + ') from booking_items where booking_id = bk.id) as services`

// GET /v1/m/calendar?date=YYYY-MM-DD&days=1|7|month
// A week starts on its Monday. A month is the six weeks that cover it.
func (s *Server) mCalendar(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	m := mc(r)
	day := parseDay(r.URL.Query().Get("date"), m.Loc)
	days := 1
	if r.URL.Query().Get("days") == "7" {
		days = 7
		day = day.AddDate(0, 0, -((int(day.Weekday()) + 6) % 7)) // back to Monday
	}
	if r.URL.Query().Get("days") == "month" {
		days = 42
		first := time.Date(day.Year(), day.Month(), 1, 0, 0, 0, 0, m.Loc)
		day = first.AddDate(0, 0, -((int(first.Weekday()) + 6) % 7))
	}
	end := day.AddDate(0, 0, days)

	var locHours any
	_ = s.pool.QueryRow(ctx, `select hours from locations where business_id=$1 and is_primary`, m.BusinessID).Scan(&locHours)
	staff, _ := rows(ctx, s.pool, `select id, name, initials, role, level, tone, bookable, hours, breaks, pay_type from staff where business_id=$1 and not archived order by role='owner' desc, name`, m.BusinessID)
	bookings, err := rows(ctx, s.pool, `select `+bookingCols+` from bookings bk join staff st on st.id = bk.staff_id
		where bk.business_id=$1 and bk.starts_at < $3 and bk.ends_at > $2 and bk.status not in ('cancelled_client','cancelled_business','rescheduled')
		order by bk.starts_at`, m.BusinessID, day, end)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	blocks, _ := rows(ctx, s.pool, `select id, staff_id, starts_at, ends_at, reason, external from calendar_blocks where business_id=$1 and starts_at < $3 and ends_at > $2 order by starts_at`, m.BusinessID, day, end)
	off, _ := rows(ctx, s.pool, `select staff_id, starts_on, ends_on, reason from time_off where business_id=$1 and status='approved' and starts_on < $3::date and ends_on >= $2::date`, m.BusinessID, day.Format("2006-01-02"), end.Format("2006-01-02"))
	stats, _ := row(ctx, s.pool, `select count(*) as bookings,
		coalesce(sum(total_cents),0) as expected_cents,
		coalesce(sum(extract(epoch from (ends_at - starts_at))/60),0)::int as booked_min,
		(select count(*) from waitlist_entries where business_id=$1 and status='waiting') as waitlist
		from bookings where business_id=$1 and starts_at >= $2 and starts_at < $3 and status not in ('cancelled_client','cancelled_business','rescheduled','no_show')`, m.BusinessID, day, end)
	if !perm(m, "see_all_calendars") {
		// This team member sees their own column only.
		mine := func(list []M) []M {
			out := []M{}
			for _, x := range list {
				if fmt.Sprint(x["staff_id"]) == m.StaffID {
					out = append(out, x)
				}
			}
			return out
		}
		bookings, blocks = mine(bookings), mine(blocks)
		only := []M{}
		for _, x := range staff {
			if fmt.Sprint(x["id"]) == m.StaffID {
				only = append(only, x)
			}
		}
		staff = only
	}
	writeJSON(w, 200, M{"date": day.Format("2006-01-02"), "days": days, "timezone": m.Timezone, "location_hours": locHours, "staff": staff, "bookings": bookings, "blocks": blocks, "time_off": off, "stats": stats})
}

// GET /v1/m/bookings?q=   find a booking by the client's name or phone: what is coming up first, then the most recent
func (s *Server) mBookingSearch(w http.ResponseWriter, r *http.Request) {
	m := mc(r)
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if len(q) < 2 {
		writeJSON(w, 200, M{"bookings": []M{}})
		return
	}
	out, err := rows(r.Context(), s.pool, `select `+bookingCols+` from bookings bk join staff st on st.id = bk.staff_id
		where bk.business_id=$1 and (bk.client_name ilike '%' || $2 || '%' or bk.guest_name ilike '%' || $2 || '%' or replace(bk.client_phone,' ','') like '%' || replace($2,' ','') || '%')
		order by (bk.starts_at >= now() - interval '3 hours') desc, abs(extract(epoch from (bk.starts_at - now()))) limit 25`, m.BusinessID, q)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, M{"bookings": out})
}

// GET /v1/m/availability?date=&services=id,id&staff=id|any&exclude=bookingId
func (s *Server) mAvailability(w http.ResponseWriter, r *http.Request) {
	m := mc(r)
	q := r.URL.Query()
	ids := splitCSV(q.Get("services"))
	if len(ids) == 0 {
		writeErr(w, 400, "choose at least one service")
		return
	}
	// Staff can book right up to the minute, so there is no lead time here.
	slots, dur, err := s.openSlots(r.Context(), m.BusinessID, m.Loc, parseDay(q.Get("date"), m.Loc), ids, q.Get("staff"), time.Now().Add(-5*time.Minute), q.Get("exclude"))
	if err != nil {
		writeErr(w, 400, "those services are not on your menu")
		return
	}
	writeJSON(w, 200, M{"slots": slots, "duration_min": dur})
}

// POST /v1/m/bookings   a booking taken by phone, at the desk, or for a walk-in
func (s *Server) mBookingCreate(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	m := mc(r)
	var req struct {
		ClientID    string   `json:"client_id"`
		ClientName  string   `json:"client_name"`
		ClientPhone string   `json:"client_phone"`
		ClientEmail string   `json:"client_email"`
		StaffID     string   `json:"staff_id"`
		StartsAt    string   `json:"starts_at"`
		ServiceIDs  []string `json:"service_ids"`
		Notes       string   `json:"notes"`
		Source      string   `json:"source"`
	}
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, "invalid json")
		return
	}
	start, ok := parseLocal(req.StartsAt, m.Loc)
	if !ok {
		writeErr(w, 400, "choose a date and time")
		return
	}
	if len(req.ServiceIDs) == 0 || req.StaffID == "" {
		writeErr(w, 400, "choose a service and who will do it")
		return
	}
	if req.Source != "phone" && req.Source != "walk_in" && req.Source != "rebook" {
		req.Source = "walk_in"
	}
	phone, phoneOK := cleanPhone(req.ClientPhone)
	if !phoneOK {
		writeErr(w, 400, "that phone number does not look right; include the country code")
		return
	}
	svcs, err := rows(ctx, s.pool, `select id, name, duration_min, processing_min, buffer_min, price_cents, deposit_cents from services where business_id=$1 and id = any($2::uuid[])`, m.BusinessID, req.ServiceIDs)
	if err != nil || len(svcs) == 0 {
		writeErr(w, 400, "those services are not on your menu")
		return
	}
	var staffOK bool
	_ = s.pool.QueryRow(ctx, `select exists(select 1 from staff where id=$1 and business_id=$2 and not archived)`, req.StaffID, m.BusinessID).Scan(&staffOK)
	if !staffOK {
		writeErr(w, 400, "that person is not on your team")
		return
	}
	prices := s.loadPricing(ctx, m.BusinessID)
	for _, sv := range svcs {
		p, _ := prices.price(fmt.Sprint(sv["id"]), req.StaffID, start.In(m.Loc))
		sv["price_cents"] = int32(p)
	}
	var total, dur, buf int
	for _, sv := range svcs {
		total += int(sv["price_cents"].(int32))
		dur += int(sv["duration_min"].(int32)) + int(sv["processing_min"].(int32))
		if b := int(sv["buffer_min"].(int32)); b > buf {
			buf = b
		}
	}
	end := start.Add(time.Duration(dur+buf) * time.Minute)
	if taken := clash(s.resourcesFor(ctx, m.BusinessID, req.ServiceIDs, start, end, ""), start, end); taken != "" {
		writeErr(w, 409, "every "+strings.ToLower(taken)+" is taken at that time")
		return
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	defer tx.Rollback(ctx)
	var clientID *string
	name := strings.TrimSpace(req.ClientName)
	if req.ClientID != "" {
		var id, n, p string
		if err := tx.QueryRow(ctx, `select id::text, name, phone from clients where id=$1 and business_id=$2`, req.ClientID, m.BusinessID).Scan(&id, &n, &p); err != nil {
			writeErr(w, 400, "that client is not in your list")
			return
		}
		clientID, name, phone = &id, n, p
	} else {
		if name == "" {
			name = "Walk-in"
		}
		if phone != "" {
			var id string
			if err := tx.QueryRow(ctx, `insert into clients (business_id, name, phone, email) values ($1,$2,$3,$4)
				on conflict (business_id, phone) do update set name = excluded.name returning id::text`, m.BusinessID, name, phone, strings.TrimSpace(req.ClientEmail)).Scan(&id); err == nil {
				clientID = &id
			}
		}
	}
	var id string
	err = tx.QueryRow(ctx, `insert into bookings (business_id, location_id, staff_id, client_id, client_name, client_phone, client_email, status, starts_at, ends_at, source, total_cents, notes)
		values ($1, (select id from locations where business_id=$1 and is_primary), $2, $3, $4, $5, $6, 'confirmed', $7, $8, $9, $10, $11) returning id::text`,
		m.BusinessID, req.StaffID, clientID, name, phone, strings.TrimSpace(req.ClientEmail), start, end, req.Source, total, req.Notes).Scan(&id)
	if err != nil {
		var pg *pgconn.PgError
		if errors.As(err, &pg) && pg.Code == "23P01" {
			writeErr(w, 409, "that person already has a booking at that time")
			return
		}
		writeErr(w, 500, err.Error())
		return
	}
	for _, sv := range svcs {
		if _, err := tx.Exec(ctx, `insert into booking_items (booking_id, service_id, name, price_cents, duration_min) values ($1,$2,$3,$4,$5)`, id, sv["id"], sv["name"], sv["price_cents"], sv["duration_min"]); err != nil {
			writeErr(w, 500, err.Error())
			return
		}
	}
	if err := tx.Commit(ctx); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 201, M{"ok": true, "id": id})
}

// GET /v1/m/bookings/{id}
func (s *Server) mBooking(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	m := mc(r)
	id := chi.URLParam(r, "id")
	b, err := row(ctx, s.pool, `select `+bookingCols+`, bk.cancel_reason, bk.created_at, bk.client_email from bookings bk join staff st on st.id = bk.staff_id where bk.id=$1 and bk.business_id=$2`, id, m.BusinessID)
	if err != nil {
		writeErr(w, 404, "booking not found")
		return
	}
	items, _ := rows(ctx, s.pool, `select service_id, name, price_cents, duration_min from booking_items where booking_id=$1`, id)
	var history M
	if b["client_id"] != nil {
		history, _ = row(ctx, s.pool, `select count(*) filter (where status in ('completed','paid')) as visits,
			count(*) filter (where status = 'no_show') as no_shows,
			coalesce(sum(total_cents) filter (where status in ('completed','paid')),0) as spent_cents,
			min(starts_at) as first_visit from bookings where client_id=$1`, b["client_id"])
		if c, err := row(ctx, s.pool, `select notes, tags from clients where id=$1`, b["client_id"]); err == nil {
			history["notes"], history["tags"] = c["notes"], c["tags"]
		}
	}
	answers, _ := rows(ctx, s.pool, `select label, kind, answer from booking_answers where booking_id=$1 order by sort`, id)
	writeJSON(w, 200, M{"booking": b, "items": items, "client": history, "answers": answers})
}

// POST /v1/m/bookings/{id}/action
// {action: confirm|check_in|start|complete|no_show|cancel|reschedule, starts_at, staff_id, reason}
func (s *Server) mBookingAction(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	m := mc(r)
	id := chi.URLParam(r, "id")
	var req struct {
		Action   string `json:"action"`
		StartsAt string `json:"starts_at"`
		StaffID  string `json:"staff_id"`
		Reason   string `json:"reason"`
	}
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, "invalid json")
		return
	}
	var status, staffID string
	var start, end time.Time
	var clientID *string
	if err := s.pool.QueryRow(ctx, `select status, staff_id::text, starts_at, ends_at, client_id::text from bookings where id=$1 and business_id=$2`, id, m.BusinessID).Scan(&status, &staffID, &start, &end, &clientID); err != nil {
		writeErr(w, 404, "booking not found")
		return
	}
	closed := status == "paid" || status == "cancelled_client" || status == "cancelled_business" || status == "no_show" || status == "rescheduled"
	clash := func(err error) bool {
		var pg *pgconn.PgError
		if errors.As(err, &pg) && pg.Code == "23P01" {
			writeErr(w, 409, "that person already has a booking at that time")
			return true
		}
		return false
	}
	var err error
	switch req.Action {
	case "confirm":
		if status != "requested" {
			writeErr(w, 409, "only a requested booking can be confirmed")
			return
		}
		_, err = s.pool.Exec(ctx, `update bookings set status='confirmed' where id=$1`, id)
	case "check_in":
		if status != "confirmed" && status != "requested" {
			writeErr(w, 409, "this booking cannot be checked in")
			return
		}
		_, err = s.pool.Exec(ctx, `update bookings set status='checked_in', checked_in_at=now() where id=$1`, id)
	case "start":
		if status != "confirmed" && status != "checked_in" {
			writeErr(w, 409, "this booking cannot be started")
			return
		}
		_, err = s.pool.Exec(ctx, `update bookings set status='in_progress', started_at=now(), checked_in_at=coalesce(checked_in_at, now()) where id=$1`, id)
	case "complete":
		if closed || status == "completed" {
			writeErr(w, 409, "this booking is already finished")
			return
		}
		_, err = s.pool.Exec(ctx, `update bookings set status='completed', completed_at=now() where id=$1`, id)
	case "no_show":
		if closed || status == "completed" {
			writeErr(w, 409, "this booking is already finished")
			return
		}
		if _, err = s.pool.Exec(ctx, `update bookings set status='no_show', cancel_reason=$2 where id=$1`, id, req.Reason); err == nil && clientID != nil {
			_, _ = s.pool.Exec(ctx, `update clients set no_show_count = no_show_count + 1 where id=$1`, *clientID)
			if fee, _ := s.bizSettings(ctx, m.BusinessID)["policy"]["no_show_fee"].(string); fee == "none" {
				// This business charges nothing for a no-show, so the deposit goes back.
				_, _ = s.pool.Exec(ctx, `delete from ledger where booking_id=$1 and kind='deposit' and status='held'`, id)
				s.refundDeposit(id)
				_, _ = s.pool.Exec(ctx, `update bookings set deposit_paid=false where id=$1 and deposit_paid`, id)
			} else {
				// A deposit is kept on a no-show: it stops being held and becomes the business's money.
				_, _ = s.pool.Exec(ctx, `update ledger set status='pending', settles_at=now() + interval '2 days', description = description || ' (kept, no-show)' where booking_id=$1 and kind='deposit' and status='held'`, id)
			}
		}
	case "cancel":
		if closed || status == "completed" {
			writeErr(w, 409, "this booking is already finished")
			return
		}
		if _, err = s.pool.Exec(ctx, `update bookings set status='cancelled_business', cancel_reason=$2 where id=$1`, id, req.Reason); err == nil {
			// The business cancelled, so the client's deposit goes back in full.
			_, _ = s.pool.Exec(ctx, `delete from ledger where booking_id=$1 and kind='deposit' and status='held'`, id)
			s.refundDeposit(id)
			_, _ = s.pool.Exec(ctx, `update bookings set deposit_paid=false where id=$1 and deposit_paid`, id)
		}
	case "reschedule":
		if closed || status == "completed" || status == "in_progress" {
			writeErr(w, 409, "this booking can no longer be moved")
			return
		}
		ns, ok := parseLocal(req.StartsAt, m.Loc)
		if !ok {
			writeErr(w, 400, "choose a new date and time")
			return
		}
		if req.StaffID == "" {
			req.StaffID = staffID
		}
		var staffOK bool
		_ = s.pool.QueryRow(ctx, `select exists(select 1 from staff where id=$1 and business_id=$2 and not archived)`, req.StaffID, m.BusinessID).Scan(&staffOK)
		if !staffOK {
			writeErr(w, 400, "that person is not on your team")
			return
		}
		_, err = s.pool.Exec(ctx, `update bookings set starts_at=$2, ends_at=$3, staff_id=$4 where id=$1`, id, ns, ns.Add(end.Sub(start)), req.StaffID)
	default:
		writeErr(w, 400, "unknown action")
		return
	}
	if err != nil {
		if clash(err) {
			return
		}
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, M{"ok": true})
}

// POST /v1/m/blocks   {staff_id, starts_at, ends_at, reason}
func (s *Server) mBlockCreate(w http.ResponseWriter, r *http.Request) {
	m := mc(r)
	var req struct {
		StaffID  string `json:"staff_id"`
		StartsAt string `json:"starts_at"`
		EndsAt   string `json:"ends_at"`
		Reason   string `json:"reason"`
	}
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, "invalid json")
		return
	}
	a, ok1 := parseLocal(req.StartsAt, m.Loc)
	b, ok2 := parseLocal(req.EndsAt, m.Loc)
	if !ok1 || !ok2 || !b.After(a) || b.Sub(a) > 24*time.Hour {
		writeErr(w, 400, "choose a start and a later end on the same day")
		return
	}
	var id string
	if err := s.pool.QueryRow(r.Context(), `insert into calendar_blocks (business_id, staff_id, starts_at, ends_at, reason, created_by)
		select $1, id, $3, $4, $5, $6 from staff where id=$2 and business_id=$1 returning id::text`, m.BusinessID, req.StaffID, a, b, strings.TrimSpace(req.Reason), m.Email).Scan(&id); err != nil {
		writeErr(w, 400, "that person is not on your team")
		return
	}
	writeJSON(w, 201, M{"ok": true, "id": id})
}

// DELETE /v1/m/blocks/{id}
func (s *Server) mBlockDelete(w http.ResponseWriter, r *http.Request) {
	tag, err := s.pool.Exec(r.Context(), `delete from calendar_blocks where id=$1 and business_id=$2`, chi.URLParam(r, "id"), mc(r).BusinessID)
	if err != nil || tag.RowsAffected() == 0 {
		writeErr(w, 404, "blocked time not found")
		return
	}
	writeJSON(w, 200, M{"ok": true})
}

// GET /v1/m/waitlist
func (s *Server) mWaitlist(w http.ResponseWriter, r *http.Request) {
	out, err := rows(r.Context(), s.pool, `select wl.id, wl.client_name, wl.client_phone, wl.day, wl.time_of_day, wl.status, wl.created_at, sv.name as service
		from waitlist_entries wl left join services sv on sv.id = wl.service_id where wl.business_id=$1 and wl.status in ('waiting','offered') order by wl.created_at`, mc(r).BusinessID)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, M{"waitlist": out})
}

// PUT /v1/m/waitlist/{id}   {status: offered|booked|removed}
func (s *Server) mWaitlistUpdate(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Status string `json:"status"`
	}
	if err := readJSON(r, &req); err != nil || (req.Status != "offered" && req.Status != "booked" && req.Status != "removed" && req.Status != "waiting") {
		writeErr(w, 400, "status must be waiting, offered, booked or removed")
		return
	}
	tag, err := s.pool.Exec(r.Context(), `update waitlist_entries set status=$3 where id=$1 and business_id=$2`, chi.URLParam(r, "id"), mc(r).BusinessID, req.Status)
	if err != nil || tag.RowsAffected() == 0 {
		writeErr(w, 404, "waitlist entry not found")
		return
	}
	writeJSON(w, 200, M{"ok": true})
}
