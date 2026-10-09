package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
)

// Booking for someone else, questions asked at booking, repeat appointments,
// calendar sync, and a person's own day.

// ---------- questions a business asks at booking ----------

var intakeKinds = map[string]bool{"text": true, "yesno": true, "choice": true, "consent": true}

type intakeQuestion struct {
	ServiceID string   `json:"service_id"` // empty: asked for every service
	Label     string   `json:"label"`
	Kind      string   `json:"kind"`
	Options   []string `json:"options"`
	Required  bool     `json:"required"`
	Sort      int      `json:"sort"`
	Active    *bool    `json:"active"`
}

func (q *intakeQuestion) problem() string {
	q.Label = strings.TrimSpace(q.Label)
	opts := []string{}
	for _, o := range q.Options {
		if o = strings.TrimSpace(o); o != "" && len(o) <= 80 {
			opts = append(opts, o)
		}
	}
	q.Options = opts
	switch {
	case len(q.Label) < 3 || len(q.Label) > 300:
		return "write the question, in up to 300 characters"
	case !intakeKinds[q.Kind]:
		return "the kind of answer is a short text, yes or no, a choice from a list, or a box the client must tick"
	case q.Kind == "choice" && (len(q.Options) < 2 || len(q.Options) > 12):
		return "a choice needs between 2 and 12 options"
	}
	if q.Kind != "choice" {
		q.Options = []string{}
	}
	if q.Kind == "consent" {
		q.Required = true // a box the client must tick is always required
	}
	return ""
}

// GET /v1/m/intake
func (s *Server) mIntake(w http.ResponseWriter, r *http.Request) {
	out, err := rows(r.Context(), s.pool, `select q.id, q.service_id, sv.name as service, q.label, q.kind, q.options, q.required, q.sort, q.active,
		(select count(*) from booking_answers ba where ba.question_id = q.id) as answers
		from intake_questions q left join services sv on sv.id = q.service_id where q.business_id=$1 order by q.active desc, q.sort, q.created_at`, mc(r).BusinessID)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, M{"questions": out})
}

// POST /v1/m/intake   PUT /v1/m/intake/{id}
func (s *Server) mIntakeSave(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	m := mc(r)
	var q intakeQuestion
	if err := readJSON(r, &q); err != nil {
		writeErr(w, 400, "invalid json")
		return
	}
	if why := q.problem(); why != "" {
		writeErr(w, 400, why)
		return
	}
	var service *string
	if q.ServiceID != "" {
		var ok bool
		_ = s.pool.QueryRow(ctx, `select exists(select 1 from services where id::text=$1 and business_id=$2)`, q.ServiceID, m.BusinessID).Scan(&ok)
		if !ok {
			writeErr(w, 400, "that service was not found")
			return
		}
		service = &q.ServiceID
	}
	active := q.Active == nil || *q.Active
	if id := chi.URLParam(r, "id"); id != "" {
		tag, err := s.pool.Exec(ctx, `update intake_questions set service_id=$3, label=$4, kind=$5, options=$6, required=$7, sort=$8, active=$9 where id::text=$1 and business_id=$2`,
			id, m.BusinessID, service, q.Label, q.Kind, q.Options, q.Required, q.Sort, active)
		if err != nil || tag.RowsAffected() == 0 {
			writeErr(w, 404, "question not found")
			return
		}
		writeJSON(w, 200, M{"ok": true})
		return
	}
	var n int
	_ = s.pool.QueryRow(ctx, `select count(*) from intake_questions where business_id=$1 and active`, m.BusinessID).Scan(&n)
	if n >= 20 {
		writeErr(w, 409, "you have 20 questions, which is the most a booking can ask; switch one off first")
		return
	}
	var id string
	if err := s.pool.QueryRow(ctx, `insert into intake_questions (business_id, service_id, label, kind, options, required, sort, active) values ($1,$2,$3,$4,$5,$6,$7,$8) returning id::text`,
		m.BusinessID, service, q.Label, q.Kind, q.Options, q.Required, q.Sort, active).Scan(&id); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 201, M{"ok": true, "id": id})
}

