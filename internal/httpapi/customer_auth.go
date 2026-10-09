package httpapi

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"golang.org/x/crypto/bcrypt"

	"logaluxe/api/internal/mail"
)

// Customer accounts. A customer signs in with an email and password and gets
// a session token. Customer tokens and console tokens live in separate tables,
// so one can never be used as the other.

const (
	userSessionTTL  = 30 * 24 * time.Hour
	minUserPassword = 8
)

type userKey struct{}

// Customer is the signed-in customer for one request.
type Customer struct {
	ID        string `json:"id"`
	Email     string `json:"email"`
	FirstName string `json:"first_name"`
	LastName  string `json:"last_name"`
	Phone     string `json:"phone"`
	// False until they open the link we email at sign-up.
	EmailVerified bool `json:"email_verified"`
	// True once they typed a code sent to their number. Only such a number can sign in with a code.
	PhoneVerified bool `json:"phone_verified"`
	// How they want to hear about bookings: whatsapp, sms or email.
	Channel string `json:"preferred_channel"`
	// False for an account made with a phone alone; it can set one under its details.
	HasPassword bool `json:"has_password"`
}

func bearer(r *http.Request) string {
	return strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
}

// customerFrom returns the signed-in customer, or false. It never errors: a
// guest is a normal visitor.
func (s *Server) customerFrom(ctx context.Context, token string) (Customer, bool) {
	var c Customer
	if token == "" {
		return c, false
	}
	err := s.pool.QueryRow(ctx, `select u.id::text, coalesce(u.email,''), u.first_name, u.last_name, coalesce(u.phone,''), u.email_verified_at is not null, u.phone_verified_at is not null, u.preferred_channel, u.password_hash is not null
		from user_sessions s join users u on u.id = s.user_id where s.token_hash = $1 and s.expires_at > now() and u.deleted_at is null`, hashToken(token)).
		Scan(&c.ID, &c.Email, &c.FirstName, &c.LastName, &c.Phone, &c.EmailVerified, &c.PhoneVerified, &c.Channel, &c.HasPassword)
	return c, err == nil
}

// customerID is for routes a guest may also use, such as booking.
func (s *Server) customerID(r *http.Request) *string {
	if c, ok := s.customerFrom(r.Context(), bearer(r)); ok {
		return &c.ID
	}
	return nil
}

func (s *Server) requireCustomer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, ok := s.customerFrom(r.Context(), bearer(r))
		if !ok {
			writeErr(w, http.StatusUnauthorized, "sign in to continue")
			return
		}
		_, _ = s.pool.Exec(r.Context(), `update user_sessions set device_name=$2,last_seen_at=now() where token_hash=$1 and (device_name='' or last_seen_at<now()-interval '5 minutes')`, hashToken(bearer(r)), deviceLabel(r.UserAgent()))
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), userKey{}, c)))
	})
}

func currentCustomer(r *http.Request) Customer {
	c, _ := r.Context().Value(userKey{}).(Customer)
	return c
}

func (s *Server) startUserSession(ctx context.Context, userID string) (string, error) {
	tok, err := newToken()
	if err != nil {
		return "", err
	}
	if _, err := s.pool.Exec(ctx, `insert into user_sessions (token_hash, user_id, expires_at) values ($1,$2,$3)`, hashToken(tok), userID, time.Now().Add(userSessionTTL)); err != nil {
		return "", err
	}
	_, _ = s.pool.Exec(ctx, `delete from user_sessions where expires_at < now()`)
	return tok, nil
}

func cleanPhone(p string) (string, bool) {
	p = strings.TrimSpace(p)
	if p == "" {
		return "", true
	}
	var b strings.Builder
	for i, r := range p {
		switch {
		case r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '+' && i == 0:
			b.WriteRune(r)
		case r == ' ' || r == '-' || r == '(' || r == ')' || r == '.':
		default:
			return "", false
		}
	}
	out := b.String()
	return out, len(strings.TrimPrefix(out, "+")) >= 7 && len(out) <= 16
}

