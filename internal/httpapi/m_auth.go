package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/crypto/bcrypt"

	"logaluxe/api/internal/config"
	"logaluxe/api/internal/mail"
)

// The merchant side: people who run a business on LogaLuxe. A merchant signs
// in, and every request is then scoped to the one business their session has
// open. Merchant tokens live in their own table and work nowhere else.

const merchantSessionTTL = 14 * 24 * time.Hour

var merchantRank = map[string]int{"staff": 1, "manager": 2, "owner": 3}

type merchantKey struct{}

// Merchant is the signed-in person plus the business they are working in.
type Merchant struct {
	ID         string `json:"id"`
	Email      string `json:"email"`
	Name       string `json:"name"`
	Role       string `json:"role"`
	StaffID    string `json:"staff_id"`
	BusinessID string `json:"business_id"`
	Business   string `json:"business"`
	Slug       string `json:"slug"`
	Currency   string `json:"currency"`
	Timezone   string `json:"timezone"`
	Market     string `json:"market"`
	Plan       string `json:"plan"`
	Status     string `json:"status"`
	// What the owner switched on for this team member. Managers and the owner can do everything.
	Permissions map[string]bool `json:"permissions"`
	Loc         *time.Location  `json:"-"`
}

func mc(r *http.Request) Merchant {
	m, _ := r.Context().Value(merchantKey{}).(Merchant)
	return m
}

func (s *Server) requireMerchant(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tok := bearer(r)
		if tok == "" {
			writeErr(w, http.StatusUnauthorized, "sign in to continue")
			return
		}
		var m Merchant
		var staffID *string
		var permsRaw string
		err := s.pool.QueryRow(r.Context(), `
			select u.id::text, u.email, u.name, mm.role, mm.staff_id::text, b.id::text, b.name, b.slug, b.currency, b.timezone, b.market, b.plan, b.status,
			       coalesce((select st.permissions::text from staff st where st.id = mm.staff_id), '{}')
			from merchant_sessions ms
			join merchant_users u on u.id = ms.merchant_id
			join merchant_members mm on mm.merchant_id = u.id and mm.business_id = ms.business_id
			join businesses b on b.id = mm.business_id
			where ms.token_hash = $1 and ms.expires_at > now()`, hashToken(tok)).
			Scan(&m.ID, &m.Email, &m.Name, &m.Role, &staffID, &m.BusinessID, &m.Business, &m.Slug, &m.Currency, &m.Timezone, &m.Market, &m.Plan, &m.Status, &permsRaw)
		if err != nil {
			writeErr(w, http.StatusUnauthorized, "your session has ended; sign in again")
			return
		}
		if staffID != nil {
			m.StaffID = *staffID
		}
		m.Permissions = map[string]bool{}
		var saved map[string]any
		_ = json.Unmarshal([]byte(permsRaw), &saved)
		for k, def := range staffPermDefaults {
			m.Permissions[k] = def
			if v, ok := saved[k].(bool); ok {
				m.Permissions[k] = v
			}
			if merchantRank[m.Role] >= merchantRank["manager"] {
				m.Permissions[k] = true
			}
		}
		if m.Loc, err = time.LoadLocation(m.Timezone); err != nil {
			m.Loc = time.UTC
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), merchantKey{}, m)))
	})
}