// DELETE /v1/m/intake/{id}   a question that has been answered is switched off instead, so past answers keep their wording
func (s *Server) mIntakeDelete(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id, biz := chi.URLParam(r, "id"), mc(r).BusinessID
	var used bool
	_ = s.pool.QueryRow(ctx, `select exists(select 1 from booking_answers where question_id::text=$1)`, id).Scan(&used)
	if used {
		_, _ = s.pool.Exec(ctx, `update intake_questions set active=false where id::text=$1 and business_id=$2`, id, biz)
		writeJSON(w, 200, M{"ok": true, "switched_off": true})
		return
	}
	tag, err := s.pool.Exec(ctx, `delete from intake_questions where id::text=$1 and business_id=$2`, id, biz)
	if err != nil || tag.RowsAffected() == 0 {
		writeErr(w, 404, "question not found")
		return
	}
	writeJSON(w, 200, M{"ok": true})
}

// intakeFor lists the questions a booking of these services must answer. With no services, every active question.
func (s *Server) intakeFor(ctx context.Context, businessID string, serviceIDs []string) []M {
	out, _ := rows(ctx, s.pool, `select q.id, q.service_id, q.label, q.kind, q.options, q.required, q.sort from intake_questions q
		where q.business_id=$1 and q.active and (q.service_id is null or $2::text[] is null or q.service_id::text = any($2::text[])) order by q.sort, q.created_at`, businessID, serviceIDs)
	if out == nil {
		out = []M{}
	}
	return out
}

type intakeAnswer struct {
	QuestionID string `json:"question_id"`
	Answer     string `json:"answer"`
}

// checkAnswers matches what the client sent against the questions, and says what is missing in plain words.
func checkAnswers(questions []M, answers []intakeAnswer) (clean []M, why string) {
	given := map[string]string{}
	for _, a := range answers {
		given[a.QuestionID] = strings.TrimSpace(a.Answer)
	}
	for i, q := range questions {
		id, kind, label := fmt.Sprint(q["id"]), fmt.Sprint(q["kind"]), fmt.Sprint(q["label"])
		ans := given[id]
		required, _ := q["required"].(bool)
		switch kind {
		case "consent":
			if ans != "yes" {
				return nil, "please tick: " + label
			}
		case "yesno":
			if ans != "" && ans != "yes" && ans != "no" {
				return nil, "answer yes or no: " + label
			}
		case "choice":
			ok := ans == ""
			if opts, _ := q["options"].([]any); !ok {
				for _, o := range opts {
					if fmt.Sprint(o) == ans {
						ok = true
					}
				}
			}
			if !ok {
				return nil, "choose one of the options: " + label
			}
		default:
			if len(ans) > 1000 {
				return nil, "keep this answer under 1,000 characters: " + label
			}
		}
		if required && ans == "" {
			return nil, "please answer: " + label
		}
		if ans != "" {
			clean = append(clean, M{"question_id": id, "label": label, "kind": kind, "answer": ans, "sort": i})
		}
	}
	return clean, ""
}

// ---------- repeat appointments ----------

type seriesKey struct{} // the booking being made is part of a repeat: the value is the series id

