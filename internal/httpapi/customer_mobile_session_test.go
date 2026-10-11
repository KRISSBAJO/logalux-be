package httpapi

import (
	"context"
	"encoding/json"
	"golang.org/x/crypto/bcrypt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func mobileCall(t *testing.T, s *Server, handler http.HandlerFunc, token, user, body string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest("POST", "/v1/auth/mobile", strings.NewReader(body))
	r.Header.Set("Authorization", "Bearer "+token)
	if user != "" {
		r = r.WithContext(context.WithValue(r.Context(), userKey{}, Customer{ID: user}))
	}
	w := httptest.NewRecorder()
	handler(w, r)
	return w
}
func mobileAnswer(t *testing.T, w *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	if w.Code != 200 {
		t.Fatalf("mobile response %d: %s", w.Code, w.Body.String())
	}
	var out map[string]any
	if json.Unmarshal(w.Body.Bytes(), &out) != nil {
		t.Fatal("invalid answer")
	}
	return out
}

func TestSecurityPINManagement(t *testing.T) {
	s := securityServer(t)
	_, _, _, user, _ := securityFixture(t, s)
	hash, _ := bcrypt.GenerateFromPassword([]byte("A-test-password-983!"), bcrypt.DefaultCost)
	securityExec(t, s, `update users set password_hash=$2 where id=$1`, user, string(hash))
	token, err := s.startUserSession(context.Background(), user)
	if err != nil {
		t.Fatal(err)
	}
	if w := mobileCall(t, s, s.authSecurityPIN, token, user, `{"pin":"839275"}`); w.Code != 403 {
		t.Fatal("unverified PIN change accepted")
	}
	mobileAnswer(t, mobileCall(t, s, s.authSecurityVerify, token, user, `{"password":"A-test-password-983!"}`))
	if w := mobileCall(t, s, s.authSecurityPIN, token, user, `{"pin":"123456"}`); w.Code != 400 {
		t.Fatal("weak PIN accepted")
	}
	mobileAnswer(t, mobileCall(t, s, s.authSecurityPIN, token, user, `{"pin":"839275"}`))
	mobileAnswer(t, mobileCall(t, s, s.authSecurityVerify, token, user, `{"pin":"839275"}`))
	if w := mobileCall(t, s, s.authSecurityPIN, token, user, `{"pin":"593827"}`); w.Code != 403 {
		t.Fatal("PIN alone can reset PIN")
	}
	for i := 0; i < 5; i++ {
		if w := mobileCall(t, s, s.authSecurityVerify, token, user, `{"pin":"839274"}`); w.Code != 403 {
			t.Fatal("wrong PIN accepted")
		}
	}
	other, err := s.startUserSession(context.Background(), user)
	if err != nil {
		t.Fatal(err)
	}
	if w := mobileCall(t, s, s.authSecurityVerify, other, user, `{"pin":"839275"}`); w.Code != 429 {
		t.Fatal("new session bypassed PIN lock")
	}
	mobileAnswer(t, mobileCall(t, s, s.authSecurityVerify, other, user, `{"password":"A-test-password-983!"}`))
	mobileAnswer(t, mobileCall(t, s, s.authSecurityPIN, other, user, `{"pin":"593827"}`))
	if w := mobileCall(t, s, s.authSecurityVerify, token, user, `{"pin":"839275"}`); w.Code != 403 {
		t.Fatal("old PIN still accepted")
	}
	mobileAnswer(t, mobileCall(t, s, s.authSecurityVerify, token, user, `{"pin":"593827"}`))
}

func TestSecurityRotatingMobileSessions(t *testing.T) {
	s := securityServer(t)
	_, _, _, user, _ := securityFixture(t, s)
	token := "initial-mobile-token"
	securityExec(t, s, `insert into user_sessions(user_id,token_hash,expires_at) values($1,$2,now()+interval '30 days')`, user, hashToken(token))
	signed := mobileAnswer(t, mobileCall(t, s, s.authMobileSession, token, user, `{}`))
	refresh := signed["refresh_token"].(string)
	if signed["expires_in"] != float64(900) {
		t.Fatal("access credential is not short lived")
	}
	securityExec(t, s, `update user_sessions set expires_at=now()-interval '1 minute' where token_hash=$1`, hashToken(token))
	// Legacy-session cleanup must not erase a valid mobile refresh family.
	if _, err := s.startUserSession(context.Background(), user); err != nil {
		t.Fatal(err)
	}
	rotated := mobileAnswer(t, mobileCall(t, s, s.authMobileRefresh, "", "", `{"refresh_token":"`+refresh+`"}`))
	newToken := rotated["token"].(string)
	if newToken == token || rotated["refresh_token"] == refresh {
		t.Fatal("credentials did not rotate")
	}
	if _, ok := s.customerFrom(context.Background(), token); ok {
		t.Fatal("old access still works")
	}
	if _, ok := s.customerFrom(context.Background(), newToken); !ok {
		t.Fatal("new access rejected")
	}
	var idle, absolute time.Time
	if err := s.pool.QueryRow(context.Background(), `select idle_expires_at,absolute_expires_at from customer_refresh_tokens where token_hash=$1`, hashToken(rotated["refresh_token"].(string))).Scan(&idle, &absolute); err != nil {
		t.Fatal(err)
	}
	if time.Until(idle) > mobileIdleTTL || time.Until(absolute) > mobileAbsoluteTTL {
		t.Fatal("refresh lifetime too long")
	}
	replay := mobileCall(t, s, s.authMobileRefresh, "", "", `{"refresh_token":"`+refresh+`"}`)
	if replay.Code != 401 {
		t.Fatal("reused refresh accepted")
	}
	if _, ok := s.customerFrom(context.Background(), newToken); ok {
		t.Fatal("replay did not revoke family")
	}
}

