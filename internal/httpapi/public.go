package httpapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// GET /v1/businesses?q=&category=&market=
func (s *Server) listBusinesses(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	// A page of results. A city can hold hundreds of businesses, so never send them all as cards.
	limit, _ := strconv.Atoi(q.Get("limit"))
	if limit <= 0 || limit > 60 {
		limit = 60
	}
	offset, _ := strconv.Atoi(q.Get("offset"))
	if offset < 0 {
		offset = 0
	}
	filters := []any{strings.TrimSpace(q.Get("q")), q.Get("category"), q.Get("market"), strings.TrimSpace(q.Get("where"))}
	const matches = `b.status='live' and coalesce((b.settings->'booking'->>'on_search')::boolean, true)
		  and ($1='' or b.name ilike '%'||$1||'%' or b.tagline ilike '%'||$1||'%' or b.category ilike '%'||$1||'%'
		       or exists (select 1 from services sv where sv.business_id=b.id and sv.online and sv.name ilike '%'||$1||'%'))
		  and ($2='' or b.category=$2)
		  and ($3='' or b.market=$3)
		  and ($4='' or l.city ilike '%'||$4||'%' or l.name ilike '%'||$4||'%' or l.address ilike '%'||$4||'%' or l.region ilike $4)`
	const fromPrice = `(select min(price_cents) from services sv where sv.business_id=b.id and sv.online and sv.category <> 'Add-ons')`
	order := map[string]string{"reviews": "b.review_count desc, b.rating desc", "price": "from_cents asc nulls last, b.rating desc"}[q.Get("sort")]
	if order == "" {
		// A business that bids for new clients is listed first, and marked as promoted.
		order = "boost desc, b.rating desc, b.review_count desc"
	}
	out, err := rows(r.Context(), s.pool, `
		select b.id, b.slug, b.name, b.tagline, b.category, b.market, b.currency, b.timezone, b.rating, b.review_count,
		       b.verification_status, b.tone, b.highlights, (select sm.id from site_media sm where sm.slot='logo' and sm.ref = b.slug and sm.active limit 1) as logo_id,
		       l.name as area, l.city, l.hours, l.lat, l.lng, count(*) over() as total, `+boostSQL+`::float8 as boost,
		       (select count(*) from staff st where st.business_id=b.id and st.bookable) as staff_count,
		       (select min(price_cents) from services sv where sv.business_id=b.id and sv.online and sv.category <> 'Add-ons') as from_cents,
		       -- The first few services, so a result can show real prices without opening the page.
		       coalesce((select jsonb_agg(x) from (select sv.name, sv.price_cents, sv.duration_min from services sv
		         where sv.business_id=b.id and sv.online and sv.category <> 'Add-ons'
		         order by ($1 <> '' and sv.name ilike '%'||$1||'%') desc, sv.sort, sv.name limit 3) x), '[]'::jsonb) as services
		from businesses b
		left join locations l on l.business_id=b.id and l.is_primary
		where `+matches+`
		order by `+order+`, b.name
		limit `+strconv.Itoa(limit)+` offset `+strconv.Itoa(offset), filters...)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	total := 0
	var seen, promoted []string
	for _, b := range out {
		if n, ok := b["total"].(int64); ok {
			total = int(n)
		}
		delete(b, "total")
		boost, _ := b["boost"].(float64)
		b["promoted"] = boost > 0 && q.Get("sort") == ""
		delete(b, "boost")
		seen = append(seen, fmt.Sprint(b["id"]))
		if boost > 0 && q.Get("sort") == "" {
			promoted = append(promoted, fmt.Sprint(b["id"]))
		}
	}
	// A sitemap build or another machine reading the list is not a person looking at it.
	if q.Get("quiet") != "1" {
		s.countLeadEvents("impression", seen...)
		s.countLeadEvents("promoted_impression", promoted...)
	}
	// Every match as a light map pin, so the map shows the whole city while the list shows one page.
	pins, _ := rows(r.Context(), s.pool, `
		select b.slug, b.name, b.rating, b.currency, l.lat, l.lng, `+fromPrice+` as from_cents
		from businesses b left join locations l on l.business_id=b.id and l.is_primary
		where `+matches+` and l.lat is not null and l.lng is not null
		order by b.rating desc, b.review_count desc limit 800`, filters...)
	if total == 0 && offset > 0 { // a page past the end
		_ = s.pool.QueryRow(r.Context(), `select count(*) from businesses b left join locations l on l.business_id=b.id and l.is_primary where `+matches, filters...).Scan(&total)
	}
	writeJSON(w, 200, M{"businesses": out, "total": total, "pins": pins, "limit": limit, "offset": offset})
}

