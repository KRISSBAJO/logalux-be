package httpapi

import (
	"net/http"

	"github.com/go-chi/chi/v5"
)

func (s *Server) audit(r *http.Request, action, target string, before, after any) {
	_, _ = s.pool.Exec(r.Context(), `insert into audit_log (actor, action, target, before, after) values ($1,$2,$3,$4,$5)`, s.actor(r), action, target, before, after)
}

// GET /v1/admin/overview
func (s *Server) adminOverview(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	market := r.URL.Query().Get("market")
	k, err := row(ctx, s.pool, `
		select
		  (select count(*) from bookings bk join businesses b on b.id=bk.business_id where bk.starts_at::date = current_date and ($1='' or b.market=$1)) as bookings_today,
		  (select coalesce(sum(bk.total_cents),0) from bookings bk join businesses b on b.id=bk.business_id where bk.starts_at::date = current_date and bk.status not in ('cancelled_client','cancelled_business','no_show') and ($1='' or b.market=$1)) as processed_today_cents,
		  (select count(*) from businesses where status='live' and ($1='' or market=$1)) as live_businesses,
		  (select count(*) from businesses where status='live' and created_at > now() - interval '7 days' and ($1='' or market=$1)) as new_businesses_week,
		  (select count(*) from verification_requests where status in ('pending','needs_info')) as verification_queue,
		  (select count(*) from verification_requests where status='pending' and created_at < now() - interval '18 hours') as verification_at_risk,
		  (select count(*) from reviews where status='flagged') as moderation_queue,
		  (select count(*) from disputes where status in ('with_business','needs_decision')) as open_disputes,
		  (select count(*) from leads where status = 'disputed') as lead_disputes,
		  (select count(*) from disputes where status='needs_decision') as overdue_disputes,
		  (select count(*) from payment_events where status='failed') as payout_failures,
		  (select count(*) from support_tickets where status='open') as open_tickets,
		  (select round(100.0 * count(*) filter (where status in ('simulated','succeeded')) / greatest(count(*),1), 1) from payment_events) as payment_success_pct`, market)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	days, _ := rows(ctx, s.pool, `
		select d::date as day,
		  count(bk.id) filter (where bk.status not in ('no_show','cancelled_client','cancelled_business')) as completed,
		  count(bk.id) filter (where bk.status='no_show') as bad
		from generate_series(current_date - 13, current_date, interval '1 day') d
		left join bookings bk on bk.starts_at::date = d::date
		group by d order by d`)
	activity, _ := rows(ctx, s.pool, `select actor, action, target, after, created_at from audit_log order by created_at desc limit 8`)
	writeJSON(w, 200, M{"kpis": k, "days": days, "activity": activity, "payments_mode": s.cfg.PaymentsMode()})
}

// GET /v1/admin/verification?status=
func (s *Server) adminVerification(w http.ResponseWriter, r *http.Request) {
	status := r.URL.Query().Get("status")
	out, err := rows(r.Context(), s.pool, `
		select v.id, v.status, v.risk_score, v.id_type, v.id_provider, v.licence_status, v.portfolio_note, v.decision_note, v.decided_by, v.decided_at, v.created_at,
		       extract(epoch from now()-v.created_at)/3600 as age_hours,
		       b.id as business_id, b.slug, b.name, b.owner_name, b.category, b.market, b.phone, b.instagram, b.tone,
		       l.address, l.city
		from verification_requests v join businesses b on b.id=v.business_id left join locations l on l.business_id=b.id and l.is_primary
		where ($1='' or v.status=$1) order by v.created_at`, status)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, M{"requests": out})
}

type decideReq struct {
	Decision string `json:"decision"` // approve | needs_info | reject
	Note     string `json:"note"`
}

