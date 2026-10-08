package httpapi

import (
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"strings"
)

// Text messages. Twilio sends for businesses in the United States and Termii
// for businesses in Nigeria. Nothing is sent unless SMS_ENABLED=true as well
// as the provider's keys, because a text costs money and reaches a real
// phone: the sample data must never text anyone by accident.

// Numbers that are never texted: the 555 range North America reserves for fiction, and the sample clients' range.
var neverText = regexp.MustCompile(`^\+1\d{3}555\d{4}$|^\+234803555\d{4}$`)

// smsMode says whether a text for this market would really go out: "live" or "log".
func (s *Server) smsMode(market string) string {
	if !s.cfg.SMSEnabled {
		return "log"
	}
	if market == "NG" && s.cfg.TermiiKey != "" && s.cfg.TermiiFrom != "" {
		return "live"
	}
	if market != "NG" && s.cfg.TwilioSID != "" && s.cfg.TwilioToken != "" && s.cfg.TwilioFrom != "" {
		return "live"
	}
	return "log"
}

// sendSMS texts a client on behalf of a business. It returns "delivered" when the provider accepted the
// message, "logged" when texts are off or the number is one that must not be texted, and "failed" otherwise.
func (s *Server) sendSMS(ctx context.Context, businessID, to, body string) string {
	var market string
	_ = s.pool.QueryRow(ctx, `select market from businesses where id=$1`, businessID).Scan(&market)
	to = strings.ReplaceAll(strings.TrimSpace(to), " ", "")
	if s.smsMode(market) != "live" || !strings.HasPrefix(to, "+") || neverText.MatchString(to) {
		slog.Info("text recorded, not sent", "to", to, "body", body)
		return "logged"
	}
	if len(body) > 600 {
		body = body[:600]
	}
	var req *http.Request
	var err error
	if market == "NG" {
		base := strings.TrimRight(firstNonEmpty(s.cfg.TermiiBase, "https://api.ng.termii.com"), "/")
		payload := fmt.Sprintf(`{"api_key":%q,"to":%q,"from":%q,"sms":%q,"type":"plain","channel":"generic"}`, s.cfg.TermiiKey, strings.TrimPrefix(to, "+"), s.cfg.TermiiFrom, body)
		req, err = http.NewRequestWithContext(ctx, http.MethodPost, base+"/api/sms/send", strings.NewReader(payload))
		if err == nil {
			req.Header.Set("Content-Type", "application/json")
		}
	} else {
		form := url.Values{"To": {to}, "From": {s.cfg.TwilioFrom}, "Body": {body}}
		req, err = http.NewRequestWithContext(ctx, http.MethodPost, "https://api.twilio.com/2010-04-01/Accounts/"+url.PathEscape(s.cfg.TwilioSID)+"/Messages.json", strings.NewReader(form.Encode()))
		if err == nil {
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			req.Header.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(s.cfg.TwilioSID+":"+s.cfg.TwilioToken)))
		}
	}
	if err != nil {
		return "failed"
	}
	res, err := providerHTTP.Do(req)
	if err != nil {
		slog.Error("text failed", "to", to, "err", err)
		return "failed"
	}
	defer res.Body.Close()
	if res.StatusCode >= 300 {
		raw, _ := io.ReadAll(io.LimitReader(res.Body, 2000))
		slog.Error("text refused by the provider", "to", to, "status", res.StatusCode, "answer", string(raw))
		return "failed"
	}
	return "delivered"
}
