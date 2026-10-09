package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"logaluxe/api/internal/geo"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"logaluxe/api/internal/mail"
)

// After the sale: sending an order back, reporting a problem with a visit,
// tipping once the visit is over, buying a gift card, and tax by state.

// ---------- tax ----------

// orderTax works out sales tax seller by seller. A business charges its own rate on what it sells.
// Brand products are sold by LogaLuxe: they are taxed at the rate of the state they ship to, when we list one.
func orderTax(ctx context.Context, q rowQuerier, items map[string]int, businessOf map[string]*string, subtotal, discount int, shipState string) int {
	tax := 0
	for seller, amount := range items {
		taxable := amount
		if subtotal > 0 && discount > 0 {
			taxable = amount - discount*amount/subtotal // the discount is shared out by value
		}
		bp := 0
		if biz := businessOf[seller]; biz != nil {
			_ = q.QueryRow(ctx, `select sales_tax_bp from businesses where id=$1`, *biz).Scan(&bp)
		} else if shipState != "" {
			_ = q.QueryRow(ctx, `select bp from tax_rates where region=$1`, shipState).Scan(&bp)
		}
		if taxable > 0 && bp > 0 {
			tax += (taxable*bp + 5000) / 10000
		}
	}
	return tax
}

// stateOf reads the state of an order's address ("region", or "state") as its two-letter code.
func stateOf(address M) string {
	if address == nil {
		return ""
	}
	for _, k := range []string{"region", "state"} {
		raw, ok := address[k].(string)
		if !ok {
			continue
		}
		// "TN", "tn" and "Tennessee" are the same state. A code we do not know is passed on as typed.
		if code, known := geo.Region("US", raw); known {
			return code
		}
		if v := strings.ToUpper(strings.TrimSpace(raw)); len(v) == 2 {
			return v
		}
	}
	return ""
}

// GET /v1/admin/tax-rates
func (s *Server) adminTaxRates(w http.ResponseWriter, r *http.Request) {
	out, err := rows(r.Context(), s.pool, `select region, name, bp, updated_by, updated_at from tax_rates order by region`)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, M{"rates": out})
}

// PUT /v1/admin/tax-rates/{region}   {name, bp}      DELETE removes it, so orders to that state are not taxed
func (s *Server) adminTaxRateSet(w http.ResponseWriter, r *http.Request) {
	region := strings.ToUpper(strings.TrimSpace(chi.URLParam(r, "region")))
	if len(region) != 2 || strings.Trim(region, "ABCDEFGHIJKLMNOPQRSTUVWXYZ") != "" {
		writeErr(w, 400, "use the two-letter state code, such as TN")
		return
	}
	if r.Method == http.MethodDelete {
		_, _ = s.pool.Exec(r.Context(), `delete from tax_rates where region=$1`, region)
		s.audit(r, "tax.rate_removed", region, nil, nil)
		writeJSON(w, 200, M{"ok": true})
		return
	}
	var req struct {
		Name string `json:"name"`
		BP   int    `json:"bp"`
	}
	if err := readJSON(r, &req); err != nil || req.BP < 0 || req.BP > 2500 {
		writeErr(w, 400, "the rate is between 0% and 25%")
		return
	}
	if _, err := s.pool.Exec(r.Context(), `insert into tax_rates (region, name, bp, updated_by) values ($1,$2,$3,$4)
		on conflict (region) do update set name = excluded.name, bp = excluded.bp, updated_by = excluded.updated_by, updated_at = now()`, region, strings.TrimSpace(req.Name), req.BP, currentAdmin(r).Email); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	s.audit(r, "tax.rate_set", region, nil, M{"bp": req.BP})
	writeJSON(w, 200, M{"ok": true})
}

// ---------- returns ----------

var returnReasons = map[string]string{"damaged": "It arrived damaged", "wrong_item": "The wrong item came", "not_as_described": "It is not as described", "changed_mind": "Changed my mind", "other": "Something else"}

