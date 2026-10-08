package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// Creating and editing what the marketplace sells: businesses with their
// locations, services and team, and shop products.

var (
	slugRe   = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{1,59}$`)
	slugJunk = regexp.MustCompile(`[^a-z0-9]+`)
	toneRe   = regexp.MustCompile(`^#[0-9A-Fa-f]{6}$`)
	clockRe  = regexp.MustCompile(`^([01]\d|2[0-3]):[0-5]\d$`)

	businessCategories = map[string]bool{"hair": true, "braids": true, "barber": true, "nails": true, "lashes": true, "skin": true, "makeup": true, "spa": true}
	productCategories  = map[string]bool{"hair": true, "styling": true, "tools": true, "skin": true, "nails": true, "gift": true}
	weekDays           = []string{"mon", "tue", "wed", "thu", "fri", "sat", "sun"}
)

func slugify(s string) string {
	out := strings.Trim(slugJunk.ReplaceAllString(strings.ToLower(s), "-"), "-")
	if len(out) > 60 {
		out = strings.Trim(out[:60], "-")
	}
	return out
}

func initialsOf(name string) string {
	var out []rune
	for _, w := range strings.Fields(name) {
		out = append(out, []rune(strings.ToUpper(w))[0])
		if len(out) == 2 {
			break
		}
	}
	if len(out) == 0 {
		return "?"
	}
	return string(out)
}

func isCode(err error, code string) bool {
	var pg *pgconn.PgError
	return errors.As(err, &pg) && pg.Code == code
}

func cleanList(in []string, max int) []string {
	out := []string{}
	for _, v := range in {
		if v = strings.TrimSpace(v); v != "" && len(out) < max {
			out = append(out, v)
		}
	}
	return out
}

// hoursJSON checks opening hours and returns them in the stored shape:
// {"mon": ["09:00","18:00"], "sun": null, ...}
func hoursJSON(in map[string][]string) ([]byte, string) {
	out := map[string]any{}
	for _, d := range weekDays {
		v := in[d]
		if len(v) == 0 {
			out[d] = nil
			continue
		}
		if len(v) != 2 || !clockRe.MatchString(v[0]) || !clockRe.MatchString(v[1]) || v[0] >= v[1] {
			return nil, "opening hours for " + d + " need an opening time before a closing time, like 09:00 and 18:00"
		}
		out[d] = v
	}
	b, _ := json.Marshal(out)
	return b, ""
}

// ---------- businesses ----------

type businessReq struct {
	Name       string   `json:"name"`
	Slug       string   `json:"slug"`
	Category   string   `json:"category"`
	Market     string   `json:"market"`
	OwnerName  string   `json:"owner_name"`
	Tagline    string   `json:"tagline"`
	About      string   `json:"about"`
	Phone      string   `json:"phone"`
	Email      string   `json:"email"`
	Instagram  string   `json:"instagram"`
	Timezone   string   `json:"timezone"`
	Tone       string   `json:"tone"`
	Highlights []string `json:"highlights"`
	// Used only when creating: the first location.
	Address string `json:"address"`
	City    string `json:"city"`
	Region  string `json:"region"`
}

func (b *businessReq) check(creating bool) string {
	b.Name, b.OwnerName = strings.TrimSpace(b.Name), strings.TrimSpace(b.OwnerName)
	switch {
	case len(b.Name) < 2 || len(b.Name) > 80:
		return "the business name must be 2 to 80 characters"
	case !businessCategories[b.Category]:
		return "choose a category"
	case creating && b.Market != "US" && b.Market != "NG":
		return "market must be US or NG"
	case b.OwnerName == "":
		return "the owner's name is required"
	case len(b.Tagline) > 140:
		return "keep the tagline under 140 characters"
	case len(b.About) > 2000:
		return "keep the description under 2,000 characters"
	case b.Tone != "" && !toneRe.MatchString(b.Tone):
		return "the colour must look like #7A1F2B"
	}
	if b.Timezone != "" {
		if _, err := time.LoadLocation(b.Timezone); err != nil {
			return "unknown time zone; use a name like America/Chicago or Africa/Lagos"
		}
	}
	return ""
}