// POST /v1/auth/signup   {first_name, last_name, email, phone, password}
func (s *Server) authSignup(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var req struct {
		FirstName string `json:"first_name"`
		LastName  string `json:"last_name"`
		Email     string `json:"email"`
		Phone     string `json:"phone"`
		Password  string `json:"password"`
		Ref       string `json:"ref"` // a friend's referral code, optional
	}
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, "invalid json")
		return
	}
	req.FirstName, req.LastName = strings.TrimSpace(req.FirstName), strings.TrimSpace(req.LastName)
	req.Email = strings.ToLower(strings.TrimSpace(req.Email))
	phone, phoneOK := cleanPhone(req.Phone)
	switch {
	case req.FirstName == "" || len(req.FirstName) > 60 || len(req.LastName) > 60:
		writeErr(w, 400, "tell us your first name")
		return
	case !mail.Valid(req.Email):
		writeErr(w, 400, "that email address does not look right")
		return
	case !phoneOK:
		writeErr(w, 400, "that phone number does not look right; include the country code, like +1 615 555 0100")
		return
	case len(req.Password) < minUserPassword:
		writeErr(w, 400, "choose a password of at least 8 characters")
		return
	case len(req.Password) > 200:
		writeErr(w, 400, "that password is too long")
		return
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(req.Password), bcrypt.DefaultCost)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	var id string
	err = s.pool.QueryRow(ctx, `insert into users (email, first_name, last_name, phone, password_hash, last_login_at) values ($1,$2,$3,nullif($4,''),$5, now()) returning id::text`,
		req.Email, req.FirstName, req.LastName, phone, string(hash)).Scan(&id)
	if err != nil {
		if isCode(err, "23505") {
			writeErr(w, 409, "there is already an account with that email; sign in instead")
			return
		}
		writeErr(w, 500, err.Error())
		return
	}
	tok, err := s.startUserSession(ctx, id)
	if err != nil {
		writeErr(w, 500, "the account was created, but we could not sign you in; try signing in")
		return
	}
	s.sendVerifyEmail(ctx, id, req.Email, req.FirstName)
	s.noteReferral(ctx, id, req.Ref)
	writeJSON(w, 201, M{"token": tok, "expires_in": int(userSessionTTL.Seconds()), "user": Customer{ID: id, Email: req.Email, FirstName: req.FirstName, LastName: req.LastName, Phone: phone}})
}

// POST /v1/auth/login   {email, password}
func (s *Server) authLogin(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var req struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if err := readJSON(r, &req); err != nil || req.Email == "" || req.Password == "" {
		writeErr(w, 400, "enter your email and password")
		return
	}
	var c Customer
	var hash string
	var locked *time.Time
	err := s.pool.QueryRow(ctx, `select id::text, email, first_name, last_name, coalesce(phone,''), password_hash, locked_until from users where lower(email) = lower($1) and password_hash is not null`, strings.TrimSpace(req.Email)).
		Scan(&c.ID, &c.Email, &c.FirstName, &c.LastName, &c.Phone, &hash, &locked)
	if err != nil {
		_ = bcrypt.CompareHashAndPassword(dummyHash, []byte(req.Password)) // same time whether or not the account exists
		writeErr(w, http.StatusUnauthorized, "wrong email or password")
		return
	}
	if locked != nil && locked.After(time.Now()) {
		writeErr(w, http.StatusTooManyRequests, "too many attempts; try again in 15 minutes, or reset your password")
		return
	}
	if bcrypt.CompareHashAndPassword([]byte(hash), []byte(req.Password)) != nil {
		_, _ = s.pool.Exec(ctx, `update users set failed_logins = failed_logins + 1,
			locked_until = case when failed_logins + 1 >= 8 then now() + interval '15 minutes' else locked_until end where id = $1`, c.ID)
		writeErr(w, http.StatusUnauthorized, "wrong email or password")
		return
	}
	tok, err := s.startUserSession(ctx, c.ID)
	if err != nil {
		writeErr(w, 500, "could not sign you in")
		return
	}
	_, _ = s.pool.Exec(ctx, `update users set failed_logins = 0, locked_until = null, last_login_at = now() where id = $1`, c.ID)
	c.HasPassword = true
	writeJSON(w, 200, M{"token": tok, "expires_in": int(userSessionTTL.Seconds()), "user": c})
}

// POST /v1/auth/logout
func (s *Server) authLogout(w http.ResponseWriter, r *http.Request) {
	_, _ = s.pool.Exec(r.Context(), `delete from user_sessions where token_hash = $1`, hashToken(bearer(r)))
	writeJSON(w, 200, M{"ok": true})
}

