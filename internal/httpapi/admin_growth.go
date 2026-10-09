package httpapi

import (
	"context"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"

	"logaluxe/api/internal/mail"
)

// Promo codes and gift cards: the admin side, and the checks checkout runs.
// The server works out every discount itself. Nothing the browser sends about
// an amount is trusted.

var promoRe = regexp.MustCompile(`^[A-Z0-9][A-Z0-9-]{2,23}$`)

type querier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// promoDiscount returns what a code takes off, or a plain reason it cannot be used.
// Pass a transaction and lock=true when the result will be committed.
// A code made by a business works only for that business; pass its id, or "" where no business is involved.
func promoDiscount(ctx context.Context, q querier, code, scope, currency string, subtotal int, lock bool, businessID string) (id string, discount int, reason string) {
	code = strings.ToUpper(strings.TrimSpace(code))
	if code == "" {
		return "", 0, ""
	}
	sql := `select id::text, kind, value, currency, applies_to, min_cents, max_uses, used, starts_at, ends_at, active, coalesce(business_id::text,'') from promo_codes where code=$1`
	if lock {
		sql += " for update"
	}
	var kind, cur, applies string
	var value, minCents, used int
	var maxUses *int
	var starts, ends *time.Time
	var active bool
	var owner string
	if err := q.QueryRow(ctx, sql, code).Scan(&id, &kind, &value, &cur, &applies, &minCents, &maxUses, &used, &starts, &ends, &active, &owner); err != nil {
		return "", 0, "that promo code is not valid"
	}
	if owner != "" && owner != businessID {
		return "", 0, "that promo code is for a different business"
	}
	now := time.Now()
	switch {
	case !active:
		return "", 0, "that promo code is no longer active"
	case starts != nil && now.Before(*starts):
		return "", 0, "that promo code has not started yet"
	case ends != nil && now.After(*ends):
		return "", 0, "that promo code has expired"
	case maxUses != nil && used >= *maxUses:
		return "", 0, "that promo code has been used up"
	case applies != "both" && applies != scope:
		return "", 0, "that promo code does not apply here"
	case subtotal < minCents:
		return "", 0, "your total is below the minimum for that promo code"
	case kind == "fixed" && cur != currency:
		return "", 0, "that promo code is for a different currency"
	}
	if kind == "percent" {
		discount = (subtotal*value + 50) / 100
	} else {
		discount = value
	}
	if discount > subtotal {
		discount = subtotal
	}
	return id, discount, ""
}

// giftBalance returns a usable card's id and balance, or a plain reason.
func giftBalance(ctx context.Context, q querier, code, currency string, lock bool) (id string, balance int, reason string) {
	code = strings.ToUpper(strings.TrimSpace(code))
	if code == "" {
		return "", 0, ""
	}
	sql := `select id::text, balance_cents, currency, status, expires_on from gift_cards where code=$1`
	if lock {
		sql += " for update"
	}
	var cur, status string
	var expires *time.Time
	if err := q.QueryRow(ctx, sql, code).Scan(&id, &balance, &cur, &status, &expires); err != nil {
		return "", 0, "that gift card is not valid"
	}
	switch {
	case status != "active":
		return "", 0, "that gift card has been cancelled"
	case expires != nil && time.Now().After(expires.Add(24*time.Hour)):
		return "", 0, "that gift card has expired"
	case cur != currency:
		return "", 0, "that gift card is in a different currency"
	case balance == 0:
		return "", 0, "that gift card has no balance left"
	}
	return id, balance, ""
}

// POST /v1/checkout/check   {scope, currency, subtotal_cents, promo_code, gift_code}
// Lets the cart show a discount before paying. The order endpoint checks again.
func (s *Server) checkoutCheck(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Scope     string `json:"scope"`
		Currency  string `json:"currency"`
		Subtotal  int    `json:"subtotal_cents"`
		PromoCode string `json:"promo_code"`
		GiftCode  string `json:"gift_code"`
		Business  string `json:"business_slug"` // lets a business's own code be checked for a booking with it
	}
	if err := readJSON(r, &req); err != nil || req.Subtotal < 0 {
		writeErr(w, 400, "invalid request")
		return
	}
	if req.Scope != "bookings" {
		req.Scope = "orders"
	}
	if req.Currency == "" {
		req.Currency = "USD"
	}
	businessID := ""
	if req.Business != "" {
		_ = s.pool.QueryRow(r.Context(), `select id::text from businesses where slug=$1`, req.Business).Scan(&businessID)
	}
	_, discount, promoMsg := promoDiscount(r.Context(), s.pool, req.PromoCode, req.Scope, req.Currency, req.Subtotal, false, businessID)
	_, balance, giftMsg := giftBalance(r.Context(), s.pool, req.GiftCode, req.Currency, false)
	writeJSON(w, 200, M{"discount_cents": discount, "promo_error": promoMsg, "gift_balance_cents": balance, "gift_error": giftMsg})
}