// mNeed blocks roles below the one given: staff < manager < owner.
func (s *Server) mNeed(role string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if merchantRank[mc(r).Role] < merchantRank[role] {
				writeErr(w, http.StatusForbidden, "your role cannot do this; ask the owner or a manager")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func (s *Server) startMerchantSession(ctx context.Context, merchantID, businessID string) (string, error) {
	tok, err := newToken()
	if err != nil {
		return "", err
	}
	if _, err := s.pool.Exec(ctx, `insert into merchant_sessions (token_hash, merchant_id, business_id, expires_at) values ($1,$2,$3,$4)`, hashToken(tok), merchantID, businessID, time.Now().Add(merchantSessionTTL)); err != nil {
		return "", err
	}
	_, _ = s.pool.Exec(ctx, `delete from merchant_sessions where expires_at < now()`)
	return tok, nil
}

// Messages every new business starts with. They can be edited under Marketing.
var defaultAutomations = map[string]string{
	"confirmation":   "Hi {first name}, you are booked at {business} for {last service} on {time}. Need to change it? {booking link}",
	"reminder_24h":   "Hi {first name}, a reminder of your {last service} at {business} tomorrow, {time}. Reply here if anything has changed.",
	"reminder_2h":    "See you soon, {first name}. Your {last service} at {business} is at {time}.",
	"review_request": "Thank you for coming in, {first name}. How was your {last service} with {staff}? A short review helps a lot: {booking link}",
	"rebook":         "Hi {first name}, it has been a few weeks since your {last service}. Ready for the next one? {booking link}",
	"win_back":       "We miss you, {first name}. It has been a while since your last visit to {business}. Book when you are ready: {booking link}",
	"birthday":       "Happy birthday, {first name}! Treat yourself this month at {business}: {booking link}",
}

var defaultSavedReplies = [][2]string{
	{"Price list", "Hi! Our full price list with durations is on our booking page, and you can book straight from there."},
	{"Directions", "We will send the address and parking notes with your confirmation. Message us if you get lost on the day."},
	{"Deposit policy", "A deposit holds your slot and comes off your total. It is returned in full if you cancel inside the free window."},
	{"Running late", "Thanks for letting us know. Please come as soon as you can. If you are more than 15 minutes late we may need to shorten or move the appointment."},
	{"Aftercare", "Thank you for coming in! Keep it moisturised, sleep in a bonnet, and message us if anything feels too tight."},
}

func seedBusinessDefaults(ctx context.Context, q interface {
	Exec(context.Context, string, ...any) (pgconnTag, error)
}, businessID string) {
	for k, msg := range defaultAutomations {
		_, _ = q.Exec(ctx, `insert into automations (business_id, key, message) values ($1,$2,$3) on conflict do nothing`, businessID, k, msg)
	}
	for i, r := range defaultSavedReplies {
		_, _ = q.Exec(ctx, `insert into saved_replies (business_id, title, body, sort) select $1,$2,$3,$4 where not exists (select 1 from saved_replies where business_id=$1 and title=$2)`, businessID, r[0], r[1], i)
	}
}

// POST /v1/m/signup   a professional lists their business
func (s *Server) mSignup(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var req struct {
		Name     string `json:"name"`
		Email    string `json:"email"`
		Phone    string `json:"phone"`
		Password string `json:"password"`
		Business string `json:"business"`
		Category string `json:"category"`
		Market   string `json:"market"`
		City     string `json:"city"`
		Region   string `json:"region"`
		Address  string `json:"address"`
	}
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, "invalid json")
		return
	}
	req.Name, req.Business = strings.TrimSpace(req.Name), strings.TrimSpace(req.Business)
	req.Email = strings.ToLower(strings.TrimSpace(req.Email))
	phone, phoneOK := cleanPhone(req.Phone)
	switch {
	case req.Name == "" || len(req.Name) > 80:
		writeErr(w, 400, "tell us your name")
		return
	case !mail.Valid(req.Email):
		writeErr(w, 400, "that email address does not look right")
		return
	case !phoneOK:
		writeErr(w, 400, "that phone number does not look right; include the country code")
		return
	case len(req.Password) < minPassword || len(req.Password) > 200:
		writeErr(w, 400, "choose a password of at least 10 characters")
		return
	case len(req.Business) < 2 || len(req.Business) > 80:
		writeErr(w, 400, "the business name must be 2 to 80 characters")
		return
	case !businessCategories[req.Category]:
		writeErr(w, 400, "choose a category")
		return
	case req.Market != "US" && req.Market != "NG":
		writeErr(w, 400, "choose the United States or Nigeria")
		return
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(req.Password), bcrypt.DefaultCost)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	currency, country, tz := "USD", "US", "America/Chicago"
	if req.Market == "NG" {
		currency, country, tz = "NGN", "NG", "Africa/Lagos"
	}
	// A free handle: the name, then name-2, name-3.
	base := slugify(req.Business)
	if !slugRe.MatchString(base) {
		base = "studio"
	}
	slug := base
	for i := 2; i < 60; i++ {
		var taken bool
		_ = s.pool.QueryRow(ctx, `select exists(select 1 from businesses where slug=$1)`, slug).Scan(&taken)
		if !taken {
			break
		}
		slug = base + "-" + itoa(i)
	}
	var lat, lng *float64
	if la, ln, ok := geocode(ctx, req.Address, req.City, req.Region); ok {
		lat, lng = &la, &ln
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	defer tx.Rollback(ctx)
	var merchantID, bizID, staffID string
	if err := tx.QueryRow(ctx, `insert into merchant_users (email, name, phone, password_hash, last_login_at) values ($1,$2,$3,$4, now()) returning id::text`, req.Email, req.Name, phone, string(hash)).Scan(&merchantID); err != nil {
		if isCode(err, "23505") {
			writeErr(w, 409, "there is already a business account with that email; sign in instead")
			return
		}
		writeErr(w, 500, err.Error())
		return
	}
	if err := tx.QueryRow(ctx, `insert into businesses (slug, name, category, market, currency, timezone, phone, email, status, verification_status, owner_name)
		values ($1,$2,$3,$4,$5,$6,$7,$8,'pending','unverified',$9) returning id::text`, slug, req.Business, req.Category, req.Market, currency, tz, phone, req.Email, req.Name).Scan(&bizID); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	locName := firstNonEmpty(req.City, "Main location")
	hours := `{"mon":["09:00","18:00"],"tue":["09:00","18:00"],"wed":["09:00","18:00"],"thu":["09:00","18:00"],"fri":["09:00","18:00"],"sat":["09:00","16:00"],"sun":null}`
	if _, err := tx.Exec(ctx, `insert into locations (business_id, name, address, city, region, country, timezone, is_primary, hours, lat, lng) values ($1,$2,$3,$4,$5,$6,$7,true,$8::jsonb,$9,$10)`,
		bizID, locName, req.Address, req.City, req.Region, country, tz, hours, lat, lng); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if err := tx.QueryRow(ctx, `insert into staff (business_id, name, initials, role, level, email, phone) values ($1,$2,$3,'owner','master',$4,$5) returning id::text`, bizID, req.Name, initialsOf(req.Name), req.Email, phone).Scan(&staffID); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if _, err := tx.Exec(ctx, `insert into merchant_members (merchant_id, business_id, role, staff_id) values ($1,$2,'owner',$3)`, merchantID, bizID, staffID); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if _, err := tx.Exec(ctx, `insert into verification_requests (business_id, status, portfolio_note) values ($1,'pending','Signed up on the website')`, bizID); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	seedBusinessDefaults(ctx, txExec{tx}, bizID)
	if err := tx.Commit(ctx); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	tok, err := s.startMerchantSession(ctx, merchantID, bizID)
	if err != nil {
		writeErr(w, 500, "the account was created, but we could not sign you in; try signing in")
		return
	}
	_, _ = s.pool.Exec(ctx, `insert into audit_log (actor, action, target, after) values ($1, 'merchant.signup', $2, $3)`, req.Email, bizID, M{"business": req.Business, "market": req.Market})
	writeJSON(w, 201, M{"token": tok, "expires_in": int(merchantSessionTTL.Seconds()), "slug": slug})
}

// POST /v1/m/login   {email, password}
func (s *Server) mLogin(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var req struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if err := readJSON(r, &req); err != nil || req.Email == "" || req.Password == "" {
		writeErr(w, 400, "enter your email and password")
		return
	}
	var id, hash string
	var locked *time.Time
	err := s.pool.QueryRow(ctx, `select id::text, password_hash, locked_until from merchant_users where lower(email) = lower($1)`, strings.TrimSpace(req.Email)).Scan(&id, &hash, &locked)
	if err != nil {
		_ = bcrypt.CompareHashAndPassword(dummyHash, []byte(req.Password))
		writeErr(w, http.StatusUnauthorized, "wrong email or password")
		return
	}
	if locked != nil && locked.After(time.Now()) {
		writeErr(w, http.StatusTooManyRequests, "too many attempts; try again in 15 minutes, or reset your password")
		return
	}
	if bcrypt.CompareHashAndPassword([]byte(hash), []byte(req.Password)) != nil {
		_, _ = s.pool.Exec(ctx, `update merchant_users set failed_logins = failed_logins + 1,
			locked_until = case when failed_logins + 1 >= 6 then now() + interval '15 minutes' else locked_until end where id = $1`, id)
		writeErr(w, http.StatusUnauthorized, "wrong email or password")
		return
	}
	// Open the business they used last, or the first they belong to.
	var bizID string
	err = s.pool.QueryRow(ctx, `select mm.business_id::text from merchant_members mm
		left join lateral (select max(created_at) as at from merchant_sessions ms where ms.merchant_id = mm.merchant_id and ms.business_id = mm.business_id) last on true
		where mm.merchant_id = $1 order by last.at desc nulls last, mm.created_at limit 1`, id).Scan(&bizID)
	if err != nil {
		writeErr(w, 403, "this account is not linked to a business any more; contact support")
		return
	}
	tok, err := s.startMerchantSession(ctx, id, bizID)
	if err != nil {
		writeErr(w, 500, "could not sign you in")
		return
	}
	_, _ = s.pool.Exec(ctx, `update merchant_users set failed_logins = 0, locked_until = null, last_login_at = now() where id = $1`, id)
	writeJSON(w, 200, M{"token": tok, "expires_in": int(merchantSessionTTL.Seconds())})
}

// POST /v1/m/logout
func (s *Server) mLogout(w http.ResponseWriter, r *http.Request) {
	_, _ = s.pool.Exec(r.Context(), `delete from merchant_sessions where token_hash = $1`, hashToken(bearer(r)))
	writeJSON(w, 200, M{"ok": true})
}

// GET /v1/m/me   who is signed in, which business is open, and the badges the menu shows
func (s *Server) mMe(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	m := mc(r)
	businesses, _ := rows(ctx, s.pool, `select b.id, b.name, b.slug, mm.role, l.name as area from merchant_members mm join businesses b on b.id = mm.business_id
		left join locations l on l.business_id = b.id and l.is_primary where mm.merchant_id = $1 order by b.name`, m.ID)
	badges, _ := row(ctx, s.pool, `select
		(select coalesce(sum(unread_business),0) from threads where business_id=$1 and status='open') as inbox,
		(select count(*) from bookings where business_id=$1 and status in ('checked_in','in_progress','completed') and paid_at is null and starts_at > now() - interval '2 days') as checkout,
		(select count(*) from time_off where business_id=$1 and status='requested') as time_off,
		(select count(*) from locations where business_id=$1) as locations,
		(select name from locations where business_id=$1 and is_primary) as area,
		(select verification_status from businesses where id=$1) as verification`, m.BusinessID)
	writeJSON(w, 200, M{"merchant": m, "businesses": businesses, "badges": badges})
}

// POST /v1/m/switch   {business_id}   open another business you belong to
func (s *Server) mSwitch(w http.ResponseWriter, r *http.Request) {
	var req struct {
		BusinessID string `json:"business_id"`
	}
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, "invalid json")
		return
	}
	tag, err := s.pool.Exec(r.Context(), `update merchant_sessions ms set business_id = $2 where ms.token_hash = $1
		and exists (select 1 from merchant_members mm where mm.merchant_id = ms.merchant_id and mm.business_id = $2)`, hashToken(bearer(r)), req.BusinessID)
	if err != nil || tag.RowsAffected() == 0 {
		writeErr(w, 403, "you do not belong to that business")
		return
	}
	writeJSON(w, 200, M{"ok": true})
}