// GET /v1/auth/me   the customer, with their bookings and orders
func (s *Server) authMe(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	c := currentCustomer(r)
	if r.URL.Query().Get("brief") == "1" { // the site header only needs a name
		writeJSON(w, 200, M{"user": c})
		return
	}
	page, orderPage := customerPage(r, "page"), customerPage(r, "order_page")
	per := 60
	if r.URL.Query().Get("paged") == "1" {
		per = 12
	}
	scope, q, status := r.URL.Query().Get("scope"), strings.TrimSpace(r.URL.Query().Get("q")), r.URL.Query().Get("status")
	if len(q) > 100 {
		q = q[:100]
	}
	filter := ` and ($2='' or b.name ilike '%'||$2||'%' or exists(select 1 from booking_items bi where bi.booking_id=bk.id and bi.name ilike '%'||$2||'%'))
	and ($3 not in ('upcoming','past') or ($3='upcoming' and bk.status in ('requested','confirmed','checked_in','in_progress') and bk.ends_at>now()) or ($3='past' and not(bk.status in ('requested','confirmed','checked_in','in_progress') and bk.ends_at>now())))`
	var bookingTotal, orderTotal int
	if err := s.pool.QueryRow(ctx, `select count(*) from bookings bk join businesses b on b.id=bk.business_id where bk.user_id=$1`+filter, c.ID, q, scope).Scan(&bookingTotal); err != nil {
		writeErr(w, 500, "could not load booking history")
		return
	}
	if err := s.pool.QueryRow(ctx, `select count(*) from orders o where user_id=$1 and ($2='' or status=$2)`, c.ID, status).Scan(&orderTotal); err != nil {
		writeErr(w, 500, "could not load order history")
		return
	}
	clamp := func(p, total int) int {
		pages := (total + per - 1) / per
		if pages == 0 {
			return 1
		}
		if pages > 0 && p > pages {
			return pages
		}
		return p
	}
	page, orderPage = clamp(page, bookingTotal), clamp(orderPage, orderTotal)
	order := "bk.starts_at desc,bk.id desc"
	if scope == "upcoming" || r.URL.Query().Get("sort") == "oldest" {
		order = "bk.starts_at asc,bk.id asc"
	}
	bookings, err := rows(ctx, s.pool, `
		select bk.id, bk.status, bk.starts_at, bk.ends_at, bk.total_cents, bk.discount_cents, bk.deposit_cents, bk.deposit_paid,
		       b.name as business, b.slug, b.currency, b.timezone, b.tone, st.name as staff, l.address, l.city,
		       coalesce((select sm.id from site_media sm where sm.slot='business' and sm.active and sm.ref=b.slug order by sm.sort,sm.created_at limit 1),(select sm.id from site_media sm where sm.slot='logo' and sm.active and sm.ref=b.slug limit 1)) as photo_id,
		       (select case when p.refunded_cents=p.amount_cents then 'refunded' when exists(select 1 from payment_refund_jobs j where j.payment_id=p.id and j.completed_at is null) or exists(select 1 from payment_refunds rf where rf.payment_id=p.id and rf.status in ('created','sending','unknown')) then 'pending' else '' end from payments p where p.booking_id=bk.id and p.purpose='deposit' order by p.created_at desc limit 1) as refund_state,
		       (select string_agg(name, ', ') from booking_items where booking_id = bk.id) as services,
		       (bk.status in ('requested','confirmed') and bk.starts_at > now()) as can_cancel,
		       bk.is_internal,
		       (bk.status in ('completed','paid') and not bk.is_internal and not customer_business_member(bk.user_id,bk.business_id) and not exists (select 1 from reviews rv where rv.booking_id = bk.id)) as can_review,
		       (select rv.id from reviews rv where rv.booking_id = bk.id limit 1) as review_id, bk.guest_name, bk.series_id,
		       (bk.status in ('paid','completed') and bk.starts_at > now() - interval '30 days') as can_tip,
		       (select coalesce(sum(l.amount_cents),0) from ledger l where l.booking_id = bk.id and l.kind = 'tip')::int as tip_cents,
		       (bk.starts_at < now() and bk.starts_at > now() - interval '14 days' and bk.status not like 'cancelled%' and not exists (select 1 from disputes d where d.booking_id = bk.id)) as can_report,
		       (select json_build_object('ref', d.ref, 'status', d.status, 'outcome', d.outcome, 'outcome_cents', d.outcome_cents, 'decision_note', d.decision_note) from disputes d where d.booking_id = bk.id order by d.created_at desc limit 1) as problem,
		       (select coalesce(json_agg(sm.id order by sm.sort, sm.created_at), '[]') from site_media sm join reviews rv on rv.id::text = sm.ref where sm.slot='review' and rv.booking_id = bk.id) as review_photos
		from bookings bk join businesses b on b.id = bk.business_id join staff st on st.id = bk.staff_id left join locations l on l.id = bk.location_id
		where bk.user_id = $1`+filter+` order by `+order+` limit $4 offset $5`, c.ID, q, scope, per, (page-1)*per)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	orders, err := rows(ctx, s.pool, `
		select o.id, o.status, o.fulfilment, o.total_cents, o.discount_cents, o.gift_cents, o.credit_cents, o.currency, o.created_at,
		       (select string_agg(oi.qty || ' × ' || oi.name, ', ') from order_items oi where oi.order_id = o.id) as items,
		       (select coalesce(json_agg(json_build_object('seller', sh.seller_name, 'fulfilment', sh.fulfilment, 'status', sh.status, 'tracking', sh.tracking) order by sh.seller_name), '[]') from order_shipments sh where sh.order_id = o.id) as shipments,
		       (select p.url from payments p where p.order_id = o.id and p.status = 'pending' order by p.created_at desc limit 1) as pay_url
		from orders o where o.user_id = $1 and ($2='' or status=$2) order by o.created_at desc,o.id desc limit $3 offset $4`, c.ID, status, per, (orderPage-1)*per)
	if err != nil {
		writeErr(w, 500, "could not load order history")
		return
	}
	summary, err := row(ctx, s.pool, `select
	(select count(*) from bookings where user_id=$1 and status in ('requested','confirmed','checked_in','in_progress') and ends_at>now()) as upcoming,
	(select count(*) from orders o where user_id=$1 and (o.status='pending' or exists(select 1 from order_shipments sh where sh.order_id=o.id and sh.status in ('new','ready','shipped')))) as active_orders,
	(select coalesce(sum(unread_client),0) from threads where user_id=$1) as unread,
	(select coalesce(sum(amount_cents),0) from user_credits where user_id=$1 and currency='USD') as credit_cents,
 (select coalesce(sum(amount_cents),0) from user_credits where user_id=$1 and currency='NGN') as credit_ngn_cents,
	(select json_build_object('business',b.name,'slug',b.slug,'starts_at',bk.starts_at,'timezone',b.timezone) from bookings bk join businesses b on b.id=bk.business_id where bk.user_id=$1 and bk.ends_at>now() and bk.status in ('requested','confirmed','checked_in','in_progress') order by bk.starts_at,bk.id limit 1) as next_booking`, c.ID)
	if err != nil {
		writeErr(w, 500, "could not load your account overview")
		return
	}
	writeJSON(w, 200, M{"user": c, "bookings": bookings, "orders": orders, "summary": summary,
		"booking_page": M{"page": page, "total": bookingTotal, "pages": (bookingTotal + per - 1) / per}, "order_page": M{"page": orderPage, "total": orderTotal, "pages": (orderTotal + per - 1) / per}})
}

