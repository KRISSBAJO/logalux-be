package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
)

// Features a LogaLuxe admin switches on and off in the console. A feature is live only when it is
// switched on AND the keys it needs are in the environment; until then nobody is offered it.

type featureInfo struct {
	Key, Title, About string
}

var featureList = []featureInfo{
	{"sms_login", "Sign in with a texted code", "Customers sign in and sign up with a 6-digit code sent to their phone, as well as with email and password."},
	{"sms_messages", "Texts to clients", "Reminders, campaign messages and inbox replies on the SMS channel are really sent. A text costs money and reaches a real phone."},
	{"whatsapp", "WhatsApp messages", "Booking confirmations and messages on the WhatsApp channel are really sent, through Twilio."},
	{"wallets", "Apple Pay and Google Pay", "Customers are told they can pay with Apple Pay or Google Pay on the payment page. The wallets themselves are switched on in the Stripe dashboard, under Payment methods."},
	{"saved_cards", "Saved cards", "A signed-in customer can keep a card and pay with it in one tap. Card numbers stay with Stripe and Paystack; LogaLuxe never holds them."},
}

// featureNeeds lists the environment keys a feature still lacks. Empty means it is ready.
func (s *Server) featureNeeds(key string) []string {
	c := s.cfg
	twilio := c.TwilioSID != "" && c.TwilioToken != ""
	var missing []string
	need := func(ok bool, name string) {
		if !ok {
			missing = append(missing, name)
		}
	}
	switch key {
	case "sms_login", "sms_messages":
		us := twilio && c.TwilioFrom != ""
		ng := c.TermiiKey != "" && c.TermiiFrom != ""
		if !us && !ng { // one provider is enough to start; each covers its own country
			need(c.TwilioSID != "", "TWILIO_ACCOUNT_SID")
			need(c.TwilioToken != "", "TWILIO_AUTH_TOKEN")
			need(c.TwilioFrom != "", "TWILIO_FROM_NUMBER")
		}
	case "whatsapp":
		need(c.TwilioSID != "", "TWILIO_ACCOUNT_SID")
		need(c.TwilioToken != "", "TWILIO_AUTH_TOKEN")
		need(c.TwilioWhatsAppFrom != "", "TWILIO_WHATSAPP_FROM")
	case "wallets":
		need(c.StripeSecret != "", "STRIPE_SECRET_KEY")
	case "saved_cards":
		if c.StripeSecret == "" && c.PaystackSecret == "" {
			missing = append(missing, "STRIPE_SECRET_KEY or PAYSTACK_SECRET_KEY")
		}
	}
	return missing
}

var featureCache struct {
	sync.Mutex
	at    time.Time
	flags map[string]bool
}

// featureFlags reads what the admin switched on. It is asked for on every message, so it is kept for a few seconds.
func (s *Server) featureFlags(ctx context.Context) map[string]bool {
	featureCache.Lock()
	defer featureCache.Unlock()
	if featureCache.flags != nil && time.Since(featureCache.at) < 10*time.Second {
		return featureCache.flags
	}
	flags := map[string]bool{}
	var raw []byte
	if err := s.pool.QueryRow(ctx, `select value from platform_settings where key='features'`).Scan(&raw); err == nil {
		_ = json.Unmarshal(raw, &flags)
	}
	featureCache.flags, featureCache.at = flags, time.Now()
	return flags
}

// featureOn says whether a feature is live: switched on, with its keys in place.
func (s *Server) featureOn(key string) bool {
	return s.featureFlags(context.Background())[key] && len(s.featureNeeds(key)) == 0
}

// GET /v1/features   public: what the apps may offer right now
func (s *Server) features(w http.ResponseWriter, r *http.Request) {
	out := M{}
	for _, f := range featureList {
		out[f.Key] = s.featureOn(f.Key)
	}
	texts := s.featureOn("sms_messages")
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, M{"features": out, "texts_in": M{"US": texts && s.smsReady("US"), "NG": texts && s.smsReady("NG")}})
}

// GET /v1/admin/features
func (s *Server) adminFeatures(w http.ResponseWriter, r *http.Request) {
	flags := s.featureFlags(r.Context())
	out := []M{}
	for _, f := range featureList {
		missing := s.featureNeeds(f.Key)
		if missing == nil {
			missing = []string{}
		}
		out = append(out, M{"key": f.Key, "title": f.Title, "about": f.About, "on": flags[f.Key], "ready": len(missing) == 0, "missing": missing, "live": flags[f.Key] && len(missing) == 0})
	}
	writeJSON(w, 200, M{"features": out})
}

// PUT /v1/admin/features/{key}   {on}   super admin
func (s *Server) adminFeatureSet(w http.ResponseWriter, r *http.Request) {
	key := chi.URLParam(r, "key")
	known := false
	for _, f := range featureList {
		known = known || f.Key == key
	}
	var req struct {
		On bool `json:"on"`
	}
	if err := readJSON(r, &req); err != nil || !known {
		writeErr(w, 400, "unknown feature")
		return
	}
	ctx := r.Context()
	before := s.featureFlags(ctx)[key]
	if _, err := s.pool.Exec(ctx, `insert into platform_settings (key, value, updated_by) values ('features', jsonb_build_object($1::text, $2::bool), $3)
		on conflict (key) do update set value = platform_settings.value || jsonb_build_object($1::text, $2::bool), updated_by = $3, updated_at = now()`, key, req.On, s.actor(r)); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	featureCache.Lock()
	featureCache.flags = nil
	featureCache.Unlock()
	s.audit(r, "feature.set", key, M{"on": before}, M{"on": req.On})
	writeJSON(w, 200, M{"ok": true, "on": req.On, "live": s.featureOn(key), "missing": s.featureNeeds(key)})
}