// POST /v1/admin/businesses   creates a business with one location and its owner on the team
func (s *Server) adminBusinessCreate(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var req businessReq
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, "invalid json")
		return
	}
	if msg := req.check(true); msg != "" {
		writeErr(w, 400, msg)
		return
	}
	if req.Slug == "" {
		req.Slug = slugify(req.Name)
	}
	if !slugRe.MatchString(req.Slug) {
		writeErr(w, 400, "the booking link can use lower-case letters, numbers and dashes")
		return
	}
	currency, country := "USD", "US"
	if req.Market == "NG" {
		currency, country = "NGN", "NG"
	}
	if req.Timezone == "" {
		req.Timezone = map[string]string{"US": "America/Chicago", "NG": "Africa/Lagos"}[req.Market]
	}
	if req.Tone == "" {
		req.Tone = "#3B1D22"
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	defer tx.Rollback(ctx)
	var id string
	err = tx.QueryRow(ctx, `insert into businesses (slug, name, tagline, about, category, market, currency, timezone, phone, email, instagram, status, verification_status, tone, highlights, owner_name)
		values ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,'pending','unverified',$12,$13,$14) returning id::text`,
		req.Slug, req.Name, req.Tagline, req.About, req.Category, req.Market, currency, req.Timezone, req.Phone, req.Email, req.Instagram, req.Tone, cleanList(req.Highlights, 8), req.OwnerName).Scan(&id)
	if err != nil {
		if isCode(err, "23505") {
			writeErr(w, 409, "that booking link is taken; choose another")
			return
		}
		writeErr(w, 500, err.Error())
		return
	}
	hours := `{"mon":["09:00","18:00"],"tue":["09:00","18:00"],"wed":["09:00","18:00"],"thu":["09:00","18:00"],"fri":["09:00","18:00"],"sat":["09:00","16:00"],"sun":null}`
	locName := req.City
	if locName == "" {
		locName = "Main location"
	}
	var lat, lng *float64
	if la, ln, ok := geocode(ctx, req.Address, req.City, req.Region); ok {
		lat, lng = &la, &ln
	}
	if _, err := tx.Exec(ctx, `insert into locations (business_id, name, address, city, region, country, timezone, is_primary, hours, lat, lng) values ($1,$2,$3,$4,$5,$6,$7,true,$8::jsonb,$9,$10)`,
		id, locName, req.Address, req.City, req.Region, country, req.Timezone, hours, lat, lng); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if _, err := tx.Exec(ctx, `insert into staff (business_id, name, initials, role, level, tone) values ($1,$2,$3,'owner','master',$4)`, id, req.OwnerName, initialsOf(req.OwnerName), req.Tone); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	// It joins the verification queue; approving it there sets it live.
	if _, err := tx.Exec(ctx, `insert into verification_requests (business_id, status, portfolio_note) values ($1,'pending','Added by the LogaXP team')`, id); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if err := tx.Commit(ctx); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	s.audit(r, "business.create", id, nil, M{"name": req.Name, "slug": req.Slug, "market": req.Market})
	writeJSON(w, 201, M{"ok": true, "id": id, "slug": req.Slug})
}

// PUT /v1/admin/businesses/{id}/profile
func (s *Server) adminBusinessProfile(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id := chi.URLParam(r, "id")
	var req businessReq
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, "invalid json")
		return
	}
	if msg := req.check(false); msg != "" {
		writeErr(w, 400, msg)
		return
	}
	before, err := row(ctx, s.pool, `select name, tagline, category, phone, email, instagram, owner_name, timezone, tone from businesses where id=$1`, id)
	if err != nil {
		writeErr(w, 404, "business not found")
		return
	}
	if req.Timezone == "" {
		req.Timezone = before["timezone"].(string)
	}
	if req.Tone == "" {
		req.Tone = before["tone"].(string)
	}
	if _, err := s.pool.Exec(ctx, `update businesses set name=$2, tagline=$3, about=$4, category=$5, phone=$6, email=$7, instagram=$8, owner_name=$9, timezone=$10, tone=$11, highlights=$12 where id=$1`,
		id, req.Name, req.Tagline, req.About, req.Category, req.Phone, req.Email, req.Instagram, req.OwnerName, req.Timezone, req.Tone, cleanList(req.Highlights, 8)); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	// Bookings are placed in the location's time zone; keep them in step.
	_, _ = s.pool.Exec(ctx, `update locations set timezone=$2 where business_id=$1`, id, req.Timezone)
	s.audit(r, "business.profile", id, before, M{"name": req.Name, "category": req.Category, "timezone": req.Timezone})
	writeJSON(w, 200, M{"ok": true})
}