// returnWindow says whether one seller's part of an order can still be sent back, and until when.
func (s *Server) returnWindow(ctx context.Context, shipmentID string) (ok bool, until time.Time, why string) {
	var status string
	var updated time.Time
	var bizDays, minProduct *int
	var anyUnstated bool
	if err := s.pool.QueryRow(ctx, `select sh.status, sh.updated_at, b.returns_days,
		(select min(p.returns_days) from order_items oi join products p on p.id = oi.product_id where oi.order_id = sh.order_id and oi.seller_name = sh.seller_name),
		exists(select 1 from order_items oi join products p on p.id = oi.product_id where oi.order_id = sh.order_id and oi.seller_name = sh.seller_name and p.returns_days is null)
		from order_shipments sh left join businesses b on b.id = sh.business_id where sh.id=$1`, shipmentID).Scan(&status, &updated, &bizDays, &minProduct, &anyUnstated); err != nil {
		return false, until, "that part of the order was not found"
	}
	days := bizDays
	if days == nil && !anyUnstated { // a brand's products carry their own policy; the shortest one applies
		days = minProduct
	}
	switch {
	case status != "delivered" && status != "collected":
		return false, until, "a return can be asked for once the items have reached you"
	case days == nil:
		return false, until, "this seller has not stated a returns policy; message them to ask"
	case *days == 0:
		return false, until, "this seller does not take returns"
	}
	until = updated.Add(time.Duration(*days) * 24 * time.Hour)
	if time.Now().After(until) {
		return false, until, "the time to return these items ended on " + until.Format("2 January 2006")
	}
	return true, until, ""
}

// POST /v1/auth/orders/{id}/returns   {seller, reason, note}
func (s *Server) authReturnAsk(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	c := currentCustomer(r)
	var req struct {
		Seller string `json:"seller"`
		Reason string `json:"reason"`
		Note   string `json:"note"`
	}
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, "invalid json")
		return
	}
	req.Note = strings.TrimSpace(req.Note)
	if returnReasons[req.Reason] == "" {
		writeErr(w, 400, "choose why you are returning it")
		return
	}
	if len(req.Note) > 1000 || ((req.Reason == "other" || req.Reason == "not_as_described") && len(req.Note) < 10) {
		writeErr(w, 400, "tell the seller what is wrong, in a sentence or two")
		return
	}
	var shipmentID, orderID, seller string
	var bizID *string
	if err := s.pool.QueryRow(ctx, `select sh.id::text, o.id::text, sh.business_id::text, sh.seller_name from order_shipments sh join orders o on o.id = sh.order_id
		where o.id::text=$1 and o.user_id=$2 and sh.seller_name=$3`, chi.URLParam(r, "id"), c.ID, req.Seller).Scan(&shipmentID, &orderID, &bizID, &seller); err != nil {
		writeErr(w, 404, "order not found")
		return
	}
	if ok, _, why := s.returnWindow(ctx, shipmentID); !ok {
		writeErr(w, 409, why)
		return
	}
	var id string
	if err := s.pool.QueryRow(ctx, `insert into order_returns (order_id, shipment_id, business_id, user_id, reason, note) values ($1,$2,$3,$4,$5,$6) on conflict (shipment_id) do nothing returning id::text`,
		orderID, shipmentID, bizID, c.ID, req.Reason, req.Note).Scan(&id); err != nil {
		writeErr(w, 409, "you have already asked to return these items")
		return
	}
	if bizID != nil {
		s.notifyBusiness(*bizID, "order_email", "A customer asked to return an order", c.FirstName+" asked to return their order from your shop. Reason: "+returnReasons[req.Reason]+"."+
			map[bool]string{true: "\n\n\"" + req.Note + "\"", false: ""}[req.Note != ""]+"\n\nAnswer it here: "+strings.TrimRight(s.cfg.WebURL, "/")+"/business/inventory?tab=orders")
	}
	writeJSON(w, 201, M{"ok": true, "id": id})
}

