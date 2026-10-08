package httpapi

import (
	"context"
	"fmt"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
)

// What the customer web needs beyond searching and booking: the next free
// times of a business, which days of a month have room, saved businesses,
// reviews a page at a time with a written summary, a calendar file for a
// booking, moving a booking, and what a client holds across businesses.

// ---------- when can I come? ----------

// firstService picks the service to quote openings for when none was named: the first one on the menu that is not an add-on.
func (s *Server) firstService(ctx context.Context, businessID, like string) (id, name string) {
	_ = s.pool.QueryRow(ctx, `select id::text, name from services where business_id=$1 and online and not archived
		order by ($2 <> '' and name ilike '%'||$2||'%') desc, (category = 'Add-ons'), sort, name limit 1`, businessID, like).Scan(&id, &name)
	return
}

// nextOpenings walks forward from today until it has enough free times, obeying the business's own booking rules.
func (s *Server) nextOpenings(ctx context.Context, businessID string, loc *time.Location, serviceIDs []string, staff string, want, maxPerDay int) []openSlot {
	rules := s.bizSettings(ctx, businessID)["booking"]
	lead, maxDays := settingInt(rules["lead_hours"], 2), settingInt(rules["max_days"], 60)
	now := time.Now().In(loc)
	day := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc)
	out := []openSlot{}
	for i := 0; i <= maxDays && i < 28 && len(out) < want; i++ {
		slots, _, err := s.openSlots(ctx, businessID, loc, day.AddDate(0, 0, i), serviceIDs, staff, now.Add(time.Duration(lead)*time.Hour), "")
		if err != nil {
			return out
		}
		// With "anyone", the same time can be free with several people. One of each time is enough here.
		seen, taken := map[string]bool{}, 0
		for _, sl := range slots {
			if seen[sl.Time] || taken >= maxPerDay || len(out) >= want {
				continue
			}
			seen[sl.Time] = true
			taken++
			out = append(out, sl)
		}
	}
	return out
}

func (s *Server) liveBusiness(ctx context.Context, slug string) (id string, loc *time.Location, ok bool) {
	var tz string
	if err := s.pool.QueryRow(ctx, `select id::text, timezone from businesses where slug=$1 and status='live'`, slug).Scan(&id, &tz); err != nil {
		return "", nil, false
	}
	loc, err := time.LoadLocation(tz)
	if err != nil {
		loc = time.UTC
	}
	return id, loc, true
}

// GET /v1/businesses/{slug}/openings?services=id,id&staff=id|any&limit=6
func (s *Server) openings(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	q := r.URL.Query()
	id, loc, ok := s.liveBusiness(ctx, chi.URLParam(r, "slug"))
	if !ok {
		writeErr(w, 404, "business not found")
		return
	}
	ids := splitCSV(q.Get("services"))
	label := ""
	if len(ids) == 0 {
		var one string
		if one, label = s.firstService(ctx, id, ""); one == "" {
			writeJSON(w, 200, M{"slots": []openSlot{}, "service": ""})
			return
		}
		ids = []string{one}
	}
	limit, _ := strconv.Atoi(q.Get("limit"))
	if limit <= 0 || limit > 12 {
		limit = 6
	}
	writeJSON(w, 200, M{"slots": s.nextOpenings(ctx, id, loc, ids, q.Get("staff"), limit, 3), "service": label, "service_ids": ids})
}

// GET /v1/openings?slugs=a,b,c&q=knotless   the next three free times of several businesses, for search results
func (s *Server) openingsBatch(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	slugs := splitCSV(r.URL.Query().Get("slugs"))
	if len(slugs) > 24 {
		slugs = slugs[:24]
	}
	out := M{}
	for _, slug := range slugs {
		id, loc, ok := s.liveBusiness(ctx, slug)
		if !ok {
			continue
		}
		svc, name := s.firstService(ctx, id, strings.TrimSpace(r.URL.Query().Get("q")))
		if svc == "" {
			continue
		}
		out[slug] = M{"service_id": svc, "service": name, "slots": s.nextOpenings(ctx, id, loc, []string{svc}, "any", 3, 2)}
	}
	writeJSON(w, 200, M{"openings": out})
}