// PUT /v1/auth/me   {first_name, last_name, phone, email}
// email is taken only from an account that has none yet (one made with a phone alone); it is then confirmed by the usual link.
func (s *Server) authUpdate(w http.ResponseWriter, r *http.Request) {
	var req struct {
		FirstName string `json:"first_name"`
		LastName  string `json:"last_name"`
		Phone     string `json:"phone"`
		Email     string `json:"email"`
	}
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, "invalid json")
		return
	}
	req.FirstName, req.LastName = strings.TrimSpace(req.FirstName), strings.TrimSpace(req.LastName)
	phone, ok := cleanPhone(req.Phone)
	if req.FirstName == "" || len(req.FirstName) > 60 || len(req.LastName) > 60 {
		writeErr(w, 400, "tell us your first name")
		return
	}
	if !ok {
		writeErr(w, 400, "that phone number does not look right; include the country code")
		return
	}
	c := currentCustomer(r)
	if _, err := s.pool.Exec(r.Context(), `update users set first_name=$2, last_name=$3, phone=nullif($4,''),
		phone_verified_at = case when coalesce(phone,'') = $4 then phone_verified_at end where id=$1`, c.ID, req.FirstName, req.LastName, phone); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if email := strings.ToLower(strings.TrimSpace(req.Email)); email != "" && c.Email == "" {
		if !mail.Valid(email) {
			writeErr(w, 400, "that email address does not look right")
			return
		}
		var taken bool
		_ = s.pool.QueryRow(r.Context(), `select exists(select 1 from users where lower(email) = $1 and id <> $2)`, email, c.ID).Scan(&taken)
		if taken {
			writeErr(w, 409, "that email already has an account")
			return
		}
		if _, err := s.pool.Exec(r.Context(), `update users set email=$2, email_verified_at=null where id=$1 and email is null`, c.ID, email); err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		s.sendVerifyEmail(r.Context(), c.ID, email, req.FirstName)
	}
	writeJSON(w, 200, M{"ok": true})
}

