package httpapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

// refundPayment persists the intent BEFORE contacting a provider. An uncertain response
// keeps that reservation; another call cannot issue a fresh refund against the same money.
func (s *Server) refundPayment(ctx context.Context, paymentID string, amount int) error {
	return s.refundPaymentTarget(ctx, paymentID, amount, 0)
}

// Absolute targets prevent a recovery retry from creating a second partial refund.
func (s *Server) refundPaymentTarget(ctx context.Context, paymentID string, amount, target int) error {
	conn, err := s.pool.Acquire(ctx)
	if err != nil {
		return err
	}
	defer conn.Release()
	// Dedicated session lock spans the provider request without holding a long SQL transaction.
	if _, err = conn.Exec(ctx, `select pg_advisory_lock(hashtextextended($1,9132))`, paymentID); err != nil {
		return err
	}
	defer func() {
		c, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := conn.Exec(c, `select pg_advisory_unlock(hashtextextended($1,9132))`, paymentID); err != nil {
			conn.Hijack().Close(c)
		}
	}()
	tx, err := conn.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var provider, ref, intent, status string
	var paid, refunded int
	if err = tx.QueryRow(ctx, `select provider,reference,payment_intent,status,amount_cents,refunded_cents from payments where id=$1 for update`, paymentID).Scan(&provider, &ref, &intent, &status, &paid, &refunded); err != nil {
		return errors.New("payment not found")
	}
	if status != "paid" && status != "refunded" {
		return errors.New("that payment was never completed")
	}
	if target > 0 {
		if refunded >= target {
			return nil
		}
		amount = target - refunded
	}
	if refunded == paid {
		return nil
	}
	var operation, opStatus, knownProviderRef string
	var reserved int
	var created time.Time
	err = tx.QueryRow(ctx, `select id::text,status,amount_cents,created_at,provider_ref from payment_refunds where payment_id=$1 and status in ('created','sending','unknown') order by created_at limit 1`, paymentID).Scan(&operation, &opStatus, &reserved, &created, &knownProviderRef)
	if err == nil {
		if amount > 0 && amount != reserved {
			return errors.New("another refund is being reconciled; wait for its outcome")
		}
		amount = reserved
	} else {
		if !isNoRows(err) {
			return err
		}
		if amount == 0 {
			amount = paid - refunded
		}
		if amount <= 0 {
			return nil
		}
		if amount > paid-refunded {
			return errors.New("refund exceeds the remaining payment")
		}
		err = tx.QueryRow(ctx, `insert into payment_refunds(payment_id,amount_cents,base_refunded_cents) values($1,$2,$3) returning id::text,status,created_at`, paymentID, amount, refunded).Scan(&operation, &opStatus, &created)
		if err != nil {
			return err
		}
	}
	if err = tx.Commit(ctx); err != nil {
		return err
	}
	providerRef := ""
	if provider == "stripe" && knownProviderRef != "" {
		var out struct {
			ID     string `json:"id"`
			Status string `json:"status"`
		}
		err = providerJSON(ctx, http.MethodGet, "https://api.stripe.com/v1/refunds/"+url.PathEscape(knownProviderRef), s.cfg.StripeSecret, nil, nil, &out)
		if err != nil {
			return fmt.Errorf("refund status reconciliation pending: %w", err)
		}
		if out.ID != knownProviderRef || out.Status != "succeeded" {
			return errors.New("provider refund is still pending or needs attention; no second refund was sent")
		}
		providerRef = out.ID
	} else if provider == "paystack" && (opStatus == "sending" || opStatus == "unknown") {
		// Paystack does not document Stripe-style idempotency. Reconcile the durable merchant note instead of POSTing twice.
		var out struct {
			Data []struct {
				ID     int64  `json:"id"`
				Amount int    `json:"amount"`
				Note   string `json:"merchant_note"`
				Status string `json:"status"`
			} `json:"data"`
		}
		err = providerJSON(ctx, http.MethodGet, "https://api.paystack.co/refund?transaction="+url.QueryEscape(intent)+"&perPage=100", s.cfg.PaystackSecret, nil, nil, &out)
		if err == nil {
			for _, r := range out.Data {
				if r.Note == "LogaLuxe refund "+operation && r.Amount == amount && (r.Status == "processed" || r.Status == "success") {
					providerRef = strconv.FormatInt(r.ID, 10)
					break
				}
			}
		}
		if providerRef == "" {
			return errors.New("refund outcome is uncertain; reconciliation is pending, no second refund was sent")
		}
	} else {
		if provider == "stripe" && opStatus != "created" && time.Since(created) > 23*time.Hour {
			return errors.New("refund needs provider reconciliation before retrying outside the idempotency window")
		}
		if _, err = conn.Exec(ctx, `update payment_refunds set status='sending',updated_at=now() where id=$1`, operation); err != nil {
			return err
		}
		if provider == "stripe" {
			var out struct {
				ID     string `json:"id"`
				Status string `json:"status"`
			}
			err = providerJSONWithKey(ctx, http.MethodPost, "https://api.stripe.com/v1/refunds", s.cfg.StripeSecret, url.Values{"payment_intent": {intent}, "amount": {strconv.Itoa(amount)}, "metadata[ref]": {ref}}, nil, &out, "logaluxe-refund-"+operation)
			if err == nil && out.Status != "succeeded" {
				err = errors.New("provider refund is not yet confirmed as succeeded")
			}
			providerRef = out.ID
		} else if provider == "paystack" {
			var out struct {
				Status bool `json:"status"`
				Data   struct {
					ID     int64  `json:"id"`
					Status string `json:"status"`
				} `json:"data"`
			}
			err = providerJSON(ctx, http.MethodPost, "https://api.paystack.co/refund", s.cfg.PaystackSecret, nil, M{"transaction": ref, "amount": amount, "merchant_note": "LogaLuxe refund " + operation}, &out)
			if err == nil && (!out.Status || (out.Data.Status != "processed" && out.Data.Status != "success")) {
				err = errors.New("provider did not accept the refund")
			}
			if out.Data.ID > 0 {
				providerRef = strconv.FormatInt(out.Data.ID, 10)
			}
		} else {
			err = errors.New("unsupported refund provider")
		}
		if err == nil && providerRef == "" {
			err = errors.New("provider response did not identify a refund")
		}
		if err != nil {
			c, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_, _ = conn.Exec(c, `update payment_refunds set status='unknown',problem=$2,provider_ref=case when $3='' then provider_ref else $3 end,updated_at=now() where id=$1`, operation, err.Error(), providerRef)
			return fmt.Errorf("refund awaiting reconciliation: %w", err)
		}
	}
	finish, err := conn.Begin(ctx)
	if err != nil {
		return err
	}
	defer finish.Rollback(ctx)
	tag, err := finish.Exec(ctx, `update payments set refunded_cents=refunded_cents+$2,status=case when refunded_cents+$2=amount_cents then 'refunded' else status end where id=$1 and refunded_cents+$2<=amount_cents`, paymentID, amount)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return errors.New("refund accounting changed; reconciliation is required")
	}
	if _, err = finish.Exec(ctx, `update payment_refunds set status='accepted',provider_ref=$2,problem='',updated_at=now() where id=$1`, operation, providerRef); err != nil {
		return err
	}
	return finish.Commit(ctx)
}

func (s *Server) reconcileRefunds(ctx context.Context) {
	s.recoverSaleRefunds(ctx)
	s.recoverReturnRefunds(ctx)
	jobs, _ := rows(ctx, s.pool, `select payment_id,amount_cents from payment_refund_jobs where completed_at is null order by created_at limit 20`)
	for _, j := range jobs {
		if err := s.refundPayment(ctx, fmt.Sprint(j["payment_id"]), int(toInt(j["amount_cents"]))); err == nil {
			_, _ = s.pool.Exec(ctx, `update payment_refund_jobs set completed_at=now() where payment_id=$1`, j["payment_id"])
		}
	}
	pending, _ := rows(ctx, s.pool, `select payment_id,amount_cents from payment_refunds where status in ('created','sending','unknown') and updated_at<now()-interval '1 minute' order by created_at limit 20`)
	for _, j := range pending {
		_ = s.refundPayment(ctx, fmt.Sprint(j["payment_id"]), int(toInt(j["amount_cents"])))
	}
}
