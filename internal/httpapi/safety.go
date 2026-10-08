package httpapi

import (
	"context"
	"crypto/subtle"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/bcrypt"
)

// ---------- request limits ----------
//
// Each account already locks for 15 minutes after repeated wrong passwords. These limits are the
// other half: they cap how often one connection may try the doors that cost us something or can be
// guessed at (signing in, signing up, reset emails, promo and gift codes, bookings, orders).
// Counts are kept in memory, per API process, which is enough to blunt a script.

type limitBucket struct {
	n     int
	until time.Time
}

type limiter struct {
	mu      sync.Mutex
	buckets map[string]*limitBucket
	swept   time.Time
}

func newLimiter() *limiter { return &limiter{buckets: map[string]*limitBucket{}, swept: time.Now()} }

// allow counts one try and reports whether it is within the limit, and how long until the count starts again.
func (l *limiter) allow(key string, max int, window time.Duration, now time.Time) (bool, time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if now.Sub(l.swept) > 10*time.Minute {
		for k, b := range l.buckets {
			if now.After(b.until) {
				delete(l.buckets, k)
			}
		}
		l.swept = now
	}
	b := l.buckets[key]
	if b == nil || now.After(b.until) {
		b = &limitBucket{until: now.Add(window)}
		l.buckets[key] = b
	}
	b.n++
	return b.n <= max, b.until.Sub(now)
}

// visitorIP is the address of the person, not of our own web server. The web app passes it on in
// X-Visitor-IP; it is believed only with the shared WEB_API_KEY (or, with no key set, outside production).
func (s *Server) visitorIP(r *http.Request) string {
	if v := strings.TrimSpace(strings.Split(r.Header.Get("X-Visitor-IP"), ",")[0]); v != "" && net.ParseIP(v) != nil {
		if s.cfg.WebAPIKey != "" {
			if subtle.ConstantTimeCompare([]byte(r.Header.Get("X-Web-Key")), []byte(s.cfg.WebAPIKey)) == 1 {
				return v
			}
		} else if s.cfg.Env != "production" {
			return v
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// limit allows max requests per window from one connection to the routes it wraps.
func (s *Server) limit(name string, max int, window time.Duration) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if s.cfg.RateLimits {
				if ok, wait := s.limits.allow(name+"|"+s.visitorIP(r), max, window, time.Now()); !ok {
					w.Header().Set("Retry-After", strconv.Itoa(int(wait.Seconds())+1))
					mins := int(wait.Minutes()) + 1
					writeErr(w, http.StatusTooManyRequests, "too many tries from this connection; wait "+strconv.Itoa(mins)+" min and try again")
					return
				}
			}
			next.ServeHTTP(w, r)
		})
	}
}

// ---------- customers confirm their email ----------

const verifyTTL = 48 * time.Hour

// sendVerifyEmail emails a confirmation link, at most one every two minutes per account.
func (s *Server) sendVerifyEmail(ctx context.Context, userID, email, name string) (sent bool) {
	if email == "" {
		return false
	}
	var recent bool
	_ = s.pool.QueryRow(ctx, `select exists(select 1 from user_email_tokens where user_id=$1 and created_at > now() - interval '2 minutes')`, userID).Scan(&recent)
	if recent {
		return false
	}
	tok, err := newToken()
	if err != nil {
		return false
	}
	if _, err := s.pool.Exec(ctx, `insert into user_email_tokens (token_hash, user_id, email, expires_at) values ($1,$2,lower($3),$4)`, hashToken(tok), userID, email, time.Now().Add(verifyTTL)); err != nil {
		return false
	}
	link := strings.TrimRight(s.cfg.WebURL, "/") + "/verify?token=" + url.QueryEscape(tok)
	body := "Hello " + firstNonEmpty(name, "there") + ",\n\nPlease confirm this is your email address for LogaLuxe. Open this link. It works for 48 hours.\n\n" + link +
		"\n\nConfirming lets you leave reviews, and it is where we send your booking and order emails.\n\nIf you did not make a LogaLuxe account, ignore this email and nothing happens.\n\nLogaLuxe"
	if _, err := s.mail.Send(ctx, email, "Confirm your email for LogaLuxe", body); err != nil {
		s.logMailFailure("customer email confirmation", email, err)
		return false
	}
	return true
}