// GET /v1/businesses/{slug}
func (s *Server) getBusiness(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	slug := chi.URLParam(r, "slug")
	biz, err := row(ctx, s.pool, `select id, slug, name, tagline, about, category, market, currency, timezone, phone, instagram, tiktok, website,
		status, verification_status, plan, rating, review_count, tone, highlights, owner_name, review_summary,
		(select sm.id from site_media sm where sm.slot='logo' and sm.ref = businesses.slug and sm.active limit 1) as logo_id from businesses where slug=$1 and status<>'suspended'`, slug)
	if err != nil {
		if isNoRows(err) {
			writeErr(w, 404, "business not found")
			return
		}
		writeErr(w, 500, err.Error())
		return
	}
	id := biz["id"]
	locs, _ := rows(ctx, s.pool, `select id, name, address, city, region, country, lat, lng, timezone, is_primary, hours, arrival_notes from locations where business_id=$1 order by is_primary desc`, id)
	staff, _ := rows(ctx, s.pool, `select id, name, initials, role, level, tone, bookable, rating, (select coalesce(array_agg(ss.service_id::text), '{}') from staff_services ss where ss.staff_id = staff.id) as service_ids from staff where business_id=$1 and bookable and not archived order by role='owner' desc, name`, id)
	services, _ := rows(ctx, s.pool, `select id, name, category, description, duration_min, processing_min, buffer_min, price_cents, deposit_cents from services where business_id=$1 and online and not archived order by sort, name`, id)
	reviews, _ := rows(ctx, s.pool, `select id, author_name, service_name, rating, body, reply, pinned, created_at from reviews where business_id=$1 and status='published' order by pinned desc, created_at desc limit 20`, id)
	products, _ := rows(ctx, s.pool, `select id, slug, name, price_cents, tone, rating, review_count from products where business_id=$1 order by sold desc limit 8`, id)
	if leadSources[r.URL.Query().Get("src")] {
		s.countLeadEvents("view", fmt.Sprint(id))
	}
	rules := s.bizSettings(ctx, fmt.Sprint(id))
	display := rules["storefront"]
	// What a client should know before booking: the rules, not the internal settings.
	policy := M{"instant": settingBool(rules["booking"]["instant"], true), "waitlist": settingBool(rules["booking"]["waitlist"], true),
		"anyone": settingBool(rules["booking"]["anyone"], true), "multi_service": settingBool(rules["booking"]["multi_service"], true),
		"cancel_hours": settingInt(rules["policy"]["cancel_hours"], 24), "late_cancel_fee": rules["policy"]["late_cancel_fee"], "max_days": settingInt(rules["booking"]["max_days"], 60),
		"new_client_deposit_pct": settingInt(rules["policy"]["new_client_deposit_pct"], 0), "prepay_after_no_show": settingBool(rules["policy"]["prepay_after_no_show"], false),
		"payments_live": s.payMode(fmt.Sprint(biz["market"])) == "live"}
	if !settingBool(display["show_phone"], false) {
		delete(biz, "phone") // shared after booking, not before
	}
	if !settingBool(display["show_reviews"], true) {
		reviews = []M{}
		delete(biz, "review_summary")
	}
	saved := false
	if uid := s.customerID(r); uid != nil {
		_ = s.pool.QueryRow(ctx, `select exists(select 1 from user_favourites where user_id=$1 and business_id=$2)`, *uid, id).Scan(&saved)
	}
	var photoCount int
	_ = s.pool.QueryRow(ctx, `select count(*) from site_media where slot='business' and ref=$1 and active`, slug).Scan(&photoCount)
	writeJSON(w, 200, M{"business": biz, "locations": locs, "staff": staff, "services": services, "reviews": reviews, "products": products, "display": display, "policy": policy, "saved": saved, "photo_count": photoCount, "extras": s.bizExtras(ctx, fmt.Sprint(id)), "intake": s.intakeFor(ctx, fmt.Sprint(id), nil)})
}

// GET /v1/businesses/{slug}/availability?date=2026-10-10&services=id,id&staff=id|any
func (s *Server) availability(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	q := r.URL.Query()
	var bizID, tz string
	if err := s.pool.QueryRow(ctx, `select id::text, timezone from businesses where slug=$1 and status='live'`, chi.URLParam(r, "slug")).Scan(&bizID, &tz); err != nil {
		writeErr(w, 404, "business not found")
		return
	}
	loc, err := time.LoadLocation(tz)
	if err != nil {
		writeErr(w, 500, "bad timezone")
		return
	}
	day, err := time.ParseInLocation("2006-01-02", q.Get("date"), loc)
	if err != nil {
		writeErr(w, 400, "date must be YYYY-MM-DD")
		return
	}
	svcIDs := splitCSV(q.Get("services"))
	if len(svcIDs) == 0 {
		writeErr(w, 400, "services is required")
		return
	}
	// The business decides how late and how far ahead clients may book.
	rules := s.bizSettings(ctx, bizID)["booking"]
	lead, maxDays := settingInt(rules["lead_hours"], 2), settingInt(rules["max_days"], 60)
	now := time.Now()
	if day.After(now.AddDate(0, 0, maxDays)) {
		writeJSON(w, 200, M{"date": q.Get("date"), "timezone": tz, "duration_min": 0, "slots": []openSlot{}, "note": "This business takes bookings up to " + itoa(maxDays) + " days ahead."})
		return
	}
	slots, dur, err := s.openSlots(ctx, bizID, loc, day, svcIDs, q.Get("staff"), now.Add(time.Duration(lead)*time.Hour), "")
	if err != nil {
		writeErr(w, 400, "unknown services")
		return
	}
	writeJSON(w, 200, M{"date": q.Get("date"), "timezone": tz, "duration_min": dur, "slots": slots})
}