// POST /v1/auth/bookings/{id}/repeat   {every_weeks, times}
// Books the same visit again at the same time with the same person, every so many weeks.
// Each date is booked through the normal booking rules, so a date that is taken or closed is
// reported and skipped, and a deposit is asked for each visit exactly as it would be on its own.
func (s *Server) authBookingRepeat(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	c := currentCustomer(r)
	var req struct {
		EveryWeeks int `json:"every_weeks"`
		Times      int `json:"times"`
	}
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, "invalid json")
		return
	}
	if req.EveryWeeks < 1 || req.EveryWeeks > 12 || req.Times < 1 || req.Times > 12 {
		writeErr(w, 400, "repeat every 1 to 12 weeks, up to 12 more times")
		return
	}
	b, err := row(ctx, s.pool, `select bk.id::text as id, bk.status, bk.starts_at, bk.staff_id::text as staff_id, bk.client_name, bk.client_phone, coalesce(bk.client_email,'') as client_email, bk.notes, bk.guest_name,
		bk.series_id::text as series_id, z.slug, z.timezone,
		(select coalesce(array_agg(bi.service_id::text) filter (where bi.service_id is not null), '{}') from booking_items bi where bi.booking_id = bk.id) as service_ids
		from bookings bk join businesses z on z.id = bk.business_id where bk.id::text=$1 and bk.user_id=$2`, chi.URLParam(r, "id"), c.ID)
	if err != nil {
		writeErr(w, 404, "booking not found")
		return
	}
	if st := fmt.Sprint(b["status"]); st != "confirmed" && st != "requested" && st != "completed" && st != "paid" {
		writeErr(w, 409, "this booking was cancelled, so it cannot be repeated; book a new time instead")
		return
	}
	services, _ := b["service_ids"].([]any)
	if len(services) == 0 {
		writeErr(w, 409, "the services of this booking are no longer offered; book a new time instead")
		return
	}
	series := fmt.Sprint(b["series_id"])
	if b["series_id"] == nil {
		_ = s.pool.QueryRow(ctx, `update bookings set series_id = gen_random_uuid() where id=$1 returning series_id::text`, b["id"]).Scan(&series)
	}
	answers, _ := rows(ctx, s.pool, `select question_id::text as question_id, answer from booking_answers where booking_id=$1 and question_id is not null`, b["id"])
	loc, err := time.LoadLocation(fmt.Sprint(b["timezone"]))
	if err != nil {
		loc = time.UTC
	}
	first := b["starts_at"].(time.Time).In(loc)
	made, skipped := []M{}, []M{}
	for k := 1; k <= req.Times; k++ {
		day := first.AddDate(0, 0, 7*req.EveryWeeks*k) // the same clock time, whatever the clocks do in between
		at := time.Date(day.Year(), day.Month(), day.Day(), first.Hour(), first.Minute(), 0, 0, loc)
		body, _ := json.Marshal(M{"business_slug": b["slug"], "staff_id": b["staff_id"], "starts_at": at.Format(time.RFC3339), "service_ids": services,
			"client_name": b["client_name"], "client_phone": b["client_phone"], "client_email": b["client_email"], "notes": b["notes"], "guest_name": b["guest_name"], "source": "repeat", "answers": answers})
		inner := r.Clone(context.WithValue(ctx, seriesKey{}, series))
		inner.Body = io.NopCloser(bytes.NewReader(body))
		rec := &capture{header: http.Header{}}
		s.createBooking(rec, inner)
		var out M
		_ = json.Unmarshal(rec.body.Bytes(), &out)
		if rec.code == 201 || rec.code == 200 || rec.code == 0 {
			bk, _ := out["booking"].(map[string]any)
			made = append(made, M{"id": bk["id"], "starts_at": at, "payment": bk["payment"], "deposit_cents": bk["deposit_cents"]})
		} else {
			skipped = append(skipped, M{"starts_at": at, "why": firstNonEmpty(fmt.Sprint(out["error"]), "that time could not be booked")})
		}
	}
	writeJSON(w, 201, M{"ok": true, "series_id": series, "made": made, "skipped": skipped})
}

// ---------- calendar sync ----------

// calStaff resolves whose calendar is meant: the signed-in person's own, or (for a manager) the one named.
func (s *Server) calStaff(w http.ResponseWriter, r *http.Request) (string, bool) {
	m := mc(r)
	id := r.URL.Query().Get("staff_id")
	if id == "" || id == m.StaffID {
		if m.StaffID == "" {
			writeErr(w, 409, "your sign-in is not linked to a person on the calendar; ask a manager to link it under Staff")
			return "", false
		}
		return m.StaffID, true
	}
	if merchantRank[m.Role] < merchantRank["manager"] {
		writeErr(w, 403, "you can only set up your own calendar")
		return "", false
	}
	var ok bool
	_ = s.pool.QueryRow(r.Context(), `select exists(select 1 from staff where id::text=$1 and business_id=$2)`, id, m.BusinessID).Scan(&ok)
	if !ok {
		writeErr(w, 404, "that person was not found")
		return "", false
	}
	return id, true
}

func (s *Server) calState(ctx context.Context, staffID string) M {
	out, err := row(ctx, s.pool, `select st.id, st.name, st.cal_token, st.cal_import_url, st.cal_import_at, st.cal_import_note,
		(select count(*) from calendar_blocks cb where cb.staff_id = st.id and cb.external and cb.ends_at > now()) as imported_blocks from staff st where st.id=$1`, staffID)
	if err != nil {
		return M{}
	}
	if tok, _ := out["cal_token"].(string); tok != "" {
		api := strings.TrimRight(firstNonEmpty(s.cfg.PublicAPIURL, "http://localhost:"+s.cfg.Port), "/")
		out["feed_url"] = api + "/v1/cal/" + tok + ".ics"
	}
	delete(out, "cal_token")
	if u, _ := out["cal_import_url"].(string); u != "" { // the address is a secret: show only enough to recognise it
		if p, err := url.Parse(u); err == nil {
			out["cal_import_host"] = p.Host
		}
		out["cal_import_set"] = true
	}
	delete(out, "cal_import_url")
	return out
}

