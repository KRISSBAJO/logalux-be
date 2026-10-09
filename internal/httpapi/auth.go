package httpapi

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/crypto/bcrypt"

	"logaluxe/api/internal/config"
)

// Admin is the signed-in console user for one request.
type Admin struct {
	MustChangePassword bool   `json:"must_change_password"`
	ID                 string `json:"id"`
	Email              string `json:"email"`
	Name               string `json:"name"`
	Role               string `json:"role"`
}

type adminKey struct{}

// Roles, weakest to strongest. support reads everything and handles bookings;
// ops decides verification, moderation, disputes, payouts; super_admin owns fees, flags, team.
var roleRank = map[string]int{"support": 1, "ops": 2, "super_admin": 3}

const sessionTTL = 12 * time.Hour

func hashToken(t string) string {
	h := sha256.Sum256([]byte(t))
	return hex.EncodeToString(h[:])
}

func newToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// BootstrapAdmin creates the first super admin from ADMIN_EMAIL / ADMIN_PASSWORD
// when the admin table is empty. It never overwrites an existing account.
func BootstrapAdmin(ctx context.Context, pool *pgxpool.Pool, cfg config.Config) error {
	var n int
	if err := pool.QueryRow(ctx, `select count(*) from admin_users`).Scan(&n); err != nil {
		return err
	}
	if n > 0 {
		return nil
	}
	if cfg.AdminEmail == "" || cfg.AdminPassword == "" {
		slog.Warn("no admin accounts exist; set ADMIN_EMAIL and ADMIN_PASSWORD to create the first one")
		return nil
	}
	if len(cfg.AdminPassword) < 10 {
		return errors.New("ADMIN_PASSWORD must be at least 10 characters")
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(cfg.AdminPassword), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	_, err = pool.Exec(ctx, `insert into admin_users (email, name, role, password_hash) values (lower($1), $2, 'super_admin', $3)`,
		cfg.AdminEmail, strings.Split(cfg.AdminEmail, "@")[0], string(hash))
	if err == nil {
		slog.Info("created first super admin", "email", cfg.AdminEmail)
	}
	return err
}

func currentAdmin(r *http.Request) Admin {
	a, _ := r.Context().Value(adminKey{}).(Admin)
	return a
}

// requireAdmin accepts a console session token, or the static ADMIN_TOKEN for scripts.
func (s *Server) requireAdmin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tok := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if tok == "" {
			writeErr(w, http.StatusUnauthorized, "sign in required")
			return
		}
		var a Admin
		if s.cfg.AdminToken != "" && subtle.ConstantTimeCompare([]byte(tok), []byte(s.cfg.AdminToken)) == 1 {
			a = Admin{ID: "", Email: "service-token", Name: "Service token", Role: "super_admin"}
		} else {
			err := s.pool.QueryRow(r.Context(), `select a.id::text, a.email, a.name, a.role, a.must_change_password from admin_sessions s join admin_users a on a.id = s.admin_id
				where s.token_hash = $1 and s.expires_at > now() and a.active`, hashToken(tok)).Scan(&a.ID, &a.Email, &a.Name, &a.Role, &a.MustChangePassword)
			if err != nil {
				writeErr(w, http.StatusUnauthorized, "session expired, sign in again")
				return
			}
		}
		if a.MustChangePassword && !adminPasswordChangeAllowed(r) {
			writeErr(w, 403, "change your temporary password before using the admin console")
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), adminKey{}, a)))
	})
}

