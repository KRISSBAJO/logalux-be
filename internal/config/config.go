// Package config reads settings from the environment. A missing .env is fine;
// Docker Compose and production hosts set variables directly.
package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/joho/godotenv"
)

type Config struct {
	Port          string
	Env           string
	DatabaseURL   string
	RedisURL      string
	AdminToken    string
	AdminEmail    string // first super admin, created once if no admins exist
	AdminPassword string
	WhatsAppToken string
	// Drafts of replies and campaign messages. A person always reads a draft before it is sent.
	OpenAIKey   string
	OpenAIModel string
	// Text and WhatsApp messages. Nothing is sent unless an admin switched the feature on in the console as well.
	TwilioWhatsAppFrom string // the WhatsApp sender Twilio gave, like whatsapp:+14155238886
	TwilioSID          string
	TwilioToken        string
	TwilioFrom         string
	TermiiKey          string
	TermiiBase         string
	TermiiFrom         string
	CORSOrigins        []string
	Seed               bool

	// Image storage. All four are needed, or uploads are switched off.
	AWSRegion    string
	AWSBucket    string
	AWSAccessKey string
	AWSSecretKey string

	// Email. Without a working provider, messages are written to the log.
	MailProvider string
	MailFrom     string
	ResendKey    string
	SMTPHost     string
	SMTPPort     string
	SMTPUser     string
	SMTPPass     string
	SMTPSecure   bool
	WebURL       string // public address of the website, for links in emails
	RateLimits   bool   // per-connection limits on sign-in, sign-up, codes, bookings and orders; RATE_LIMITS=off turns them off for test runs
	WebAPIKey    string // shared with the web app, so the API believes the visitor address it passes on
	PublicAPIURL string // the API's own public address, for links other services fetch (calendar subscriptions)

	// Password for the sample business owners (ada@logaluxe.test and so on). Only used with SEED=true.
	MerchantDemoPassword string

	// The first guess of where a visitor is, from their internet address. See internal/httpapi/geoip.go.
	GeoIPProvider  string // geojs (the default, no key), ipinfo (needs GEOIP_API_KEY), ipapi (key optional), or off
	GeoIPKey       string
	GeoIPTimeoutMS int // how long a lookup may take before the page goes on without it; 1200 when unset

	StripeSecret   string
	PaystackSecret string
	// Signs the events Stripe sends to /v1/webhooks/stripe. Paystack signs with its secret key.
	StripeWebhookSecret string
	FlutterwaveSecret   string
}

func Load() (Config, error) {
	_ = godotenv.Load() // optional

	c := Config{
		Port:                 get("PORT", "18080"),
		Env:                  get("APP_ENV", "development"),
		DatabaseURL:          os.Getenv("DATABASE_URL"),
		RedisURL:             get("REDIS_URL", ""),
		AdminToken:           os.Getenv("ADMIN_TOKEN"),
		AdminEmail:           os.Getenv("ADMIN_EMAIL"),
		AdminPassword:        os.Getenv("ADMIN_PASSWORD"),
		WhatsAppToken:        os.Getenv("WHATSAPP_TOKEN"),
		OpenAIKey:            os.Getenv("OPENAI_API_KEY"),
		OpenAIModel:          os.Getenv("OPENAI_MODEL"),
		TwilioWhatsAppFrom:   os.Getenv("TWILIO_WHATSAPP_FROM"),
		TwilioSID:            os.Getenv("TWILIO_ACCOUNT_SID"),
		TwilioToken:          os.Getenv("TWILIO_AUTH_TOKEN"),
		TwilioFrom:           os.Getenv("TWILIO_FROM_NUMBER"),
		TermiiKey:            os.Getenv("TERMII_API_KEY"),
		TermiiBase:           os.Getenv("TERMII_BASE_URL"),
		TermiiFrom:           os.Getenv("TERMII_FROM"),
		Seed:                 get("SEED", "false") == "true",
		AWSRegion:            os.Getenv("AWS_REGION"),
		AWSBucket:            os.Getenv("AWS_S3_BUCKET"),
		AWSAccessKey:         os.Getenv("AWS_ACCESS_KEY_ID"),
		AWSSecretKey:         os.Getenv("AWS_SECRET_ACCESS_KEY"),
		MailProvider:         os.Getenv("MAIL_PROVIDER"),
		MailFrom:             os.Getenv("MAIL_FROM"),
		ResendKey:            os.Getenv("RESEND_API_KEY"),
		SMTPHost:             os.Getenv("SMTP_HOST"),
		SMTPPort:             get("SMTP_PORT", "587"),
		SMTPUser:             os.Getenv("SMTP_USER"),
		SMTPPass:             os.Getenv("SMTP_PASSWORD"),
		SMTPSecure:           os.Getenv("SMTP_SECURE") == "true",
		WebURL:               get("WEB_URL", "http://localhost:3100"),
		RateLimits:           strings.ToLower(os.Getenv("RATE_LIMITS")) != "off",
		WebAPIKey:            os.Getenv("WEB_API_KEY"),
		PublicAPIURL:         os.Getenv("API_PUBLIC_URL"),
		MerchantDemoPassword: os.Getenv("MERCHANT_DEMO_PASSWORD"),
		GeoIPProvider:        os.Getenv("GEOIP_PROVIDER"),
		GeoIPKey:             os.Getenv("GEOIP_API_KEY"),
		StripeSecret:         os.Getenv("STRIPE_SECRET_KEY"),
		PaystackSecret:       os.Getenv("PAYSTACK_SECRET_KEY"),
		StripeWebhookSecret:  os.Getenv("STRIPE_WEBHOOK_SECRET"),
		FlutterwaveSecret:    os.Getenv("FLUTTERWAVE_SECRET_KEY"),
	}
	if n, err := strconv.Atoi(os.Getenv("GEOIP_TIMEOUT_MS")); err == nil && n > 0 && n <= 5000 {
		c.GeoIPTimeoutMS = n
	}
	for _, o := range strings.Split(get("CORS_ORIGIN", "http://localhost:3100"), ",") {
		if o = strings.TrimSpace(o); o != "" {
			c.CORSOrigins = append(c.CORSOrigins, o)
		}
	}
	if c.DatabaseURL == "" {
		// Build from the Compose-style parts so one set of variables works everywhere.
		host := get("POSTGRES_HOST", "127.0.0.1")
		port := get("POSTGRES_PORT", "15433")
		c.DatabaseURL = fmt.Sprintf("postgres://%s:%s@%s:%s/%s?sslmode=disable",
			get("POSTGRES_USER", "logaluxe"), get("POSTGRES_PASSWORD", ""), host, port, get("POSTGRES_DB", "logaluxe"))
	}
	if c.AdminToken == "" {
		if c.Env == "production" {
			return c, fmt.Errorf("ADMIN_TOKEN is required in production")
		}
		c.AdminToken = "dev-admin-token"
	}
	return c, nil
}

func (c Config) PaymentsMode() string {
	if c.StripeSecret == "" && c.PaystackSecret == "" {
		return "simulation"
	}
	return "live"
}

func (c Config) MessagingMode() string {
	if c.WhatsAppToken == "" {
		return "log only"
	}
	return "live"
}

func get(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