// ---------- promo codes ----------

// GET /v1/admin/promos
func (s *Server) adminPromos(w http.ResponseWriter, r *http.Request) {
	out, err := rows(r.Context(), s.pool, `select *, (active and (starts_at is null or starts_at <= now()) and (ends_at is null or ends_at > now()) and (max_uses is null or used < max_uses)) as usable
		from promo_codes order by active desc, created_at desc`)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, M{"promos": out})
}

type promoReq struct {
	Code        string `json:"code"`
	Description string `json:"description"`
	Kind        string `json:"kind"`
	Value       int    `json:"value"`
	Currency    string `json:"currency"`
	AppliesTo   string `json:"applies_to"`
	MinCents    int    `json:"min_cents"`
	MaxUses     *int   `json:"max_uses"`
	StartsAt    string `json:"starts_at"` // YYYY-MM-DD or empty
	EndsAt      string `json:"ends_at"`
}

func day(v string, endOfDay bool) (*time.Time, bool) {
	if v == "" {
		return nil, true
	}
	t, err := time.Parse("2006-01-02", v)
	if err != nil {
		return nil, false
	}
	if endOfDay {
		t = t.Add(24*time.Hour - time.Second)
	}
	return &t, true
}

// POST /v1/admin/promos
func (s *Server) adminPromoCreate(w http.ResponseWriter, r *http.Request) {
	var req promoReq
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, "invalid json")
		return
	}
	req.Code = strings.ToUpper(strings.TrimSpace(req.Code))
	if req.Currency == "" {
		req.Currency = "USD"
	}
	if req.AppliesTo == "" {
		req.AppliesTo = "both"
	}
	starts, ok1 := day(req.StartsAt, false)
	ends, ok2 := day(req.EndsAt, true)
	switch {
	case !promoRe.MatchString(req.Code):
		writeErr(w, 400, "the code must be 3 to 24 letters, numbers or dashes")
		return
	case req.Kind != "percent" && req.Kind != "fixed":
		writeErr(w, 400, "choose a percentage or a fixed amount")
		return
	case req.Value <= 0 || (req.Kind == "percent" && req.Value > 100):
		writeErr(w, 400, "the value must be above zero, and a percentage at most 100")
		return
	case req.Currency != "USD" && req.Currency != "NGN":
		writeErr(w, 400, "currency must be USD or NGN")
		return
	case req.AppliesTo != "orders" && req.AppliesTo != "bookings" && req.AppliesTo != "both":
		writeErr(w, 400, "applies_to must be orders, bookings or both")
		return
	case req.MinCents < 0 || (req.MaxUses != nil && *req.MaxUses <= 0):
		writeErr(w, 400, "the minimum and the use limit cannot be negative or zero")
		return
	case !ok1 || !ok2 || (starts != nil && ends != nil && ends.Before(*starts)):
		writeErr(w, 400, "check the start and end dates")
		return
	}
	var id string
	err := s.pool.QueryRow(r.Context(), `insert into promo_codes (code, description, kind, value, currency, applies_to, min_cents, max_uses, starts_at, ends_at, created_by)
		values ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11) returning id::text`, req.Code, req.Description, req.Kind, req.Value, req.Currency, req.AppliesTo, req.MinCents, req.MaxUses, starts, ends, s.actor(r)).Scan(&id)
	if err != nil {
		if isCode(err, "23505") {
			writeErr(w, 409, "that code already exists")
			return
		}
		writeErr(w, 500, err.Error())
		return
	}
	s.audit(r, "promo.create", req.Code, nil, req)
	writeJSON(w, 201, M{"ok": true, "id": id})
}

