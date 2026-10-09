package httpapi

import (
	"context"
	"fmt"
)

func (s *Server) reserveReturnRefund(ctx context.Context, id, business, actor string, req returnDecision, refund int) (int, M) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 503, M{"error": "could not reserve refund"}
	}
	defer tx.Rollback(ctx)
	var order, shipment, seller, currency, status string
	var user, biz *string
	var items int
	err = tx.QueryRow(ctx, `select rt.order_id::text,rt.shipment_id::text,sh.seller_name,o.currency,rt.status,rt.user_id::text,rt.business_id::text,sh.items_cents from order_returns rt join orders o on o.id=rt.order_id join order_shipments sh on sh.id=rt.shipment_id where rt.id=$1 and (($2='' and rt.business_id is null) or rt.business_id::text=$2) for update of rt`, id, business).Scan(&order, &shipment, &seller, &currency, &status, &user, &biz, &items)
	if err != nil || status != "requested" {
		return 409, M{"error": "this return has already been answered"}
	}
	var pay string
	var paid, already, target int
	err = tx.QueryRow(ctx, `select id::text,amount_cents,refunded_cents from payments where order_id=$1 and purpose='order' and status in ('paid','refunded') order by paid_at desc limit 1 for update`, order).Scan(&pay, &paid, &already)
	toCard := 0
	if err == nil {
		if err = tx.QueryRow(ctx, `select greatest($2::int,coalesce(max(target_refunded_cents),0)) from order_return_refund_jobs where payment_id=$1`, pay, already).Scan(&target); err != nil {
			return 503, M{"error": "could not check refund reservations"}
		}
		toCard = refund
		if toCard > paid-target {
			toCard = paid - target
		}
	} else if !isNoRows(err) {
		return 503, M{"error": "could not check the original payment"}
	}
	credit := refund - toCard
	if credit > 0 && user == nil {
		return 409, M{"error": "the non-card part needs an account or a manual refund before approval"}
	}
	providerState := ""
	if toCard > 0 {
		providerState = "pending"
	}
	if _, err = tx.Exec(ctx, `update order_returns set status='approved',reply=$2,decided_by=$3,decided_at=now(),refund_cents=$4,credit_cents=$5,restocked=$6,provider_refund_status=$7 where id=$1`, id, req.Reply, actor, refund, credit, req.Restock, providerState); err != nil {
		return 503, M{"error": "could not record the return"}
	}
	if toCard > 0 {
		if _, err = tx.Exec(ctx, `insert into order_return_refund_jobs(return_id,payment_id,target_refunded_cents) values($1,$2,$3)`, id, pay, target+toCard); err != nil {
			return 503, M{"error": "could not reserve provider recovery"}
		}
	}
	if credit > 0 {
		if _, err = tx.Exec(ctx, `insert into user_credits(user_id,amount_cents,currency,reason,order_id) values($1,$2,$3,'Refund for a return',$4)`, *user, credit, currency, order); err != nil {
			return 503, M{"error": "could not return store credit"}
		}
	}
	if biz != nil {
		if _, err = tx.Exec(ctx, `insert into ledger(business_id,kind,amount_cents,currency,method,status,in_balance,order_id,description) values($1,'refund',$2,$3,'card','settled',true,$4,'Return reserved · shop order')`, *biz, -refund, currency, order); err != nil {
			return 503, M{"error": "could not reserve seller funds"}
		}
		itemPart := refund
		if itemPart > items {
			itemPart = items
		}
		if _, err = tx.Exec(ctx, `insert into ledger(business_id,kind,amount_cents,currency,method,status,in_balance,order_id,description) select business_id,'fee',round(-amount_cents::numeric*$3/nullif($4,0))::int,currency,'card','settled',true,order_id,'Marketplace fee returned · shop order' from ledger where order_id=$2 and business_id=$1 and kind='fee' and amount_cents<0 limit 1`, *biz, order, itemPart, items); err != nil {
			return 503, M{"error": "could not return the seller fee"}
		}
	}
	if req.Restock {
		if refund < items {
			return 400, M{"error": "automatic restocking requires a full item refund; adjust partial returned stock in Inventory"}
		}
		if _, err = tx.Exec(ctx, `update products p set stock=p.stock+x.qty,sold=greatest(p.sold-x.qty,0) from (select product_id,sum(qty)::int qty from order_items where order_id=$1 and seller_name=$2 group by product_id) x where p.id=x.product_id`, order, seller); err != nil {
			return 503, M{"error": "could not restock returned items"}
		}
	}
	if err = tx.Commit(ctx); err != nil {
		return 503, M{"error": "could not finish refund reservation; reload the return before retrying"}
	}
	s.mailOrder(order, "Your return is approved", func(o M, _ string) string {
		detail := "Your store credit has been returned."
		if toCard > 0 {
			detail = "Your card refund is reserved and awaiting provider confirmation. Recovery is automatic; do not request it again."
		}
		return "Hello,\n\n" + seller + " approved your return for order " + orderRef(o) + ". " + detail + "\n\nLogaLuxe"
	})
	code := 200
	if toCard > 0 {
		code = 202
	}
	return code, M{"ok": true, "status": "approved", "provider_refund_status": providerState, "refund_cents": refund, "to_card_cents": toCard, "credit_cents": credit}
}
func (s *Server) recoverReturnRefunds(ctx context.Context) {
	jobs, err := rows(ctx, s.pool, `select return_id from order_return_refund_jobs where status='pending' order by target_refunded_cents,created_at limit 20`)
	if err != nil {
		return
	}
	for _, j := range jobs {
		s.recoverReturnRefund(ctx, fmt.Sprint(j["return_id"]))
	}
}

// recoverReturnRefund asks the provider for one reserved return refund and marks the return once it is accepted.
// It returns the problem text, empty when the refund went through. The worker and the console's Retry now share it.
func (s *Server) recoverReturnRefund(ctx context.Context, returnID string) string {
	var payment string
	var target int
	if err := s.pool.QueryRow(ctx, `select payment_id::text, target_refunded_cents from order_return_refund_jobs where return_id::text=$1 and status='pending'`, returnID).Scan(&payment, &target); err != nil {
		return "this refund is no longer waiting"
	}
	if err := s.refundPaymentTarget(ctx, payment, 0, target); err != nil {
		_, _ = s.pool.Exec(ctx, `update order_return_refund_jobs set problem=$2 where return_id::text=$1`, returnID, err.Error())
		return err.Error()
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err.Error()
	}
	_, err = tx.Exec(ctx, `update order_return_refund_jobs set status='accepted',problem='' where return_id::text=$1`, returnID)
	if err == nil {
		_, err = tx.Exec(ctx, `update order_returns set provider_refund_status='accepted' where id::text=$1`, returnID)
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
