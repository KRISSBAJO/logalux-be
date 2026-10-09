package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Calls to the payment providers that hold and pay out a business's money.
// Each is used only when its secret key is set. Without a key the payout
// account screens run in simulation, and say so.

var providerHTTP = &http.Client{Timeout: 20 * time.Second}

// The banks offered in simulation. With a Paystack key the live list is used.
var nigerianBanks = [][2]string{
	{"Access Bank", "044"}, {"Fidelity Bank", "070"}, {"First Bank of Nigeria", "011"}, {"First City Monument Bank", "214"},
	{"Guaranty Trust Bank", "058"}, {"Kuda Bank", "50211"}, {"Moniepoint MFB", "50515"}, {"OPay", "999992"}, {"PalmPay", "999991"},
	{"Stanbic IBTC Bank", "221"}, {"Sterling Bank", "232"}, {"Union Bank of Nigeria", "032"}, {"United Bank for Africa", "033"},
	{"Wema Bank", "035"}, {"Zenith Bank", "057"},
}

func providerJSON(ctx context.Context, method, endpoint, bearerKey string, form url.Values, jsonBody any, out any) error {
	return providerJSONWithKey(ctx, method, endpoint, bearerKey, form, jsonBody, out, "")
}

func providerJSONWithKey(ctx context.Context, method, endpoint, bearerKey string, form url.Values, jsonBody any, out any, idempotencyKey string) error {
	var body io.Reader
	contentType := ""
	if form != nil {
		body, contentType = strings.NewReader(form.Encode()), "application/x-www-form-urlencoded"
	} else if jsonBody != nil {
		b, _ := json.Marshal(jsonBody)
		body, contentType = strings.NewReader(string(b)), "application/json"
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint, body)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+bearerKey)
	if idempotencyKey != "" {
		req.Header.Set("Idempotency-Key", idempotencyKey)
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	res, err := providerHTTP.Do(req)
	if err != nil {
		return fmt.Errorf("the payment provider did not answer; try again")
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if res.StatusCode >= 300 {
		var e struct {
			Message string `json:"message"`
			Error   struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		_ = json.Unmarshal(raw, &e)
		return fmt.Errorf("%s", firstNonEmpty(firstNonEmpty(e.Error.Message, e.Message), "the payment provider refused the request"))
	}
	if out != nil {
		return json.Unmarshal(raw, out)
	}
	return nil
}

// paystackResolve asks the bank who owns an account, so the business confirms
// the right name before any money is sent there.
func (s *Server) paystackResolve(ctx context.Context, bankCode, accountNumber string) (string, error) {
	var out struct {
		Data struct {
			AccountName string `json:"account_name"`
		} `json:"data"`
	}
	err := providerJSON(ctx, http.MethodGet, "https://api.paystack.co/bank/resolve?account_number="+url.QueryEscape(accountNumber)+"&bank_code="+url.QueryEscape(bankCode), s.cfg.PaystackSecret, nil, nil, &out)
	return out.Data.AccountName, err
}

func (s *Server) paystackRecipient(ctx context.Context, name, bankCode, accountNumber string) (string, error) {
	var out struct {
		Data struct {
			RecipientCode string `json:"recipient_code"`
		} `json:"data"`
	}
	err := providerJSON(ctx, http.MethodPost, "https://api.paystack.co/transferrecipient", s.cfg.PaystackSecret, nil,
		M{"type": "nuban", "name": name, "account_number": accountNumber, "bank_code": bankCode, "currency": "NGN"}, &out)
	return out.Data.RecipientCode, err
}

func (s *Server) paystackBanks(ctx context.Context) [][2]string {
	var out struct {
		Data []struct {
			Name string `json:"name"`
			Code string `json:"code"`
		} `json:"data"`
	}
	if err := providerJSON(ctx, http.MethodGet, "https://api.paystack.co/bank?country=nigeria&perPage=200", s.cfg.PaystackSecret, nil, nil, &out); err != nil || len(out.Data) == 0 {
		return nigerianBanks
	}
	banks := make([][2]string, 0, len(out.Data))
	for _, b := range out.Data {
		banks = append(banks, [2]string{b.Name, b.Code})
	}
	return banks
}

// stripeOnboard creates a Stripe Express account (once) and returns a link to
// Stripe's own pages, where the business enters its bank and identity details.
// LogaLuxe never sees them.
func (s *Server) stripeOnboard(ctx context.Context, existingAccount, email, businessName, back string) (accountID, link string, err error) {
	accountID = existingAccount
	if accountID == "" {
		var acct struct {
			ID string `json:"id"`
		}
		form := url.Values{"type": {"express"}, "country": {"US"}, "email": {email}, "business_profile[name]": {businessName},
			"capabilities[transfers][requested]": {"true"}, "capabilities[card_payments][requested]": {"true"}}
		if err = providerJSON(ctx, http.MethodPost, "https://api.stripe.com/v1/accounts", s.cfg.StripeSecret, form, nil, &acct); err != nil {
			return "", "", err
		}
		accountID = acct.ID
	}
	if back == "" {
		back = strings.TrimRight(s.cfg.WebURL, "/") + "/business/money/payout-account"
	}
	var l struct {
		URL string `json:"url"`
	}
	form := url.Values{"account": {accountID}, "type": {"account_onboarding"}, "refresh_url": {back + "?stripe=retry"}, "return_url": {back + "?stripe=done"}}
	if err = providerJSON(ctx, http.MethodPost, "https://api.stripe.com/v1/account_links", s.cfg.StripeSecret, form, nil, &l); err != nil {
		return accountID, "", err
	}
	return accountID, l.URL, nil
}

// stripeStatus reads whether Stripe will pay the account out, and which bank it holds.
func (s *Server) stripeStatus(ctx context.Context, accountID string) (payouts bool, bank, last4 string, err error) {
	var a struct {
		PayoutsEnabled   bool `json:"payouts_enabled"`
		ExternalAccounts struct {
			Data []struct {
				BankName string `json:"bank_name"`
				Last4    string `json:"last4"`
			} `json:"data"`
		} `json:"external_accounts"`
	}
	if err = providerJSON(ctx, http.MethodGet, "https://api.stripe.com/v1/accounts/"+url.PathEscape(accountID), s.cfg.StripeSecret, nil, nil, &a); err != nil {
		return false, "", "", err
	}
	if len(a.ExternalAccounts.Data) > 0 {
		bank, last4 = a.ExternalAccounts.Data[0].BankName, a.ExternalAccounts.Data[0].Last4
	}
	return a.PayoutsEnabled, bank, last4, nil
}
