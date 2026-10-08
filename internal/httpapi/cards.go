package httpapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
)

// Saved cards. A signed-in customer may keep a card when they pay and use it again in one tap.
// Stripe keeps its cards against a Stripe customer; Paystack hands back a reusable authorisation,
// which is all that is stored here. No card number ever reaches LogaLuxe.
// Only live when an admin has switched "Saved cards" on.

// stripeCustomer returns the person's Stripe customer, making it the first time.
func (s *Server) stripeCustomer(ctx context.Context, userID string) (string, error) {
	var id, email, first, last string
	if err := s.pool.QueryRow(ctx, `select coalesce(stripe_customer_id,''), coalesce(email,''), first_name, last_name from users where id=$1`, userID).Scan(&id, &email, &first, &last); err != nil {
		return "", err
	}
	if id != "" {
		return id, nil
	}
	form := url.Values{"name": {strings.TrimSpace(first + " " + last)}, "metadata[user]": {userID}}
	if email != "" && !strings.HasSuffix(strings.ToLower(email), ".test") {
		form.Set("email", email)
	}
	var out struct {
		ID string `json:"id"`
	}
	if err := providerJSON(ctx, http.MethodPost, "https://api.stripe.com/v1/customers", s.cfg.StripeSecret, form, nil, &out); err != nil || out.ID == "" {
		return "", errors.New("the card provider could not be reached")
	}
	_, _ = s.pool.Exec(ctx, `update users set stripe_customer_id=$2 where id=$1 and stripe_customer_id is null`, userID, out.ID)
	return out.ID, nil
}

type stripeCard struct {
	ID   string `json:"id"`
	Card struct {
		Brand    string `json:"brand"`
		Last4    string `json:"last4"`
		ExpMonth int    `json:"exp_month"`
		ExpYear  int    `json:"exp_year"`
	} `json:"card"`
}

// userCards lists the cards a person has kept, from both providers.
func (s *Server) userCards(ctx context.Context, userID string) []M {
	out := []M{}
	var customer string
	_ = s.pool.QueryRow(ctx, `select coalesce(stripe_customer_id,'') from users where id=$1`, userID).Scan(&customer)
	if customer != "" && s.cfg.StripeSecret != "" {
		var list struct {
			Data []stripeCard `json:"data"`
		}
		if providerJSON(ctx, http.MethodGet, "https://api.stripe.com/v1/customers/"+url.PathEscape(customer)+"/payment_methods?type=card&limit=20", s.cfg.StripeSecret, nil, nil, &list) == nil {
			seen := map[string]bool{}
			for _, c := range list.Data {
				key := c.Card.Brand + c.Card.Last4 + strconv.Itoa(c.Card.ExpMonth) + strconv.Itoa(c.Card.ExpYear)
				if seen[key] { // the same card kept twice shows once
					continue
				}
				seen[key] = true
				out = append(out, M{"id": c.ID, "provider": "stripe", "currency": "USD", "brand": c.Card.Brand, "last4": c.Card.Last4, "exp_month": c.Card.ExpMonth, "exp_year": c.Card.ExpYear})
			}
		}
	}
	saved, _ := rows(ctx, s.pool, `select id::text as id, provider, brand, last4, exp_month, exp_year from user_cards where user_id=$1 order by created_at desc`, userID)
	for _, c := range saved {
		month, _ := strconv.Atoi(fmt.Sprint(c["exp_month"]))
		year, _ := strconv.Atoi(fmt.Sprint(c["exp_year"]))
		out = append(out, M{"id": c["id"], "provider": c["provider"], "currency": "NGN", "brand": c["brand"], "last4": c["last4"], "exp_month": month, "exp_year": year})
	}
	return out
}

// GET /v1/auth/cards
func (s *Server) authCards(w http.ResponseWriter, r *http.Request) {
	if !s.featureOn("saved_cards") {
		writeJSON(w, 200, M{"enabled": false, "cards": []M{}})
		return
	}
	writeJSON(w, 200, M{"enabled": true, "cards": s.userCards(r.Context(), currentCustomer(r).ID)})
}

// DELETE /v1/auth/cards/{id}
func (s *Server) authCardDelete(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id, userID := chi.URLParam(r, "id"), currentCustomer(r).ID
	if strings.HasPrefix(id, "pm_") {
		// Only a card that hangs on this person's own Stripe customer may be removed.
		var customer string
		_ = s.pool.QueryRow(ctx, `select coalesce(stripe_customer_id,'') from users where id=$1`, userID).Scan(&customer)
		var pm struct {
			Customer string `json:"customer"`
		}
		if customer == "" || providerJSON(ctx, http.MethodGet, "https://api.stripe.com/v1/payment_methods/"+url.PathEscape(id), s.cfg.StripeSecret, nil, nil, &pm) != nil || pm.Customer != customer {
			writeErr(w, 404, "card not found")
			return
		}
		if err := providerJSON(ctx, http.MethodPost, "https://api.stripe.com/v1/payment_methods/"+url.PathEscape(id)+"/detach", s.cfg.StripeSecret, url.Values{}, nil, nil); err != nil {
			writeErr(w, 502, "the card could not be removed just now; try again")
			return
		}
		writeJSON(w, 200, M{"ok": true})
		return
	}
	tag, err := s.pool.Exec(ctx, `delete from user_cards where id::text=$1 and user_id=$2`, id, userID)
	if err != nil || tag.RowsAffected() == 0 {
		writeErr(w, 404, "card not found")
		return
	}
	writeJSON(w, 200, M{"ok": true})
}

