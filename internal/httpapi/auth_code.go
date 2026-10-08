package httpapi

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"strings"
	"time"

	"logaluxe/api/internal/mail"
)

// Signing in with a code sent to a phone, and confirming the number on an account.
// Only live when an admin has switched "Sign in with a texted code" on.

const (
	codeLife     = 10 * time.Minute
	codeTries    = 5 // wrong guesses allowed against one code
	codesPerTen  = 3 // codes one number may ask for in ten minutes
	codeNotReady = "signing in with a code is not switched on; use your email and password"
)

var errTooManyCodes = errors.New("too many codes were sent to that number; wait ten minutes and try again")

func codeHash(phone, code string) string {
	sum := sha256.Sum256([]byte(phone + "|" + code))
	return hex.EncodeToString(sum[:])
}

// sendCode makes a 6-digit code for a phone and sends it. channel is "sms" or "whatsapp".
// It answers "delivered" or "logged" (texts are off for this number, or it is a number that is never texted).
func (s *Server) sendCode(ctx context.Context, phone, purpose, channel string) (string, error) {
	var recent int
	_ = s.pool.QueryRow(ctx, `select count(*) from login_codes where phone=$1 and created_at > now() - interval '10 minutes'`, phone).Scan(&recent)
	if recent >= codesPerTen {
		return "", errTooManyCodes
	}
	n, err := rand.Int(rand.Reader, big.NewInt(1000000))
	if err != nil {
		return "", err
	}
	code := fmt.Sprintf("%06d", n.Int64())
	// An older code stops working, but stays counted so one number cannot ask for codes without end.
	_, _ = s.pool.Exec(ctx, `update login_codes set expires_at = now() where phone=$1 and purpose=$2 and expires_at > now()`, phone, purpose)
	if _, err := s.pool.Exec(ctx, `insert into login_codes (phone, purpose, code_hash, expires_at) values ($1,$2,$3,$4)`, phone, purpose, codeHash(phone, code), time.Now().Add(codeLife)); err != nil {
		return "", err
	}
	_, _ = s.pool.Exec(ctx, `delete from login_codes where expires_at < now() - interval '1 day'`)
	body := "Your LogaLuxe code is " + code + ". It works for 10 minutes. Do not share it with anyone."
	status := "logged"
	if channel == "whatsapp" && s.whatsappMode() == "live" {
		status = s.sendWhatsApp(ctx, phone, body)
	} else {
		status = s.rawSMS(ctx, phoneMarket(phone), phone, body)
	}
	if status == "failed" {
		return "", errors.New("the code could not be sent; check the number and try again")
	}
	return status, nil
}

// checkCode says whether the code is the one sent to this phone. A right code is used up when consume is true.
func (s *Server) checkCode(ctx context.Context, phone, purpose, code string, consume bool) (bool, string) {
	var id, hash string
	var attempts int
	if err := s.pool.QueryRow(ctx, `select id::text, code_hash, attempts from login_codes where phone=$1 and purpose=$2 and expires_at > now() order by created_at desc limit 1`, phone, purpose).
		Scan(&id, &hash, &attempts); err != nil {
		return false, "that code has run out; ask for a new one"
	}
	if attempts >= codeTries {
		return false, "too many wrong codes; ask for a new one"
	}
	if subtle.ConstantTimeCompare([]byte(hash), []byte(codeHash(phone, strings.TrimSpace(code)))) != 1 {
		_, _ = s.pool.Exec(ctx, `update login_codes set attempts = attempts + 1 where id=$1`, id)
		return false, "that code is not right"
	}
	if consume {
		_, _ = s.pool.Exec(ctx, `update login_codes set expires_at = now() where id=$1`, id)
	}
	return true, ""
}

func strictPhone(raw string) (string, bool) {
	p, ok := cleanPhone(raw)
	return p, ok && strings.HasPrefix(p, "+")
}