type createBookingReq struct {
	BusinessSlug string         `json:"business_slug"`
	StaffID      string         `json:"staff_id"`
	StartsAt     string         `json:"starts_at"`
	ServiceIDs   []string       `json:"service_ids"`
	ClientName   string         `json:"client_name"`
	ClientPhone  string         `json:"client_phone"`
	ClientEmail  string         `json:"client_email"` // for the payment receipt when a deposit is paid online
	Notes        string         `json:"notes"`
	Source       string         `json:"source"`
	PromoCode    string         `json:"promo_code"`
	GuestName    string         `json:"guest_name"` // who is coming, when it is not the person booking
	Answers      []intakeAnswer `json:"answers"`
}

// POST /v1/bookings
func (s *Server) createBooking(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var req createBookingReq
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, "invalid json: "+err.Error())
		return
	}
	if req.BusinessSlug == "" || req.StaffID == "" || len(req.ServiceIDs) == 0 || req.ClientName == "" {
		writeErr(w, 400, "business_slug, staff_id, service_ids and client_name are required")
		return
	}
	start, err := time.Parse(time.RFC3339, req.StartsAt)
	if err != nil {
		writeErr(w, 400, "starts_at must be RFC3339")
		return
	}
	if req.ClientPhone != "" {
		var blocked bool
		_ = s.pool.QueryRow(ctx, `select exists(select 1 from blocked_contacts where phone=$1)`, req.ClientPhone).Scan(&blocked)
		if blocked {
			writeErr(w, 403, "this number cannot book on LogaLuxe; contact support")
			return
		}
	}
	biz, err := row(ctx, s.pool, `select b.id, b.name, b.market, b.currency, b.timezone, l.id as location_id from businesses b join locations l on l.business_id=b.id and l.is_primary where b.slug=$1 and b.status='live'`, req.BusinessSlug)
	if err != nil {
		writeErr(w, 404, "business not found")
		return
	}
	payMode := s.payMode(fmt.Sprint(biz["market"])) // live when this market's payment provider is connected
	svcs, err := rows(ctx, s.pool, `select id, name, duration_min, processing_min, buffer_min, price_cents, deposit_cents from services where business_id=$1 and id = any($2::uuid[])`, biz["id"], req.ServiceIDs)
	if err != nil || len(svcs) == 0 {
		writeErr(w, 400, "unknown services")
		return
	}
	loc, err := time.LoadLocation(fmt.Sprint(biz["timezone"]))
	if err != nil {
		loc = time.UTC
	}
	prices := s.loadPricing(ctx, fmt.Sprint(biz["id"]))
	for _, sv := range svcs {
		p, _ := prices.price(fmt.Sprint(sv["id"]), req.StaffID, start.In(loc))
		sv["price_cents"] = int32(p)
	}
	var total, deposit, dur, buf int
	for _, sv := range svcs {
		total += int(sv["price_cents"].(int32))
		deposit += int(sv["deposit_cents"].(int32))
		dur += int(sv["duration_min"].(int32)) + int(sv["processing_min"].(int32))
		if b := int(sv["buffer_min"].(int32)); b > buf {
			buf = b
		}
	}
	end := start.Add(time.Duration(dur+buf) * time.Minute)
	// The person must really be free: not blocked off (by hand or by their own calendar) and not on approved time off.
	var away bool
	_ = s.pool.QueryRow(ctx, `select exists(select 1 from calendar_blocks cb where cb.staff_id::text=$1 and cb.starts_at < $3 and cb.ends_at > $2)
		or exists(select 1 from time_off t where t.staff_id::text=$1 and t.status='approved' and ($2 at time zone $4)::date between t.starts_on and t.ends_on)`, req.StaffID, start, end, fmt.Sprint(biz["timezone"])).Scan(&away)
	if away {
		writeErr(w, 409, "that time is no longer free; please choose another")
		return
	}
	if taken := clash(s.resourcesFor(ctx, fmt.Sprint(biz["id"]), req.ServiceIDs, start, end, ""), start, end); taken != "" {
		writeErr(w, 409, "that time has just been taken; please choose another")
		return
	}
	req.GuestName = strings.TrimSpace(req.GuestName)
	if len(req.GuestName) > 80 {
		writeErr(w, 400, "keep the name under 80 characters")
		return
	}
	answers, why := checkAnswers(s.intakeFor(ctx, fmt.Sprint(biz["id"]), req.ServiceIDs), req.Answers)
	if why != "" {
		writeErr(w, 400, why)
		return
	}
	list := total // the price for this time and person, before any promo code
	if req.Source == "" {
		req.Source = "web"
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	defer tx.Rollback(ctx)

	promoID, discount, why := promoDiscount(ctx, tx, req.PromoCode, "bookings", biz["currency"].(string), list, true, fmt.Sprint(biz["id"]))
	if why != "" {
		writeErr(w, 400, why)
		return
	}
	total = list - discount
	rules := s.bizSettings(ctx, fmt.Sprint(biz["id"]))
	if len(req.ServiceIDs) > 1 && !settingBool(rules["booking"]["multi_service"], true) {
		writeErr(w, 400, "this business takes one service per booking; book the second one separately")
		return
	}
	// Someone who missed a visit before can be asked to pay in full up front.
	if settingBool(rules["policy"]["prepay_after_no_show"], false) && req.ClientPhone != "" {
		var missed bool
		_ = tx.QueryRow(ctx, `select exists(select 1 from clients where business_id=$1 and phone=$2 and no_show_count > 0)`, biz["id"], req.ClientPhone).Scan(&missed)
		if missed {
			deposit = total
		}
	}
	// A first-time client can be asked for a deposit even on services that do not usually need one.
	if pct := settingInt(rules["policy"]["new_client_deposit_pct"], 0); pct > 0 && deposit == 0 {
		var known bool
		_ = tx.QueryRow(ctx, `select exists(select 1 from bookings where business_id=$1 and client_phone=$2 and $2 <> '' and status in ('completed','paid'))`, biz["id"], req.ClientPhone).Scan(&known)
		if !known {
			deposit = total * pct / 100
		}
	}
	if deposit > total {
		deposit = total
	}
	promoCode := ""
	if promoID != "" {
		promoCode = strings.ToUpper(strings.TrimSpace(req.PromoCode))
		if _, err := tx.Exec(ctx, `update promo_codes set used = used + 1 where id=$1`, promoID); err != nil {
			writeErr(w, 500, err.Error())
			return
		}
	}

	var clientID *string
	if req.ClientPhone != "" {
		var id string
		err = tx.QueryRow(ctx, `insert into clients (business_id, name, phone) values ($1,$2,$3)
			on conflict (business_id, phone) do update set name=excluded.name returning id`, biz["id"], req.ClientName, req.ClientPhone).Scan(&id)
		if err == nil {
			clientID = &id
		}
	}
	var bookingID string
	err = tx.QueryRow(ctx, `insert into bookings (business_id, location_id, staff_id, client_id, client_name, client_phone, status, starts_at, ends_at, source, total_cents, deposit_cents, deposit_paid, notes, promo_code, discount_cents)
		values ($1,$2,$3,$4,$5,$6,'confirmed',$7,$8,$9,$10,$11,$12,$13,$14,$15) returning id`,
		biz["id"], biz["location_id"], req.StaffID, clientID, req.ClientName, req.ClientPhone, start, end, req.Source, total, deposit, deposit > 0 && payMode == "simulation", req.Notes, promoCode, discount).Scan(&bookingID)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23P01" { // exclusion_violation
			writeErr(w, 409, "that time was just taken, pick another slot")
			return
		}
		writeErr(w, 500, err.Error())
		return
	}
	series, _ := ctx.Value(seriesKey{}).(string)
	if _, err := tx.Exec(ctx, `update bookings set guest_name=$2, series_id=nullif($3,'')::uuid where id=$1`, bookingID, req.GuestName, series); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	for _, a := range answers {
		if _, err := tx.Exec(ctx, `insert into booking_answers (booking_id, question_id, label, kind, answer, sort) values ($1,$2,$3,$4,$5,$6)`, bookingID, a["question_id"], a["label"], a["kind"], a["answer"], a["sort"]); err != nil {
			writeErr(w, 500, err.Error())
			return
		}
	}
	openLead(ctx, tx, fmt.Sprint(biz["id"]), bookingID, clientID, req.ClientName, req.Source, total)
	if uid := s.customerID(r); uid != nil {
		if _, err := tx.Exec(ctx, `update bookings set user_id=$2 where id=$1`, bookingID, *uid); err != nil {
			writeErr(w, 500, err.Error())
			return
		}
	}
	for _, sv := range svcs {
		if _, err := tx.Exec(ctx, `insert into booking_items (booking_id, service_id, name, price_cents, duration_min) values ($1,$2,$3,$4,$5)`,
			bookingID, sv["id"], sv["name"], sv["price_cents"], sv["duration_min"]); err != nil {
			writeErr(w, 500, err.Error())
			return
		}
	}
	if !settingBool(rules["booking"]["instant"], true) {
		// The business approves each booking itself; until then the slot is only requested.
		_, _ = tx.Exec(ctx, `update bookings set status='requested' where id=$1`, bookingID)
	}
	if deposit > 0 && payMode == "simulation" {
		_, _ = tx.Exec(ctx, `insert into ledger (business_id, kind, amount_cents, currency, method, status, booking_id, description)
			select b.id, 'deposit', $2, b.currency, 'card', 'held', $1, 'Deposit · ' || $3 from businesses b where b.id=$4`, bookingID, deposit, req.ClientName, biz["id"])
	}
	if deposit > 0 {
		_, _ = tx.Exec(ctx, `insert into payment_events (provider, kind, booking_id, amount_cents, currency, status, reference)
			select $1, 'deposit', $2, $3, b.currency, $4, $5 from businesses b where b.id=$6`,
			payMode, bookingID, deposit, map[bool]string{true: "simulated", false: "pending"}[payMode == "simulation"], "sim_"+bookingID[:8], biz["id"])
	}
	if err := tx.Commit(ctx); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if deposit == 0 || payMode != "live" {
		s.bookingPlaced(bookingID, req.ClientEmail)
	}
	if deposit > 0 && payMode == "live" {
		// The deposit is paid on the provider's page. The time is held while the client pays, and released if they do not.
		email := strings.TrimSpace(req.ClientEmail)
		if uid := s.customerID(r); uid != nil && email == "" {
			_ = s.pool.QueryRow(ctx, `select email from users where id=$1`, *uid).Scan(&email)
		}
		if _, _, err := s.startPayment(ctx, payStart{Provider: providerFor(fmt.Sprint(biz["market"])), Purpose: "deposit", BusinessID: fmt.Sprint(biz["id"]), BookingID: bookingID,
			Amount: deposit, Currency: fmt.Sprint(biz["currency"]), Email: email, Description: "Deposit · " + fmt.Sprint(biz["name"])}); err != nil {
			slog.Error("deposit payment could not be started", "booking", bookingID, "err", err)
			_, _ = s.pool.Exec(ctx, `update bookings set status='cancelled_client', cancel_reason='The payment page could not be opened' where id=$1`, bookingID)
			writeErr(w, 502, "the payment page could not be opened, so nothing was booked; please try again")
			return
		}
	}
	s.notifyBusiness(fmt.Sprint(biz["id"]), "new_booking_email", "New booking: "+req.ClientName, req.ClientName+" booked for "+start.In(loc).Format("Monday 2 January at 15:04")+".\n\nOpen your calendar: "+strings.TrimRight(s.cfg.WebURL, "/")+"/business/calendar?date="+start.In(loc).Format("2006-01-02")+"&booking="+bookingID)
	s.getBookingByID(w, r, bookingID, 201)
}

