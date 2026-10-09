package httpapi

import (
	"net/http"

	"github.com/go-chi/chi/v5"
)

// Work the system could not finish on its own: refunds the payment provider has not accepted yet, and
// campaigns whose delivery outcomes are unknown. Staff see them here instead of hearing about them from a customer.

// Both kinds of refund job in one shape. A sale refund is a merchant's; a return refund is a shop order's.
// Neither table counts retries: the worker tries every pending job each run, so only the last problem is kept.
const pendingRefundJobsSQL = `
	select j.id, 'sale' as kind, j.amount_cents, b.currency, j.problem, j.created_at, j.updated_at,
	  b.id as business_id, b.name as business, sa.client_name as customer, sa.id as sale_id, null::uuid as order_id, null::uuid as return_id
	from sale_refund_jobs j join sales sa on sa.id = j.sale_id join businesses b on b.id = sa.business_id where j.status = 'pending'
	union all
	select j.return_id as id, 'return' as kind, greatest(rt.refund_cents - rt.credit_cents, 0) as amount_cents, o.currency, j.problem, j.created_at, j.created_at as updated_at,
	  b.id as business_id, coalesce(b.name, 'LogaLuxe') as business, o.customer_name as customer, null::uuid as sale_id, o.id as order_id, rt.id as return_id
	from order_return_refund_jobs j join order_returns rt on rt.id = j.return_id join orders o on o.id = rt.order_id left join businesses b on b.id = rt.business_id where j.status = 'pending'
	order by created_at`

// GET /v1/admin/attention   ops and up
func (s *Server) adminAttention(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	jobs, err := rows(ctx, s.pool, pendingRefundJobsSQL)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	campaigns, err := rows(ctx, s.pool, `select c.id, c.name, c.channel, c.audience, c.stalled_reason, c.recipients, c.uncertain, c.created_at, c.sent_at, b.id as business_id, b.name as business
		from campaigns c join businesses b on b.id = c.business_id where c.status = 'stalled' order by c.created_at desc`)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, M{"refund_jobs": jobs, "stalled_campaigns": campaigns, "counts": M{"refund_jobs": len(jobs), "stalled_campaigns": len(campaigns)}})
}

// POST /v1/admin/attention/refunds/{id}/retry   super admin
// Asks the provider again right now, the same way the worker does on its next run. The id is a sale refund
// job's id or a return's id; a job that is no longer pending is left alone.
func (s *Server) adminRefundRetry(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id := chi.URLParam(r, "id")
	state := func() (M, error) {
		return row(ctx, s.pool, `select 'sale' as kind, status, problem from sale_refund_jobs where id::text = $1
			union all select 'return' as kind, status, problem from order_return_refund_jobs where return_id::text = $1 limit 1`, id)
	}
	before, err := state()
	if err != nil {
		writeErr(w, 404, "refund job not found")
		return
	}
	if before["status"] != "pending" {
		writeErr(w, 409, "that refund has already been accepted by the provider")
		return
	}
	var problem string
	if before["kind"] == "sale" {
		problem = s.recoverSaleRefund(ctx, id)
	} else {
		problem = s.recoverReturnRefund(ctx, id)
	}
	after, _ := state()
	s.audit(r, "refund.retry", id, before, after)
	writeJSON(w, 200, M{"ok": problem == "", "status": after["status"], "problem": problem})
}