// POST /v1/m/password   {current, new}
func (s *Server) mPassword(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var req struct {
		Current string `json:"current"`
		New     string `json:"new"`
	}
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, "invalid json")
		return
	}
	if len(req.New) < minPassword || len(req.New) > 200 {
		writeErr(w, 400, "choose a new password of at least 10 characters")
		return
	}
	id := mc(r).ID
	var hash string
	if err := s.pool.QueryRow(ctx, `select password_hash from merchant_users where id=$1`, id).Scan(&hash); err != nil || bcrypt.CompareHashAndPassword([]byte(hash), []byte(req.Current)) != nil {
		writeErr(w, 403, "the current password is wrong")
		return
	}
	nh, err := bcrypt.GenerateFromPassword([]byte(req.New), bcrypt.DefaultCost)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	_, _ = s.pool.Exec(ctx, `update merchant_users set password_hash=$2 where id=$1`, id, string(nh))
	_, _ = s.pool.Exec(ctx, `delete from merchant_sessions where merchant_id=$1 and token_hash <> $2`, id, hashToken(bearer(r)))
	writeJSON(w, 200, M{"ok": true})
}

// sendMerchantSetLink emails a one-time link to choose a password. It is used
// for "forgot password" and for inviting a team member to sign in.
func (s *Server) sendMerchantSetLink(ctx context.Context, merchantID, email, name, subject, intro string, ttl time.Duration) {
	tok, err := newToken()
	if err != nil {
		return
	}
	if _, err := s.pool.Exec(ctx, `insert into merchant_password_resets (token_hash, merchant_id, expires_at) values ($1,$2,$3)`, hashToken(tok), merchantID, time.Now().Add(ttl)); err != nil {
		return
	}
	link := strings.TrimRight(s.cfg.WebURL, "/") + "/business/reset?token=" + tok
	body := "Hello " + firstNonEmpty(name, "there") + ",\n\n" + intro + "\n\n" + link + "\n\nThe link works once. If you were not expecting this, ignore this email.\n\nLogaLuxe for business"
	if _, err := s.mail.Send(ctx, email, subject, body); err != nil {
		s.logMailFailure("merchant link", email, err)
	}
}