// ---------- locations ----------

type locationReq struct {
	Name         string              `json:"name"`
	Address      string              `json:"address"`
	City         string              `json:"city"`
	Region       string              `json:"region"`
	ArrivalNotes string              `json:"arrival_notes"`
	Lat          *float64            `json:"lat"` // leave both empty to find the position from the address
	Lng          *float64            `json:"lng"`
	Hours        map[string][]string `json:"hours"`
}

// PUT /v1/admin/locations/{id}
func (s *Server) adminLocationUpdate(w http.ResponseWriter, r *http.Request) {
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
	if (req.Lat == nil) != (req.Lng == nil) {
		writeErr(w, 400, "give both latitude and longitude, or neither")
		return
	}
	if req.Lat != nil && (*req.Lat < -90 || *req.Lat > 90 || *req.Lng < -180 || *req.Lng > 180) {
		writeErr(w, 400, "that map position is not on Earth; latitude is -90 to 90 and longitude -180 to 180")
		return
	}
	var oldAddress, oldCity string
	var hadPosition bool
	if err := s.pool.QueryRow(r.Context(), `select address, city, lat is not null from locations where id=$1`, id).Scan(&oldAddress, &oldCity, &hadPosition); err != nil {
		writeErr(w, 404, "location not found")
		return
	}
	positioned := "kept"
	if req.Lat != nil {
		positioned = "set by hand"
	} else if !hadPosition || oldAddress != req.Address || oldCity != req.City {
		// No position given and the address is new or changed: look it up.
		if lat, lng, ok := geocode(r.Context(), req.Address, req.City, req.Region); ok {
			req.Lat, req.Lng, positioned = &lat, &lng, "found from the address"
		} else {
			positioned = "not found"
		}
	}
	tag, err := s.pool.Exec(r.Context(), `update locations set name=$2, address=$3, city=$4, region=$5, arrival_notes=$6, hours=$7::jsonb, lat=coalesce($8, lat), lng=coalesce($9, lng) where id=$1`,
		id, strings.TrimSpace(req.Name), req.Address, req.City, req.Region, req.ArrivalNotes, string(hours), req.Lat, req.Lng)
	if err != nil || tag.RowsAffected() == 0 {
		writeErr(w, 404, "location not found")
		return
	}
	s.audit(r, "location.update", id, nil, M{"name": req.Name, "hours": json.RawMessage(hours), "position": positioned})
	writeJSON(w, 200, M{"ok": true, "position": positioned})
}

// ---------- services ----------

type serviceReq struct {
	Name          string   `json:"name"`
	Category      string   `json:"category"`
	Description   string   `json:"description"`
	DurationMin   int      `json:"duration_min"`
	ProcessingMin int      `json:"processing_min"`
	BufferMin     int      `json:"buffer_min"`
	PriceCents    int      `json:"price_cents"`
	DepositCents  int      `json:"deposit_cents"`
	Online        *bool    `json:"online"`
	Sort          int      `json:"sort"`
	StaffIDs      []string `json:"staff_ids"` // who performs it; empty means everyone bookable
}