const returnHistorySQL = `select rt.id, rt.status, rt.provider_refund_status, rt.reason, rt.note, rt.reply, rt.refund_cents, rt.credit_cents, rt.restocked, rt.created_at, rt.decided_at, rt.decided_by,
		o.id as order_id, o.currency, greatest(rt.refund_cents - rt.credit_cents, 0) as to_card_cents, o.customer_name, o.customer_email, o.customer_phone, sh.seller_name, sh.fulfilment, sh.items_cents, sh.shipping_cents, sh.updated_at as received_at,
		(select coalesce(json_agg(json_build_object('name', oi.name, 'size', oi.size_label, 'qty', oi.qty, 'unit_cents', oi.unit_cents) order by oi.name), '[]') from order_items oi where oi.order_id = o.id and oi.seller_name = sh.seller_name) as items
		from order_returns rt join orders o on o.id = rt.order_id join order_shipments sh on sh.id = rt.shipment_id and sh.order_id = rt.order_id and sh.business_id is not distinct from rt.business_id
		where (($1 = '' and rt.business_id is null) or rt.business_id::text = $1) and ($2 = '' or rt.status = $2)
		order by (rt.status = 'requested') desc, rt.created_at desc`

// returnsFor lists return requests: one business's, or (business "") the brand ones LogaLuxe staff decide.
func (s *Server) returnsFor(ctx context.Context, businessID, status string) ([]M, error) {
	return rows(ctx, s.pool, returnHistorySQL, businessID, status)
}

// GET /v1/m/returns?status=
func (s *Server) mReturns(w http.ResponseWriter, r *http.Request) {
	out, pagination, counts, selected, err := s.returnHistoryPage(r, mc(r).BusinessID)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, M{"returns": out, "reasons": returnReasons, "returns_pagination": pagination, "counts": counts, "selected_return": selected})
}

// GET /v1/admin/returns?status=   returns of brand products
func (s *Server) adminReturns(w http.ResponseWriter, r *http.Request) {
	out, pagination, counts, selected, err := s.returnHistoryPage(r, "")
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, M{"returns": out, "reasons": returnReasons, "returns_pagination": pagination, "counts": counts, "selected_return": selected})
}

type returnDecision struct {
	Action      string `json:"action"` // approve or refuse
	Reply       string `json:"reply"`
	RefundCents *int   `json:"refund_cents"` // leave out to refund the items in full
	Restock     bool   `json:"restock"`
}

// decideReturn approves or refuses a return. Approving sends the money back the way it came: to the card
// through the provider first, and as store credit for any part that was paid with credit or a gift card.
func (s *Server) decideReturn(ctx context.Context, id, businessID, actor string, req returnDecision) (int, M) {
	req.Reply = strings.TrimSpace(req.Reply)
	if req.Action != "approve" && req.Action != "refuse" {
		return 400, M{"error": "choose to approve or refuse the return"}
	}
	if req.Action == "refuse" && len(req.Reply) < 10 {
		return 400, M{"error": "tell the customer why, in a sentence"}
	}
	if len(req.Reply) > 1000 {
		return 400, M{"error": "keep the reply under 1,000 characters"}
	}
	var orderID, shipmentID, status, seller, cur string
	var userID, bizID *string
	var items, shipping int
	if err := s.pool.QueryRow(ctx, `select rt.order_id::text, rt.shipment_id::text, rt.status, rt.user_id::text, rt.business_id::text, sh.items_cents, sh.shipping_cents, sh.seller_name, o.currency
		from order_returns rt join order_shipments sh on sh.id = rt.shipment_id join orders o on o.id = rt.order_id where rt.id::text=$1 and (($2 = '' and rt.business_id is null) or rt.business_id::text = $2)`, id, businessID).
		Scan(&orderID, &shipmentID, &status, &userID, &bizID, &items, &shipping, &seller, &cur); err != nil {
		return 404, M{"error": "return not found"}
	}
	if status != "requested" {
		return 409, M{"error": "this return has already been answered"}
	}
	if req.Action == "refuse" {
		tag, err := s.pool.Exec(ctx, `update order_returns set status='refused', reply=$2, decided_by=$3, decided_at=now() where id=$1 and status='requested'`, id, req.Reply, actor)
		if err != nil || tag.RowsAffected() != 1 {
			return 409, M{"error": "this return has already been answered"}
		}
		s.mailOrder(orderID, "About your return", func(o M, _ string) string {
			return "Hello,\n\n" + seller + " could not accept the return for order " + orderRef(o) + ".\n\n\"" + req.Reply + "\"\n\nIf you disagree, reply to this email or write to us from the help page.\n\nLogaLuxe"
		})
		return 200, M{"ok": true, "status": "refused"}
	}
	refund := items
	if req.RefundCents != nil {
		refund = *req.RefundCents
	}
	if refund <= 0 || refund > items+shipping {
		return 400, M{"error": "the refund is between " + formatMoney(1, cur) + " and " + formatMoney(items+shipping, cur) + ", what the customer paid this seller"}
	}
	return s.reserveReturnRefund(ctx, id, businessID, actor, req, refund)
}