// POST /v1/auth/password   {current, new}
func (s *Server) authPassword(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var req struct {
		Current string `json:"current"`
		New     string `json:"new"`
	}
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, "invalid json")
		return
	}
	if len(req.New) < minUserPassword || len(req.New) > 200 {
		writeErr(w, 400, "choose a new password of at least 8 characters")
		return
	}
	c := currentCustomer(r)
	id := c.ID
	var hash *string
	if err := s.pool.QueryRow(ctx, `select password_hash from users where id=$1`, id).Scan(&hash); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	switch {
	case hash == nil && c.Email == "":
		// A password signs in with an email, so the account needs one first.
		writeErr(w, 400, "add an email address to your details first; the password goes with it")
		return
	case hash != nil && bcrypt.CompareHashAndPassword([]byte(*hash), []byte(req.Current)) != nil:
		writeErr(w, 403, "the current password is wrong")
		return
	}
	nh, err := bcrypt.GenerateFromPassword([]byte(req.New), bcrypt.DefaultCost)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	_, _ = s.pool.Exec(ctx, `update users set password_hash=$2 where id=$1`, id, string(nh))
	_, _ = s.pool.Exec(ctx, `delete from user_sessions where user_id=$1 and token_hash <> $2`, id, hashToken(bearer(r)))
	writeJSON(w, 200, M{"ok": true})
}

// POST /v1/auth/bookings/{id}/cancel   a customer cancels their own upcoming booking
// Inside the business's free window the deposit goes back. After it, the
// business keeps the deposit if its policy says so.
func (s *Server) authCancelBooking(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id := chi.URLParam(r, "id")
	var bizID string
	var starts time.Time
	var depositPaid bool
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		writeErr(w, 500, "could not cancel this visit; please retry")
		return
	}
	defer tx.Rollback(ctx)
	if err := tx.QueryRow(ctx, `update bookings set status='cancelled_client', cancel_reason='Cancelled by the client' where id=$1 and user_id=$2 and status in ('requested','confirmed') and starts_at > now()
		returning business_id::text, starts_at, deposit_paid`, id, currentCustomer(r).ID).Scan(&bizID, &starts, &depositPaid); err != nil {
		writeErr(w, 409, "this booking cannot be cancelled here; it may have started, finished, or been cancelled already")
		return
	}
	policy := s.bizSettings(ctx, bizID)["policy"]
	late := time.Until(starts) < time.Duration(settingInt(policy["cancel_hours"], 24))*time.Hour
	kept := false
	refundQueued := false
	if depositPaid {
		if fee, _ := policy["late_cancel_fee"].(string); late && fee != "none" {
			_, err = tx.Exec(ctx, `update ledger set status='pending', settles_at=now() + interval '2 days', description = description || ' (kept, late cancellation)' where booking_id=$1 and kind='deposit' and status='held'`, id)
			kept = true
		} else {
			_, err = tx.Exec(ctx, `delete from ledger where booking_id=$1 and kind='deposit' and status='held'`, id)
			if err == nil {
				_, err = tx.Exec(ctx, `update bookings set deposit_paid=false where id=$1`, id)
			}
			if err == nil {
				tag, e := tx.Exec(ctx, `insert into payment_refund_jobs(payment_id,amount_cents) select id,amount_cents-refunded_cents from payments where booking_id=$1 and purpose='deposit' and status='paid' and amount_cents>refunded_cents on conflict(payment_id) do nothing`, id)
				err = e
				refundQueued = e == nil && tag.RowsAffected() > 0
			}
		}
	}
	if err != nil {
		writeErr(w, 500, "could not finish cancelling; your visit has not changed")
		return
	}
	if err = tx.Commit(ctx); err != nil {
		writeErr(w, 500, "could not confirm cancellation; refresh your account before retrying")
		return
	}
	if depositPaid && !kept {
		s.refundDeposit(id)
	}
	s.notifyBusiness(bizID, "cancellation_email", "A client cancelled a booking", "The booking for "+starts.Format("Monday 2 January at 15:04 MST")+" was cancelled by the client. The time is open again.\n\nOpen your calendar: "+strings.TrimRight(s.cfg.WebURL, "/")+"/business/calendar")
	writeJSON(w, 200, M{"ok": true, "late": late, "deposit_kept": kept, "refund_queued": refundQueued})
}