func (v *serviceReq) check() string {
	v.Name, v.Category = strings.TrimSpace(v.Name), strings.TrimSpace(v.Category)
	switch {
	case v.Name == "" || len(v.Name) > 80:
		return "the service needs a name of up to 80 characters"
	case v.Category == "" || len(v.Category) > 40:
		return "the service needs a menu group, like Braids or Add-ons"
	case v.DurationMin < 5 || v.DurationMin > 720:
		return "the length must be between 5 minutes and 12 hours"
	case v.ProcessingMin < 0 || v.BufferMin < 0 || v.ProcessingMin > 480 || v.BufferMin > 120:
		return "processing and clean-up time cannot be negative or unreasonably long"
	case v.PriceCents < 0:
		return "the price cannot be negative"
	case v.DepositCents < 0 || v.DepositCents > v.PriceCents:
		return "the deposit must be between zero and the price"
	}
	return ""
}

// setServiceStaff replaces who performs a service. Only staff of the same business are linked.
func setServiceStaff(ctx context.Context, q interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
}, serviceID, businessID string, staffIDs []string) error {
	if _, err := q.Exec(ctx, `delete from staff_services where service_id=$1`, serviceID); err != nil {
		return err
	}
	if len(staffIDs) == 0 {
		_, err := q.Exec(ctx, `insert into staff_services (staff_id, service_id) select id, $1 from staff where business_id=$2 and bookable`, serviceID, businessID)
		return err
	}
	_, err := q.Exec(ctx, `insert into staff_services (staff_id, service_id) select id, $1 from staff where business_id=$2 and id::text = any($3)`, serviceID, businessID, staffIDs)
	return err
}

// POST /v1/admin/businesses/{id}/services
func (s *Server) adminServiceCreate(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	bizID := chi.URLParam(r, "id")
	var req serviceReq
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, "invalid json")
		return
	}
	if msg := req.check(); msg != "" {
		writeErr(w, 400, msg)
		return
	}
	online := req.Online == nil || *req.Online
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	defer tx.Rollback(ctx)
	var id string
	err = tx.QueryRow(ctx, `insert into services (business_id, name, category, description, duration_min, processing_min, buffer_min, price_cents, deposit_cents, online, sort)
		values ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10, coalesce(nullif($11,0), (select coalesce(max(sort),0)+1 from services where business_id=$1))) returning id::text`,
		bizID, req.Name, req.Category, req.Description, req.DurationMin, req.ProcessingMin, req.BufferMin, req.PriceCents, req.DepositCents, online, req.Sort).Scan(&id)
	if err != nil {
		if isCode(err, "23503") || isCode(err, "22P02") {
			writeErr(w, 404, "business not found")
			return
		}
		writeErr(w, 500, err.Error())
		return
	}
	if err := setServiceStaff(ctx, tx, id, bizID, req.StaffIDs); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if err := tx.Commit(ctx); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	s.audit(r, "service.create", bizID, nil, M{"service": req.Name, "price_cents": req.PriceCents})
	writeJSON(w, 201, M{"ok": true, "id": id})
}

// PUT /v1/admin/services/{id}
func (s *Server) adminServiceUpdate(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id := chi.URLParam(r, "id")
	var req serviceReq
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, "invalid json")
		return
	}
	if msg := req.check(); msg != "" {
		writeErr(w, 400, msg)
		return
	}
	before, err := row(ctx, s.pool, `select business_id::text as business_id, name, price_cents, deposit_cents, duration_min, online from services where id=$1`, id)
	if err != nil {
		writeErr(w, 404, "service not found")
		return
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `update services set name=$2, category=$3, description=$4, duration_min=$5, processing_min=$6, buffer_min=$7, price_cents=$8, deposit_cents=$9, online=coalesce($10, online), sort=coalesce(nullif($11,0), sort) where id=$1`,
		id, req.Name, req.Category, req.Description, req.DurationMin, req.ProcessingMin, req.BufferMin, req.PriceCents, req.DepositCents, req.Online, req.Sort); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if err := setServiceStaff(ctx, tx, id, before["business_id"].(string), req.StaffIDs); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if err := tx.Commit(ctx); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	s.audit(r, "service.update", before["business_id"].(string), before, M{"service": req.Name, "price_cents": req.PriceCents, "online": req.Online})
	writeJSON(w, 200, M{"ok": true})
}