// POST /v1/auth/code/send   {phone, channel: "sms" | "whatsapp"}
func (s *Server) authCodeSend(w http.ResponseWriter, r *http.Request) {
	if !s.featureOn("sms_login") {
		writeErr(w, http.StatusNotFound, codeNotReady)
		return
	}
	var req struct {
		Phone   string `json:"phone"`
		Channel string `json:"channel"`
	}
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, "invalid json")
		return
	}
	phone, ok := strictPhone(req.Phone)
	if !ok {
		writeErr(w, 400, "enter your mobile number with the country code, like +1 615 555 0100")
		return
	}
	status, err := s.sendCode(r.Context(), phone, "login", req.Channel)
	if err != nil {
		code := 502
		if errors.Is(err, errTooManyCodes) {
			code = http.StatusTooManyRequests
		}
		writeErr(w, code, err.Error())
		return
	}
	writeJSON(w, 200, M{"ok": true, "sent": status, "expires_in": int(codeLife.Seconds())})
}

// POST /v1/auth/code/verify   {phone, code, first_name, last_name, email, ref}
// A number that is confirmed on an account signs that person in. A number nobody has makes a new
// account: the first answer is 409 with need "name", and the same code is sent again with a name.
func (s *Server) authCodeVerify(w http.ResponseWriter, r *http.Request) {
	if !s.featureOn("sms_login") {
		writeErr(w, http.StatusNotFound, codeNotReady)
		return
	}
	ctx := r.Context()
	var req struct {
		Phone     string `json:"phone"`
		Code      string `json:"code"`
		FirstName string `json:"first_name"`
		LastName  string `json:"last_name"`
		Email     string `json:"email"`
		Ref       string `json:"ref"`
	}
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, "invalid json")
		return
	}
	phone, ok := strictPhone(req.Phone)
	if !ok {
		writeErr(w, 400, "enter your mobile number with the country code")
		return
	}
	// Whose number is it? An account that confirmed it comes first.
	var c Customer
	var confirmed bool
	err := s.pool.QueryRow(ctx, `select id::text, coalesce(email,''), first_name, last_name, coalesce(phone,''), email_verified_at is not null, phone_verified_at is not null
		from users where phone=$1 order by (phone_verified_at is not null) desc, created_at limit 1`, phone).
		Scan(&c.ID, &c.Email, &c.FirstName, &c.LastName, &c.Phone, &c.EmailVerified, &confirmed)
	found := err == nil
	if found && !confirmed {
		// Someone typed this number at sign-up and never proved it is theirs. A code alone must not open that account.
		if ok, why := s.checkCode(ctx, phone, "login", req.Code, false); !ok {
			writeErr(w, http.StatusUnauthorized, why)
			return
		}
		writeJSON(w, http.StatusConflict, M{"error": "this number is on an account that has not confirmed it yet; sign in with your email and password, then confirm the number under your details", "need": "password"})
		return
	}
	if found {
		if ok, why := s.checkCode(ctx, phone, "login", req.Code, true); !ok {
			writeErr(w, http.StatusUnauthorized, why)
			return
		}
	} else {
		req.FirstName, req.LastName = strings.TrimSpace(req.FirstName), strings.TrimSpace(req.LastName)
		req.Email = strings.ToLower(strings.TrimSpace(req.Email))
		if req.FirstName == "" {
			if ok, why := s.checkCode(ctx, phone, "login", req.Code, false); !ok {
				writeErr(w, http.StatusUnauthorized, why)
				return
			}
			writeJSON(w, http.StatusConflict, M{"error": "tell us your name to finish", "need": "name"})
			return
		}
		switch {
		case len(req.FirstName) > 60 || len(req.LastName) > 60:
			writeErr(w, 400, "that name is too long")
			return
		case req.Email != "" && !mail.Valid(req.Email):
			writeErr(w, 400, "that email address does not look right")
			return
		}
		if req.Email != "" {
			var taken bool
			_ = s.pool.QueryRow(ctx, `select exists(select 1 from users where lower(email) = $1)`, req.Email).Scan(&taken)
			if taken {
				writeErr(w, http.StatusConflict, "that email already has an account; leave it out, or sign in with your email and password")
				return
			}
		}
		if ok, why := s.checkCode(ctx, phone, "login", req.Code, true); !ok {
			writeErr(w, http.StatusUnauthorized, why)
			return
		}
		if err := s.pool.QueryRow(ctx, `insert into users (email, first_name, last_name, phone, phone_verified_at, preferred_channel, last_login_at) values (nullif($1,''),$2,$3,$4, now(), 'sms', now()) returning id::text`,
			req.Email, req.FirstName, req.LastName, phone).Scan(&c.ID); err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		c.Email, c.FirstName, c.LastName, c.Phone = req.Email, req.FirstName, req.LastName, phone
		if req.Email != "" {
			s.sendVerifyEmail(ctx, c.ID, req.Email, req.FirstName)
		}
		s.noteReferral(ctx, c.ID, req.Ref)
	}
	c.PhoneVerified = true
	tok, err := s.startUserSession(ctx, c.ID)
	if err != nil {
		writeErr(w, 500, "could not sign you in")
		return
	}
	_, _ = s.pool.Exec(ctx, `update users set failed_logins = 0, locked_until = null, last_login_at = now() where id = $1`, c.ID)
	writeJSON(w, 200, M{"token": tok, "expires_in": int(userSessionTTL.Seconds()), "user": c, "new": !found})
}