// POST /v1/m/forgot   {email}   always answers the same way
func (s *Server) mForgot(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Email string `json:"email"`
	}
	_ = readJSON(r, &req)
	ctx := r.Context()
	var id, email, name string
	if err := s.pool.QueryRow(ctx, `select id::text, email, name from merchant_users where lower(email) = lower($1)`, strings.TrimSpace(req.Email)).Scan(&id, &email, &name); err == nil {
		var recent bool
		_ = s.pool.QueryRow(ctx, `select exists(select 1 from merchant_password_resets where merchant_id=$1 and created_at > now() - interval '2 minutes')`, id).Scan(&recent)
		if !recent {
			s.sendMerchantSetLink(ctx, id, email, name, "Reset your LogaLuxe business password", "Someone asked to reset the password for your LogaLuxe business account. Open this link to choose a new one. It works for 30 minutes.", resetTTL)
		}
	}
	writeJSON(w, 200, M{"ok": true})
}

// POST /v1/m/reset   {token, password}
func (s *Server) mReset(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Token    string `json:"token"`
		Password string `json:"password"`
	}
	if err := readJSON(r, &req); err != nil || req.Token == "" {
		writeErr(w, 400, "the link is not valid")
		return
	}
	if len(req.Password) < minPassword || len(req.Password) > 200 {
		writeErr(w, 400, "choose a password of at least 10 characters")
		return
	}
	ctx := r.Context()
	var id string
	if err := s.pool.QueryRow(ctx, `update merchant_password_resets set used_at = now() where token_hash = $1 and used_at is null and expires_at > now() returning merchant_id::text`, hashToken(req.Token)).Scan(&id); err != nil {
		writeErr(w, 400, "this link has expired or was already used; ask for a new one")
		return
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(req.Password), bcrypt.DefaultCost)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	_, _ = s.pool.Exec(ctx, `update merchant_users set password_hash=$2, failed_logins=0, locked_until=null where id=$1`, id, string(hash))
	_, _ = s.pool.Exec(ctx, `delete from merchant_sessions where merchant_id=$1`, id)
	_, _ = s.pool.Exec(ctx, `update merchant_password_resets set used_at = now() where merchant_id=$1 and used_at is null`, id)
	writeJSON(w, 200, M{"ok": true})
}

