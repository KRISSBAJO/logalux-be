package httpapi

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"logaluxe/api/internal/mail"
)

// Real money. Stripe takes payments for businesses in the United States and
// Paystack for businesses in Nigeria; each is used only when its secret key
// is set. Three things are paid for online:
//
//   deposit   a client pays the deposit that holds a booking
//   order     a client pays for a shop order
//   sale      the desk sends a client a link to pay for a visit or a sale
//
// The client pays on the provider's own page, so card details never touch
// LogaLuxe. A payment is confirmed three ways, all leading to settlePayment,
// which does its work once: when the client comes back from the provider's
// page, when the provider calls the webhook, and by the worker asking the
// provider about payments that are still open. That last one means it works
// on a laptop with no public address for webhooks.
//
// Money taken any other way at the desk (a card machine, cash, a bank
// transfer) is written down but never counted towards the payout balance,
// because LogaLuxe never held it.

type linkPaidKey struct{} // the checkout is being run for a pay link that has been paid
type dryRunKey struct{}   // the checkout is only being priced and checked

func providerFor(market string) string {
	if market == "NG" {
		return "paystack"
	}
	return "stripe"
}

func newPayRef() string {
	b := make([]byte, 12)
	_, _ = rand.Read(b)
	return "lx" + hex.EncodeToString(b)
}

type payStart struct {
	Provider, Purpose, BusinessID, BookingID, OrderID, MerchantID string
	Payload                                                       []byte
	Amount                                                        int
	Currency, Email, Description                                  string
	// Keep the card for later charges (a membership monthly renewal). The client is told on the provider page.
	SaveCard bool
	// The signed-in customer who is paying, when there is one. With saved cards switched on they may pay
	// with a card they kept (CardID) or keep the one they are about to use (KeepCard).
	UserID, CardID string
	KeepCard       bool
}