func (s *Server) getBooking(w http.ResponseWriter, r *http.Request) {
	s.getBookingByID(w, r, chi.URLParam(r, "id"), 200)
}

func (s *Server) getBookingByID(w http.ResponseWriter, r *http.Request, id string, status int) {
	ctx := r.Context()
	bk, err := row(ctx, s.pool, `select bk.id, bk.status, bk.starts_at, bk.ends_at, bk.client_name, bk.total_cents, bk.discount_cents, bk.promo_code, bk.deposit_cents, bk.deposit_paid, bk.notes, bk.source, bk.guest_name, bk.series_id,
		b.name as business, b.slug as business_slug, b.currency, b.timezone, st.name as staff, l.address, l.city
		from bookings bk join businesses b on b.id=bk.business_id join staff st on st.id=bk.staff_id left join locations l on l.id=bk.location_id where bk.id=$1`, id)
	if err != nil {
		writeErr(w, 404, "booking not found")
		return
	}
	items, _ := rows(ctx, s.pool, `select name, price_cents, duration_min from booking_items where booking_id=$1`, id)
	bk["items"] = items
	// A deposit still to be paid online: where to pay it, and until when the time is held.
	if pay, err := row(ctx, s.pool, `select reference, url, amount_cents, currency, expires_at from payments where booking_id=$1 and purpose='deposit' and status='pending' order by created_at desc limit 1`, id); err == nil {
		bk["payment"] = pay
	}
	writeJSON(w, status, M{"booking": bk})
}

