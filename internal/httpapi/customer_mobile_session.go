package httpapi

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"github.com/jackc/pgx/v5"
	"net/http"
	"time"
)

const mobileAccessTTL = 15 * time.Minute
const mobileIdleTTL = 90 * 24 * time.Hour
const mobileAbsoluteTTL = 180 * 24 * time.Hour

func mobileRetryToken(secret, request, purpose string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte("logaluxe-mobile:" + purpose + ":" + request))
	return hex.EncodeToString(mac.Sum(nil))
}

// Logout also works after the short access token expires. It accepts the
// refresh secret only in the body; a hash identifies the device to revoke.
func (s *Server) authMobileLogout(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Refresh string `json:"refresh_token"`
	}
	if readJSON(r, &req) != nil || len(req.Refresh) > 256 {
		writeErr(w, 400, "invalid sign-out request")
		return
	}
	_, err := s.pool.Exec(r.Context(), `delete from user_sessions where id in (select session_id from customer_refresh_tokens where token_hash=$1)`, hashToken(req.Refresh))
	if err != nil {
		writeErr(w, 503, "could not sign out this device; please retry")
		return
	}
	writeJSON(w, 200, M{"ok": true})
}

// Upgrade a normal, freshly authenticated session. Browser and staff sessions
// retain their existing policies. Used refresh hashes stay until family removal
// so a replay can invalidate the whole device, including its access credential.
func (s *Server) authMobileSession(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		writeErr(w, 503, "could not secure your sign-in; please retry")
		return
	}
	defer tx.Rollback(ctx)
	var id string
	err = tx.QueryRow(ctx, `select id::text from user_sessions where token_hash=$1 and user_id=$2 and expires_at>now() for update`, hashToken(bearer(r)), currentCustomer(r).ID).Scan(&id)
	if err != nil && err != pgx.ErrNoRows {
		writeErr(w, 503, "could not secure your sign-in; please retry")
		return
	}
	if err != nil {
		writeErr(w, 401, "sign in to continue")
		return
	}
	var exists bool
	if err = tx.QueryRow(ctx, `select exists(select 1 from customer_refresh_tokens where session_id=$1)`, id).Scan(&exists); err != nil {
		writeErr(w, 503, "could not secure your sign-in")
		return
	}
	if exists {
		writeErr(w, 409, "this device already has a mobile sign-in")
		return
	}
	refresh, err := newToken()
	if err != nil {
		writeErr(w, 503, "could not secure your sign-in")
		return
	}
	now := time.Now()
	_, err = tx.Exec(ctx, `insert into customer_refresh_tokens(token_hash,session_id,idle_expires_at,absolute_expires_at) values($1,$2,$3,$4)`, hashToken(refresh), id, now.Add(mobileIdleTTL), now.Add(mobileAbsoluteTTL))
	if err == nil {
		_, err = tx.Exec(ctx, `update user_sessions set expires_at=$2 where id=$1`, id, now.Add(mobileAccessTTL))
	}
	if err != nil || tx.Commit(ctx) != nil {
		writeErr(w, 503, "could not secure your sign-in")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, M{"token": bearer(r), "refresh_token": refresh, "session_id": id, "expires_in": int(mobileAccessTTL.Seconds())})
}