// startPayment opens a payment page at the provider and remembers it.
func (s *Server) startPayment(ctx context.Context, p payStart) (ref, link string, err error) {
	if p.Amount <= 0 {
		return "", "", errors.New("there is nothing to pay")
	}
	ref = newPayRef()
	back := strings.TrimRight(s.cfg.WebURL, "/") + "/pay/return?ref=" + ref
	cards := p.UserID != "" && s.featureOn("saved_cards")
	null := func(v string) any {
		if v == "" {
			return nil
		}
		return v
	}
	if cards && p.CardID != "" {
		// One tap: the money is taken from the kept card here and now. If the bank wants more, the page opens as usual.
		if chargeID, ok := s.chargeSaved(ctx, p, ref); ok {
			if _, err = s.pool.Exec(ctx, `insert into payments (reference, provider, purpose, business_id, booking_id, order_id, payload, merchant_id, amount_cents, currency, email, description, provider_id, url, save_card, user_id)
				values ($1,$2,$3,$4,$5,$6,$7::jsonb,$8,$9,$10,$11,$12,$13,'',false,$14)`, ref, p.Provider, p.Purpose, null(p.BusinessID), null(p.BookingID), null(p.OrderID), null(string(p.Payload)), null(p.MerchantID), p.Amount, p.Currency, p.Email, p.Description, chargeID, p.UserID); err != nil {
				return "", "", err
			}
			s.settlePayment(ctx, ref)
			return ref, "", nil
		}
		ref = newPayRef() // the provider has seen the first reference; the page gets its own
		back = strings.TrimRight(s.cfg.WebURL, "/") + "/pay/return?ref=" + ref
	}
	// Paystack needs an address it accepts, so a guest, or an address on a reserved test domain, gets a stand-in. Receipts to it go nowhere.
	if lower := strings.ToLower(p.Email); !mail.Valid(p.Email) || strings.HasSuffix(lower, ".test") || strings.HasSuffix(lower, ".example") || strings.HasSuffix(lower, ".invalid") || strings.HasSuffix(lower, ".localhost") {
		p.Email = "client+" + ref + "@logaxp.com"
	}
	providerID := ""
	switch p.Provider {
	case "stripe":
		var out struct {
			ID  string `json:"id"`
			URL string `json:"url"`
		}
		form := url.Values{
			"mode": {"payment"}, "success_url": {back}, "cancel_url": {back + "&cancelled=1"}, "client_reference_id": {ref},
			"line_items[0][quantity]": {"1"}, "line_items[0][price_data][currency]": {strings.ToLower(p.Currency)},
			"line_items[0][price_data][unit_amount]": {strconv.Itoa(p.Amount)}, "line_items[0][price_data][product_data][name]": {firstNonEmpty(p.Description, "LogaLuxe")},
			"metadata[ref]": {ref}, "payment_intent_data[metadata][ref]": {ref}, "expires_at": {strconv.FormatInt(time.Now().Add(31*time.Minute).Unix(), 10)},
		}
		if !strings.HasSuffix(p.Email, "@logaxp.com") {
			form.Set("customer_email", p.Email)
		}
		if p.SaveCard {
			form.Set("customer_creation", "always")
			form.Set("payment_intent_data[setup_future_usage]", "off_session")
		}
		if cards {
			// The payment hangs on the person's own Stripe customer, so a card they keep can be found again.
			if customer, cerr := s.stripeCustomer(ctx, p.UserID); cerr == nil {
				form.Del("customer_email")
				form.Del("customer_creation")
				form.Set("customer", customer)
				if p.KeepCard {
					form.Set("payment_intent_data[setup_future_usage]", "off_session")
				}
			}
		}
		if err = providerJSON(ctx, http.MethodPost, "https://api.stripe.com/v1/checkout/sessions", s.cfg.StripeSecret, form, nil, &out); err != nil {
			return "", "", err
		}
		providerID, link = out.ID, out.URL
	case "paystack":
		var out struct {
			Data struct {
				URL string `json:"authorization_url"`
			} `json:"data"`
		}
		if err = providerJSON(ctx, http.MethodPost, "https://api.paystack.co/transaction/initialize", s.cfg.PaystackSecret, nil,
			M{"email": p.Email, "amount": p.Amount, "currency": p.Currency, "reference": ref, "callback_url": back, "metadata": M{"ref": ref, "purpose": p.Purpose}}, &out); err != nil {
			return "", "", err
		}
		providerID, link = ref, out.Data.URL
	default:
		return "", "", errors.New("no payment provider for this market")
	}
	if link == "" {
		return "", "", errors.New("the payment provider did not return a payment page")
	}
	var payload any
	if len(p.Payload) > 0 {
		payload = string(p.Payload)
	}
	if _, err = s.pool.Exec(ctx, `insert into payments (reference, provider, purpose, business_id, booking_id, order_id, payload, merchant_id, amount_cents, currency, email, description, provider_id, url, save_card, user_id)
		values ($1,$2,$3,$4,$5,$6,$7::jsonb,$8,$9,$10,$11,$12,$13,$14,$15,$16)`, ref, p.Provider, p.Purpose, null(p.BusinessID), null(p.BookingID), null(p.OrderID), payload, null(p.MerchantID), p.Amount, p.Currency, p.Email, p.Description, providerID, link, p.SaveCard || (cards && p.KeepCard), null(p.UserID)); err != nil {
		return "", "", err
	}
	return ref, link, nil
}

// askProvider reports what the provider says about a payment: "paid", "failed", "expired" or "pending".
func (s *Server) askProvider(ctx context.Context, provider, providerID, ref string, wantAmount int) (state, intent string) {
	switch provider {
	case "stripe":
		if strings.HasPrefix(providerID, "pi_") { // taken from a kept card, with no payment page
			var pi struct {
				Status         string `json:"status"`
				AmountReceived int    `json:"amount_received"`
			}
			if err := providerJSON(ctx, http.MethodGet, "https://api.stripe.com/v1/payment_intents/"+url.PathEscape(providerID), s.cfg.StripeSecret, nil, nil, &pi); err != nil {
				return "pending", ""
			}
			switch {
			case pi.Status == "succeeded" && pi.AmountReceived >= wantAmount:
				return "paid", providerID
			case pi.Status == "canceled" || pi.Status == "requires_payment_method":
				return "failed", ""
			}
			return "pending", ""
		}
		var out struct {
			Status        string `json:"status"`
			PaymentStatus string `json:"payment_status"`
			PaymentIntent string `json:"payment_intent"`
			AmountTotal   int    `json:"amount_total"`
		}
		if err := providerJSON(ctx, http.MethodGet, "https://api.stripe.com/v1/checkout/sessions/"+url.PathEscape(providerID), s.cfg.StripeSecret, nil, nil, &out); err != nil {
			return "pending", ""
		}
		switch {
		case out.PaymentStatus == "paid" && out.AmountTotal >= wantAmount:
			return "paid", out.PaymentIntent
		case out.Status == "expired":
			return "expired", ""
		}
	case "paystack":
		var out struct {
			Data struct {
				Status string `json:"status"`
				Amount int    `json:"amount"`
				ID     int64  `json:"id"`
			} `json:"data"`
		}
		if err := providerJSON(ctx, http.MethodGet, "https://api.paystack.co/transaction/verify/"+url.PathEscape(ref), s.cfg.PaystackSecret, nil, nil, &out); err != nil {
			return "pending", ""
		}
		switch {
		case out.Data.Status == "success" && out.Data.Amount >= wantAmount:
			return "paid", strconv.FormatInt(out.Data.ID, 10)
		case out.Data.Status == "failed":
			return "failed", ""
		}
	}
	return "pending", ""
}

