package httpapi

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"golang.org/x/crypto/bcrypt"
)

// Each admin's own account: password, two-step sign-in, and password reset by email.

const (
	minPassword = 10
	resetTTL    = 30 * time.Minute
)

// selfID returns the signed-in admin's id, or "" for the service token, which has no account.
func (s *Server) selfID(w http.ResponseWriter, r *http.Request) string {
	id := currentAdmin(r).ID
	if id == "" {
		writeErr(w, 400, "the service token has no account; sign in as a person")
	}
	return id
}

// checkSecondStep verifies a six-digit code, or burns a recovery code.
func (s *Server) checkSecondStep(ctx context.Context, adminID, secret, code string) bool {
	if totpValid(secret, code, time.Now()) {
		return true
	}
	clean := strings.ToUpper(strings.TrimSpace(code))
	if len(clean) < 8 {
		return false
	}
	tag, err := s.pool.Exec(ctx, `update admin_users set recovery_codes = array_remove(recovery_codes, $2) where id = $1 and $2 = any(recovery_codes)`, adminID, hashToken(clean))
	return err == nil && tag.RowsAffected() == 1
}

// GET /v1/admin/account
func (s *Server) adminAccount(w http.ResponseWriter, r *http.Request) {
	a := currentAdmin(r)
	out := M{"admin": a, "two_step": false, "recovery_left": 0, "password_changed_at": nil, "mail_mode": s.mail.Mode()}
	if a.ID != "" {
		var enabled bool
		var left int
		var changed *time.Time
		if err := s.pool.QueryRow(r.Context(), `select totp_enabled, coalesce(array_length(recovery_codes,1),0), password_changed_at from admin_users where id=$1`, a.ID).Scan(&enabled, &left, &changed); err == nil {
			out["two_step"], out["recovery_left"], out["password_changed_at"] = enabled, left, changed
		}
	}
	writeJSON(w, 200, out)
}

// POST /v1/admin/password   {current, new}
func (s *Server) adminPassword(w http.ResponseWriter, r *http.Request) {
	id := s.selfID(w, r)
	if id == "" {
		return
	}
	var req struct {
		Current string `json:"current"`
		New     string `json:"new"`
	}
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, "invalid json")
		return
	}
	if len(req.New) < minPassword {
		writeErr(w, 400, "the new password must be at least 10 characters")
		return
	}
	if req.New == req.Current {
		writeErr(w, 400, "the new password must be different from the current one")
		return
	}
	var hash string
	if err := s.pool.QueryRow(r.Context(), `select password_hash from admin_users where id=$1`, id).Scan(&hash); err != nil || bcrypt.CompareHashAndPassword([]byte(hash), []byte(req.Current)) != nil {
		writeErr(w, 403, "the current password is wrong")
		return
	}
	nh, err := bcrypt.GenerateFromPassword([]byte(req.New), bcrypt.DefaultCost)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if _, err := s.pool.Exec(r.Context(), `update admin_users set password_hash=$2, password_changed_at=now(), must_change_password=false where id=$1`, id, string(nh)); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	// Sign out everywhere else; this browser stays in.
	tok := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	_, _ = s.pool.Exec(r.Context(), `delete from admin_sessions where admin_id=$1 and token_hash <> $2`, id, hashToken(tok))
	s.audit(r, "account.password", currentAdmin(r).Email, nil, nil)
	writeJSON(w, 200, M{"ok": true})
}

// POST /v1/admin/2fa/setup   starts setup and returns the key to add to an authenticator app
func (s *Server) admin2FASetup(w http.ResponseWriter, r *http.Request) {
	id := s.selfID(w, r)
	if id == "" {
		return
	}
	var enabled bool
	_ = s.pool.QueryRow(r.Context(), `select totp_enabled from admin_users where id=$1`, id).Scan(&enabled)
	if enabled {
		writeErr(w, 409, "two-step sign-in is already on; turn it off first to set it up again")
		return
	}
	secret, err := newTOTPSecret()
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if _, err := s.pool.Exec(r.Context(), `update admin_users set totp_secret=$2 where id=$1`, id, secret); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, M{"secret": secret, "uri": totpURI(secret, currentAdmin(r).Email)})
}

// POST /v1/admin/2fa/enable   {code}   proves the app works, then returns recovery codes once
func (s *Server) admin2FAEnable(w http.ResponseWriter, r *http.Request) {
	id := s.selfID(w, r)
	if id == "" {
		return
	}
	var req struct {
		Code string `json:"code"`
	}
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, "invalid json")
		return
	}
	var secret *string
	var enabled bool
	_ = s.pool.QueryRow(r.Context(), `select totp_secret, totp_enabled from admin_users where id=$1`, id).Scan(&secret, &enabled)
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
	if _, err := s.pool.Exec(r.Context(), `update admin_users set totp_enabled=true, recovery_codes=$2 where id=$1`, id, hashes); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	s.audit(r, "account.2fa_on", currentAdmin(r).Email, nil, nil)
	writeJSON(w, 200, M{"ok": true, "recovery_codes": codes})
}