// GET /v1/businesses/{slug}/days?month=2026-10&services=id,id&staff=id|any   which days of a month have a free time
func (s *Server) openDays(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	q := r.URL.Query()
	id, loc, ok := s.liveBusiness(ctx, chi.URLParam(r, "slug"))
	if !ok {
		writeErr(w, 404, "business not found")
		return
	}
	ids := splitCSV(q.Get("services"))
	first, err := time.ParseInLocation("2006-01", q.Get("month"), loc)
	if err != nil || len(ids) == 0 {
		writeErr(w, 400, "month must be YYYY-MM and services is required")
		return
	}
	rules := s.bizSettings(ctx, id)["booking"]
	lead, maxDays := settingInt(rules["lead_hours"], 2), settingInt(rules["max_days"], 60)
	now := time.Now().In(loc)
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc)
	last := today.AddDate(0, 0, maxDays)
	days := []M{}
	for d := first; d.Month() == first.Month(); d = d.AddDate(0, 0, 1) {
		n, from := 0, 0
		if !d.Before(today) && !d.After(last) {
			slots, _, err := s.openSlots(ctx, id, loc, d, ids, q.Get("staff"), now.Add(time.Duration(lead)*time.Hour), "")
			if err != nil {
				writeErr(w, 400, "unknown services")
				return
			}
			seen := map[string]bool{}
			for _, sl := range slots {
				if !seen[sl.Time] {
					seen[sl.Time] = true
					n++
				}
				if from == 0 || sl.PriceCents < from {
					from = sl.PriceCents
				}
			}
		}
		days = append(days, M{"date": d.Format("2006-01-02"), "open": n, "from_cents": from, "past": d.Before(today), "too_far": d.After(last)})
	}
	writeJSON(w, 200, M{"month": first.Format("2006-01"), "days": days, "max_days": maxDays})
}

// ---------- reviews ----------

// GET /v1/businesses/{slug}/reviews?page=1&stars=5
func (s *Server) businessReviews(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	q := r.URL.Query()
	var id, owner, summary string
	if err := s.pool.QueryRow(ctx, `select id::text, coalesce(owner_name,''), review_summary from businesses where slug=$1 and status <> 'suspended'`, chi.URLParam(r, "slug")).Scan(&id, &owner, &summary); err != nil {
		writeErr(w, 404, "business not found")
		return
	}
	if !settingBool(s.bizSettings(ctx, id)["storefront"]["show_reviews"], true) {
		writeJSON(w, 200, M{"reviews": []M{}, "total": 0, "breakdown": M{}, "summary": "", "page": 1, "per_page": 10})
		return
	}
	page, _ := strconv.Atoi(q.Get("page"))
	if page < 1 {
		page = 1
	}
	stars, _ := strconv.Atoi(q.Get("stars"))
	const per = 10
	out, err := rows(ctx, s.pool, `select id, author_name, service_name, rating, body, reply, pinned, created_at, count(*) over() as total,
		(select coalesce(json_agg(sm.id order by sm.sort, sm.created_at), '[]') from site_media sm where sm.slot='review' and sm.ref = reviews.id::text and sm.active) as photos from reviews
		where business_id=$1 and status='published' and ($2 = 0 or rating = $2) order by pinned desc, created_at desc limit $3 offset $4`, id, stars, per, (page-1)*per)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	total := 0
	for _, x := range out {
		total = int(toInt(x["total"]))
		delete(x, "total")
	}
	bd, _ := row(ctx, s.pool, `select count(*) as all, count(*) filter (where rating=5) as s5, count(*) filter (where rating=4) as s4, count(*) filter (where rating=3) as s3,
		count(*) filter (where rating=2) as s2, count(*) filter (where rating=1) as s1, coalesce(round(avg(rating),1),0)::float8 as average from reviews where business_id=$1 and status='published'`, id)
	writeJSON(w, 200, M{"reviews": out, "total": total, "breakdown": bd, "summary": summary, "owner": owner, "page": page, "per_page": per})
}