// settlePayment brings one payment up to date with the provider and, the first time it is seen as
// paid, does what the money was for. It is safe to call any number of times.
func (s *Server) settlePayment(ctx context.Context, ref string) M {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil
	}
	defer tx.Rollback(ctx)
	// Lock the payment before checking the provider. Webhooks, polling and the worker serialize here.
	var id string
	if err := tx.QueryRow(ctx, `select id::text from payments where reference=$1 for update`, ref).Scan(&id); err != nil {
		return nil
	}
	effects := []func(){}
	atomic := *s
	atomic.pool = &paymentDatabase{database: s.pool, tx: tx}
	atomic.paymentEffects = &effects
	out := atomic.settlePaymentLocked(ctx, ref)
	if out == nil && atomic.paymentFailure == nil {
		return nil
	}
	if atomic.paymentFailure != nil {
		_ = tx.Rollback(ctx)
		slog.Error("payment effects failed; retrying later", "reference", ref, "err", atomic.paymentFailure)
		_, _ = s.pool.Exec(ctx, `update payments set problem='Payment received; business records are awaiting reconciliation' where reference=$1 and status='pending'`, ref)
		return M{"reference": ref, "status": "pending", "problem": "Payment received; business records are awaiting reconciliation"}
	}
	if err := tx.Commit(ctx); err != nil {
		slog.Error("payment settlement rolled back", "reference", ref, "err", err)
		// Leave pending so a later webhook or sweep can retry the complete operation.
		_, _ = s.pool.Exec(ctx, `update payments set problem='Recording the payment failed; reconciliation will retry' where reference=$1 and status='pending'`, ref)
		return M{"reference": ref, "status": "pending", "problem": "Payment confirmation is being reconciled; please check again"}
	}
	for _, effect := range effects {
		effect()
	}
	return out
}