// GET /v1/m/calendar-sync?staff_id=
func (s *Server) mCalSync(w http.ResponseWriter, r *http.Request) {
	id, ok := s.calStaff(w, r)
	if !ok {
		return
	}
	writeJSON(w, 200, s.calState(r.Context(), id))
}

// POST /v1/m/calendar-sync/feed?staff_id=   makes the private address (or a new one, which stops the old one working)
// DELETE turns it off.
func (s *Server) mCalFeed(w http.ResponseWriter, r *http.Request) {
	id, ok := s.calStaff(w, r)
	if !ok {
		return
	}
	if r.Method == http.MethodDelete {
		_, _ = s.pool.Exec(r.Context(), `update staff set cal_token=null where id=$1`, id)
		writeJSON(w, 200, s.calState(r.Context(), id))
		return
	}
	tok, err := newToken()
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if _, err := s.pool.Exec(r.Context(), `update staff set cal_token=$2 where id=$1`, id, tok); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, s.calState(r.Context(), id))
}

// GET /v1/cal/{token}.ics   a person's bookings, for Google Calendar, Apple Calendar or Outlook to subscribe to.
// The address is the key: anyone who has it can read it, which is how calendar subscriptions work.
func (s *Server) calFeed(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	tok := strings.TrimSuffix(chi.URLParam(r, "token"), ".ics")
	var staffID, staff, biz string
	if len(tok) < 32 || s.pool.QueryRow(ctx, `select st.id::text, st.name, b.name from staff st join businesses b on b.id = st.business_id where st.cal_token=$1`, tok).Scan(&staffID, &staff, &biz) != nil {
		writeErr(w, 404, "calendar not found")
		return
	}
	list, _ := rows(ctx, s.pool, `select bk.id, bk.starts_at, bk.ends_at, bk.status, bk.client_name, bk.guest_name, bk.client_phone, bk.notes, bk.created_at,
		coalesce((select string_agg(name, ' + ') from booking_items where booking_id = bk.id), 'Appointment') as services, coalesce(l.address,'') as address, coalesce(l.city,'') as city
		from bookings bk left join locations l on l.id = bk.location_id
		where bk.staff_id=$1 and bk.status in ('requested','confirmed','checked_in','in_progress','completed','paid') and bk.starts_at > now() - interval '30 days' and bk.starts_at < now() + interval '180 days'
		order by bk.starts_at`, staffID)
	stamp := func(v any) string { return v.(time.Time).UTC().Format("20060102T150405Z") }
	esc := func(v any) string {
		return strings.NewReplacer("\\", "\\\\", ";", "\\;", ",", "\\,", "\r", "", "\n", "\\n").Replace(fmt.Sprint(v))
	}
	lines := []string{"BEGIN:VCALENDAR", "VERSION:2.0", "PRODID:-//LogaLuxe//Bookings//EN", "CALSCALE:GREGORIAN", "METHOD:PUBLISH",
		"X-WR-CALNAME:" + esc(staff+" · "+biz), "X-PUBLISHED-TTL:PT30M", "REFRESH-INTERVAL;VALUE=DURATION:PT30M"}
	for _, b := range list {
		who := fmt.Sprint(b["client_name"])
		if g := fmt.Sprint(b["guest_name"]); g != "" {
			who = g + " (booked by " + who + ")"
		}
		desc := "Client: " + who
		if p := fmt.Sprint(b["client_phone"]); p != "" {
			desc += "\nPhone: " + p
		}
		if n := strings.TrimSpace(fmt.Sprint(b["notes"])); n != "" {
			desc += "\nNote: " + n
		}
		desc += "\nOpen it: " + strings.TrimRight(s.cfg.WebURL, "/") + "/business/calendar"
		lines = append(lines, "BEGIN:VEVENT", "UID:"+fmt.Sprint(b["id"])+"@logaluxe", "DTSTAMP:"+stamp(b["created_at"]), "DTSTART:"+stamp(b["starts_at"]), "DTEND:"+stamp(b["ends_at"]),
			"SUMMARY:"+esc(fmt.Sprint(b["services"])+" · "+who), "LOCATION:"+esc(strings.Trim(fmt.Sprint(b["address"])+", "+fmt.Sprint(b["city"]), ", ")), "DESCRIPTION:"+esc(desc),
			"STATUS:"+map[bool]string{true: "TENTATIVE", false: "CONFIRMED"}[b["status"] == "requested"], "END:VEVENT")
	}
	lines = append(lines, "END:VCALENDAR", "")
	w.Header().Set("Content-Type", "text/calendar; charset=utf-8")
	w.Header().Set("Cache-Control", "private, max-age=300")
	_, _ = w.Write([]byte(strings.Join(lines, "\r\n")))
}