// summariseReviews writes "what people say" for businesses whose reviews have changed. It needs the AI key, and
// at least five reviews, so one visit cannot be dressed up as a pattern.
func (s *Server) summariseReviews(ctx context.Context) {
	if s.cfg.OpenAIKey == "" {
		return
	}
	due, _ := rows(ctx, s.pool, `select b.id, b.name from businesses b where b.status='live'
		and (select count(*) from reviews rv where rv.business_id=b.id and rv.status='published') >= 5
		and (b.review_summary_at is null or exists (select 1 from reviews rv where rv.business_id=b.id and rv.status='published' and rv.created_at > b.review_summary_at)) limit 10`)
	for _, b := range due {
		list, _ := rows(ctx, s.pool, `select rating, service_name, body from reviews where business_id=$1 and status='published' order by created_at desc limit 40`, b["id"])
		var text strings.Builder
		for _, rv := range list {
			fmt.Fprintf(&text, "%v stars (%v): %v\n", rv["rating"], rv["service_name"], rv["body"])
		}
		system := `You summarise what clients say about a beauty business, for people deciding whether to book. Write two plain sentences, at most 45 words in total.
Say only what several reviews support, including a common complaint if there is one. No names of clients, no superlatives, no "overall", no advice. Do not mention the star ratings. Output only the two sentences.`
		summary, err := s.aiWrite(ctx, fmt.Sprint(b["id"]), system, "Business: "+fmt.Sprint(b["name"])+"\nReviews:\n"+text.String(), false)
		if err != nil {
			return
		}
		_, _ = s.pool.Exec(ctx, `update businesses set review_summary=$2, review_summary_at=now() where id=$1`, b["id"], strings.Trim(summary, "\""))
	}
}

// ---------- saved businesses ----------

// GET /v1/auth/favourites
func (s *Server) authFavourites(w http.ResponseWriter, r *http.Request) {
	out, err := rows(r.Context(), s.pool, `select b.slug, b.name, b.tagline, b.category, b.tone, b.rating, b.review_count, b.currency, l.name as area, l.city,
		(select min(price_cents) from services sv where sv.business_id=b.id and sv.online and not sv.archived and sv.category <> 'Add-ons') as from_cents, f.created_at
		from user_favourites f join businesses b on b.id = f.business_id left join locations l on l.business_id = b.id and l.is_primary
		where f.user_id=$1 and b.status <> 'suspended' order by f.created_at desc`, currentCustomer(r).ID)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, M{"favourites": out})
}

// PUT /v1/auth/favourites/{slug}   DELETE /v1/auth/favourites/{slug}
func (s *Server) authFavouriteSet(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	uid, slug := currentCustomer(r).ID, chi.URLParam(r, "slug")
	if r.Method == http.MethodDelete {
		_, _ = s.pool.Exec(ctx, `delete from user_favourites f using businesses b where b.id = f.business_id and f.user_id=$1 and b.slug=$2`, uid, slug)
		writeJSON(w, 200, M{"ok": true, "saved": false})
		return
	}
	tag, err := s.pool.Exec(ctx, `insert into user_favourites (user_id, business_id) select $1, id from businesses where slug=$2 and status <> 'suspended' on conflict do nothing`, uid, slug)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	var exists bool
	_ = s.pool.QueryRow(ctx, `select exists(select 1 from user_favourites f join businesses b on b.id = f.business_id where f.user_id=$1 and b.slug=$2)`, uid, slug).Scan(&exists)
	if !exists && tag.RowsAffected() == 0 {
		writeErr(w, 404, "business not found")
		return
	}
	writeJSON(w, 200, M{"ok": true, "saved": true})
}

// ---------- a booking in the client's calendar ----------