func (s *Server) settlePaymentLocked(ctx context.Context, ref string) M {
	p, err := row(ctx, s.pool, `select id, reference, provider, purpose, business_id, booking_id, order_id, sale_id, amount_cents, currency, status, provider_id, description, url, expires_at, problem, email,
		(select slug from businesses b where b.id = payments.business_id) as business_slug from payments where reference=$1`, ref)
	if err != nil {
		return nil
	}
	view := func() M {
		out, _ := row(ctx, s.pool, `select reference, purpose, status, amount_cents, currency, description, booking_id, order_id, sale_id, url, expires_at, problem,
			(select slug from businesses b where b.id = payments.business_id) as business_slug, (select name from businesses b where b.id = payments.business_id) as business from payments where reference=$1`, ref)
		return out
	}
	if p["status"] != "pending" {
		return view()
	}
	amount := int(toInt(p["amount_cents"]))
	state, intent := s.askProvider(ctx, fmt.Sprint(p["provider"]), fmt.Sprint(p["provider_id"]), ref, amount)
	if state == "pending" && time.Now().After(p["expires_at"].(time.Time).Add(5*time.Minute)) {
		state = "expired" // the page has closed and the provider still has no payment
	}
	if state == "pending" {
		return view()
	}
	// Claim it, so two callers arriving together do the work once.
	tag, err := s.pool.Exec(ctx, `update payments set status=$2, payment_intent=$3, paid_at = case when $2 = 'paid' then now() end where reference=$1 and status='pending'`, ref, state, intent)
	if err != nil || tag.RowsAffected() == 0 {
		return view()
	}
	id := fmt.Sprint(p["id"])
	switch {
	case state != "paid":
		if p["purpose"] == "deposit" && p["booking_id"] != nil {
			// The deposit never arrived, so the time goes back on sale.
			_, _ = s.pool.Exec(ctx, `update bookings set status='cancelled_client', cancel_reason='The deposit was not paid in time' where id=$1 and not deposit_paid and status in ('requested','confirmed')`, p["booking_id"])
		}
		if p["purpose"] == "order" && p["order_id"] != nil {
			if tag, err := s.pool.Exec(ctx, `update orders set status='cancelled' where id=$1 and status='pending'`, p["order_id"]); err == nil && tag.RowsAffected() == 1 {
				s.unwindOrder(ctx, fmt.Sprint(p["order_id"])) // nothing was paid, so the stock goes back on the shelf
			}
		}
	case p["purpose"] == "deposit":
		var live bool
		_ = s.pool.QueryRow(ctx, `update bookings set deposit_paid=true where id=$1 and status in ('requested','confirmed','checked_in','in_progress','completed') returning true`, p["booking_id"]).Scan(&live)
		if !live {
			// The booking was cancelled while the client was paying. Send the money straight back.
			_, _ = s.pool.Exec(ctx, `insert into payment_refund_jobs (payment_id, amount_cents) values ($1,$2) on conflict (payment_id) do nothing`, id, amount)
			break
		}
		_, _ = s.pool.Exec(ctx, `insert into ledger (business_id, kind, amount_cents, currency, method, status, booking_id, description)
			select bk.business_id, 'deposit', $2, $3, 'card', 'held', bk.id, 'Deposit · ' || bk.client_name from bookings bk where bk.id=$1
			and not exists (select 1 from ledger l where l.booking_id = bk.id and l.kind = 'deposit')`, p["booking_id"], amount, p["currency"])
		_, _ = s.pool.Exec(ctx, `insert into payment_events (provider, kind, booking_id, amount_cents, currency, status, reference) values ($1,'deposit',$2,$3,$4,'paid',$5)`, p["provider"], p["booking_id"], amount, p["currency"], ref)
		s.bookingPlaced(fmt.Sprint(p["booking_id"]), fmt.Sprint(p["email"]))
	case p["purpose"] == "order":
		_, _ = s.pool.Exec(ctx, `update orders set status='paid' where id=$1 and status='pending'`, p["order_id"])
		s.settleOrder(ctx, fmt.Sprint(p["order_id"]))
		_, _ = s.pool.Exec(ctx, `insert into payment_events (provider, kind, order_id, amount_cents, currency, status, reference) values ($1,'order',$2,$3,$4,'paid',$5)`, p["provider"], p["order_id"], amount, p["currency"], ref)
	case p["purpose"] == "gift":
		s.paymentFailure = s.giftPaid(ctx, id, ref)
	case p["purpose"] == "tip":
		s.recordTip(ctx, fmt.Sprint(p["booking_id"]), amount, "card", "pending")
	case p["purpose"] == "sale":
		s.paymentFailure = s.runPaidSale(ctx, id)
		s.rememberCard(ctx, id) // when the sale was a membership, so next month can be charged
	}
	if state == "paid" {
		s.keepCard(ctx, id) // when the customer asked to keep the card they paid with
	}
	return view()
}

// capture is a response writer that keeps what a handler wrote, for running a handler from inside the API.
type capture struct {
	code   int
	body   bytes.Buffer
	header http.Header
}

func (c *capture) Header() http.Header         { return c.header }
func (c *capture) WriteHeader(code int)        { c.code = code }
func (c *capture) Write(b []byte) (int, error) { return c.body.Write(b) }