// safeCalendarURL accepts only an https address on the public internet, so the import cannot be pointed at our own network.
func safeCalendarURL(raw string) (*url.URL, string) {
	raw = strings.TrimSpace(raw)
	if strings.HasPrefix(strings.ToLower(raw), "webcal://") {
		raw = "https://" + raw[len("webcal://"):]
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil {
		return nil, "paste the private address of your calendar; it starts with https:// and usually ends in .ics"
	}
	ips, err := net.LookupIP(u.Hostname())
	if err != nil || len(ips) == 0 {
		return nil, "that address could not be found; check it and paste it again"
	}
	for _, ip := range ips {
		if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsUnspecified() || ip.IsMulticast() {
			return nil, "that address is not on the public internet"
		}
	}
	return u, ""
}

// importCalendar reads one person's own calendar and replaces their imported busy times with what it says now.
func (s *Server) importCalendar(ctx context.Context, staffID string) (blocks int, note string) {
	var raw, tz, biz string
	if err := s.pool.QueryRow(ctx, `select coalesce(st.cal_import_url,''), b.timezone, b.id::text from staff st join businesses b on b.id = st.business_id where st.id=$1`, staffID).Scan(&raw, &tz, &biz); err != nil || raw == "" {
		return 0, ""
	}
	save := func(n int, msg string) (int, string) {
		_, _ = s.pool.Exec(ctx, `update staff set cal_import_at=now(), cal_import_note=$2 where id=$1`, staffID, msg)
		return n, msg
	}
	u, why := safeCalendarURL(raw)
	if why != "" {
		return save(0, "Could not read it: "+why+".")
	}
	c, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(c, http.MethodGet, u.String(), nil)
	req.Header.Set("User-Agent", "LogaLuxe calendar sync")
	client := &http.Client{Transport: calendarTransport(), CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= 3 || req.URL.Scheme != "https" {
			return http.ErrUseLastResponse
		}
		if _, why := safeCalendarURL(req.URL.String()); why != "" {
			return http.ErrUseLastResponse
		}
		return nil
	}}
	defer client.CloseIdleConnections()
	res, err := client.Do(req)
	if err != nil {
		return save(0, "Could not reach your calendar. We will try again in ten minutes.")
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return save(0, fmt.Sprintf("Your calendar answered with an error (%d). The address may have been changed or switched off.", res.StatusCode))
	}
	data, err := io.ReadAll(io.LimitReader(res.Body, 5<<20))
	if err != nil || !strings.Contains(string(data), "BEGIN:VCALENDAR") {
		return save(0, "That address did not give a calendar. Paste the private address in iCal format.")
	}
	loc, err := time.LoadLocation(tz)
	if err != nil {
		loc = time.UTC
	}
	now := time.Now()
	got := parseICS(string(data), loc, now.Add(-24*time.Hour), now.AddDate(0, 0, 120))
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, ""
	}
	defer tx.Rollback(ctx)
	_, _ = tx.Exec(ctx, `delete from calendar_blocks where staff_id=$1 and external`, staffID)
	for i, b := range got.Busy {
		if i >= 2000 {
			break
		}
		// Only the times are kept. What the event is stays in the person's own calendar.
		_, _ = tx.Exec(ctx, `insert into calendar_blocks (business_id, staff_id, starts_at, ends_at, reason, created_by, external) values ($1,$2,$3,$4,'Busy (own calendar)','calendar sync',true)`, biz, staffID, b.Start, b.End)
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, ""
	}
	msg := fmt.Sprintf("Read %d busy %s for the next four months.", len(got.Busy), map[bool]string{true: "time", false: "times"}[len(got.Busy) == 1])
	if got.Unread > 0 {
		msg += fmt.Sprintf(" %d repeating %s could not be read (monthly or yearly rules), so block those times yourself.", got.Unread, map[bool]string{true: "event", false: "events"}[got.Unread == 1])
	}
	return save(len(got.Busy), msg)
}

