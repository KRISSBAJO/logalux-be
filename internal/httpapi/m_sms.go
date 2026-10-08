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

// Text and WhatsApp messages. Twilio texts numbers outside Nigeria and Termii texts Nigerian ones;
// WhatsApp goes through Twilio. Nothing is sent unless an admin has switched the feature on in the
// console and the provider's keys are set, because a message costs money and reaches a real phone.

// Numbers that are never messaged: the 555 range North America reserves for fiction, and the sample clients' range.
var neverText = regexp.MustCompile(`^\+1\d{3}555\d{4}$|^\+234803555\d{4}$`)

// phoneMarket says which provider's country a number belongs to.
func phoneMarket(phone string) string {
	if strings.HasPrefix(phone, "+234") {
		return "NG"
	}
	return "US"
}

func (s *Server) smsReady(market string) bool {
	if market == "NG" {
		return s.cfg.TermiiKey != "" && s.cfg.TermiiFrom != ""
	}
	return s.cfg.TwilioSID != "" && s.cfg.TwilioToken != "" && s.cfg.TwilioFrom != ""
}

// smsMode says whether a text to a client in this market would really go out: "live" or "log".
func (s *Server) smsMode(market string) string {
	if s.featureOn("sms_messages") && s.smsReady(market) {
		return "live"
	}
	return "log"
}

// whatsappMode says whether a WhatsApp message would really go out: "live" or "log".
func (s *Server) whatsappMode() string {
	if s.featureOn("whatsapp") {
		return "live"
	}
	return "log"
}

func cleanTo(to string) string { return strings.ReplaceAll(strings.TrimSpace(to), " ", "") }

// sendSMS texts a client on behalf of a business. It returns "delivered" when the provider accepted the
// message, "logged" when texts are off or the number is one that must not be texted, and "failed" otherwise.
func (s *Server) sendSMS(ctx context.Context, businessID, to, body string) string {
	var market string
	_ = s.pool.QueryRow(ctx, `select market from businesses where id=$1`, businessID).Scan(&market)
	if s.smsMode(market) != "live" {
		slog.Info("text recorded, not sent", "to", cleanTo(to), "body", body)
		return "logged"
	}
	return s.rawSMS(ctx, market, to, body)
}

// rawSMS hands one text to the provider for that market. The caller has already decided texts are on.
func (s *Server) rawSMS(ctx context.Context, market, to, body string) string {
	to = cleanTo(to)
	if !s.smsReady(market) || !strings.HasPrefix(to, "+") || neverText.MatchString(to) {
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
		req, err = s.twilioMessage(ctx, to, s.cfg.TwilioFrom, body)
	}
	if err != nil {
		return "failed"
	}
	return providerSend(req, "text", to)
}

// sendWhatsApp sends one WhatsApp message through Twilio. Same answers as sendSMS.
func (s *Server) sendWhatsApp(ctx context.Context, to, body string) string {
	to = cleanTo(to)
	if s.whatsappMode() != "live" || !strings.HasPrefix(to, "+") || neverText.MatchString(to) {
		slog.Info("WhatsApp message recorded, not sent", "to", to, "body", body)
		return "logged"
	}
	if len(body) > 1500 {
		body = body[:1500]
	}
	from := s.cfg.TwilioWhatsAppFrom
	if !strings.HasPrefix(from, "whatsapp:") {
		from = "whatsapp:" + from
	}
	req, err := s.twilioMessage(ctx, "whatsapp:"+to, from, body)
	if err != nil {
		return "failed"
	}
	return providerSend(req, "WhatsApp message", to)
}

func (s *Server) twilioMessage(ctx context.Context, to, from, body string) (*http.Request, error) {
	form := url.Values{"To": {to}, "From": {from}, "Body": {body}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.twilio.com/2010-04-01/Accounts/"+url.PathEscape(s.cfg.TwilioSID)+"/Messages.json", strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(s.cfg.TwilioSID+":"+s.cfg.TwilioToken)))
	return req, nil
}

func providerSend(req *http.Request, what, to string) string {
	res, err := providerHTTP.Do(req)
	if err != nil {
		slog.Error(what+" failed", "to", to, "err", err)
		return "failed"
	}
	defer res.Body.Close()
	if res.StatusCode >= 300 {
		raw, _ := io.ReadAll(io.LimitReader(res.Body, 2000))
		slog.Error(what+" refused by the provider", "to", to, "status", res.StatusCode, "answer", string(raw))
		return "failed"
	}
	return "delivered"
}

// tellPhone sends a short message to a person's own phone on the channel they prefer, when that
// channel is live. It returns the channel used, or "" when nothing was sent.
func (s *Server) tellPhone(ctx context.Context, businessID, phone, prefer, body string) string {
	phone = cleanTo(phone)
	if phone == "" || prefer == "email" {
		return ""
	}
	if prefer != "sms" && s.whatsappMode() == "live" && s.sendWhatsApp(ctx, phone, body) == "delivered" {
		return "whatsapp"
	}
	if s.sendSMS(ctx, businessID, phone, body) == "delivered" {
		return "sms"
	}
	return ""
}
