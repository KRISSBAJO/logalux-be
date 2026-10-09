package httpapi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"regexp"

	"github.com/jackc/pgx/v5"
)

var checkoutRequestID = regexp.MustCompile(`^[A-Za-z0-9_-]{20,80}$`)

func checkoutHash(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func checkoutFingerprint(v any) (string, error) {
	b, err := json.Marshal(v)
	return checkoutHash(b), err
}

// Serializes a request before stock, credits or booking state is read.
// The transaction lock also makes recovery wait for an in-flight commit/rollback.
func checkoutReplay(ctx context.Context, tx pgx.Tx, w http.ResponseWriter, m Merchant, key, fingerprint string, recovery bool) bool {
	if _, err := tx.Exec(ctx, `select pg_advisory_xact_lock(hashtextextended($1, 0))`, m.BusinessID+":"+key); err != nil {
		writeErr(w, 503, "could not reserve this checkout; retry the same request")
		return true
	}
	var saved, actor, response string
	err := tx.QueryRow(ctx, `select payload_hash, merchant_id, response_body from checkout_requests where business_id=$1 and request_key=$2`, m.BusinessID, key).Scan(&saved, &actor, &response)
	if isNoRows(err) {
		if recovery {
			writeErr(w, 404, "this checkout has not been recorded; rebuild the same ticket to retry")
			return true
		}
		return false
	}
	if err != nil {
		writeErr(w, 503, "could not check this checkout; retry the same request")
		return true
	}
	if actor != m.ID || (!recovery && saved != fingerprint) {
		writeErr(w, 409, "this checkout request was already used with different details")
		return true
	}
	w.Header().Set("Idempotency-Replayed", "true")
	checkoutResponse(w, []byte(response))
	return true
}

func checkoutResponse(w http.ResponseWriter, body []byte) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusCreated)
	_, _ = w.Write(body)
}

// Receipt recovery is independent of today's selected history page (or date).
// It retains the same business/calendar visibility rules as checkout history.
func (s *Server) checkoutReceipt(r *http.Request, id string) (M, error) {
	m := mc(r)
	return row(r.Context(), s.pool, `select sa.id, sa.client_name, sa.total_cents, sa.tip_cents, sa.method, sa.status,
		sa.provider_refund_status, sa.refunded_cents, sa.created_at, st.name as staff,
		(select problem from sale_refund_jobs rf where rf.sale_id=sa.id and rf.status='pending' limit 1) as refund_problem,
		(select string_agg(si.name, ', ') from sale_items si where si.sale_id=sa.id) as items
		from sales sa left join staff st on st.id=sa.staff_id
		where sa.business_id=$1 and sa.id::text=$2 and ($3 or sa.staff_id=nullif($4,'')::uuid)`, m.BusinessID, id, perm(m, "see_all_calendars"), m.StaffID)
}