// PUT /v1/m/calendar-sync/import?staff_id=   {url}   an empty address stops the import and removes the busy times it made
func (s *Server) mCalImport(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id, ok := s.calStaff(w, r)
	if !ok {
		return
	}
	var req struct {
		URL string `json:"url"`
	}
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, "invalid json")
		return
	}
	if strings.TrimSpace(req.URL) == "" {
		_, _ = s.pool.Exec(ctx, `update staff set cal_import_url=null, cal_import_at=null, cal_import_note='' where id=$1`, id)
		_, _ = s.pool.Exec(ctx, `delete from calendar_blocks where staff_id=$1 and external`, id)
		writeJSON(w, 200, s.calState(ctx, id))
		return
	}
	u, why := safeCalendarURL(req.URL)
	if why != "" {
		writeErr(w, 400, why)
		return
	}
	if strings.Contains(u.Path, "/v1/cal/") && strings.Contains(strings.ToLower(u.Host), "logaluxe") {
		writeErr(w, 400, "that is your LogaLuxe calendar address; paste the private address of your own calendar instead")
		return
	}
	if _, err := s.pool.Exec(ctx, `update staff set cal_import_url=$2 where id=$1`, id, u.String()); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	s.importCalendar(ctx, id)
	writeJSON(w, 200, s.calState(ctx, id))
}

// POST /v1/m/calendar-sync/import/run?staff_id=   read it again now
func (s *Server) mCalImportRun(w http.ResponseWriter, r *http.Request) {
	id, ok := s.calStaff(w, r)
	if !ok {
		return
	}
	s.importCalendar(r.Context(), id)
	writeJSON(w, 200, s.calState(r.Context(), id))
}

// importCalendars is the worker's ten-minute pass over everyone who has an import set up.
func (s *Server) importCalendars(ctx context.Context) {
	ids, _ := rows(ctx, s.pool, `select id::text as id from staff where cal_import_url is not null and not archived order by cal_import_at nulls first limit 200`)
	for _, st := range ids {
		s.importCalendar(ctx, fmt.Sprint(st["id"]))
	}
}

// ---------- a person's own day ----------

// GET /v1/m/my-day?date=YYYY-MM-DD&staff_id=
// What one person has on: their bookings in order with what they need to know, and the times they are blocked.
func (s *Server) mMyDay(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	m := mc(r)
	staffID, ok := s.calStaff(w, r)
	if !ok {
		return
	}
	day, err := time.ParseInLocation("2006-01-02", r.URL.Query().Get("date"), m.Loc)
	if err != nil {
		now := time.Now().In(m.Loc)
		day = time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, m.Loc)
	}
	next := day.AddDate(0, 0, 1)
	list, err := rows(ctx, s.pool, `select `+bookingCols+`,
		(select coalesce(json_agg(json_build_object('label', ba.label, 'kind', ba.kind, 'answer', ba.answer) order by ba.sort), '[]') from booking_answers ba where ba.booking_id = bk.id) as answers,
		(select c.notes from clients c where c.id = bk.client_id) as client_notes,
		(select count(*) from bookings p where p.client_id = bk.client_id and p.status in ('completed','paid') and p.id <> bk.id) as past_visits
		from bookings bk join staff st on st.id = bk.staff_id
		where bk.business_id=$1 and bk.staff_id=$2 and bk.starts_at >= $3 and bk.starts_at < $4 and bk.status not in ('cancelled_client','cancelled_business','rescheduled')
		order by bk.starts_at`, m.BusinessID, staffID, day, next)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	blocks, _ := rows(ctx, s.pool, `select id, starts_at, ends_at, reason, external from calendar_blocks where staff_id=$1 and starts_at < $3 and ends_at > $2 order by starts_at`, staffID, day, next)
	who, _ := row(ctx, s.pool, `select id, name, initials, tone, hours, breaks from staff where id=$1`, staffID)
	writeJSON(w, 200, M{"date": day.Format("2006-01-02"), "today": time.Now().In(m.Loc).Format("2006-01-02"), "timezone": m.Timezone, "currency": m.Currency, "staff": who, "bookings": list, "blocks": blocks,
		"can_take_payments": m.Permissions["take_payments"]})
}