// PUT /v1/admin/promos/{id}   {active}
func (s *Server) adminPromoUpdate(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Active *bool `json:"active"`
	}
	if err := readJSON(r, &req); err != nil || req.Active == nil {
		writeErr(w, 400, "active is required")
		return
	}
	var code string
	if err := s.pool.QueryRow(r.Context(), `update promo_codes set active=$2 where id=$1 returning code`, chi.URLParam(r, "id"), *req.Active).Scan(&code); err != nil {
		writeErr(w, 404, "promo code not found")
		return
	}
	s.audit(r, "promo.update", code, nil, req)
	writeJSON(w, 200, M{"ok": true})
}

// DELETE /v1/admin/promos/{id}   only a code nobody has used
func (s *Server) adminPromoDelete(w http.ResponseWriter, r *http.Request) {
	var code string
	var used int
	if err := s.pool.QueryRow(r.Context(), `select code, used from promo_codes where id=$1`, chi.URLParam(r, "id")).Scan(&code, &used); err != nil {
		writeErr(w, 404, "promo code not found")
		return
	}
	if used > 0 {
		writeErr(w, 409, "this code has been used, so it is kept for the record; switch it off instead")
		return
	}
	_, _ = s.pool.Exec(r.Context(), `delete from promo_codes where id=$1`, chi.URLParam(r, "id"))
	s.audit(r, "promo.delete", code, nil, nil)
	writeJSON(w, 200, M{"ok": true})
}

// ---------- gift cards ----------

// The most ops may put on one card; above it a super admin must issue the card. The console prints it from here.
var giftCardMaxCents = map[string]int{"USD": 50000, "NGN": 75000000} // 500 USD or 750,000 NGN

// GET /v1/admin/gift-cards?q=
func (s *Server) adminGiftCards(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	out, err := rows(ctx, s.pool, `select g.*, (select count(*) from gift_card_txns t where t.gift_card_id = g.id and t.amount_cents < 0) as uses
		from gift_cards g where ($1 = '' or g.code ilike '%'||$1||'%' or g.recipient_name ilike '%'||$1||'%' or g.recipient_email ilike '%'||$1||'%')
		order by g.created_at desc limit 300`, r.URL.Query().Get("q"))
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	// A code is as good as money: only roles that can issue cards see it in full.
	if roleRank[currentAdmin(r).Role] < roleRank["ops"] {
		for _, c := range out {
			if code, ok := c["code"].(string); ok && len(code) > 4 {
				c["code"] = "ending " + code[len(code)-4:]
			}
		}
	}
	totals, _ := rows(ctx, s.pool, `select currency, count(*) as n, coalesce(sum(initial_cents),0) as issued_cents, coalesce(sum(balance_cents) filter (where status='active'),0) as outstanding_cents from gift_cards group by currency order by currency`)
	txns, _ := rows(ctx, s.pool, `select t.id, t.amount_cents, t.note, t.actor, t.created_at, t.order_id, g.code, g.currency from gift_card_txns t join gift_cards g on g.id = t.gift_card_id order by t.created_at desc limit 40`)
	writeJSON(w, 200, M{"cards": out, "totals": totals, "txns": txns, "mail_mode": s.mail.Mode(), "max_cents": giftCardMaxCents})
}

