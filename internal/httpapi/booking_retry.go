package httpapi

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"regexp"
)

var bookingRequestID = regexp.MustCompile(`^[A-Za-z0-9_-]{20,80}$`)

func bookingDigest(value []byte) string {
	sum := sha256.Sum256(value)
	return hex.EncodeToString(sum[:])
}

func (s *Server) bookingRequestIdentity(r *http.Request, req createBookingReq) (string, string) {
	if req.RequestID == "" {
		return "", ""
	}
	owner := "guest"
	if id := s.customerID(r); id != nil {
		owner = *id
	}
	key := bookingDigest([]byte(owner + ":" + req.RequestID))
	req.RequestID = ""
	body, _ := json.Marshal(req)
	return key, bookingDigest(body)
}

// A request key is a capability for guests and is scoped to the account for members.
// Never return a booking for a reused key with a different payload.
func (s *Server) replayBooking(w http.ResponseWriter, r *http.Request, key, body string) bool {
	if key == "" {
		return false
	}
	var id, saved string
	err := s.pool.QueryRow(r.Context(), `select id::text,request_body_hash from bookings where request_key_hash=$1`, key).Scan(&id, &saved)
	if isNoRows(err) {
		return false
	}
	if err != nil {
		writeErr(w, 503, "could not check the previous booking; retry with the same request")
		return true
	}
	if saved != body {
		writeErr(w, 409, "this booking request was already used with different details")
		return true
	}
	w.Header().Set("Idempotency-Replayed", "true")
	s.getBookingByID(w, r, id, http.StatusOK)
	return true
}