type waitlistReq struct {
	BusinessSlug string   `json:"business_slug"`
	ClientName   string   `json:"client_name"`
	ClientPhone  string   `json:"client_phone"`
	Dates        []string `json:"dates"`
	TimeOfDay    string   `json:"time_of_day"`
}

// POST /v1/waitlist — recorded as a client tag until the waitlist table lands in phase 1.
func (s *Server) joinWaitlist(w http.ResponseWriter, r *http.Request) {
	var req waitlistReq
	if err := readJSON(r, &req); err != nil || req.BusinessSlug == "" || req.ClientPhone == "" {
		writeErr(w, 400, "business_slug and client_phone are required")
		return
	}
	_, err := s.pool.Exec(r.Context(), `insert into clients (business_id, name, phone, tags, notes)
		select id, $2, $3, '{waitlist}', $4 from businesses where slug=$1
		on conflict (business_id, phone) do update set tags = array_append(array_remove(clients.tags,'waitlist'),'waitlist'), notes = excluded.notes`,
		req.BusinessSlug, req.ClientName, req.ClientPhone, "Waitlist: "+strings.Join(req.Dates, ", ")+" "+req.TimeOfDay)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	days := req.Dates
	if len(days) == 0 {
		days = []string{""}
	}
	for _, d := range days {
		_, _ = s.pool.Exec(r.Context(), `insert into waitlist_entries (business_id, client_name, client_phone, day, time_of_day)
			select id, $2, $3, nullif($4,'')::date, $5 from businesses where slug=$1`, req.BusinessSlug, req.ClientName, req.ClientPhone, d, req.TimeOfDay)
	}
	var position int
	_ = s.pool.QueryRow(r.Context(), `select count(*) from waitlist_entries wl join businesses b on b.id = wl.business_id where b.slug=$1 and wl.status='waiting'`, req.BusinessSlug).Scan(&position)
	writeJSON(w, 201, M{"ok": true, "position": position})
}