func (s *Server) authMobileRefresh(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Refresh string `json:"refresh_token"`
		Request string `json:"request_id"`
	}
	if readJSON(r, &req) != nil || len(req.Refresh) < 32 || len(req.Refresh) > 256 || (req.Request != "" && len(req.Request) != 36) {
		writeErr(w, 401, "your sign-in expired; sign in again")
		return
	}
	ctx := r.Context()
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		writeErr(w, 503, "could not reconnect; your sign-in has been kept")
		return
	}
	defer tx.Rollback(ctx)
	// Lock the session first to serialize simultaneous refreshes and revocations.
	var id, oldHash string
	err = tx.QueryRow(ctx, `select s.id::text,s.token_hash from user_sessions s join customer_refresh_tokens t on t.session_id=s.id join users u on u.id=s.user_id where t.token_hash=$1 and u.deleted_at is null for update of s`, hashToken(req.Refresh)).Scan(&id, &oldHash)
	if err == pgx.ErrNoRows {
		writeErr(w, 401, "your sign-in expired; sign in again")
		return
	}
	if err != nil {
		writeErr(w, 503, "could not reconnect; your sign-in has been kept")
		return
	}
	var used *time.Time
	var idle, absolute time.Time
	var requestHash *string
	if err = tx.QueryRow(ctx, `select used_at,idle_expires_at,absolute_expires_at,refresh_request_hash from customer_refresh_tokens where token_hash=$1`, hashToken(req.Refresh)).Scan(&used, &idle, &absolute, &requestHash); err != nil {
		writeErr(w, 503, "could not reconnect")
		return
	}
	now := time.Now()
	// A lost response may be recovered only by the exact persisted operation,
	// briefly, while its successor is still the current unspent credential.
	if used != nil && req.Request != "" && requestHash != nil && *requestHash == hashToken(req.Request) && now.Sub(*used) < 2*time.Minute && now.Before(idle) && now.Before(absolute) {
		access := mobileRetryToken(req.Refresh, req.Request, "access")
		refresh := mobileRetryToken(req.Refresh, req.Request, "refresh")
		var seconds int
		err = tx.QueryRow(ctx, `select greatest(0,extract(epoch from(s.expires_at-now()))::int) from user_sessions s join customer_refresh_tokens t on t.session_id=s.id where s.id=$1 and s.token_hash=$2 and s.expires_at>now() and t.token_hash=$3 and t.used_at is null and least(t.idle_expires_at,t.absolute_expires_at)>now()`, id, hashToken(access), hashToken(refresh)).Scan(&seconds)
		if err == nil {
			if tx.Commit(ctx) != nil {
				writeErr(w, 503, "could not reconnect")
				return
			}
			w.Header().Set("Cache-Control", "no-store")
			writeJSON(w, 200, M{"token": access, "refresh_token": refresh, "session_id": id, "expires_in": seconds})
			return
		}
		if err != pgx.ErrNoRows {
			writeErr(w, 503, "could not reconnect")
			return
		}
	}
	if used != nil || !now.Before(idle) || !now.Before(absolute) {
		_, err = tx.Exec(ctx, `delete from user_sessions where id=$1`, id)
		if err != nil || tx.Commit(ctx) != nil {
			writeErr(w, 503, "could not verify your sign-in")
			return
		}
		writeErr(w, 401, "your sign-in expired; sign in again")
		return
	}
	access, err := newToken()
	if err != nil {
		writeErr(w, 503, "could not reconnect")
		return
	}
	refresh, err := newToken()
	if err != nil {
		writeErr(w, 503, "could not reconnect")
		return
	}
	nextIdle := now.Add(mobileIdleTTL)
	if req.Request != "" {
		access = mobileRetryToken(req.Refresh, req.Request, "access")
		refresh = mobileRetryToken(req.Refresh, req.Request, "refresh")
	}
	if absolute.Before(nextIdle) {
		nextIdle = absolute
	}
	_, err = tx.Exec(ctx, `update customer_refresh_tokens set used_at=$2,refresh_request_hash=$3 where token_hash=$1`, hashToken(req.Refresh), now, hashToken(req.Request))
	if err == nil {
		_, err = tx.Exec(ctx, `insert into customer_refresh_tokens(token_hash,session_id,idle_expires_at,absolute_expires_at) values($1,$2,$3,$4)`, hashToken(refresh), id, nextIdle, absolute)
	}
	if err == nil {
		_, err = tx.Exec(ctx, `update user_sessions set token_hash=$2,expires_at=$3,last_seen_at=now() where id=$1`, id, hashToken(access), now.Add(mobileAccessTTL))
	}
	if err == nil {
		_, err = tx.Exec(ctx, `update push_devices set session_hash=$2 where owner_kind='customer' and session_hash=$1`, oldHash, hashToken(access))
	}
	if err != nil || tx.Commit(ctx) != nil {
		writeErr(w, 503, "could not reconnect; please retry")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, M{"token": access, "refresh_token": refresh, "session_id": id, "expires_in": int(mobileAccessTTL.Seconds())})
}
