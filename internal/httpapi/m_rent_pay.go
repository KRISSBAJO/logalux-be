package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/go-chi/chi/v5"
	"net/http"
)

// Creates or reuses a payment link for the exact unpaid rental period.
func (s *Server) mRentLink(w http.ResponseWriter, r *http.Request) {
	m := mc(r)
	ctx := r.Context()
	id := chi.URLParam(r, "id")
	if s.payMode(m.Market) != "live" {
		writeErr(w, 409, "Online rent payments need the payment provider configured. Record cash instead.")
		return
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		writeErr(w, 500, "Could not open rent payment")
		return
	}
	defer tx.Rollback(ctx)
	var amount int
	var status, name, email string
	var ref *string
	err = tx.QueryRow(ctx, `select rc.amount_cents,rc.status,st.name,coalesce(st.email,''),rc.payment_reference from rent_charges rc join staff st on st.id=rc.staff_id where rc.id=$1 and rc.business_id=$2 for update of rc`, id, m.BusinessID).Scan(&amount, &status, &name, &email, &ref)
	if err != nil {
		writeErr(w, 404, "Rent charge not found")
		return
	}
	if status != "due" {
		writeErr(w, 409, "Only unpaid rent can have a payment link")
		return
	}
	if ref != nil {
		var state, link string
		if err = tx.QueryRow(ctx, `select status,url from payments where reference=$1`, *ref).Scan(&state, &link); err != nil {
			writeErr(w, 500, "Could not check existing payment")
			return
		}
		if state == "pending" {
			writeJSON(w, 200, M{"reference": *ref, "url": link})
			return
		}
		if state == "paid" {
			writeErr(w, 409, "This rent has already been paid online")
			return
		}
	}
	payload, _ := json.Marshal(M{"rent_id": id, "market": m.Market, "plan": m.Plan})
	atomic := *s
	atomic.pool = &paymentDatabase{database: s.pool, tx: tx}
	reference, link, err := atomic.startPayment(ctx, payStart{Provider: providerFor(m.Market), Purpose: "rent", BusinessID: m.BusinessID, MerchantID: m.ID, Payload: payload, Amount: amount, Currency: m.Currency, Email: email, Description: "Chair rent · " + name + " · " + m.Business})
	if err != nil {
		writeErr(w, 502, "Could not create the rent payment link")
		return
	}
	if _, err = tx.Exec(ctx, `update rent_charges set payment_reference=$2 where id=$1`, id, reference); err != nil {
		writeErr(w, 500, "Could not save rent payment")
		return
	}
	if err = tx.Commit(ctx); err != nil {
		writeErr(w, 500, "Could not save rent payment")
		return
	}
	writeJSON(w, 201, M{"reference": reference, "url": link})
}
func (s *Server) rentPaid(ctx context.Context, ref string, amount int, currency string) error {
	var id, business, market, plan string
	if err := s.pool.QueryRow(ctx, `update rent_charges rc set status='paid',method=p.provider,paid_at=now() from payments p,businesses b where rc.payment_reference=$1 and p.reference=$1 and rc.business_id=b.id and rc.status='due' and rc.amount_cents=$2 returning rc.id::text,rc.business_id::text,b.market,b.plan`, ref, amount).Scan(&id, &business, &market, &plan); err != nil {
		return fmt.Errorf("rent payment could not be applied: %w", err)
	}
	_, err := s.pool.Exec(ctx, `insert into ledger (business_id,kind,amount_cents,currency,method,status,in_balance,rent_charge_id,description,settles_at) values ($1,'adjustment',$2,$3,'card','pending',true,$4,'Chair rental payment',now()+interval '2 days')`, business, amount, currency, id)
	if err != nil {
		return err
	}
	fee := feeFor(ctx, s.pool, market, plan, amount)
	if fee > 0 {
		_, err = s.pool.Exec(ctx, `insert into ledger (business_id,kind,amount_cents,currency,method,status,in_balance,rent_charge_id,description,settles_at) values ($1,'fee',$2,$3,'card','pending',true,$4,'Chair rental processing fee',now()+interval '2 days')`, business, -fee, currency, id)
	}
	return err
}
