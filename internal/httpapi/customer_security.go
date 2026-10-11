package httpapi

import (
	"crypto/rand"
	"crypto/subtle"
	"fmt"
	"github.com/jackc/pgx/v5"
	"golang.org/x/crypto/bcrypt"
	"math/big"
	"net/http"
	"strings"
	"time"
)

func (s *Server) customerSecurityOK(r *http.Request) bool {
	var ok bool
	err := s.pool.QueryRow(r.Context(), `select coalesce(security_verified_until>now(),false) from user_sessions where token_hash=$1 and expires_at>now()`, hashToken(bearer(r))).Scan(&ok)
	return err == nil && ok
}

func (s *Server) authSecuritySend(w http.ResponseWriter, r *http.Request) {
	c := currentCustomer(r)
	if !c.PhoneVerified && !c.EmailVerified {
		writeErr(w, 403, "use your password, or verify a contact address first")
		return
	}
	ctx := r.Context()
	n, err := rand.Int(rand.Reader, big.NewInt(1000000))
	if err != nil {
		writeErr(w, 503, "could not create a verification code")
		return
	}
	code := fmt.Sprintf("%06d", n.Int64())
	var id string
	err = s.pool.QueryRow(ctx, `insert into customer_security_codes(session_id,code_hash,expires_at) select id,$2,now()+interval '10 minutes' from user_sessions where token_hash=$1 and expires_at>now() on conflict(session_id) do update set code_hash=excluded.code_hash,expires_at=excluded.expires_at,sent_at=now() where customer_security_codes.sent_at<now()-interval '1 minute' returning session_id::text`, hashToken(bearer(r)), hashToken(code)).Scan(&id)
	if err == pgx.ErrNoRows {
		writeErr(w, 429, "wait one minute before requesting another code")
		return
	}
	if err != nil {
		writeErr(w, 503, "could not save the verification code")
		return
	}
	body := "Your LogaLuxe security code is " + code + ". It works for 10 minutes. Only enter it in LogaLuxe to confirm your account or use store credit. Do not share it."
	via := "email"
	if c.EmailVerified {
		var status string
		status, err = s.mail.Send(ctx, c.Email, "Confirm your LogaLuxe account", body)
		if status == "logged" {
			err = fmt.Errorf("email delivery is unavailable")
		}
	} else {
		via = "text"
		status := s.rawSMS(ctx, phoneMarket(c.Phone), c.Phone, body)
		if status != "delivered" {
			err = fmt.Errorf("the security code could not be delivered")
		}
	}
	if err != nil {
		writeErr(w, 502, "the security code could not be sent; use your password or try again later")
		return
	}
	writeJSON(w, 200, M{"ok": true, "via": via})
}

