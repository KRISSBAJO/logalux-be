package httpapi

import (
	"encoding/json"
	"net/http"
	"strings"
)

// A shop order carries an optional request_id, like a booking. The key is scoped to the signed-in customer,
// or to the email given at checkout for a guest, so a lost response can be retried without a second order.
func (s *Server) orderRequestIdentity(r *http.Request, req orderReq) (string, string) {
	if req.RequestID == "" {
		return "", ""
	}
	owner := "guest:" + strings.ToLower(strings.TrimSpace(req.CustomerEmail))
	if id := s.customerID(r); id != nil {
		owner = *id
	}
	key := bookingDigest([]byte(owner + ":" + req.RequestID))
	req.RequestID = ""
	body, _ := json.Marshal(req)
	return key, bookingDigest(body)
}

// replayOrder answers with the order already placed for this request key. A reused key with a
// different payload is refused rather than answered with someone's order.
func (s *Server) replayOrder(w http.ResponseWriter, r *http.Request, key, body string) bool {
	if key == "" {
		return false
	}
	var id, saved string
	err := s.pool.QueryRow(r.Context(), `select id::text, request_body_hash from orders where request_key_hash=$1`, key).Scan(&id, &saved)
	if isNoRows(err) {
		return false
	}
	if err != nil {
		writeErr(w, 503, "could not check the previous order; retry with the same request")
		return true
	}
	if saved != body {
		writeErr(w, 409, "this order request was already used with different details")
		return true
	}
	w.Header().Set("Idempotency-Replayed", "true")
	s.getOrderByID(w, r, id, http.StatusOK)
	return true
}