// checkoutAs runs the checkout for a merchant with a stored request. The flag says why: a dry run, or a paid link.
func (s *Server) checkoutAs(ctx context.Context, m Merchant, body []byte, flag any) (int, M) {
	req, _ := http.NewRequestWithContext(context.WithValue(context.WithValue(ctx, merchantKey{}, m), flag, true), http.MethodPost, "/v1/m/checkout", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := &capture{code: 200, header: http.Header{}}
	s.mCheckoutPay(rec, req)
	var out M
	_ = json.Unmarshal(rec.body.Bytes(), &out)
	return rec.code, out
}

// merchantFor rebuilds the signed-in person a pay link was made by, as the owner would see the business.
func (s *Server) merchantFor(ctx context.Context, merchantID, businessID string) (Merchant, error) {
	var m Merchant
	var staffID *string
	err := s.pool.QueryRow(ctx, `select u.id::text, u.email, u.name, mm.role, mm.staff_id::text, b.id::text, b.name, b.slug, b.currency, b.timezone, b.market, b.plan, b.status
		from merchant_users u join merchant_members mm on mm.merchant_id = u.id join businesses b on b.id = mm.business_id where u.id=$1 and b.id=$2`, merchantID, businessID).
		Scan(&m.ID, &m.Email, &m.Name, &m.Role, &staffID, &m.BusinessID, &m.Business, &m.Slug, &m.Currency, &m.Timezone, &m.Market, &m.Plan, &m.Status)
	if err != nil {
		return m, err
	}
	if staffID != nil {
		m.StaffID = *staffID
	}
	if m.Loc, err = time.LoadLocation(m.Timezone); err != nil {
		m.Loc = time.UTC
	}
	m.Permissions = map[string]bool{"take_payments": true, "see_all_calendars": true, "see_reports": true} // the payment was already authorised when the link was made
	return m, nil
}

// runPaidSale records the sale a pay link was for, now that the money is in.
func (s *Server) runPaidSale(ctx context.Context, paymentID string) error {
	var merchantID, businessID string
	var payload []byte
	if err := s.pool.QueryRow(ctx, `select coalesce(merchant_id::text,''),business_id::text,payload from payments where id=$1`, paymentID).Scan(&merchantID, &businessID, &payload); err != nil {
		return err
	}
	m, err := s.merchantFor(ctx, merchantID, businessID)
	if err != nil {
		return err
	}
	code, out := s.checkoutAs(ctx, m, payload, linkPaidKey{})
	if code != 201 {
		return fmt.Errorf("sale could not be recorded: %v", out["error"])
	}
	_, err = s.pool.Exec(ctx, `update payments set sale_id=$2 where id=$1`, paymentID, out["sale_id"])
	return err
}

// refundDeposit returns a deposit that was paid online, when a booking is cancelled in the client's favour.
// It runs in the background and records any problem on the payment, where the console can see it.
func (s *Server) refundDeposit(bookingID string) {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
		defer cancel()
		var id string
		if err := s.pool.QueryRow(ctx, `select id::text from payments where booking_id=$1 and purpose='deposit' and status='paid' order by paid_at desc limit 1`, bookingID).Scan(&id); err != nil {
			return // simulated, or never paid online
		}
		if err := s.refundPayment(ctx, id, 0); err != nil {
			_, _ = s.pool.Exec(ctx, `update payments set problem=$2 where id=$1`, id, "The deposit refund failed: "+err.Error())
			slog.Error("deposit refund failed", "booking", bookingID, "err", err)
		}
	}()
}

// sweepPayments asks the providers about payments that are still open. The worker calls it every minute.
func (s *Server) sweepPayments(ctx context.Context) {
	s.reconcileRefunds(ctx)
	open, _ := rows(ctx, s.pool, `select reference from payments where status='pending' and created_at < now() - interval '20 seconds' order by created_at limit 60`)
	for _, p := range open {
		s.settlePayment(ctx, fmt.Sprint(p["reference"]))
	}
	sending, _ := rows(ctx, s.pool, `select id from payouts where status='sending' and created_at > now() - interval '7 days' limit 40`)
	for _, p := range sending {
		s.checkPayout(ctx, fmt.Sprint(p["id"]))
	}
}

// POST /v1/payments/{ref}/confirm   the client is back from the payment page
// The reference is long and random, so knowing it is the proof of being the payer.
func (s *Server) payConfirm(w http.ResponseWriter, r *http.Request) {
	out := s.settlePayment(r.Context(), chi.URLParam(r, "ref"))
	if out == nil {
		writeErr(w, 404, "payment not found")
		return
	}
	writeJSON(w, 200, M{"payment": out})
}