// POST /v1/auth/forgot   {email}   always answers the same way
func (s *Server) authForgot(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Email string `json:"email"`
	}
	_ = readJSON(r, &req)
	ctx := r.Context()
	done := func() { writeJSON(w, 200, M{"ok": true}) }
	var id, email, name string
	if err := s.pool.QueryRow(ctx, `select id::text, email, first_name from users where lower(email) = lower($1) and password_hash is not null`, strings.TrimSpace(req.Email)).Scan(&id, &email, &name); err != nil {
		done()
		return
	}
	var recent bool
	_ = s.pool.QueryRow(ctx, `select exists(select 1 from user_password_resets where user_id=$1 and created_at > now() - interval '2 minutes')`, id).Scan(&recent)
	if recent {
		done()
		return
	}
	tok, err := newToken()
	if err != nil {
		done()
		return
	}
	if _, err := s.pool.Exec(ctx, `insert into user_password_resets (token_hash, user_id, expires_at) values ($1,$2,$3)`, hashToken(tok), id, time.Now().Add(resetTTL)); err != nil {
		done()
		return
	}
	link := strings.TrimRight(s.cfg.WebURL, "/") + "/reset?token=" + tok
	body := "Hello " + firstNonEmpty(name, "there") + ",\n\nSomeone asked to reset the password for your LogaLuxe account. Open this link to choose a new one. It works once, for 30 minutes.\n\n" + link +
		"\n\nIf this was not you, ignore this email. Your password has not changed.\n\nLogaLuxe"
	if _, err := s.mail.Send(ctx, email, "Reset your LogaLuxe password", body); err != nil {
		s.logMailFailure("customer password reset", email, err)
	}
	done()
}

// POST /v1/auth/reset   {token, password}
func (s *Server) authReset(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Token    string `json:"token"`
		Password string `json:"password"`
	}
	if err := readJSON(r, &req); err != nil || req.Token == "" {
		writeErr(w, 400, "the reset link is not valid")
		return
	}
	if len(req.Password) < minUserPassword || len(req.Password) > 200 {
		writeErr(w, 400, "choose a password of at least 8 characters")
		return
	}
	ctx := r.Context()
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	defer tx.Rollback(ctx)
	var id string
	if err := tx.QueryRow(ctx, `update user_password_resets set used_at = now() where token_hash = $1 and used_at is null and expires_at > now() returning user_id::text`, hashToken(req.Token)).Scan(&id); err != nil {
		writeErr(w, 400, "this reset link has expired or was already used; ask for a new one")
		return
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(req.Password), bcrypt.DefaultCost)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	_, _ = tx.Exec(ctx, `update users set password_hash=$2, failed_logins=0, locked_until=null, email_verified_at = coalesce(email_verified_at, now()) where id=$1`, id, string(hash))
	_, _ = tx.Exec(ctx, `delete from user_sessions where user_id=$1`, id)
	_, _ = tx.Exec(ctx, `update user_password_resets set used_at = now() where user_id=$1 and used_at is null`, id)
	if err := tx.Commit(ctx); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, M{"ok": true})
}