// GET /v1/bookings/{id}/calendar.ics
func (s *Server) bookingCalendar(w http.ResponseWriter, r *http.Request) {
	b, err := row(r.Context(), s.pool, `select bk.id, bk.starts_at, bk.ends_at, bk.status, b.name as business, b.slug, st.name as staff, coalesce(l.address,'') as address, coalesce(l.city,'') as city, coalesce(l.region,'') as region,
		coalesce((select string_agg(name, ' + ') from booking_items where booking_id = bk.id), 'Appointment') as services
		from bookings bk join businesses b on b.id = bk.business_id join staff st on st.id = bk.staff_id left join locations l on l.id = bk.location_id where bk.id::text=$1`, chi.URLParam(r, "id"))
	if err != nil {
		writeErr(w, 404, "booking not found")
		return
	}
	stamp := func(t time.Time) string { return t.UTC().Format("20060102T150405Z") }
	esc := func(v any) string {
		return strings.NewReplacer("\\", "\\\\", ";", "\\;", ",", "\\,", "\n", "\\n").Replace(fmt.Sprint(v))
	}
	where := strings.Trim(strings.Join([]string{fmt.Sprint(b["address"]), fmt.Sprint(b["city"]), fmt.Sprint(b["region"])}, ", "), ", ")
	link := strings.TrimRight(s.cfg.WebURL, "/") + "/b/" + fmt.Sprint(b["slug"])
	ics := strings.Join([]string{
		"BEGIN:VCALENDAR", "VERSION:2.0", "PRODID:-//LogaLuxe//Bookings//EN", "CALSCALE:GREGORIAN", "METHOD:PUBLISH", "BEGIN:VEVENT",
		"UID:" + fmt.Sprint(b["id"]) + "@logaluxe", "DTSTAMP:" + stamp(time.Now()), "DTSTART:" + stamp(b["starts_at"].(time.Time)), "DTEND:" + stamp(b["ends_at"].(time.Time)),
		"SUMMARY:" + esc(fmt.Sprint(b["services"])+" at "+fmt.Sprint(b["business"])), "LOCATION:" + esc(where),
		"DESCRIPTION:" + esc("With "+fmt.Sprint(b["staff"])+". Manage this booking: "+strings.TrimRight(s.cfg.WebURL, "/")+"/account\nBusiness page: "+link),
		"BEGIN:VALARM", "TRIGGER:-PT2H", "ACTION:DISPLAY", "DESCRIPTION:" + esc("Your appointment at "+fmt.Sprint(b["business"])+" is in 2 hours"), "END:VALARM",
		"END:VEVENT", "END:VCALENDAR", ""}, "\r\n")
	w.Header().Set("Content-Type", "text/calendar; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="logaluxe-booking.ics"`)
	_, _ = w.Write([]byte(ics))
}

// ---------- moving a booking ----------

// GET /v1/auth/bookings/{id}   one of the client's own bookings, with what they may still do to it
func (s *Server) authBooking(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	b, err := row(ctx, s.pool, `select bk.id, bk.status, bk.starts_at, bk.ends_at, bk.total_cents, bk.discount_cents, bk.deposit_cents, bk.deposit_paid, bk.notes, bk.staff_id, bk.business_id, bk.guest_name, bk.series_id,
		b.name as business, b.slug, b.currency, b.timezone, b.tone, st.name as staff, l.address, l.city,
		(select coalesce(array_agg(bi.service_id::text) filter (where bi.service_id is not null), '{}') from booking_items bi where bi.booking_id = bk.id) as service_ids,
		(select string_agg(name, ', ') from booking_items where booking_id = bk.id) as services
		from bookings bk join businesses b on b.id = bk.business_id join staff st on st.id = bk.staff_id left join locations l on l.id = bk.location_id
		where bk.id::text=$1 and bk.user_id=$2`, chi.URLParam(r, "id"), currentCustomer(r).ID)
	if err != nil {
		writeErr(w, 404, "booking not found")
		return
	}
	policy := s.bizSettings(ctx, fmt.Sprint(b["business_id"]))["policy"]
	hours := settingInt(policy["cancel_hours"], 24)
	starts := b["starts_at"].(time.Time)
	open := (b["status"] == "requested" || b["status"] == "confirmed") && starts.After(time.Now())
	b["can_cancel"] = open
	b["can_reschedule"] = open && time.Until(starts) >= time.Duration(hours)*time.Hour
	b["free_until"] = starts.Add(-time.Duration(hours) * time.Hour)
	b["cancel_hours"] = hours
	b["late_cancel_fee"] = firstNonEmpty(fmt.Sprint(policy["late_cancel_fee"]), "deposit")
	delete(b, "business_id")
	writeJSON(w, 200, M{"booking": b})
}

// POST /v1/auth/bookings/{id}/reschedule   {starts_at, staff_id}
// A client can move their own booking while it is still inside the free window. The new time must be one the
// business is offering, so the same rules as a new booking apply. The price agreed when booking is kept.
func (s *Server) authReschedule(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id := chi.URLParam(r, "id")
	var req struct {
		StartsAt string `json:"starts_at"`
		StaffID  string `json:"staff_id"`
	}
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, "invalid json")
		return
	}
	start, err := time.Parse(time.RFC3339, req.StartsAt)
	if err != nil {
		writeErr(w, 400, "choose a new time")
		return
	}
	var bizID, tz, status, staffID string
	var starts time.Time
	var svc []string
	if err := s.pool.QueryRow(ctx, `select bk.business_id::text, b.timezone, bk.status, bk.staff_id::text, bk.starts_at,
		(select coalesce(array_agg(bi.service_id::text) filter (where bi.service_id is not null), '{}') from booking_items bi where bi.booking_id = bk.id)
		from bookings bk join businesses b on b.id = bk.business_id where bk.id::text=$1 and bk.user_id=$2`, id, currentCustomer(r).ID).Scan(&bizID, &tz, &status, &staffID, &starts, &svc); err != nil {
		writeErr(w, 404, "booking not found")
		return
	}
	hours := settingInt(s.bizSettings(ctx, bizID)["policy"]["cancel_hours"], 24)
	switch {
	case status != "requested" && status != "confirmed":
		writeErr(w, 409, "this booking can no longer be moved")
		return
	case time.Until(starts) < time.Duration(hours)*time.Hour:
		writeErr(w, 409, "it is less than "+itoa(hours)+" hours before your appointment, so it cannot be moved here; message the business instead")
		return
	case len(svc) == 0:
		writeErr(w, 409, "this booking cannot be moved online; message the business instead")
		return
	}
	loc, err := time.LoadLocation(tz)
	if err != nil {
		loc = time.UTC
	}
	want := firstNonEmpty(req.StaffID, staffID)
	local := start.In(loc)
	day := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, loc)
	lead := settingInt(s.bizSettings(ctx, bizID)["booking"]["lead_hours"], 2)
	slots, _, err := s.openSlots(ctx, bizID, loc, day, svc, want, time.Now().Add(time.Duration(lead)*time.Hour), id)
	if err != nil {
		writeErr(w, 409, "this booking cannot be moved online; message the business instead")
		return
	}
	var pick *openSlot
	for i := range slots {
		if t, _ := time.Parse(time.RFC3339, slots[i].StartAt); t.Equal(start) && (want == "any" || slots[i].StaffID == want) {
			pick = &slots[i]
			break
		}
	}
	if pick == nil {
		writeErr(w, 409, "that time is no longer free; choose another")
		return
	}
	// The length of the visit does not change, so the end moves with the start.
	tag, err := s.pool.Exec(ctx, `update bookings set starts_at=$2::timestamptz, ends_at=$2::timestamptz + (ends_at - starts_at), staff_id=$3::uuid where id::text=$1 and status in ('requested','confirmed')`, id, start, pick.StaffID)
	if err != nil || tag.RowsAffected() == 0 {
		writeErr(w, 409, "that time was just taken; choose another")
		return
	}
	s.notifyBusiness(bizID, "new_booking_email", "A client moved a booking", "A client moved their booking to "+local.Format("Monday 2 January at 15:04")+".\n\nOpen your calendar: "+strings.TrimRight(s.cfg.WebURL, "/")+"/business/calendar?date="+local.Format("2006-01-02")+"&booking="+id)
	writeJSON(w, 200, M{"ok": true, "starts_at": start, "staff": pick.Staff})
}