// POST /v1/m/checkout/link   the same body as POST /v1/m/checkout, plus {email}
// Checks and prices the sale, then makes a payment page for the client. The sale is recorded when they pay.
func (s *Server) mCheckoutLink(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	m := mc(r)
	if s.payMode(m.Market) != "live" {
		writeErr(w, 409, "online payments are not switched on for this market yet, so there is no link to send")
		return
	}
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<18))
	if err != nil {
		writeErr(w, 400, "invalid json")
		return
	}
	var body map[string]any
	if json.Unmarshal(raw, &body) != nil {
		writeErr(w, 400, "invalid json")
		return
	}
	email, _ := body["email"].(string)
	delete(body, "email")
	body["method"] = "link"
	clean, _ := json.Marshal(body)
	code, out := s.checkoutAs(ctx, m, clean, dryRunKey{})
	if code != 200 {
		writeErr(w, code, firstNonEmpty(fmt.Sprint(out["error"]), "the sale could not be priced"))
		return
	}
	due := int(toInt(out["total_cents"]))
	if f, ok := out["total_cents"].(float64); ok {
		due = int(f)
	}
	if due <= 0 {
		writeErr(w, 400, "there is nothing left to pay on this sale; record it as it is")
		return
	}
	who, _ := body["client_name"].(string)
	bookingID, _ := body["booking_id"].(string)
	if bookingID != "" {
		_ = s.pool.QueryRow(ctx, `select client_name, coalesce(nullif(client_email,''), $2) from bookings where id=$1`, bookingID, email).Scan(&who, &email)
	}
	if cid, _ := body["client_id"].(string); cid != "" {
		var onFile string
		_ = s.pool.QueryRow(ctx, `select name, email from clients where id=$1 and business_id=$2`, cid, m.BusinessID).Scan(&who, &onFile)
		email = firstNonEmpty(email, onFile)
	}
	// Selling a membership by link keeps the card, so the monthly renewal can be charged without the client there.
	saveCard := false
	if items, ok := body["items"].([]any); ok {
		for _, it := range items {
			if line, ok := it.(map[string]any); ok && line["kind"] == "membership" {
				saveCard = true
			}
		}
	}
	ref, link, err := s.startPayment(ctx, payStart{SaveCard: saveCard, Provider: providerFor(m.Market), Purpose: "sale", BusinessID: m.BusinessID, BookingID: bookingID, MerchantID: m.ID, Payload: clean,
		Amount: due, Currency: m.Currency, Email: email, Description: strings.TrimSpace(m.Business + " · " + firstNonEmpty(who, "Sale"))})
	if err != nil {
		writeErr(w, 502, "the payment page could not be made: "+err.Error())
		return
	}
	writeJSON(w, 201, M{"ok": true, "reference": ref, "url": link, "amount_cents": due, "currency": m.Currency, "expires_in": 1800, "totals": out})
}

// GET /v1/m/payments/{ref}   has the client paid yet? The till asks while the link is open.
func (s *Server) mPayment(w http.ResponseWriter, r *http.Request) {
	m := mc(r)
	ref := chi.URLParam(r, "ref")
	var mine bool
	_ = s.pool.QueryRow(r.Context(), `select exists(select 1 from payments where reference=$1 and business_id=$2)`, ref, m.BusinessID).Scan(&mine)
	if !mine {
		writeErr(w, 404, "payment not found")
		return
	}
	writeJSON(w, 200, M{"payment": s.settlePayment(r.Context(), ref)})
}

// GET /v1/m/payments   online payments for this business, newest first
func (s *Server) mPayments(w http.ResponseWriter, r *http.Request) {
	m := mc(r)
	out, paymentsPage, err := s.historyRows(r, "payments", `select reference, provider, purpose, amount_cents, currency, status, description, booking_id, sale_id, refunded_cents, problem, url, created_at, paid_at, expires_at
		from payments where business_id=$1 order by created_at desc`, "created_at desc", "created_at amount_cents status purpose provider", m.BusinessID)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, M{"payments": out, "payments_pagination": paymentsPage, "mode": s.payMode(m.Market), "provider": providerFor(m.Market)})
}

// ---------- payouts through the provider ----------

