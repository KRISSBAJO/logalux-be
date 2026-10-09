package httpapi

import (
	"net/http"
	"net/url"
	"strings"
	"time"
)

func shopReturnPath(raw string) (string, bool) {
	if len(raw) > 500 || !strings.HasPrefix(raw, "/") || strings.HasPrefix(raw, "//") || strings.ContainsAny(raw, "\\\r\n") {
		return "", false
	}
	u, err := url.Parse(raw)
	if err != nil || u.IsAbs() || u.Host != "" || u.Fragment != "" {
		return "", false
	}
	if u.Path != "/shop" && !strings.HasPrefix(u.Path, "/shop/") && u.Path != "/cart" && u.Path != "/gift-cards" {
		return "", false
	}
	// A path containing escaped traversal must not escape the allowed destinations.
	if strings.Contains(u.Path, "..") {
		return "", false
	}
	return u.RequestURI(), true
}

func (s *Server) authWebHandoff(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Path string `json:"path"`
	}
	if readJSON(r, &req) != nil {
		writeErr(w, 400, "invalid request")
		return
	}
	path, ok := shopReturnPath(req.Path)
	if !ok {
		writeErr(w, 400, "choose a shop, cart or gift-card page")
		return
	}
	code, err := newToken()
	if err != nil {
		writeErr(w, 500, "could not prepare browser sign-in")
		return
	}
	ctx := r.Context()
	if _, err = s.pool.Exec(ctx, `insert into customer_web_handoffs(code_hash,user_id,return_path,source_session_hash,expires_at) values($1,$2,$3,$4,now()+interval '2 minutes')`, hashToken(code), currentCustomer(r).ID, path, hashToken(bearer(r))); err != nil {
		writeErr(w, 500, "could not prepare browser sign-in")
		return
	}
	_, _ = s.pool.Exec(ctx, `delete from customer_web_handoffs where expires_at<now()`)
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, M{"url": strings.TrimRight(s.cfg.WebURL, "/") + "/mobile/continue#" + code})
}

type handoffRequest struct {
	Code string `json:"code"`
}

func handoffCode(r *http.Request) (string, bool) {
	var req handoffRequest
	if readJSON(r, &req) != nil || len(req.Code) != 64 {
		return "", false
	}
	for _, c := range req.Code {
		if !strings.ContainsRune("0123456789abcdef", c) {
			return "", false
		}
	}
	return req.Code, true
}

func (s *Server) webHandoffPreview(w http.ResponseWriter, r *http.Request) {
	code, ok := handoffCode(r)
	if !ok {
		writeErr(w, 400, "invalid sign-in link")
		return
	}
	out, err := row(r.Context(), s.pool, `select u.first_name,h.return_path as next from customer_web_handoffs h join users u on u.id=h.user_id where h.code_hash=$1 and h.expires_at>now() and exists(select 1 from user_sessions ss where ss.token_hash=h.source_session_hash and ss.expires_at>now()) and u.deleted_at is null`, hashToken(code))
	if err != nil {
		writeErr(w, 410, "this link expired or was already used; open the shop from the app again")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, out)
}

func (s *Server) webHandoffExchange(w http.ResponseWriter, r *http.Request) {
	code, ok := handoffCode(r)
	if !ok {
		writeErr(w, 400, "invalid sign-in link")
		return
	}
	ctx := r.Context()
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		writeErr(w, 503, "could not complete sign-in")
		return
	}
	defer tx.Rollback(ctx)
	var user, path string
	if err = tx.QueryRow(ctx, `delete from customer_web_handoffs h using users u where h.user_id=u.id and u.deleted_at is null and h.code_hash=$1 and h.expires_at>now() and exists(select 1 from user_sessions ss where ss.token_hash=h.source_session_hash and ss.expires_at>now()) returning h.user_id::text,h.return_path`, hashToken(code)).Scan(&user, &path); err != nil {
		writeErr(w, 410, "this link expired or was already used; open the shop from the app again")
		return
	}
	token, err := newToken()
	if err != nil {
		writeErr(w, 500, "could not complete sign-in")
		return
	}
	if _, err = tx.Exec(ctx, `insert into user_sessions(token_hash,user_id,expires_at,device_name) values($1,$2,$3,'Browser opened from mobile app')`, hashToken(token), user, time.Now().Add(userSessionTTL)); err != nil {
		writeErr(w, 500, "could not complete sign-in")
		return
	}
	if err = tx.Commit(ctx); err != nil {
		writeErr(w, 503, "could not complete sign-in")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, M{"token": token, "next": path, "expires_in": int(userSessionTTL.Seconds())})
}