// POST /v1/m/returns/{id}   {action, reply, refund_cents, restock}
func (s *Server) mReturnDecide(w http.ResponseWriter, r *http.Request) {
	var req returnDecision
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, "invalid json")
		return
	}
	code, out := s.decideReturn(r.Context(), chi.URLParam(r, "id"), mc(r).BusinessID, mc(r).Name, req)
	writeJSON(w, code, out)
}

// POST /v1/admin/returns/{id}
func (s *Server) adminReturnDecide(w http.ResponseWriter, r *http.Request) {
	var req returnDecision
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, "invalid json")
		return
	}
	code, out := s.decideReturn(r.Context(), chi.URLParam(r, "id"), "", currentAdmin(r).Email, req)
	if code == 200 || code == 202 {
		s.audit(r, "return."+req.Action, chi.URLParam(r, "id"), nil, out)
	}
	writeJSON(w, code, out)
}

// ---------- a problem with a visit ----------

var problemReasons = map[string]string{"quality": "The result was not what was agreed", "charged": "I was charged the wrong amount", "no_show": "The professional did not show up", "conduct": "How I was treated", "other": "Something else"}

// POST /v1/auth/bookings/{id}/problem   {reason, statement}
// The business has 48 hours to answer; LogaLuxe staff then decide. It uses the disputes the console already works.
func (s *Server) authBookingProblem(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	c := currentCustomer(r)
	var req struct {
		Reason    string `json:"reason"`
		Statement string `json:"statement"`
	}
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, "invalid json")
		return
	}
	req.Statement = strings.TrimSpace(req.Statement)
	if problemReasons[req.Reason] == "" {
		writeErr(w, 400, "choose what went wrong")
		return
	}
	if len(req.Statement) < 20 || len(req.Statement) > 2000 {
		writeErr(w, 400, "describe what happened in a few sentences")
		return
	}
	var bookingID, bizID, client, currency, status string
	var total int
	var starts time.Time
	if err := s.pool.QueryRow(ctx, `select bk.id::text, bk.business_id::text, bk.client_name, b.currency, bk.status, bk.total_cents, bk.starts_at from bookings bk join businesses b on b.id = bk.business_id
		where bk.id::text=$1 and bk.user_id=$2`, chi.URLParam(r, "id"), c.ID).Scan(&bookingID, &bizID, &client, &currency, &status, &total, &starts); err != nil {
		writeErr(w, 404, "booking not found")
		return
	}
	switch {
	case starts.After(time.Now()):
		writeErr(w, 409, "this visit has not happened yet; to change it, move or cancel the booking, or message the business")
		return
	case time.Since(starts) > 14*24*time.Hour:
		writeErr(w, 409, "a problem can be reported for 14 days after a visit; for anything older, write to us from the help page")
		return
	case strings.HasPrefix(status, "cancelled"):
		writeErr(w, 409, "this booking was cancelled; write to us from the help page if you were charged for it")
		return
	}
	var open bool
	_ = s.pool.QueryRow(ctx, `select exists(select 1 from disputes where booking_id=$1)`, bookingID).Scan(&open)
	if open {
		writeErr(w, 409, "a problem has already been reported for this visit; we will email you when there is an answer")
		return
	}
	code, err := randomCode(1, 6)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	ref := "D-" + code
	if _, err := s.pool.Exec(ctx, `insert into disputes (ref, business_id, booking_id, client_name, amount_cents, currency, reason, client_statement, status, user_id, raised_by)
		values ($1,$2,$3,$4,$5,$6,$7,$8,'with_business',$9,'client')`, ref, bizID, bookingID, client, total, currency, problemReasons[req.Reason], req.Statement, c.ID); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	s.notifyBusiness(bizID, "cancellation_email", "A client reported a problem with a visit", client+" reported a problem with their visit on "+starts.Format("Monday 2 January")+": "+problemReasons[req.Reason]+
		".\n\nYou have 48 hours to give your side. After that LogaLuxe decides with what it has.\n\nAnswer here: "+strings.TrimRight(s.cfg.WebURL, "/")+"/business/inbox?tab=problems")
	writeJSON(w, 201, M{"ok": true, "ref": ref})
}

