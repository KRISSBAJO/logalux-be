package httpapi

import (
	"context"
	"fmt"
)

// Recovery can run concurrently: the provider path locks each payment and uses
// an absolute refunded target, while final accounting is one SQL transaction.
func (s *Server) recoverSaleRefunds(ctx context.Context) {
	jobs, err := rows(ctx, s.pool, `select id,payment_id,target_refunded_cents from sale_refund_jobs where status='pending' order by created_at limit 20`)
	if err != nil {
		return
	}
	for _, j := range jobs {
		if err := s.refundPaymentTarget(ctx, fmt.Sprint(j["payment_id"]), 0, int(toInt(j["target_refunded_cents"]))); err != nil {
			_, _ = s.pool.Exec(ctx, `update sale_refund_jobs set problem=$2,updated_at=now() where id=$1 and status='pending'`, j["id"], err.Error())
			continue
		}
		tx, err := s.pool.Begin(ctx)
		if err != nil {
			continue
		}
		var sale string
		err = tx.QueryRow(ctx, `update sale_refund_jobs set status='accepted',problem='',updated_at=now() where id=$1 and status='pending' returning sale_id::text`, j["id"]).Scan(&sale)
		if err == nil {
			_, err = tx.Exec(ctx, `update ledger set status='settled',description=replace(description,'Refund reserved ·','Refund confirmed ·') where sale_refund_job_id=$1`, j["id"])
		}
		if err == nil {
			_, err = tx.Exec(ctx, `update sales set provider_refund_status='accepted' where id=$1`, sale)
		}
		if err == nil {
			err = tx.Commit(ctx)
		}
		if err != nil {
			_ = tx.Rollback(ctx)
		}
	}
}