// need blocks admins below the given role.
func (s *Server) need(role string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if roleRank[currentAdmin(r).Role] < roleRank[role] {
				writeErr(w, http.StatusForbidden, "your role cannot do this; it needs "+strings.ReplaceAll(role, "_", " "))
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// A fixed hash to compare against when the email is unknown, so timing does not reveal which emails exist.
var dummyHash, _ = bcrypt.GenerateFromPassword([]byte("not-a-real-password"), bcrypt.DefaultCost)

// POST /v1/admin/login
func (s *Server) adminLogin(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var req struct {
		Email    string `json:"email"`
		Password string `json:"password"`
		Code     string `json:"code"` // six digits from an authenticator app, or a recovery code
	}
	if err := readJSON(r, &req); err != nil || req.Email == "" || req.Password == "" {
		writeErr(w, 400, "email and password are required")
		return
	}
	var a Admin
	var hash string
	var active bool
	var locked *time.Time
	var twoStep bool
	var secret *string
	err := s.pool.QueryRow(ctx, `select id::text, email, name, role, password_hash, active, locked_until, totp_enabled, totp_secret from admin_users where email = lower($1)`, req.Email).
		Scan(&a.ID, &a.Email, &a.Name, &a.Role, &hash, &active, &locked, &twoStep, &secret)
	if err != nil {
		_ = bcrypt.CompareHashAndPassword(dummyHash, []byte(req.Password))
		writeErr(w, http.StatusUnauthorized, "wrong email or password")
		return
	}
	if locked != nil && locked.After(time.Now()) {
		writeErr(w, http.StatusTooManyRequests, "too many attempts, try again in 15 minutes")
		return
	}
	if bcrypt.CompareHashAndPassword([]byte(hash), []byte(req.Password)) != nil || !active {
		_, _ = s.pool.Exec(ctx, `update admin_users set failed_logins = failed_logins + 1,
			locked_until = case when failed_logins + 1 >= 5 then now() + interval '15 minutes' else locked_until end where id = $1`, a.ID)
		writeErr(w, http.StatusUnauthorized, "wrong email or password")
		return
	}
	if twoStep && secret != nil {
		if strings.TrimSpace(req.Code) == "" {
			writeJSON(w, http.StatusUnauthorized, M{"error": "enter the 6-digit code from your authenticator app", "need_code": true})
			return
		}
		if !s.checkSecondStep(ctx, a.ID, *secret, req.Code) {
			// A wrong code counts towards the lockout, like a wrong password.
			_, _ = s.pool.Exec(ctx, `update admin_users set failed_logins = failed_logins + 1,
				locked_until = case when failed_logins + 1 >= 5 then now() + interval '15 minutes' else locked_until end where id = $1`, a.ID)
			writeJSON(w, http.StatusUnauthorized, M{"error": "that code is not right", "need_code": true})
			return
		}
	}
	tok, err := newToken()
	if err != nil {
		writeErr(w, 500, "could not start a session")
		return
	}
	if _, err := s.pool.Exec(ctx, `insert into admin_sessions (token_hash, admin_id, expires_at) values ($1, $2, $3)`, hashToken(tok), a.ID, time.Now().Add(sessionTTL)); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	_, _ = s.pool.Exec(ctx, `update admin_users set failed_logins = 0, locked_until = null, last_login_at = now() where id = $1`, a.ID)
	_, _ = s.pool.Exec(ctx, `delete from admin_sessions where expires_at < now()`)
	_, _ = s.pool.Exec(ctx, `insert into audit_log (actor, action, target) values ($1, 'admin.login', $1)`, a.Email)
	writeJSON(w, 200, M{"token": tok, "expires_in": int(sessionTTL.Seconds()), "admin": a})
}

// POST /v1/admin/logout
func (s *Server) adminLogout(w http.ResponseWriter, r *http.Request) {
	tok := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	_, _ = s.pool.Exec(r.Context(), `delete from admin_sessions where token_hash = $1`, hashToken(tok))
	writeJSON(w, 200, M{"ok": true})
}

// GET /v1/admin/me
func (s *Server) adminMe(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, M{"admin": currentAdmin(r)})
}

// GET /v1/admin/team
func (s *Server) adminTeam(w http.ResponseWriter, r *http.Request) {
	out, err := rows(r.Context(), s.pool, `select id, email, name, role, active, last_login_at, created_at, totp_enabled as two_step,
		(locked_until is not null and locked_until > now()) as locked from admin_users order by created_at`)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, M{"team": out})
}