func (s *Server) authSecurityVerify(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Password string `json:"password"`
		Code     string `json:"code"`
		PIN      string `json:"pin"`
	}
	if readJSON(r, &req) != nil || len(req.Password) > 200 || len(req.Code) > 10 || len(req.PIN) > 6 {
		writeErr(w, 400, "enter your password or the code sent to your verified contact")
		return
	}
	ctx := r.Context()
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		writeErr(w, 503, "could not verify your account")
		return
	}
	defer tx.Rollback(ctx)
	var id string
	var attempts int
	var password *string
	var pinHash *string
	var pinLocked bool
	err = tx.QueryRow(ctx, `select s.id::text,s.security_attempts,u.password_hash,u.security_pin_hash,coalesce(u.security_pin_locked_until>now(),false) from user_sessions s join users u on u.id=s.user_id where s.token_hash=$1 and s.expires_at>now() and u.deleted_at is null for update of s,u`, hashToken(bearer(r))).Scan(&id, &attempts, &password, &pinHash, &pinLocked)
	if err == pgx.ErrNoRows {
		writeErr(w, 401, "sign in again to continue")
		return
	}
	if err != nil {
		writeErr(w, 503, "could not verify your account")
		return
	}
	if attempts >= 5 {
		writeErr(w, 403, "too many verification attempts; sign out and sign in again")
		return
	}
	valid := req.Password != "" && password != nil && bcrypt.CompareHashAndPassword([]byte(*password), []byte(req.Password)) == nil
	method := "password"
	if !valid && req.Code != "" {
		var hash string
		err = tx.QueryRow(ctx, `select code_hash from customer_security_codes where session_id=$1 and expires_at>now() for update`, id).Scan(&hash)
		if err != nil && err != pgx.ErrNoRows {
			writeErr(w, 503, "could not verify your code")
			return
		}
		valid = err == nil && subtle.ConstantTimeCompare([]byte(hash), []byte(hashToken(strings.TrimSpace(req.Code)))) == 1
		method = "code"
	}
	if !valid && req.PIN != "" && req.Password == "" && req.Code == "" {
		if pinLocked {
			writeErr(w, 429, "PIN is locked for 15 minutes; use your password or a verification code")
			return
		}
		valid = pinHash != nil && bcrypt.CompareHashAndPassword([]byte(*pinHash), []byte(req.PIN)) == nil
		method = "pin"
		if !valid {
			_, err = tx.Exec(ctx, `update users set security_pin_attempts=case when security_pin_locked_until<=now() then 1 else security_pin_attempts+1 end,security_pin_locked_until=case when security_pin_locked_until<=now() then null when security_pin_attempts>=4 then now()+interval '15 minutes' else security_pin_locked_until end where id=(select user_id from user_sessions where id=$1)`, id)
			if err != nil || tx.Commit(ctx) != nil {
				writeErr(w, 503, "could not verify your PIN")
				return
			}
			writeErr(w, 403, "the security PIN is not correct")
			return
		}
	}
	if !valid {
		_, err = tx.Exec(ctx, `update user_sessions set security_attempts=security_attempts+1 where id=$1`, id)
		if err != nil || tx.Commit(ctx) != nil {
			writeErr(w, 503, "could not verify your account")
			return
		}
		writeErr(w, 403, "the password or security code is not correct")
		return
	}
	_, err = tx.Exec(ctx, `update user_sessions set security_attempts=0,security_verified_until=now()+interval '5 minutes',security_verified_method=$2 where id=$1`, id, method)
	if err == nil && method == "pin" {
		_, err = tx.Exec(ctx, `update users set security_pin_attempts=0,security_pin_locked_until=null where id=(select user_id from user_sessions where id=$1)`, id)
	}
	if err == nil {
		_, err = tx.Exec(ctx, `delete from customer_security_codes where session_id=$1`, id)
	}
	if err != nil || tx.Commit(ctx) != nil {
		writeErr(w, 503, "could not confirm your account")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, M{"ok": true, "expires_in": int((5 * time.Minute).Seconds())})
}

func (s *Server) authSecurityStatus(w http.ResponseWriter, r *http.Request) {
	var configured bool
	if err := s.pool.QueryRow(r.Context(), `select security_pin_hash is not null from users where id=$1`, currentCustomer(r).ID).Scan(&configured); err != nil {
		writeErr(w, 503, "could not load PIN settings")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, M{"pin_set": configured})
}

func strongSecurityPIN(pin string) bool {
	if len(pin) != 6 || pin == "123456" || pin == "654321" || pin == "012345" || pin == "543210" {
		return false
	}
	repeated := true
	for _, digit := range pin {
		if digit < '0' || digit > '9' {
			return false
		}
		if byte(digit) != pin[0] {
			repeated = false
		}
	}
	return !repeated
}

func (s *Server) authSecurityPIN(w http.ResponseWriter, r *http.Request) {
	var req struct {
		PIN string `json:"pin"`
	}
	if readJSON(r, &req) != nil || !strongSecurityPIN(req.PIN) {
		writeErr(w, 400, "choose six digits; avoid repeated digits and simple sequences")
		return
	}
	ctx := r.Context()
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		writeErr(w, 503, "could not save your PIN")
		return
	}
	defer tx.Rollback(ctx)
	var id string
	var verified bool
	err = tx.QueryRow(ctx, `select id::text,coalesce(security_verified_until>now() and security_verified_method in ('password','code'),false) from user_sessions where token_hash=$1 and expires_at>now() for update`, hashToken(bearer(r))).Scan(&id, &verified)
	if err != nil || !verified {
		writeErr(w, 403, "confirm your account with your password or a verification code before setting or resetting your PIN")
		return
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(req.PIN), bcrypt.DefaultCost)
	if err == nil {
		_, err = tx.Exec(ctx, `update users set security_pin_hash=$2,security_pin_attempts=0,security_pin_locked_until=null where id=(select user_id from user_sessions where id=$1)`, id, string(hash))
	}
	if err == nil {
		_, err = tx.Exec(ctx, `update user_sessions set security_verified_until=null,security_verified_method=null where user_id=(select user_id from user_sessions where id=$1)`, id)
	}
	if err != nil || tx.Commit(ctx) != nil {
		writeErr(w, 503, "could not save your PIN")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, M{"ok": true})
}