// sendPayout moves a payout's money to the business. Stripe: a transfer to the business's connected
// account, which Stripe then pays to their bank. Paystack: a transfer to their bank account.
func (s *Server) sendPayout(payoutID string) {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		var provider, external, currency, name string
		var amount int
		if err := s.pool.QueryRow(ctx, `select a.provider, a.external_id, p.currency, p.amount_cents, b.name from payouts p join payout_accounts a on a.id = p.account_id join businesses b on b.id = p.business_id
			where p.id=$1 and p.status='scheduled' and a.mode='live'`, payoutID).Scan(&provider, &external, &currency, &amount, &name); err != nil {
			return
		}
		if external == "" {
			s.failPayout(ctx, payoutID, "the payout account is not finished being set up")
			return
		}
		switch provider {
		case "stripe":
			var out struct {
				ID string `json:"id"`
			}
			err := providerJSON(ctx, http.MethodPost, "https://api.stripe.com/v1/transfers", s.cfg.StripeSecret,
				url.Values{"amount": {strconv.Itoa(amount)}, "currency": {strings.ToLower(currency)}, "destination": {external}, "description": {"LogaLuxe payout"}, "metadata[payout]": {payoutID}, "transfer_group": {payoutID}}, nil, &out)
			if err != nil {
				s.failPayout(ctx, payoutID, err.Error())
				return
			}
			_, _ = s.pool.Exec(ctx, `update payouts set status='paid', reference=$2, paid_at=now() where id=$1`, payoutID, out.ID)
		case "paystack":
			var out struct {
				Data struct {
					Status string `json:"status"`
					Code   string `json:"transfer_code"`
				} `json:"data"`
			}
			err := providerJSON(ctx, http.MethodPost, "https://api.paystack.co/transfer", s.cfg.PaystackSecret, nil,
				M{"source": "balance", "amount": amount, "recipient": external, "reason": "LogaLuxe payout · " + name, "reference": "lxp" + strings.ReplaceAll(payoutID, "-", "")}, &out)
			switch {
			case err != nil:
				s.failPayout(ctx, payoutID, err.Error())
			case out.Data.Status == "otp":
				s.failPayout(ctx, payoutID, "Paystack asked for a one-time code. Switch off the code for transfers in the Paystack dashboard, then pay out again.")
			case out.Data.Status == "success":
				_, _ = s.pool.Exec(ctx, `update payouts set status='paid', reference=$2, paid_at=now() where id=$1`, payoutID, out.Data.Code)
			default:
				_, _ = s.pool.Exec(ctx, `update payouts set status='sending', reference=$2 where id=$1`, payoutID, "lxp"+strings.ReplaceAll(payoutID, "-", ""))
			}
		default:
			s.failPayout(ctx, payoutID, "no provider can send this payout")
		}
	}()
}

// checkPayout asks Paystack whether a transfer that was still on its way has arrived.
func (s *Server) checkPayout(ctx context.Context, payoutID string) {
	var ref string
	if err := s.pool.QueryRow(ctx, `select reference from payouts where id=$1 and status='sending'`, payoutID).Scan(&ref); err != nil || ref == "" {
		return
	}
	var out struct {
		Data struct {
			Status string `json:"status"`
			Code   string `json:"transfer_code"`
		} `json:"data"`
	}
	if err := providerJSON(ctx, http.MethodGet, "https://api.paystack.co/transfer/verify/"+url.PathEscape(ref), s.cfg.PaystackSecret, nil, nil, &out); err != nil {
		return
	}
	switch out.Data.Status {
	case "success":
		_, _ = s.pool.Exec(ctx, `update payouts set status='paid', paid_at=now() where id=$1 and status='sending'`, payoutID)
	case "failed", "reversed", "abandoned", "blocked", "rejected":
		s.failPayout(ctx, payoutID, "the bank transfer was "+out.Data.Status)
	}
}

// failPayout marks a payout as failed and puts its money back in the business's balance.
func (s *Server) failPayout(ctx context.Context, payoutID, reason string) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return
	}
	defer tx.Rollback(ctx)
	var businessID, currency string
	var amount, fee int
	if err := tx.QueryRow(ctx, `update payouts set status='failed', failure_reason=$2 where id=$1 and status in ('scheduled','sending') returning business_id::text, currency, amount_cents, fee_cents`, payoutID, reason).Scan(&businessID, &currency, &amount, &fee); err != nil {
		return
	}
	if _, err := tx.Exec(ctx, `insert into ledger (business_id, kind, amount_cents, currency, method, status, payout_id, description) values ($1,'adjustment',$2,$3,'logaluxe','settled',$4,'Payout did not go through · returned to your balance')`,
		businessID, amount+fee, currency, payoutID); err != nil {
		return
	}
	_ = tx.Commit(ctx)
	slog.Error("payout failed", "payout", payoutID, "reason", reason)
	s.notifyBusiness(businessID, "new_booking_email", "A payout did not go through", "Your payout could not be sent: "+reason+"\n\nThe money is back in your LogaLuxe balance. Check your payout account: "+strings.TrimRight(s.cfg.WebURL, "/")+"/business/money/payout-account")
}