// ---------- business settings stored as JSON ----------

// Default rules for a business. Stored settings are laid over these, so a new
// rule added here applies to every business that has not chosen otherwise.
func defaultSettings() map[string]map[string]any {
	return map[string]map[string]any{
		"booking":    {"instant": true, "lead_hours": 2, "max_days": 60, "anyone": true, "multi_service": true, "waitlist": true, "on_search": true},
		"policy":     {"cancel_hours": 24, "late_cancel_fee": "deposit", "no_show_fee": "deposit", "new_client_deposit_pct": 0, "prepay_after_no_show": false},
		"storefront": {"show_from": true, "show_durations": true, "show_staff": true, "show_reviews": true, "show_address": true, "show_phone": false, "open_badge": true, "notice": ""},
		"notify":     {"new_booking_email": true, "cancellation_email": true, "daily_summary": false, "low_stock_email": true},
	}
}

func (s *Server) bizSettings(ctx context.Context, businessID string) map[string]map[string]any {
	out := defaultSettings()
	var raw []byte
	if err := s.pool.QueryRow(ctx, `select settings from businesses where id=$1`, businessID).Scan(&raw); err != nil {
		return out
	}
	var stored map[string]map[string]any
	if json.Unmarshal(raw, &stored) == nil {
		for group, vals := range stored {
			if _, ok := out[group]; !ok {
				continue
			}
			for k, v := range vals {
				if _, known := out[group][k]; known {
					out[group][k] = v
				}
			}
		}
	}
	return out
}