// chargeSaved takes a payment from a card the person kept, without a payment page. It answers the
// provider's id for the charge and true when the money was taken. Anything else (the bank wants the
// person to approve it, the card was declined, the card is not theirs) answers false, and the caller
// opens the ordinary payment page instead.
func (s *Server) chargeSaved(ctx context.Context, p payStart, ref string) (string, bool) {
	switch p.Provider {
	case "stripe":
		if !strings.HasPrefix(p.CardID, "pm_") {
			return "", false
		}
		var customer string
		_ = s.pool.QueryRow(ctx, `select coalesce(stripe_customer_id,'') from users where id=$1`, p.UserID).Scan(&customer)
		if customer == "" {
			return "", false
		}
		form := url.Values{"amount": {strconv.Itoa(p.Amount)}, "currency": {strings.ToLower(p.Currency)}, "customer": {customer}, "payment_method": {p.CardID},
			"confirm": {"true"}, "off_session": {"true"}, "description": {firstNonEmpty(p.Description, "LogaLuxe")}, "metadata[ref]": {ref}}
		var out struct {
			ID             string `json:"id"`
			Status         string `json:"status"`
			AmountReceived int    `json:"amount_received"`
		}
		if err := providerJSON(ctx, http.MethodPost, "https://api.stripe.com/v1/payment_intents", s.cfg.StripeSecret, form, nil, &out); err != nil {
			return "", false
		}
		return out.ID, out.Status == "succeeded" && out.AmountReceived >= p.Amount
	case "paystack":
		var token, email string
		if err := s.pool.QueryRow(ctx, `select token, email from user_cards where id::text=$1 and user_id=$2 and provider='paystack'`, p.CardID, p.UserID).Scan(&token, &email); err != nil {
			return "", false
		}
		var out struct {
			Data struct {
				Status string `json:"status"`
				Amount int    `json:"amount"`
			} `json:"data"`
		}
		if err := providerJSON(ctx, http.MethodPost, "https://api.paystack.co/transaction/charge_authorization", s.cfg.PaystackSecret, nil,
			M{"email": email, "amount": p.Amount, "currency": p.Currency, "authorization_code": token, "reference": ref, "metadata": M{"ref": ref, "purpose": p.Purpose}}, &out); err != nil {
			return "", false
		}
		return ref, out.Data.Status == "success" && out.Data.Amount >= p.Amount
	}
	return "", false
}

// keepCard runs after a payment is paid: when the person asked to keep the card and the provider
// is Paystack, its reusable authorisation is stored. Stripe has already kept the card itself.
func (s *Server) keepCard(ctx context.Context, paymentID string) {
	var provider, ref string
	var userID *string
	if err := s.pool.QueryRow(ctx, `select provider, reference, user_id::text from payments where id=$1 and save_card and status='paid' and user_id is not null`, paymentID).Scan(&provider, &ref, &userID); err != nil || userID == nil || provider != "paystack" {
		return
	}
	var out struct {
		Data struct {
			Authorization struct {
				Code     string `json:"authorization_code"`
				Reusable bool   `json:"reusable"`
				Brand    string `json:"brand"`
				CardType string `json:"card_type"`
				Last4    string `json:"last4"`
				ExpMonth string `json:"exp_month"`
				ExpYear  string `json:"exp_year"`
			} `json:"authorization"`
			Customer struct {
				Email string `json:"email"`
			} `json:"customer"`
		} `json:"data"`
	}
	if providerJSON(ctx, http.MethodGet, "https://api.paystack.co/transaction/verify/"+url.PathEscape(ref), s.cfg.PaystackSecret, nil, nil, &out) != nil {
		return
	}
	a := out.Data.Authorization
	if a.Code == "" || !a.Reusable || out.Data.Customer.Email == "" {
		return
	}
	// The same card kept again replaces the old authorisation.
	_, _ = s.pool.Exec(ctx, `delete from user_cards where user_id=$1 and provider='paystack' and last4=$2 and exp_month=$3 and exp_year=$4`, *userID, a.Last4, a.ExpMonth, a.ExpYear)
	_, _ = s.pool.Exec(ctx, `insert into user_cards (user_id, provider, token, email, brand, last4, exp_month, exp_year) values ($1,'paystack',$2,$3,$4,$5,$6,$7) on conflict do nothing`,
		*userID, a.Code, out.Data.Customer.Email, strings.TrimSpace(firstNonEmpty(a.Brand, a.CardType)), a.Last4, a.ExpMonth, a.ExpYear)
}