// POST /v1/auth/verify   {token}   the link in the confirmation email. No sign-in needed: the link is the proof.
func (s *Server) authVerify(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Token string `json:"token"`
	}
	if err := readJSON(r, &req); err != nil || strings.TrimSpace(req.Token) == "" {
		writeErr(w, 400, "this confirmation link is not complete; open it again from the email")
		return
	}
	ctx := r.Context()
	var userID, email string
	var used *time.Time
	var expires time.Time
	if err := s.pool.QueryRow(ctx, `select user_id::text, email, used_at, expires_at from user_email_tokens where token_hash=$1`, hashToken(strings.TrimSpace(req.Token))).Scan(&userID, &email, &used, &expires); err != nil {
		writeErr(w, 400, "this confirmation link is not valid; ask for a new one from your account")
		return
	}
	if used != nil {
		writeJSON(w, 200, M{"ok": true, "email": email, "already": true})
		return
	}
	if expires.Before(time.Now()) {
		writeErr(w, 410, "this confirmation link has expired; ask for a new one from your account")
		return
	}
	// The link confirms the address it was sent to, and only while the account still uses it.
	tag, err := s.pool.Exec(ctx, `update users set email_verified_at = now() where id=$1 and lower(email) = $2`, userID, email)
	if err != nil || tag.RowsAffected() == 0 {
		writeErr(w, 409, "the email on this account has changed since this link was sent; ask for a new one from your account")
		return
	}
	_, _ = s.pool.Exec(ctx, `update user_email_tokens set used_at = now() where user_id=$1 and used_at is null`, userID)
	writeJSON(w, 200, M{"ok": true, "email": email})
}

// POST /v1/auth/verify/send   sends the confirmation email again
func (s *Server) authVerifySend(w http.ResponseWriter, r *http.Request) {
	c := currentCustomer(r)
	if c.EmailVerified {
		writeJSON(w, 200, M{"ok": true, "already": true})
		return
	}
	if !s.sendVerifyEmail(r.Context(), c.ID, c.Email, c.FirstName) {
		writeErr(w, 429, "we sent one a moment ago; check your inbox and spam folder, or try again in two minutes")
		return
	}
	writeJSON(w, 200, M{"ok": true, "email": c.Email})
}

// needVerified stops an action that speaks in public (a review) until the email is confirmed.
func needVerified(w http.ResponseWriter, c Customer) bool {
	if c.EmailVerified {
		return true
	}
	writeJSON(w, http.StatusForbidden, M{"error": "confirm your email first; we sent you a link, and you can ask for a new one in your account", "need_verify": true})
	return false
}

// ---------- two-step sign-in for people who run a business ----------

func merchantTOTPURI(secret, email string) string {
	return "otpauth://totp/" + url.PathEscape("LogaLuxe for business:"+email) + "?secret=" + secret + "&issuer=" + url.QueryEscape("LogaLuxe for business") + "&algorithm=SHA1&digits=6&period=30"
}

// checkMerchantSecondStep verifies a six-digit code, or burns a recovery code.
func (s *Server) checkMerchantSecondStep(ctx context.Context, merchantID, secret, code string) bool {
	if totpValid(secret, code, time.Now()) {
		return true
	}
	clean := strings.ToUpper(strings.TrimSpace(code))
	if len(clean) < 8 {
		return false
	}
	tag, err := s.pool.Exec(ctx, `update merchant_users set recovery_codes = array_remove(recovery_codes, $2) where id = $1 and $2 = any(recovery_codes)`, merchantID, hashToken(clean))
	return err == nil && tag.RowsAffected() == 1
}

// GET /v1/m/security   how this person signs in
func (s *Server) mSecurity(w http.ResponseWriter, r *http.Request) {
	var enabled bool
	var left int
	var last *time.Time
	_ = s.pool.QueryRow(r.Context(), `select totp_enabled, coalesce(array_length(recovery_codes,1),0), last_login_at from merchant_users where id=$1`, mc(r).ID).Scan(&enabled, &left, &last)
	sessions := 0
	_ = s.pool.QueryRow(r.Context(), `select count(*) from merchant_sessions where merchant_id=$1 and expires_at > now()`, mc(r).ID).Scan(&sessions)
	writeJSON(w, 200, M{"two_step": enabled, "recovery_left": left, "last_login_at": last, "sessions": sessions, "email": mc(r).Email})
}