func settingInt(v any, def int) int {
	switch n := v.(type) {
	case float64:
		return int(n)
	case int:
		return n
	}
	return def
}

func settingBool(v any, def bool) bool {
	if b, ok := v.(bool); ok {
		return b
	}
	return def
}

// pgconnTag and txExec let helpers run on either the pool or a transaction.
type pgconnTag = interface{ RowsAffected() int64 }

type txExec struct{ tx pgx.Tx }

func (t txExec) Exec(ctx context.Context, sql string, args ...any) (pgconnTag, error) {
	return t.tx.Exec(ctx, sql, args...)
}

type poolExec struct{ s *Server }

func (p poolExec) Exec(ctx context.Context, sql string, args ...any) (pgconnTag, error) {
	return p.s.pool.Exec(ctx, sql, args...)
}

func hashPassword(p string) (string, error) {
	h, err := bcrypt.GenerateFromPassword([]byte(p), bcrypt.DefaultCost)
	return string(h), err
}

// BootstrapMerchants gives each sample business an owner who can sign in:
// <handle>@logaluxe.test with the password from MERCHANT_DEMO_PASSWORD. It
// only fills gaps, and never touches a business that already has an owner.
func BootstrapMerchants(ctx context.Context, pool *pgxpool.Pool, cfg config.Config) error {
	if len(cfg.MerchantDemoPassword) < minPassword {
		return nil
	}
	rs, err := rows(ctx, pool, `select b.id, b.slug, b.owner_name, (select st.id from staff st where st.business_id = b.id and st.role = 'owner' limit 1) as staff_id
		from businesses b where b.slug not like 'zz%' and not exists (select 1 from merchant_members mm where mm.business_id = b.id and mm.role = 'owner')`)
	if err != nil || len(rs) == 0 {
		return err
	}
	hash, err := hashPassword(cfg.MerchantDemoPassword)
	if err != nil {
		return err
	}
	for _, b := range rs {
		email := fmt.Sprint(b["slug"]) + "@logaluxe.test"
		var id string
		if err := pool.QueryRow(ctx, `insert into merchant_users (email, name, password_hash) values ($1,$2,$3)
			on conflict (lower(email)) do update set name = excluded.name returning id::text`, email, firstNonEmpty(fmt.Sprint(b["owner_name"]), "Owner"), hash).Scan(&id); err != nil {
			return err
		}
		if _, err := pool.Exec(ctx, `insert into merchant_members (merchant_id, business_id, role, staff_id) values ($1,$2,'owner',$3) on conflict do nothing`, id, b["id"], b["staff_id"]); err != nil {
			return err
		}
	}
	slog.Info("sample business owners can sign in", "count", len(rs), "example", "ada@logaluxe.test")
	return nil
}

var _ = chi.URLParam