// GET /v1/m/problems   problems clients reported about this business's visits
func (s *Server) mProblems(w http.ResponseWriter, r *http.Request) {
	out, pagination, err := s.historyRows(r, "problems", `select d.id, d.ref, d.client_name, d.amount_cents, d.currency, d.reason, d.client_statement, d.business_statement, d.status, d.outcome, d.outcome_cents, d.decision_note,
		d.business_deadline, d.created_at, d.resolved_at, bk.starts_at, (select string_agg(name, ', ') from booking_items where booking_id = bk.id) as services
		from disputes d left join bookings bk on bk.id = d.booking_id where d.business_id=$1 order by (d.status = 'with_business') desc, d.created_at desc`, "(status = 'with_business') desc, created_at desc", "created_at client_name status amount_cents reason", mc(r).BusinessID)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	var waiting int
	if err := s.pool.QueryRow(r.Context(), `select count(*) from disputes where business_id=$1 and status='with_business'`, mc(r).BusinessID).Scan(&waiting); err != nil {
		writeErr(w, 503, "could not count open problems")
		return
	}
	var selected M
	if id := r.URL.Query().Get("problem"); id != "" {
		selected, _ = row(r.Context(), s.pool, `select d.*,bk.starts_at from disputes d left join bookings bk on bk.id=d.booking_id where d.business_id=$1 and d.id::text=$2`, mc(r).BusinessID, id)
	}
	writeJSON(w, 200, M{"problems": out, "problems_pagination": pagination, "waiting": waiting, "selected_problem": selected})
}

// POST /v1/m/problems/{id}   {statement}   the business gives its side; the case then goes to LogaLuxe staff
func (s *Server) mProblemAnswer(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Statement string `json:"statement"`
	}
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, "invalid json")
		return
	}
	req.Statement = strings.TrimSpace(req.Statement)
	if len(req.Statement) < 20 || len(req.Statement) > 2000 {
		writeErr(w, 400, "give your side in a few sentences")
		return
	}
	tag, err := s.pool.Exec(r.Context(), `update disputes set business_statement=$3, status='needs_decision' where id::text=$1 and business_id=$2 and status='with_business'`, chi.URLParam(r, "id"), mc(r).BusinessID, req.Statement)
	if err != nil || tag.RowsAffected() == 0 {
		writeErr(w, 409, "this has already been answered, or it was not found")
		return
	}
	writeJSON(w, 200, M{"ok": true})
}

// ---------- a tip once the visit is over ----------

// recordTip adds a tip to the visit's sale and to the business's balance.
func (s *Server) recordTip(ctx context.Context, bookingID string, amount int, method, status string) {
	_, _ = s.pool.Exec(ctx, `insert into ledger (business_id, kind, amount_cents, currency, method, status, in_balance, sale_id, booking_id, staff_id, description, settles_at)
		select bk.business_id, 'tip', $2, b.currency, $3, $4, true, (select sa.id from sales sa where sa.booking_id = bk.id order by sa.created_at desc limit 1), bk.id, bk.staff_id,
		       'Tip after the visit · ' || bk.client_name, case when $4 = 'pending' then now() + interval '`+settleAfter+`' end
		from bookings bk join businesses b on b.id = bk.business_id where bk.id=$1`, bookingID, amount, method, status)
	_, _ = s.pool.Exec(ctx, `update sales set tip_cents = tip_cents + $2, total_cents = total_cents + $2 where id = (select sa.id from sales sa where sa.booking_id=$1 order by sa.created_at desc limit 1)`, bookingID, amount)
}