// POST /v1/admin/2fa/disable   {password}
func (s *Server) admin2FADisable(w http.ResponseWriter, r *http.Request) {
	id := s.selfID(w, r)
	if id == "" {
		return
	}
	var req struct {
		Password string `json:"password"`
	}
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, "invalid json")
		return
	}
	var hash string
	if err := s.pool.QueryRow(r.Context(), `select password_hash from admin_users where id=$1`, id).Scan(&hash); err != nil || bcrypt.CompareHashAndPassword([]byte(hash), []byte(req.Password)) != nil {
		writeErr(w, 403, "the password is wrong")
		return
	}
	_, _ = s.pool.Exec(r.Context(), `update admin_users set totp_enabled=false, totp_secret=null, recovery_codes='{}' where id=$1`, id)
	s.audit(r, "account.2fa_off", currentAdmin(r).Email, nil, nil)
	writeJSON(w, 200, M{"ok": true})
}

// POST /v1/admin/team/{id}/reset-2fa   super admin: for someone who lost their phone
func (s *Server) adminTeamReset2FA(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var email string
	if err := s.pool.QueryRow(r.Context(), `update admin_users set totp_enabled=false, totp_secret=null, recovery_codes='{}' where id=$1 returning email`, id).Scan(&email); err != nil {
		writeErr(w, 404, "admin not found")
		return
	}
	_, _ = s.pool.Exec(r.Context(), `delete from admin_sessions where admin_id=$1`, id)
	s.audit(r, "team.reset_2fa", email, nil, nil)
	writeJSON(w, 200, M{"ok": true})
}

// POST /v1/admin/forgot   {email}
// Always answers the same way, so it cannot be used to find out who has an account.
func (s *Server) adminForgot(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Email string `json:"email"`
	}
	_ = readJSON(r, &req)
	ctx := r.Context()
	done := func() { writeJSON(w, 200, M{"ok": true}) }

	var id, email, name string
	if err := s.pool.QueryRow(ctx, `select id::text, email, name from admin_users where email = lower($1) and active`, strings.TrimSpace(req.Email)).Scan(&id, &email, &name); err != nil {
		done()
		return
	}
	// One link every two minutes per account is plenty.
	var recent bool
	_ = s.pool.QueryRow(ctx, `select exists(select 1 from admin_password_resets where admin_id=$1 and created_at > now() - interval '2 minutes')`, id).Scan(&recent)
	if recent {
		done()
		return
	}
	tok, err := newToken()
	if err != nil {
		done()
		return
	}
	if _, err := s.pool.Exec(ctx, `insert into admin_password_resets (token_hash, admin_id, expires_at) values ($1,$2,$3)`, hashToken(tok), id, time.Now().Add(resetTTL)); err != nil {
		done()
		return
	}
	link := strings.TrimRight(s.cfg.WebURL, "/") + "/staff/reset?token=" + tok
	body := "Hello " + name + ",\n\nSomeone asked to reset the password for your LogaLuxe console account. Open this link to choose a new one. It works once, for 30 minutes.\n\n" + link +
		"\n\nIf this was not you, ignore this email. Your password has not changed.\n\nLogaXP"
	if _, err := s.mail.Send(ctx, email, "Reset your LogaLuxe console password", body); err != nil {
		// Do not tell the caller; do tell the operator.
		s.logMailFailure("password reset", email, err)
	}
	_, _ = s.pool.Exec(ctx, `insert into audit_log (actor, action, target) values ($1, 'account.reset_requested', $1)`, email)
	done()
}

// POST /v1/admin/reset   {token, password}
func (s *Server) adminReset(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Token    string `json:"token"`
		Password string `json:"password"`
	}
	if err := readJSON(r, &req); err != nil || req.Token == "" {
		writeErr(w, 400, "the reset link is not valid")
		return
	}
	if len(req.Password) < minPassword {
		writeErr(w, 400, "the password must be at least 10 characters")
		return
	}
	ctx := r.Context()
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	defer tx.Rollback(ctx)
	var adminID, email string
	err = tx.QueryRow(ctx, `update admin_password_resets pr set used_at = now() from admin_users a
		where pr.token_hash = $1 and pr.used_at is null and pr.expires_at > now() and a.id = pr.admin_id and a.active
		returning a.id::text, a.email`, hashToken(req.Token)).Scan(&adminID, &email)
	if err != nil {
		writeErr(w, 400, "this reset link has expired or was already used; ask for a new one")
		return
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(req.Password), bcrypt.DefaultCost)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if _, err := tx.Exec(ctx, `update admin_users set password_hash=$2, password_changed_at=now(), must_change_password=false, failed_logins=0, locked_until=null where id=$1`, adminID, string(hash)); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	// Every open session and every other reset link dies with the old password.
	_, _ = tx.Exec(ctx, `delete from admin_sessions where admin_id=$1`, adminID)
	_, _ = tx.Exec(ctx, `update admin_password_resets set used_at = now() where admin_id=$1 and used_at is null`, adminID)
	_, _ = tx.Exec(ctx, `insert into audit_log (actor, action, target) values ($1, 'account.password_reset', $1)`, email)
	if err := tx.Commit(ctx); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, M{"ok": true})
}