// ---------- what a client holds ----------

// GET /v1/auth/wallet   packages, memberships and loyalty points at each business the client has visited
func (s *Server) authWallet(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	c := currentCustomer(r)
	// The business's record of this person: any client row one of their bookings is attached to, or one with their phone.
	mine, _ := rows(ctx, s.pool, `select distinct cl.id, cl.business_id, b.name as business, b.slug, b.currency, b.tone from clients cl join businesses b on b.id = cl.business_id
		where cl.id in (select client_id from bookings where user_id=$1 and client_id is not null) or ($2 <> '' and cl.phone = $2) order by b.name`, c.ID, c.Phone)
	out := []M{}
	for _, cl := range mine {
		bizID, clientID := fmt.Sprint(cl["business_id"]), fmt.Sprint(cl["id"])
		plans, member := s.clientPlans(ctx, bizID, clientID)
		active := []M{}
		for _, p := range plans {
			if p["status"] == "active" || p["status"] == "past_due" {
				active = append(active, p)
			}
		}
		rules := loyaltyFor(ctx, s.pool, bizID)
		points := 0
		if rules.Enabled {
			points = pointsOf(ctx, s.pool, bizID, clientID)
		}
		if len(active) == 0 && points == 0 {
			continue
		}
		out = append(out, M{"business": cl["business"], "slug": cl["slug"], "currency": cl["currency"], "tone": cl["tone"], "plans": active, "member": member,
			"points": points, "points_value_cents": points * rules.PointValue, "min_redeem": rules.MinRedeem})
	}
	writeJSON(w, 200, M{"wallet": out, "credit_cents": creditBalance(ctx, s.pool, c.ID), "credit_currency": "USD"})
}

var _ = math.Round