// DELETE /v1/admin/services/{id}
// A service that has been booked is kept for the record and taken off the menu instead.
func (s *Server) adminServiceDelete(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id := chi.URLParam(r, "id")
	before, err := row(ctx, s.pool, `select business_id::text as business_id, name from services where id=$1`, id)
	if err != nil {
		writeErr(w, 404, "service not found")
		return
	}
	var used bool
	_ = s.pool.QueryRow(ctx, `select exists(select 1 from booking_items where service_id=$1)`, id).Scan(&used)
	if used {
		_, _ = s.pool.Exec(ctx, `update services set online=false where id=$1`, id)
		s.audit(r, "service.retire", before["business_id"].(string), before, nil)
		writeJSON(w, 200, M{"ok": true, "retired": true})
		return
	}
	if _, err := s.pool.Exec(ctx, `delete from services where id=$1`, id); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	s.audit(r, "service.delete", before["business_id"].(string), before, nil)
	writeJSON(w, 200, M{"ok": true, "retired": false})
}

// ---------- team ----------

type staffReq struct {
	Name     string `json:"name"`
	Role     string `json:"role"`
	Level    string `json:"level"`
	Tone     string `json:"tone"`
	Bookable *bool  `json:"bookable"`
}

func (v *staffReq) check() string {
	v.Name = strings.TrimSpace(v.Name)
	if v.Role == "" {
		v.Role = "staff"
	}
	if v.Level == "" {
		v.Level = "senior"
	}
	switch {
	case v.Name == "" || len(v.Name) > 60:
		return "the team member needs a name"
	case v.Role != "owner" && v.Role != "manager" && v.Role != "staff":
		return "role must be owner, manager or staff"
	case v.Level != "junior" && v.Level != "senior" && v.Level != "master":
		return "level must be junior, senior or master"
	case v.Tone != "" && !toneRe.MatchString(v.Tone):
		return "the colour must look like #7A1F2B"
	}
	return ""
}

// POST /v1/admin/businesses/{id}/staff   the new person can do every service until told otherwise
func (s *Server) adminStaffCreate(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	bizID := chi.URLParam(r, "id")
	var req staffReq
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, "invalid json")
		return
	}
	if msg := req.check(); msg != "" {
		writeErr(w, 400, msg)
		return
	}
	if req.Tone == "" {
		req.Tone = "#7A1F2B"
	}
	var id string
	err := s.pool.QueryRow(ctx, `insert into staff (business_id, name, initials, role, level, tone, bookable) values ($1,$2,$3,$4,$5,$6,$7) returning id::text`,
		bizID, req.Name, initialsOf(req.Name), req.Role, req.Level, req.Tone, req.Bookable == nil || *req.Bookable).Scan(&id)
	if err != nil {
		if isCode(err, "23503") || isCode(err, "22P02") {
			writeErr(w, 404, "business not found")
			return
		}
		writeErr(w, 500, err.Error())
		return
	}
	_, _ = s.pool.Exec(ctx, `insert into staff_services (staff_id, service_id) select $1, id from services where business_id=$2`, id, bizID)
	s.audit(r, "staff.create", bizID, nil, M{"name": req.Name, "role": req.Role})
	writeJSON(w, 201, M{"ok": true, "id": id})
}

// PUT /v1/admin/staff/{id}
func (s *Server) adminStaffUpdate(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id := chi.URLParam(r, "id")
	var req staffReq
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, "invalid json")
		return
	}
	if msg := req.check(); msg != "" {
		writeErr(w, 400, msg)
		return
	}
	before, err := row(ctx, s.pool, `select business_id::text as business_id, name, role, level, bookable from staff where id=$1`, id)
	if err != nil {
		writeErr(w, 404, "team member not found")
		return
	}
	if _, err := s.pool.Exec(ctx, `update staff set name=$2, initials=$3, role=$4, level=$5, tone=coalesce(nullif($6,''), tone), bookable=coalesce($7, bookable) where id=$1`,
		id, req.Name, initialsOf(req.Name), req.Role, req.Level, req.Tone, req.Bookable); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	s.audit(r, "staff.update", before["business_id"].(string), before, M{"name": req.Name, "role": req.Role, "bookable": req.Bookable})
	writeJSON(w, 200, M{"ok": true})
}