// GET /v1/products?category=&q=&seller=
type orderReq struct {
	CustomerName  string `json:"customer_name"`
	CustomerPhone string `json:"customer_phone"`
	CustomerEmail string `json:"customer_email"`
	Fulfilment    string `json:"fulfilment"` // pickup or ship, for every seller not named below
	// How each seller's items reach the customer, by seller name. A studio's items can be collected at a visit while a brand's are shipped.
	FulfilmentBySeller map[string]string `json:"fulfilment_by_seller"`
	Address            M                 `json:"address"`
	PromoCode          string            `json:"promo_code"`
	GiftCode           string            `json:"gift_code"`
	Items              []struct {
		ProductSlug string `json:"product_slug"`
		SizeLabel   string `json:"size_label"`
		Qty         int    `json:"qty"`
	} `json:"items"`
}

// POST /v1/orders
func (s *Server) createOrder(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var req orderReq
	if err := readJSON(r, &req); err != nil || len(req.Items) == 0 || req.CustomerName == "" {
		writeErr(w, 400, "customer_name and items are required")
		return
	}
	if req.Fulfilment != "pickup" && req.Fulfilment != "ship" {
		writeErr(w, 400, "fulfilment must be pickup or ship")
		return
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	defer tx.Rollback(ctx)

	type line struct {
		productID, seller, name, size string
		qty, unit                     int
	}
	var lines []line
	subtotal, shipping := 0, 0
	sellersShipped := map[string]bool{}
	fulfilOf := func(seller string) string {
		if f := req.FulfilmentBySeller[seller]; f == "pickup" || f == "ship" {
			return f
		}
		return req.Fulfilment
	}
	type sellerTotal struct {
		items, shipping int
		businessID      *string
	}
	bySeller := map[string]*sellerTotal{}
	currency := "" // every item must be priced in the same one
	for _, it := range req.Items {
		if it.Qty <= 0 {
			it.Qty = 1
		}
		var pid, seller, name string
		var price, shipCents, stock int32
		var sizes []byte
		var ships, collects bool
		var bizID *string
		err := tx.QueryRow(ctx, `select id, seller_name, name, price_cents, shipping_cents, stock, sizes, shipping, pickup and business_id is not null, business_id::text from products where slug=$1 and active for update`, it.ProductSlug).Scan(&pid, &seller, &name, &price, &shipCents, &stock, &sizes, &ships, &collects, &bizID)
		if err != nil {
			writeErr(w, 400, "unknown product "+it.ProductSlug)
			return
		}
		itemCurrency := "USD"
		if bizID != nil {
			_ = tx.QueryRow(ctx, `select currency from businesses where id=$1`, *bizID).Scan(&itemCurrency)
		}
		if currency == "" {
			currency = itemCurrency
		} else if currency != itemCurrency {
			writeErr(w, 400, "this order mixes items priced in dollars and in naira; place one order for each")
			return
		}
		unit := int(price)
		if it.SizeLabel != "" {
			var opts []struct {
				Label string `json:"label"`
				Price int    `json:"price_cents"`
			}
			_ = json.Unmarshal(sizes, &opts)
			for _, o := range opts {
				if o.Label == it.SizeLabel {
					unit = o.Price
				}
			}
		}
		if int(stock) < it.Qty {
			writeErr(w, 409, name+" is out of stock")
			return
		}
		switch how := fulfilOf(seller); {
		case how == "ship" && !ships:
			writeErr(w, 400, name+" cannot be shipped; choose to collect it")
			return
		case how == "pickup" && !collects:
			writeErr(w, 400, name+" cannot be collected; choose shipping for "+seller)
			return
		case how == "ship" && (req.Address == nil || strings.TrimSpace(fmt.Sprint(req.Address["line1"])) == ""):
			writeErr(w, 400, "enter the address to ship to")
			return
		}
		if bySeller[seller] == nil {
			bySeller[seller] = &sellerTotal{businessID: bizID}
		}
		if _, err := tx.Exec(ctx, `update products set stock = stock - $2, sold = sold + $2 where id=$1`, pid, it.Qty); err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		subtotal += unit * it.Qty
		bySeller[seller].items += unit * it.Qty
		if fulfilOf(seller) == "ship" && ships && !sellersShipped[seller] {
			sellersShipped[seller] = true
			shipping += int(shipCents)
			bySeller[seller].shipping = int(shipCents)
		}
		lines = append(lines, line{pid, seller, name, it.SizeLabel, it.Qty, unit})
	}
	// Discounts are worked out here. Nothing the browser says about an amount is trusted.
	market := map[string]string{"NGN": "NG"}[currency]
	if market == "" {
		market = "US"
	}
	promoID, discount, why := promoDiscount(ctx, tx, req.PromoCode, "orders", currency, subtotal, true, "")
	if why != "" {
		writeErr(w, 400, why)
		return
	}
	sellerItems, sellerBiz := map[string]int{}, map[string]*string{}
	for seller, t := range bySeller {
		sellerItems[seller], sellerBiz[seller] = t.items, t.businessID
	}
	tax := orderTax(ctx, tx, sellerItems, sellerBiz, subtotal, discount, stateOf(req.Address))
	total := subtotal - discount + shipping + tax
	if currency != "USD" && strings.TrimSpace(req.GiftCode) != "" {
		writeErr(w, 400, "gift cards are in US dollars and cannot pay for an order in naira")
		return
	}
	giftID, balance, why := giftBalance(ctx, tx, req.GiftCode, "USD", true)
	if why != "" {
		writeErr(w, 400, why)
		return
	}
	gift := balance
	if gift > total {
		gift = total
	}
	total -= gift
	// Referral credit is spent before any card is asked for.
	credit := 0
	buyer := s.customerID(r)
	if buyer != nil && total > 0 && currency == "USD" { // store credit is in dollars
		_, _ = tx.Exec(ctx, `select 1 from users where id=$1 for update`, *buyer) // one order at a time may spend it
		if credit = creditBalance(ctx, tx, *buyer); credit > total {
			credit = total
		}
		total -= credit
	}
	// The cart asks what an order would come to before placing it. Nothing is kept: the transaction is rolled back.
	if strings.HasSuffix(r.URL.Path, "/quote") {
		writeJSON(w, 200, M{"quote": M{"subtotal_cents": subtotal, "discount_cents": discount, "shipping_cents": shipping, "tax_cents": tax, "gift_cents": gift, "credit_cents": credit, "total_cents": total, "currency": currency}})
		return
	}
	promoCode, giftMask := "", ""
	if promoID != "" {
		promoCode = strings.ToUpper(strings.TrimSpace(req.PromoCode))
	}
	if giftID != "" && gift > 0 {
		c := strings.ToUpper(strings.TrimSpace(req.GiftCode))
		giftMask = "ending " + c[len(c)-4:] // the full code is as good as money, so it is not stored on the order
	}
	var orderID string
	if err := tx.QueryRow(ctx, `insert into orders (customer_name, customer_phone, status, fulfilment, subtotal_cents, shipping_cents, tax_cents, total_cents, address, promo_code, discount_cents, gift_code, gift_cents)
		values ($1,$2,$13,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12) returning id`, req.CustomerName, req.CustomerPhone, req.Fulfilment, subtotal, shipping, tax, total, req.Address, promoCode, discount, giftMask, gift, map[bool]string{true: "pending", false: "paid"}[total > 0 && s.payMode(market) == "live"]).Scan(&orderID); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if _, err := tx.Exec(ctx, `update orders set currency=$2 where id=$1`, orderID, currency); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if uid := s.customerID(r); uid != nil {
		if _, err := tx.Exec(ctx, `update orders set user_id=$2 where id=$1`, orderID, *uid); err != nil {
			writeErr(w, 500, err.Error())
			return
		}
	}
	if buyer != nil && credit > 0 {
		if _, err := tx.Exec(ctx, `update orders set credit_cents=$2 where id=$1`, orderID, credit); err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		if _, err := tx.Exec(ctx, `insert into user_credits (user_id, amount_cents, reason, order_id) values ($1,$2,'Spent on an order',$3)`, *buyer, -credit, orderID); err != nil {
			writeErr(w, 500, err.Error())
			return
		}
	}
	if promoID != "" {
		if _, err := tx.Exec(ctx, `update promo_codes set used = used + 1 where id=$1`, promoID); err != nil {
			writeErr(w, 500, err.Error())
			return
		}
	}
	if giftID != "" && gift > 0 {
		if _, err := tx.Exec(ctx, `update gift_cards set balance_cents = balance_cents - $2 where id=$1`, giftID, gift); err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		if _, err := tx.Exec(ctx, `insert into gift_card_txns (gift_card_id, order_id, amount_cents, note, actor) values ($1,$2,$3,'Spent on an order',$4)`, giftID, orderID, -gift, req.CustomerName); err != nil {
			writeErr(w, 500, err.Error())
			return
		}
	}
	for _, l := range lines {
		if _, err := tx.Exec(ctx, `insert into order_items (order_id, product_id, seller_name, name, size_label, qty, unit_cents) values ($1,$2,$3,$4,$5,$6,$7)`, orderID, l.productID, l.seller, l.name, l.size, l.qty, l.unit); err != nil {
			writeErr(w, 500, err.Error())
			return
		}
	}
	for seller, t := range bySeller {
		note := ""
		if buyer != nil && t.businessID != nil && fulfilOf(seller) == "pickup" {
			note = s.pickupNote(ctx, tx, *buyer, *t.businessID)
		}
		if _, err := tx.Exec(ctx, `insert into order_shipments (order_id, seller_name, business_id, fulfilment, items_cents, shipping_cents, note) values ($1,$2,$3,$4,$5,$6,$7)`, orderID, seller, t.businessID, fulfilOf(seller), t.items, t.shipping, note); err != nil {
			writeErr(w, 500, err.Error())
			return
		}
	}
	_, _ = tx.Exec(ctx, `update orders set customer_email=$2 where id=$1`, orderID, strings.TrimSpace(req.CustomerEmail))
	_, _ = tx.Exec(ctx, `insert into payment_events (provider, kind, order_id, amount_cents, currency, status, reference) values ($1,'order',$2,$3,$6,$4,$5)`,
		s.payMode(market), orderID, total, map[bool]string{true: "simulated", false: "pending"}[s.payMode(market) == "simulation"], "sim_"+orderID[:8], currency)
	if err := tx.Commit(ctx); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if total > 0 && s.payMode(market) == "live" {
		email := strings.TrimSpace(req.CustomerEmail)
		if uid := s.customerID(r); uid != nil && email == "" {
			_ = s.pool.QueryRow(ctx, `select email from users where id=$1`, *uid).Scan(&email)
		}
		if _, _, err := s.startPayment(ctx, payStart{Provider: providerFor(market), Purpose: "order", OrderID: orderID, Amount: total, Currency: currency, Email: email, Description: "LogaLuxe shop order"}); err != nil {
			_, _ = s.pool.Exec(ctx, `update orders set status='cancelled' where id=$1`, orderID)
			s.unwindOrder(ctx, orderID)
			writeErr(w, 502, "the payment page could not be opened, so the order was not placed; please try again")
			return
		}
	} else {
		s.settleOrder(ctx, orderID) // paid (or simulated): each business is credited for what it sold
	}
	s.getOrderByID(w, r, orderID, 201)
}

func (s *Server) getOrder(w http.ResponseWriter, r *http.Request) {
	s.getOrderByID(w, r, chi.URLParam(r, "id"), 200)
}

func (s *Server) getOrderByID(w http.ResponseWriter, r *http.Request, id string, status int) {
	ctx := r.Context()
	o, err := row(ctx, s.pool, `select * from orders where id=$1`, id)
	if err != nil {
		writeErr(w, 404, "order not found")
		return
	}
	delete(o, "user_id")
	items, _ := rows(ctx, s.pool, `select oi.seller_name, oi.name, oi.size_label, oi.qty, oi.unit_cents, coalesce(p.slug, '') as product_slug from order_items oi left join products p on p.id = oi.product_id where oi.order_id=$1`, id)
	o["items"] = items
	shipments, _ := rows(ctx, s.pool, `select sh.id, sh.seller_name, sh.fulfilment, sh.status, sh.items_cents, sh.shipping_cents, sh.tracking, sh.updated_at,
		(select json_build_object('status', rt.status, 'reason', rt.reason, 'reply', rt.reply, 'refund_cents', rt.refund_cents, 'credit_cents', rt.credit_cents, 'created_at', rt.created_at, 'decided_at', rt.decided_at) from order_returns rt where rt.shipment_id = sh.id) as return
		from order_shipments sh where sh.order_id=$1 order by sh.seller_name`, id)
	for _, sh := range shipments {
		ok, until, why := s.returnWindow(ctx, fmt.Sprint(sh["id"]))
		sh["can_return"] = ok && sh["return"] == nil
		if ok {
			sh["return_until"] = until
		} else {
			sh["return_why"] = why
		}
		delete(sh, "id")
	}
	o["shipments"] = shipments
	o["return_reasons"] = returnReasons
	if pay, err := row(ctx, s.pool, `select reference, url, amount_cents, currency, expires_at from payments where order_id=$1 and status='pending' order by created_at desc limit 1`, id); err == nil {
		o["payment"] = pay
	}
	writeJSON(w, status, M{"order": o})
}

func splitCSV(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