// POST /v1/auth/phone/send   {channel}   signed in: a code to the number on the account
func (s *Server) authPhoneSend(w http.ResponseWriter, r *http.Request) {
	if !s.featureOn("sms_login") {
		writeErr(w, http.StatusNotFound, "confirming a number by code is not switched on")
		return
	}
	var req struct {
		Channel string `json:"channel"`
	}
	_ = readJSON(r, &req)
	phone, ok := strictPhone(currentCustomer(r).Phone)
	if !ok {
		writeErr(w, 400, "add your mobile number with the country code first, like +1 615 555 0100")
		return
	}
	status, err := s.sendCode(r.Context(), phone, "verify", req.Channel)
	if err != nil {
		code := 502
		if errors.Is(err, errTooManyCodes) {
			code = http.StatusTooManyRequests
		}
		writeErr(w, code, err.Error())
		return
	}
	writeJSON(w, 200, M{"ok": true, "sent": status, "expires_in": int(codeLife.Seconds())})
}

// POST /v1/auth/phone/verify   {code}
func (s *Server) authPhoneVerify(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Code string `json:"code"`
	}
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, "invalid json")
		return
	}
	ctx := r.Context()
	c := currentCustomer(r)
	phone, ok := strictPhone(c.Phone)
	if !ok {
		writeErr(w, 400, "add your mobile number first")
		return
	}
	if ok, why := s.checkCode(ctx, phone, "verify", req.Code, true); !ok {
		writeErr(w, http.StatusUnauthorized, why)
		return
	}
	// The number now belongs to this account alone: nobody else can sign in with it.
	_, _ = s.pool.Exec(ctx, `update users set phone_verified_at = null where phone=$1 and id <> $2`, phone, c.ID)
	if _, err := s.pool.Exec(ctx, `update users set phone_verified_at = now() where id=$1`, c.ID); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, M{"ok": true})
}

// PUT /v1/auth/channel   {channel: "whatsapp" | "sms" | "email"}   how the person wants to hear about bookings
func (s *Server) authChannel(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Channel string `json:"channel"`
	}
	if err := readJSON(r, &req); err != nil || (req.Channel != "whatsapp" && req.Channel != "sms" && req.Channel != "email") {
		writeErr(w, 400, "choose WhatsApp, text or email")
		return
	}
	if _, err := s.pool.Exec(r.Context(), `update users set preferred_channel=$2 where id=$1`, currentCustomer(r).ID, req.Channel); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, M{"ok": true})
}