// DELETE /v1/admin/staff/{id}
// Someone with bookings on record is kept and made unbookable instead.
func (s *Server) adminStaffDelete(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id := chi.URLParam(r, "id")
	before, err := row(ctx, s.pool, `select business_id::text as business_id, name, role from staff where id=$1`, id)
	if err != nil {
		writeErr(w, 404, "team member not found")
		return
	}
	var upcoming int
	_ = s.pool.QueryRow(ctx, `select count(*) from bookings where staff_id=$1 and starts_at > now() and status in ('requested','confirmed')`, id).Scan(&upcoming)
	if upcoming > 0 {
		writeErr(w, 409, "this person has upcoming bookings; move or cancel those first")
		return
	}
	var used bool
	_ = s.pool.QueryRow(ctx, `select exists(select 1 from bookings where staff_id=$1)`, id).Scan(&used)
	if used {
		_, _ = s.pool.Exec(ctx, `update staff set bookable=false where id=$1`, id)
		s.audit(r, "staff.retire", before["business_id"].(string), before, nil)
		writeJSON(w, 200, M{"ok": true, "retired": true})
		return
	}
	if _, err := s.pool.Exec(ctx, `delete from staff where id=$1`, id); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	s.audit(r, "staff.delete", before["business_id"].(string), before, nil)
	writeJSON(w, 200, M{"ok": true, "retired": false})
}

// ---------- products ----------

type productReq struct {
	Name          string   `json:"name"`
	Slug          string   `json:"slug"`
	SellerName    string   `json:"seller_name"`
	BusinessSlug  string   `json:"business_slug"` // optional: the business that sells it
	Category      string   `json:"category"`
	Description   string   `json:"description"`
	HowToUse      string   `json:"how_to_use"`
	PriceCents    int      `json:"price_cents"`
	CompareCents  *int     `json:"compare_cents"`
	Stock         int      `json:"stock"`
	Tone          string   `json:"tone"`
	Tags          []string `json:"tags"`
	Pickup        bool     `json:"pickup"`
	Shipping      bool     `json:"shipping"`
	ShippingCents int      `json:"shipping_cents"`
	Sizes         []struct {
		Label      string `json:"label"`
		PriceCents int    `json:"price_cents"`
	} `json:"sizes"`
}

// check validates the product and returns the sizes as stored JSON and the seller's business id.
func (s *Server) checkProduct(ctx context.Context, p *productReq) (sizes []byte, businessID *string, msg string) {
	p.Name, p.SellerName = strings.TrimSpace(p.Name), strings.TrimSpace(p.SellerName)
	if p.Tone == "" {
		p.Tone = "#3B1D22"
	}
	switch {
	case len(p.Name) < 2 || len(p.Name) > 100:
		return nil, nil, "the product needs a name of 2 to 100 characters"
	case !productCategories[p.Category]:
		return nil, nil, "choose a category"
	case p.PriceCents <= 0:
		return nil, nil, "the price must be more than zero"
	case p.CompareCents != nil && *p.CompareCents <= p.PriceCents:
		return nil, nil, "the 'was' price must be higher than the price, or left empty"
	case p.Stock < 0:
		return nil, nil, "stock cannot be negative"
	case p.ShippingCents < 0:
		return nil, nil, "shipping cannot be negative"
	case !p.Pickup && !p.Shipping:
		return nil, nil, "offer pickup, shipping, or both"
	case !toneRe.MatchString(p.Tone):
		return nil, nil, "the colour must look like #7A1F2B"
	case len(p.Description) > 2000 || len(p.HowToUse) > 2000:
		return nil, nil, "keep each text under 2,000 characters"
	case len(p.Sizes) > 8:
		return nil, nil, "a product can have up to 8 sizes"
	}
	seen := map[string]bool{}
	for i := range p.Sizes {
		p.Sizes[i].Label = strings.TrimSpace(p.Sizes[i].Label)
		if p.Sizes[i].Label == "" || p.Sizes[i].PriceCents <= 0 || seen[p.Sizes[i].Label] {
			return nil, nil, "each size needs its own name and a price above zero"
		}
		seen[p.Sizes[i].Label] = true
	}
	if p.Sizes == nil {
		sizes = []byte("[]")
	} else {
		sizes, _ = json.Marshal(p.Sizes)
	}
	if p.BusinessSlug != "" {
		var id, name string
		if err := s.pool.QueryRow(ctx, `select id::text, name from businesses where slug=$1`, p.BusinessSlug).Scan(&id, &name); err != nil {
			return nil, nil, "no business with the link " + p.BusinessSlug
		}
		businessID = &id
		if p.SellerName == "" {
			p.SellerName = name
		}
	}
	if p.SellerName == "" {
		return nil, nil, "say who sells it: a seller name, or a business"
	}
	return sizes, businessID, ""
}