// POST /v1/auth/bookings/{id}/tip   {amount_cents}
// For a visit that is finished and paid. With payments live the client pays on the provider's page.
func (s *Server) authBookingTip(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	c := currentCustomer(r)
	var req struct {
		AmountCents int    `json:"amount_cents"`
		CardID      string `json:"card_id"`
		SaveCard    bool   `json:"save_card"`
	}
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, "invalid json")
		return
	}
	var bookingID, bizID, biz, market, currency, status, staff string
	var total int
	var starts time.Time
	if err := s.pool.QueryRow(ctx, `select bk.id::text, bk.business_id::text, b.name, b.market, b.currency, bk.status, bk.total_cents, bk.starts_at, st.name from bookings bk
		join businesses b on b.id = bk.business_id join staff st on st.id = bk.staff_id where bk.id::text=$1 and bk.user_id=$2`, chi.URLParam(r, "id"), c.ID).
		Scan(&bookingID, &bizID, &biz, &market, &currency, &status, &total, &starts, &staff); err != nil {
		writeErr(w, 404, "booking not found")
		return
	}
	floor := map[string]int{"USD": 100, "NGN": 20000}[currency]
	switch {
	case status != "paid" && status != "completed":
		writeErr(w, 409, "a tip can be added once the visit is finished")
		return
	case time.Since(starts) > 30*24*time.Hour:
		writeErr(w, 409, "a tip can be added for 30 days after a visit")
		return
	case req.AmountCents < floor:
		writeErr(w, 400, "the smallest tip is "+formatMoney(floor, currency))
		return
	case total > 0 && req.AmountCents > total:
		writeErr(w, 400, "a tip can be up to the price of the visit, "+formatMoney(total, currency))
		return
	}
	var pending bool
	_ = s.pool.QueryRow(ctx, `select exists(select 1 from payments where booking_id=$1 and purpose='tip' and status='pending' and expires_at > now())`, bookingID).Scan(&pending)
	if pending {
		writeErr(w, 409, "a tip for this visit is already waiting to be paid; finish or close that payment page first")
		return
	}
	if s.payMode(market) != "live" {
		s.recordTip(ctx, bookingID, req.AmountCents, "card", "pending")
		writeJSON(w, 201, M{"ok": true, "amount_cents": req.AmountCents, "currency": currency})
		return
	}
	ref, link, err := s.startPayment(ctx, payStart{UserID: c.ID, CardID: req.CardID, KeepCard: req.SaveCard, Provider: providerFor(market), Purpose: "tip", BusinessID: bizID, BookingID: bookingID, Amount: req.AmountCents, Currency: currency, Email: c.Email, Description: "Tip for " + staff + " · " + biz})
	if err != nil {
		writeErr(w, 502, "the payment page could not be opened; nothing was charged, please try again")
		return
	}
	if link == "" { // taken from a card they kept
		writeJSON(w, 201, M{"ok": true, "amount_cents": req.AmountCents, "currency": currency, "paid": true})
		return
	}
	writeJSON(w, 201, M{"ok": true, "amount_cents": req.AmountCents, "currency": currency, "payment": M{"url": link, "reference": ref}})
}

// ---------- a gift card bought by a customer ----------

type giftOrder struct {
	AmountCents    int    `json:"amount_cents"`
	RecipientName  string `json:"recipient_name"`
	RecipientEmail string `json:"recipient_email"`
	Note           string `json:"note"`
	BuyerName      string `json:"buyer_name"`
	BuyerEmail     string `json:"buyer_email"`
	// Where the person it is for will spend it: US (dollars, the default) or NG (naira).
	// A card is spent only in its own currency, so this is chosen before the amount.
	Country string `json:"country"`
}

