package httpapi

import (
	"context"
	"fmt"
)

// Recovery can run concurrently: the provider path locks each payment and uses
// an absolute refunded target, while final accounting is one SQL transaction.
func (s *Server) recoverSaleRefunds(ctx context.Context) {
	jobs, err := rows(ctx, s.pool, `select id from sale_refund_jobs where status='pending' order by created_at limit 20`)
	if err != nil {
		return
	}
	for _, j := range jobs {
		s.recoverSaleRefund(ctx, fmt.Sprint(j["id"]))
	}
}

// recoverSaleRefund asks the provider for one reserved refund and settles the books once it is accepted.
// It returns the problem text, empty when the refund went through. The worker and the console's Retry now share it.
func (s *Server) recoverSaleRefund(ctx context.Context, id string) string {
	var payment string
	var target int
	if err := s.pool.QueryRow(ctx, `select payment_id::text, target_refunded_cents from sale_refund_jobs where id::text=$1 and status='pending'`, id).Scan(&payment, &target); err != nil {
		return "this refund is no longer waiting"
	}
	if err := s.refundPaymentTarget(ctx, payment, 0, target); err != nil {
		_, _ = s.pool.Exec(ctx, `update sale_refund_jobs set problem=$2,updated_at=now() where id::text=$1 and status='pending'`, id, err.Error())
		return err.Error()
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err.Error()
	}
	var sale string
	err = tx.QueryRow(ctx, `update sale_refund_jobs set status='accepted',problem='',updated_at=now() where id::text=$1 and status='pending' returning sale_id::text`, id).Scan(&sale)
	if err == nil {
		_, err = tx.Exec(ctx, `update ledger set status='settled',description=replace(description,'Refund reserved ·','Refund confirmed ·') where sale_refund_job_id::text=$1`, id)
	}
	if err == nil {
		_, err = tx.Exec(ctx, `update sales set provider_refund_status='accepted' where id=$1`, sale)
	}
	if err == nil {
		err = tx.Commit(ctx)
	}
	if err != nil {
		_ = tx.Rollback(ctx)
		return err.Error()
	}
	return ""
}