// POST /v1/admin/verification/{id}/decide
func (s *Server) adminVerificationDecide(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id := chi.URLParam(r, "id")
	var req decideReq
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, "invalid json")
		return
	}
	status := map[string]string{"approve": "approved", "needs_info": "needs_info", "reject": "rejected"}[req.Decision]
	if status == "" {
		writeErr(w, 400, "decision must be approve, needs_info or reject")
		return
	}
	before, err := row(ctx, s.pool, `select status, business_id from verification_requests where id=$1`, id)
	if err != nil {
		writeErr(w, 404, "request not found")
		return
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `update verification_requests set status=$2, decision_note=$3, decided_by=$4, decided_at=now() where id=$1`, id, status, req.Note, s.actor(r)); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	bizStatus := map[string]string{"approved": "verified", "rejected": "rejected", "needs_info": "pending"}[status]
	live := map[string]string{"approved": "live", "rejected": "pending", "needs_info": "pending"}[status]
	if _, err := tx.Exec(ctx, `update businesses set verification_status=$2, status=$3 where id=$1`, before["business_id"], bizStatus, live); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if err := tx.Commit(ctx); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	s.audit(r, "verification."+req.Decision, id, before, M{"status": status, "note": req.Note})
	writeJSON(w, 200, M{"ok": true, "status": status})
}

// GET /v1/admin/moderation?status=flagged
func (s *Server) adminModeration(w http.ResponseWriter, r *http.Request) {
	status := r.URL.Query().Get("status")
	if status == "" {
		status = "flagged"
	}
	out, err := rows(r.Context(), s.pool, `
		select rv.id, rv.author_name, rv.service_name, rv.rating, rv.body, rv.status, rv.flag_reason, rv.reply, rv.created_at,
		       extract(epoch from now()-rv.created_at)/3600 as age_hours,
		       b.name as business, b.slug, b.market, b.rating as business_rating, b.review_count, b.tone
		from reviews rv join businesses b on b.id=rv.business_id where rv.status=$1 order by rv.created_at`, status)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, M{"reviews": out})
}

type moderateReq struct {
	Action string `json:"action"` // publish | hide | remove
	Reason string `json:"reason"`
}

// POST /v1/admin/moderation/{id}/decide
func (s *Server) adminModerationDecide(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var req moderateReq
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, "invalid json")
		return
	}
	status := map[string]string{"publish": "published", "hide": "hidden", "remove": "removed"}[req.Action]
	if status == "" {
		writeErr(w, 400, "action must be publish, hide or remove")
		return
	}
	before, err := row(r.Context(), s.pool, `select status, flag_reason, business_id from reviews where id=$1`, id)
	if err != nil {
		writeErr(w, 404, "review not found")
		return
	}
	if _, err := s.pool.Exec(r.Context(), `update reviews set status=$2, flag_reason=$3 where id=$1`, id, status, req.Reason); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	// Keep the business's public rating honest: only published reviews count.
	_, _ = s.pool.Exec(r.Context(), `update businesses b set rating = coalesce((select round(avg(rating),2) from reviews where business_id=b.id and status='published'),0),
		review_count = (select count(*) from reviews where business_id=b.id and status='published') where b.id=$1`, before["business_id"])
	s.audit(r, "moderation."+req.Action, id, before, M{"status": status, "reason": req.Reason})
	writeJSON(w, 200, M{"ok": true, "status": status})
}

// GET /v1/admin/disputes?status=
func (s *Server) adminDisputes(w http.ResponseWriter, r *http.Request) {
	status := r.URL.Query().Get("status")
	out, err := rows(r.Context(), s.pool, `
		select d.*, b.name as business, b.slug, b.market, b.tone,
		       extract(epoch from d.business_deadline-now())/3600 as hours_left
		from disputes d join businesses b on b.id=d.business_id
		where ($1='' or ($1='open' and d.status in ('with_business','needs_decision')) or d.status=$1)
		order by d.status='needs_decision' desc, d.created_at`, status)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, M{"disputes": out})
}

type resolveReq struct {
	Outcome string `json:"outcome"` // full | partial | credit | decline | out_of_scope
	Cents   int    `json:"amount_cents"`
	Reason  string `json:"reason"`
}

// POST /v1/admin/disputes/{id}/resolve
func (s *Server) adminDisputeResolve(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var req resolveReq
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, "invalid json")
		return
	}
	switch req.Outcome {
	case "full", "partial", "credit", "decline", "out_of_scope":
	default:
		writeErr(w, 400, "outcome must be full, partial, credit, decline or out_of_scope")
		return
	}
	before, err := row(r.Context(), s.pool, `select status, outcome from disputes where id=$1`, id)
	if err != nil {
		writeErr(w, 404, "dispute not found")
		return
	}
	status := "resolved"
	if req.Outcome == "out_of_scope" {
		status = "out_of_scope"
	}
	if _, err := s.pool.Exec(r.Context(), `update disputes set status=$2, outcome=$3, outcome_cents=$4, decision_note=$5, resolved_at=now() where id=$1`, id, status, req.Outcome, req.Cents, req.Reason); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	s.audit(r, "dispute."+req.Outcome, id, before, M{"status": status, "amount_cents": req.Cents, "reason": req.Reason})
	writeJSON(w, 200, M{"ok": true, "status": status})
}