func TestSecurityMobileRefreshResponseRecovery(t *testing.T) {
	s := securityServer(t)
	_, _, _, user, _ := securityFixture(t, s)
	token, err := s.startUserSession(context.Background(), user)
	if err != nil {
		t.Fatal(err)
	}
	initial := mobileAnswer(t, mobileCall(t, s, s.authMobileSession, token, user, `{}`))
	body := `{"refresh_token":"` + initial["refresh_token"].(string) + `","request_id":"9683a174-115b-4c4e-9a78-8796c14310af"}`
	first := mobileAnswer(t, mobileCall(t, s, s.authMobileRefresh, "", "", body))
	retry := mobileAnswer(t, mobileCall(t, s, s.authMobileRefresh, "", "", body))
	if first["token"] != retry["token"] || first["refresh_token"] != retry["refresh_token"] {
		t.Fatal("lost response was not recovered")
	}
	wrong := strings.Replace(body, "9683a174-115b-4c4e-9a78-8796c14310af", "0683a174-115b-4c4e-9a78-8796c14310af", 1)
	if w := mobileCall(t, s, s.authMobileRefresh, "", "", wrong); w.Code != 401 {
		t.Fatal("different refresh operation accepted replay")
	}
	if w := mobileCall(t, s, s.authMobileRefresh, "", "", `{"refresh_token":"`+first["refresh_token"].(string)+`"}`); w.Code != 401 {
		t.Fatal("replayed family survived")
	}
}

func TestSecurityMobileExpiryAndLogout(t *testing.T) {
	s := securityServer(t)
	_, _, _, user, _ := securityFixture(t, s)
	for _, mode := range []string{"idle", "absolute", "logout"} {
		token, err := s.startUserSession(context.Background(), user)
		if err != nil {
			t.Fatal(err)
		}
		out := mobileAnswer(t, mobileCall(t, s, s.authMobileSession, token, user, `{}`))
		refresh := out["refresh_token"].(string)
		if mode == "logout" {
			mobileAnswer(t, mobileCall(t, s, s.authMobileLogout, "", "", `{"refresh_token":"`+refresh+`"}`))
		} else {
			column := "idle_expires_at"
			if mode == "absolute" {
				column = "absolute_expires_at"
			}
			securityExec(t, s, `update customer_refresh_tokens set `+column+`=now()-interval '1 minute' where token_hash=$1`, hashToken(refresh))
		}
		if w := mobileCall(t, s, s.authMobileRefresh, "", "", `{"refresh_token":"`+refresh+`"}`); w.Code != 401 {
			t.Fatalf("%s refresh accepted", mode)
		}
	}
}

func TestSecurityAccountVerification(t *testing.T) {
	s := securityServer(t)
	_, _, _, user, _ := securityFixture(t, s)
	password, err := bcrypt.GenerateFromPassword([]byte("A-test-password-983!"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	securityExec(t, s, `update users set password_hash=$2 where id=$1`, user, string(password))
	token, err := s.startUserSession(context.Background(), user)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest("POST", "/orders", nil)
	request.Header.Set("Authorization", "Bearer "+token)
	if s.customerSecurityOK(request) {
		t.Fatal("unverified session can spend credit")
	}
	if w := mobileCall(t, s, s.authSecurityVerify, token, user, `{"password":"wrong"}`); w.Code != 403 {
		t.Fatal("wrong password accepted")
	}
	mobileAnswer(t, mobileCall(t, s, s.authSecurityVerify, token, user, `{"password":"A-test-password-983!"}`))
	if !s.customerSecurityOK(request) {
		t.Fatal("verified session rejected")
	}
	another, err := s.startUserSession(context.Background(), user)
	if err != nil {
		t.Fatal(err)
	}
	other := httptest.NewRequest("POST", "/orders", nil)
	other.Header.Set("Authorization", "Bearer "+another)
	if s.customerSecurityOK(other) {
		t.Fatal("verification leaked to another device")
	}
	securityExec(t, s, `update user_sessions set security_verified_until=now()-interval '1 second' where token_hash=$1`, hashToken(token))
	if s.customerSecurityOK(request) {
		t.Fatal("expired verification still accepted")
	}
	// A security code is scoped to this device and consumed after one use.
	securityExec(t, s, `insert into customer_security_codes(session_id,code_hash,expires_at) select id,$2,now()+interval '10 minutes' from user_sessions where token_hash=$1`, hashToken(token), hashToken("918276"))
	if w := mobileCall(t, s, s.authSecurityVerify, another, user, `{"code":"918276"}`); w.Code != 403 {
		t.Fatal("code worked on a different device")
	}
	mobileAnswer(t, mobileCall(t, s, s.authSecurityVerify, token, user, `{"code":"918276"}`))
	if w := mobileCall(t, s, s.authSecurityVerify, token, user, `{"code":"918276"}`); w.Code != 403 {
		t.Fatal("code reused")
	}
}