// POST /v1/admin/products
func (s *Server) adminProductCreate(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var req productReq
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, "invalid json")
		return
	}
	sizes, bizID, msg := s.checkProduct(ctx, &req)
	if msg != "" {
		writeErr(w, 400, msg)
		return
	}
	if req.Slug == "" {
		req.Slug = slugify(req.Name)
	}
	if !slugRe.MatchString(req.Slug) {
		writeErr(w, 400, "the link can use lower-case letters, numbers and dashes")
		return
	}
	var id string
	err := s.pool.QueryRow(ctx, `insert into products (business_id, seller_name, slug, name, description, how_to_use, category, price_cents, compare_cents, stock, sizes, tone, tags, pickup, shipping, shipping_cents)
		values ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11::jsonb,$12,$13,$14,$15,$16) returning id::text`,
		bizID, req.SellerName, req.Slug, req.Name, req.Description, req.HowToUse, req.Category, req.PriceCents, req.CompareCents, req.Stock, string(sizes), req.Tone, cleanList(req.Tags, 8), req.Pickup, req.Shipping, req.ShippingCents).Scan(&id)
	if err != nil {
		if isCode(err, "23505") {
			writeErr(w, 409, "a product with that link already exists; change the link")
			return
		}
		writeErr(w, 500, err.Error())
		return
	}
	s.audit(r, "product.create", id, nil, M{"name": req.Name, "price_cents": req.PriceCents, "stock": req.Stock})
	writeJSON(w, 201, M{"ok": true, "id": id, "slug": req.Slug})
}

// PUT /v1/admin/products/{id}   the link never changes, so old URLs and photos keep working
func (s *Server) adminProductUpdate(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id := chi.URLParam(r, "id")
	var req productReq
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, "invalid json")
		return
	}
	sizes, bizID, msg := s.checkProduct(ctx, &req)
	if msg != "" {
		writeErr(w, 400, msg)
		return
	}
	before, err := row(ctx, s.pool, `select name, price_cents, compare_cents, stock, category from products where id=$1`, id)
	if err != nil {
		writeErr(w, 404, "product not found")
		return
	}
	if _, err := s.pool.Exec(ctx, `update products set business_id=$2, seller_name=$3, name=$4, description=$5, how_to_use=$6, category=$7, price_cents=$8, compare_cents=$9, stock=$10, sizes=$11::jsonb, tone=$12, tags=$13, pickup=$14, shipping=$15, shipping_cents=$16 where id=$1`,
		id, bizID, req.SellerName, req.Name, req.Description, req.HowToUse, req.Category, req.PriceCents, req.CompareCents, req.Stock, string(sizes), req.Tone, cleanList(req.Tags, 8), req.Pickup, req.Shipping, req.ShippingCents); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	s.audit(r, "product.update", id, before, M{"name": req.Name, "price_cents": req.PriceCents, "compare_cents": req.CompareCents, "stock": req.Stock})
	writeJSON(w, 200, M{"ok": true})
}