// POST /v1/m/2fa/setup   starts setup and returns the key to add to an authenticator app
func (s *Server) m2FASetup(w http.ResponseWriter, r *http.Request) {
	m := mc(r)
	var enabled bool
	_ = s.pool.QueryRow(r.Context(), `select totp_enabled from merchant_users where id=$1`, m.ID).Scan(&enabled)
	if enabled {
		writeErr(w, 409, "two-step sign-in is already on; turn it off first to set it up again")
		return
	}
	secret, err := newTOTPSecret()
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if _, err := s.pool.Exec(r.Context(), `update merchant_users set totp_secret=$2 where id=$1`, m.ID, secret); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, M{"secret": secret, "uri": merchantTOTPURI(secret, m.Email)})
}

// POST /v1/m/2fa/enable   {code}   proves the app works, then returns recovery codes once
func (s *Server) m2FAEnable(w http.ResponseWriter, r *http.Request) {
	m := mc(r)
	var req struct {
		Code string `json:"code"`
	}
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, "invalid json")
		return
	}
	var secret *string
	var enabled bool
	_ = s.pool.QueryRow(r.Context(), `select totp_secret, totp_enabled from merchant_users where id=$1`, m.ID).Scan(&secret, &enabled)
	if enabled {
		writeErr(w, 409, "two-step sign-in is already on")
		return
	}
	if secret == nil || !totpValid(*secret, req.Code, time.Now()) {
		writeErr(w, 400, "that code is not right; check the time on your phone and try the newest code")
		return
	}
	codes, hashes := make([]string, 8), make([]string, 8)
	for i := range codes {
		c, err := randomCode(2, 5)
		if err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		codes[i], hashes[i] = c, hashToken(c)
	}
	if _, err := s.pool.Exec(r.Context(), `update merchant_users set totp_enabled=true, recovery_codes=$2 where id=$1`, m.ID, hashes); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	// Anyone else signed in as this person is signed out; this session stays.
	_, _ = s.pool.Exec(r.Context(), `delete from merchant_sessions where merchant_id=$1 and token_hash <> $2`, m.ID, hashToken(bearer(r)))
	writeJSON(w, 200, M{"ok": true, "recovery_codes": codes})
}

// POST /v1/m/2fa/disable   {password}
func (s *Server) m2FADisable(w http.ResponseWriter, r *http.Request) {
	m := mc(r)
	var req struct {
		Password string `json:"password"`
	}
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, "invalid json")
		return
	}
	var hash string
	if err := s.pool.QueryRow(r.Context(), `select password_hash from merchant_users where id=$1`, m.ID).Scan(&hash); err != nil || bcrypt.CompareHashAndPassword([]byte(hash), []byte(req.Password)) != nil {
		writeErr(w, 403, "the password is wrong")
		return
	}
	_, _ = s.pool.Exec(r.Context(), `update merchant_users set totp_enabled=false, totp_secret=null, recovery_codes='{}' where id=$1`, m.ID)
	writeJSON(w, 200, M{"ok": true})
}

// POST /v1/admin/merchants/reset-2fa   {email}   super admin: for a business person who lost their phone and their recovery codes
func (s *Server) adminMerchantReset2FA(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Email string `json:"email"`
	}
	if err := readJSON(r, &req); err != nil || strings.TrimSpace(req.Email) == "" {
		writeErr(w, 400, "give the email they sign in with")
		return
	}
	var id, email string
	var was bool
	if err := s.pool.QueryRow(r.Context(), `select id::text, email, totp_enabled from merchant_users where lower(email) = lower($1)`, strings.TrimSpace(req.Email)).Scan(&id, &email, &was); err != nil {
		writeErr(w, 404, "no business sign-in uses that email")
		return
	}
	if !was {
		writeErr(w, 409, "two-step sign-in is not on for that person")
		return
	}
	_, _ = s.pool.Exec(r.Context(), `update merchant_users set totp_enabled=false, totp_secret=null, recovery_codes='{}' where id=$1`, id)
	_, _ = s.pool.Exec(r.Context(), `delete from merchant_sessions where merchant_id=$1`, id)
	s.audit(r, "merchant.reset_2fa", email, nil, nil)
	writeJSON(w, 200, M{"ok": true, "email": email})
}