// giftCurrency is the money a gift card for a country is in, and the least and most it can hold.
func giftCurrency(country string) (currency string, min, max int) {
	if country == "NG" {
		return "NGN", 500000, 50000000 // 5,000 to 500,000 naira, in kobo
	}
	return "USD", 1000, 50000 // 10 to 500 dollars, in cents
}

// issueBoughtGift makes the card and emails it: the code to the person it is for, a receipt to the buyer.
func (s *Server) issueBoughtGift(ctx context.Context, g giftOrder, paymentRef string) (string, error) {
	country := firstNonEmpty(geo.CleanCountry(g.Country), "US")
	currency, _, _ := giftCurrency(country)
	var code string
	for attempt := 0; attempt < 4; attempt++ {
		c, err := randomCode(3, 4)
		if err != nil {
			return "", err
		}
		code = "LX-" + c
		var id string
		err = s.pool.QueryRow(ctx, `insert into gift_cards (code, initial_cents, balance_cents, currency, recipient_name, recipient_email, note, issued_by)
			values ($1,$2,$2,$7,$3,$4,$5,$6) on conflict (code) do nothing returning id::text`, code, g.AmountCents, g.RecipientName, g.RecipientEmail, g.Note, "bought by "+g.BuyerEmail, currency).Scan(&id)
		if err == nil {
			_, _ = s.pool.Exec(ctx, `insert into gift_card_txns (gift_card_id, amount_cents, note, actor) values ($1,$2,$3,$4)`, id, g.AmountCents, "Bought online"+map[bool]string{true: " · " + paymentRef, false: ""}[paymentRef != ""], g.BuyerName)
			break
		}
		code = ""
	}
	if code == "" {
		return "", fmt.Errorf("could not make a card code")
	}
	// The link opens the shop of the card's own country, where it can be spent.
	shop := strings.TrimRight(s.cfg.WebURL, "/") + "/shop?country=" + strings.ToLower(country)
	amount := formatMoney(g.AmountCents, currency)
	to := firstNonEmpty(g.RecipientEmail, g.BuyerEmail)
	note := ""
	if g.Note != "" {
		note = "\n\n\"" + g.Note + "\"\n"
	}
	body := "Hello " + firstNonEmpty(g.RecipientName, "there") + ",\n\n" + firstNonEmpty(g.BuyerName, "Someone") + " sent you a " + amount + " LogaLuxe gift card." + note +
		"\n\nYour code: " + code + "\n\nUse it at checkout in the LogaLuxe shop: " + shop + "\nWhatever you do not spend stays on the card.\n\nLogaLuxe"
	if g.RecipientEmail == "" {
		body = "Hello " + firstNonEmpty(g.BuyerName, "there") + ",\n\nHere is your " + amount + " LogaLuxe gift card.\n\nCode: " + code + "\n\nUse it at checkout in the LogaLuxe shop: " + shop + "\nWhatever is not spent stays on the card.\n\nLogaLuxe"
	}
	send := func() {
		go func() {
			c, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			if !strings.HasSuffix(strings.ToLower(to), ".test") {
				if _, err := s.mail.Send(c, to, "Your LogaLuxe gift card", body); err != nil {
					s.logMailFailure("gift card", to, err)
				}
			}
			if g.RecipientEmail != "" && !strings.HasSuffix(strings.ToLower(g.BuyerEmail), ".test") {
				if _, err := s.mail.Send(c, g.BuyerEmail, "Your gift card is on its way", "Hello "+firstNonEmpty(g.BuyerName, "there")+",\n\nYour "+amount+" LogaLuxe gift card has been emailed to "+g.RecipientEmail+
					". The code is only in their email, so it stays a surprise.\n\nLogaLuxe"); err != nil {
					s.logMailFailure("gift card receipt", g.BuyerEmail, err)
				}
			}
		}()
	}
	if !s.afterPaymentCommit(func(_ *Server) { send() }) {
		send()
	}
	return code, nil
}