// POST /v1/admin/team
func (s *Server) adminTeamCreate(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Email              string `json:"email"`
		Name               string `json:"name"`
		Role               string `json:"role"`
		Password           string `json:"password"`
		MustChangePassword bool   `json:"must_change_password"`
	}
	if err := readJSON(r, &req); err != nil || req.Email == "" || req.Name == "" {
		writeErr(w, 400, "email and name are required")
		return
	}
	if roleRank[req.Role] == 0 {
		writeErr(w, 400, "role must be support, ops or super_admin")
		return
	}
	if len(req.Password) < 10 {
		writeErr(w, 400, "password must be at least 10 characters")
		return
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(req.Password), bcrypt.DefaultCost)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	var id string
	err = s.pool.QueryRow(r.Context(), `insert into admin_users (email, name, role, password_hash, must_change_password) values (lower($1), $2, $3, $4, $5) returning id::text`, req.Email, req.Name, req.Role, string(hash), req.MustChangePassword).Scan(&id)
	if err != nil {
		if strings.Contains(err.Error(), "admin_users_email_key") {
			writeErr(w, 409, "an admin with that email already exists")
			return
		}
		writeErr(w, 500, err.Error())
		return
	}
	s.audit(r, "team.create", req.Email, nil, M{"role": req.Role})
	writeJSON(w, 201, M{"ok": true, "id": id})
}

// PUT /v1/admin/team/{id}
func (s *Server) adminTeamUpdate(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id := chi.URLParam(r, "id")
	var req struct {
		Role     *string `json:"role"`
		Active   *bool   `json:"active"`
		Password *string `json:"password"`
	}
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, "invalid json")
		return
	}
	before, err := row(ctx, s.pool, `select email, role, active from admin_users where id = $1`, id)
	if err != nil {
		writeErr(w, 404, "admin not found")
		return
	}
	me := currentAdmin(r)
	losesSuper := (req.Role != nil && *req.Role != "super_admin") || (req.Active != nil && !*req.Active)
	if before["role"] == "super_admin" && losesSuper {
		if me.ID == id {
			writeErr(w, 400, "you cannot demote or disable your own account")
			return
		}
		var others int
		_ = s.pool.QueryRow(ctx, `select count(*) from admin_users where role = 'super_admin' and active and id <> $1`, id).Scan(&others)
		if others == 0 {
			writeErr(w, 400, "this is the last active super admin")
			return
		}
	}
	if req.Role != nil && roleRank[*req.Role] == 0 {
		writeErr(w, 400, "role must be support, ops or super_admin")
		return
	}
	var hash *string
	if req.Password != nil {
		if len(*req.Password) < 10 {
			writeErr(w, 400, "password must be at least 10 characters")
			return
		}
		h, err := bcrypt.GenerateFromPassword([]byte(*req.Password), bcrypt.DefaultCost)
		if err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		hs := string(h)
		hash = &hs
	}
	if _, err := s.pool.Exec(ctx, `update admin_users set role = coalesce($2, role), active = coalesce($3, active),
		password_hash = coalesce($4, password_hash), failed_logins = 0, locked_until = null where id = $1`, id, req.Role, req.Active, hash); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	// A disabled account or a changed password ends that admin's open sessions.
	if (req.Active != nil && !*req.Active) || req.Password != nil {
		_, _ = s.pool.Exec(ctx, `delete from admin_sessions where admin_id = $1`, id)
	}
	s.audit(r, "team.update", before["email"].(string), before, M{"role": req.Role, "active": req.Active, "password_changed": req.Password != nil})
	writeJSON(w, 200, M{"ok": true})
}

func adminPasswordChangeAllowed(r *http.Request) bool {
	return (r.Method == http.MethodGet && r.URL.Path == "/v1/admin/me") || (r.Method == http.MethodPost && (r.URL.Path == "/v1/admin/password" || r.URL.Path == "/v1/admin/logout"))
}