// GET /v1/admin/businesses?q=&status=&market=
func (s *Server) adminBusinesses(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	out, err := rows(r.Context(), s.pool, `
		select b.id, b.slug, b.name, b.owner_name, b.category, b.market, b.currency, b.plan, b.status, b.verification_status, b.rating, b.review_count, b.tone, b.created_at,
		  (select count(*) from staff st where st.business_id=b.id) as staff_count,
		  (select count(*) from bookings bk where bk.business_id=b.id and bk.created_at > now() - interval '30 days') as bookings_30d,
		  (select coalesce(sum(total_cents),0) from bookings bk where bk.business_id=b.id and bk.created_at > now() - interval '30 days' and bk.status not in ('cancelled_client','cancelled_business','no_show')) as processed_30d_cents,
		  (select count(*) from disputes d where d.business_id=b.id and d.status in ('with_business','needs_decision')) as open_disputes,
		  (select count(*) from reviews rv where rv.business_id=b.id and rv.status='flagged') as flagged_reviews
		from businesses b
		where ($1='' or b.name ilike '%'||$1||'%' or b.owner_name ilike '%'||$1||'%' or b.slug ilike '%'||$1||'%')
		  and ($2='' or b.status=$2) and ($3='' or b.market=$3)
		order by processed_30d_cents desc`, q.Get("q"), q.Get("status"), q.Get("market"))
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, M{"businesses": out})
}

type statusReq struct {
	Status string `json:"status"`
	Reason string `json:"reason"`
}

// POST /v1/admin/businesses/{id}/status
func (s *Server) adminBusinessStatus(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var req statusReq
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, "invalid json")
		return
	}
	switch req.Status {
	case "live", "paused", "suspended":
	default:
		writeErr(w, 400, "status must be live, paused or suspended")
		return
	}
	before, err := row(r.Context(), s.pool, `select status, name from businesses where id=$1`, id)
	if err != nil {
		writeErr(w, 404, "business not found")
		return
	}
	if _, err := s.pool.Exec(r.Context(), `update businesses set status=$2 where id=$1`, id, req.Status); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	s.audit(r, "business.status", before["name"].(string), before, M{"status": req.Status, "reason": req.Reason})
	writeJSON(w, 200, M{"ok": true, "status": req.Status})
}

// GET /v1/admin/flags
func (s *Server) adminFlags(w http.ResponseWriter, r *http.Request) {
	out, err := rows(r.Context(), s.pool, `select * from feature_flags order by key`)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, M{"flags": out})
}

type flagReq struct {
	Enabled    *bool   `json:"enabled"`
	RolloutPct *int    `json:"rollout_pct"`
	Market     *string `json:"market"`
	Plan       *string `json:"plan"`
}

// PUT /v1/admin/flags/{key}
func (s *Server) adminFlagUpdate(w http.ResponseWriter, r *http.Request) {
	key := chi.URLParam(r, "key")
	var req flagReq
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, "invalid json")
		return
	}
	before, err := row(r.Context(), s.pool, `select enabled, rollout_pct, market, plan from feature_flags where key=$1`, key)
	if err != nil {
		writeErr(w, 404, "flag not found")
		return
	}
	_, err = s.pool.Exec(r.Context(), `update feature_flags set enabled=coalesce($2,enabled), rollout_pct=coalesce($3,rollout_pct), market=coalesce($4,market), plan=coalesce($5,plan), updated_at=now() where key=$1`,
		key, req.Enabled, req.RolloutPct, req.Market, req.Plan)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	after, _ := row(r.Context(), s.pool, `select enabled, rollout_pct, market, plan from feature_flags where key=$1`, key)
	s.audit(r, "flag.update", key, before, after)
	writeJSON(w, 200, M{"ok": true, "flag": after})
}