// ---------- webhooks ----------

// POST /v1/webhooks/stripe
func (s *Server) stripeWebhook(w http.ResponseWriter, r *http.Request) {
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
	if err != nil || s.cfg.StripeWebhookSecret == "" || !stripeSigned(raw, r.Header.Get("Stripe-Signature"), s.cfg.StripeWebhookSecret, time.Now()) {
		writeErr(w, 400, "signature not valid")
		return
	}
	var ev struct {
		Type string `json:"type"`
		Data struct {
			Object struct {
				Ref string `json:"client_reference_id"`
			} `json:"object"`
		} `json:"data"`
	}
	_ = json.Unmarshal(raw, &ev)
	if strings.HasPrefix(ev.Type, "checkout.session.") && ev.Data.Object.Ref != "" {
		s.settlePayment(r.Context(), ev.Data.Object.Ref)
	}
	writeJSON(w, 200, M{"received": true})
}

// stripeSigned checks Stripe's signature header: "t=<unix>,v1=<hmac sha256 of 't.body'>". Old messages are refused.
func stripeSigned(body []byte, header, secret string, now time.Time) bool {
	var ts string
	var sigs []string
	for _, part := range strings.Split(header, ",") {
		kv := strings.SplitN(strings.TrimSpace(part), "=", 2)
		if len(kv) != 2 {
			continue
		}
		switch kv[0] {
		case "t":
			ts = kv[1]
		case "v1":
			sigs = append(sigs, kv[1])
		}
	}
	sec, err := strconv.ParseInt(ts, 10, 64)
	if err != nil || now.Sub(time.Unix(sec, 0)) > 5*time.Minute || time.Unix(sec, 0).Sub(now) > 5*time.Minute {
		return false
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(ts + "."))
	mac.Write(body)
	want := hex.EncodeToString(mac.Sum(nil))
	for _, sig := range sigs {
		if hmac.Equal([]byte(sig), []byte(want)) {
			return true
		}
	}
	return false
}

// POST /v1/webhooks/paystack   signed with HMAC SHA-512 of the body, keyed by the secret key
func (s *Server) paystackWebhook(w http.ResponseWriter, r *http.Request) {
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
	if err != nil || s.cfg.PaystackSecret == "" || !paystackSigned(raw, r.Header.Get("X-Paystack-Signature"), s.cfg.PaystackSecret) {
		writeErr(w, 400, "signature not valid")
		return
	}
	var ev struct {
		Event string `json:"event"`
		Data  struct {
			Reference string `json:"reference"`
		} `json:"data"`
	}
	_ = json.Unmarshal(raw, &ev)
	switch {
	case ev.Event == "charge.success":
		s.settlePayment(r.Context(), ev.Data.Reference)
	case strings.HasPrefix(ev.Event, "transfer."):
		var id string
		if s.pool.QueryRow(r.Context(), `select id::text from payouts where reference=$1 and status='sending'`, ev.Data.Reference).Scan(&id) == nil {
			s.checkPayout(r.Context(), id)
		}
	}
	writeJSON(w, 200, M{"received": true})
}

func paystackSigned(body []byte, header, secret string) bool {
	mac := hmac.New(sha512.New, []byte(secret))
	mac.Write(body)
	return header != "" && hmac.Equal([]byte(strings.ToLower(header)), []byte(hex.EncodeToString(mac.Sum(nil))))
}

// POST /v1/m/checkout/quote   the same body as a sale; nothing is charged or kept.
// It answers what the sale would come to, so a till never has to work the total out itself.
func (s *Server) mCheckoutQuote(w http.ResponseWriter, r *http.Request) {
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<18))
	if err != nil {
		writeErr(w, 400, "invalid json")
		return
	}
	code, out := s.checkoutAs(r.Context(), mc(r), raw, dryRunKey{})
	if code != 200 {
		writeJSON(w, code, out)
		return
	}
	writeJSON(w, 200, M{"quote": out})
}