// GET /v1/gift-cards/options
// What a gift card can be for each country: its currency, the least and most, the amounts to offer, and who takes the payment.
func (s *Server) giftOptions(w http.ResponseWriter, r *http.Request) {
	out := []M{}
	for _, c := range geo.Countries {
		currency, min, max := giftCurrency(c)
		amounts := []int{2500, 5000, 10000, 20000}
		if c == "NG" {
			amounts = []int{1000000, 2500000, 5000000, 10000000}
		}
		out = append(out, M{"country": c, "country_name": geo.CountryName(c), "currency": currency, "min_cents": min, "max_cents": max, "amounts_cents": amounts,
			"provider": providerFor(c), "payments": s.payMode(c)})
	}
	writeJSON(w, 200, M{"options": out})
}

// POST /v1/gift-cards/buy   {country, amount_cents, recipient_name, recipient_email, note, buyer_name, buyer_email}
// US dollars, $10 to $500. The code is only ever sent by email, never shown in the browser.
func (s *Server) giftBuy(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var g giftOrder
	if err := readJSON(r, &g); err != nil {
		writeErr(w, 400, "invalid json")
		return
	}
	g.RecipientName, g.RecipientEmail, g.Note = strings.TrimSpace(g.RecipientName), strings.ToLower(strings.TrimSpace(g.RecipientEmail)), strings.TrimSpace(g.Note)
	g.BuyerName, g.BuyerEmail = strings.TrimSpace(g.BuyerName), strings.ToLower(strings.TrimSpace(g.BuyerEmail))
	if c, ok := s.customerFrom(ctx, bearer(r)); ok {
		g.BuyerName, g.BuyerEmail = firstNonEmpty(g.BuyerName, strings.TrimSpace(c.FirstName+" "+c.LastName)), firstNonEmpty(g.BuyerEmail, c.Email)
	}
	country := geo.CleanCountry(g.Country)
	if strings.TrimSpace(g.Country) == "" {
		country = "US"
	}
	currency, min, max := giftCurrency(country)
	g.Country = country
	switch {
	case country == "":
		writeErr(w, 400, "choose whether the card is for someone in the United States or in Nigeria")
		return
	case g.AmountCents < min || g.AmountCents > max:
		writeErr(w, 400, "a gift card for "+geo.InCountry(country)+" is between "+formatMoney(min, currency)+" and "+formatMoney(max, currency))
		return
	case !mail.Valid(g.BuyerEmail):
		writeErr(w, 400, "add your email, for the receipt")
		return
	case g.RecipientEmail != "" && !mail.Valid(g.RecipientEmail):
		writeErr(w, 400, "the recipient's email does not look right")
		return
	case len(g.RecipientName) > 80 || len(g.BuyerName) > 80 || len(g.Note) > 300:
		writeErr(w, 400, "keep names under 80 characters and the message under 300")
		return
	}
	sentTo := firstNonEmpty(g.RecipientEmail, g.BuyerEmail)
	// The card is paid for in its own currency, through that country's provider, whoever is buying it.
	if s.payMode(country) != "live" {
		if _, err := s.issueBoughtGift(ctx, g, ""); err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		writeJSON(w, 201, M{"ok": true, "sent_to": sentTo, "country": country, "currency": currency})
		return
	}
	payload, _ := json.Marshal(g)
	ref, link, err := s.startPayment(ctx, payStart{Provider: providerFor(country), Purpose: "gift", Payload: payload, Amount: g.AmountCents, Currency: currency, Email: g.BuyerEmail, Description: "LogaLuxe gift card"})
	if err != nil {
		writeErr(w, 502, "the payment page could not be opened; nothing was charged, please try again")
		return
	}
	writeJSON(w, 201, M{"ok": true, "sent_to": sentTo, "country": country, "currency": currency, "payment": M{"url": link, "reference": ref, "provider": providerFor(country), "currency": currency}})
}

// giftPaid runs when a gift card's payment arrives.
func (s *Server) giftPaid(ctx context.Context, paymentID, ref string) error {
	var payload []byte
	if err := s.pool.QueryRow(ctx, `select payload from payments where id=$1`, paymentID).Scan(&payload); err != nil {
		return err
	}
	var g giftOrder
	if err := json.Unmarshal(payload, &g); err != nil {
		return err
	}
	_, err := s.issueBoughtGift(ctx, g, ref)
	return err
}