// POST /v1/admin/gift-cards
func (s *Server) adminGiftCardIssue(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var req struct {
		AmountCents    int    `json:"amount_cents"`
		Currency       string `json:"currency"`
		RecipientName  string `json:"recipient_name"`
		RecipientEmail string `json:"recipient_email"`
		Note           string `json:"note"`
		ExpiresOn      string `json:"expires_on"`
		SendEmail      bool   `json:"send_email"`
	}
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, "invalid json")
		return
	}
	if req.Currency == "" {
		req.Currency = "USD"
	}
	limit := giftCardMaxCents[req.Currency]
	expires, okDate := day(req.ExpiresOn, false)
	req.RecipientEmail = strings.TrimSpace(req.RecipientEmail)
	switch {
	case limit == 0:
		writeErr(w, 400, "currency must be USD or NGN")
		return
	case req.AmountCents <= 0:
		writeErr(w, 400, "the amount must be above zero")
		return
	case req.AmountCents > limit && roleRank[currentAdmin(r).Role] < roleRank["super_admin"]:
		writeErr(w, 403, "a card this large needs a super admin")
		return
	case !okDate:
		writeErr(w, 400, "check the expiry date")
		return
	case req.RecipientEmail != "" && !mail.Valid(req.RecipientEmail):
		writeErr(w, 400, "that email address does not look right")
		return
	case req.SendEmail && req.RecipientEmail == "":
		writeErr(w, 400, "add the recipient's email to send the card")
		return
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	defer tx.Rollback(ctx)
	var id, code string
	for attempt := 0; attempt < 4; attempt++ { // a clash is close to impossible; try again if it happens
		c, err := randomCode(3, 4)
		if err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		code = "LX-" + c
		err = tx.QueryRow(ctx, `insert into gift_cards (code, initial_cents, balance_cents, currency, recipient_name, recipient_email, note, expires_on, issued_by)
			values ($1,$2,$2,$3,$4,$5,$6,$7,$8) on conflict (code) do nothing returning id::text`, code, req.AmountCents, req.Currency, req.RecipientName, req.RecipientEmail, req.Note, expires, s.actor(r)).Scan(&id)
		if err == nil {
			break
		}
	}
	if id == "" {
		writeErr(w, 500, "could not create the card; try again")
		return
	}
	if _, err := tx.Exec(ctx, `insert into gift_card_txns (gift_card_id, amount_cents, note, actor) values ($1,$2,'Issued',$3)`, id, req.AmountCents, s.actor(r)); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if err := tx.Commit(ctx); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	delivery := ""
	if req.SendEmail {
		amount := formatMoney(req.AmountCents, req.Currency)
		body := "Hello " + firstNonEmpty(req.RecipientName, "there") + ",\n\nYou have a LogaLuxe gift card worth " + amount + ".\n\nYour code: " + code +
			"\n\nEnter it at checkout in the LogaLuxe shop: " + strings.TrimRight(s.cfg.WebURL, "/") + "/shop\n"
		if req.Note != "" {
			body += "\nA note for you: " + req.Note + "\n"
		}
		if expires != nil {
			body += "\nIt can be used until " + expires.Format("2 January 2006") + ".\n"
		}
		body += "\nLogaLuxe"
		var err error
		if delivery, err = s.mail.Send(ctx, req.RecipientEmail, "Your LogaLuxe gift card: "+amount, body); err != nil {
			delivery = "failed"
			s.logMailFailure("gift card", req.RecipientEmail, err)
		}
	}
	// The code itself is not written to the audit log: it is as good as money.
	s.audit(r, "giftcard.issue", id, nil, M{"amount_cents": req.AmountCents, "currency": req.Currency, "recipient": req.RecipientEmail, "email": delivery})
	writeJSON(w, 201, M{"ok": true, "id": id, "code": code, "email": delivery})
}

// POST /v1/admin/gift-cards/{id}/void   {reason}
func (s *Server) adminGiftCardVoid(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id := chi.URLParam(r, "id")
	var req struct {
		Reason string `json:"reason"`
	}
	if err := readJSON(r, &req); err != nil || strings.TrimSpace(req.Reason) == "" {
		writeErr(w, 400, "a reason is required to cancel a gift card")
		return
	}
	var balance int
	err := s.pool.QueryRow(ctx, `with old as (select balance_cents from gift_cards where id=$1 and status='active' for update)
		update gift_cards g set status='void', balance_cents=0 from old where g.id=$1 returning old.balance_cents`, id).Scan(&balance)
	if err != nil {
		writeErr(w, 404, "no active gift card with that id")
		return
	}
	if balance > 0 {
		_, _ = s.pool.Exec(ctx, `insert into gift_card_txns (gift_card_id, amount_cents, note, actor) values ($1,$2,$3,$4)`, id, -balance, "Cancelled: "+req.Reason, s.actor(r))
	}
	s.audit(r, "giftcard.void", id, M{"balance_cents": balance}, M{"reason": req.Reason})
	writeJSON(w, 200, M{"ok": true})
}

func firstNonEmpty(a, b string) string {
	if strings.TrimSpace(a) != "" {
		return a
	}
	return b
}

func formatMoney(cents int, currency string) string {
	whole, frac := cents/100, cents%100
	// Thousands separators.
	digits := []byte{}
	str := []byte(itoa(whole))
	for i, c := range str {
		if i > 0 && (len(str)-i)%3 == 0 {
			digits = append(digits, ',')
		}
		digits = append(digits, c)
	}
	if currency == "NGN" {
		return "NGN " + string(digits)
	}
	out := "$" + string(digits)
	if frac != 0 {
		out += "." + string([]byte{byte('0' + frac/10), byte('0' + frac%10)})
	}
	return out
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}
