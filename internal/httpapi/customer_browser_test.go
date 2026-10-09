package httpapi

// Opt-in fixture server for browser checks. Uses a disposable database and
// logged mail. Never starts workers or connects to the production database.
import (
	"context"
	"github.com/jackc/pgx/v5/pgxpool"
	"logaluxe/api/internal/db"
	"net"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

func TestCustomerBrowserFixture(t *testing.T) {
	stop := os.Getenv("CUSTOMER_QA_STOP_FILE")
	if stop == "" {
		t.Skip("interactive customer QA only")
	}
	s := securityServer(t)
	s.cfg.StripeSecret = ""
	s.cfg.PaystackSecret = ""
	if err := db.Seed(context.Background(), s.pool.(*pgxpool.Pool)); err != nil {
		t.Fatal(err)
	}
	if os.Getenv("QA_MERCHANT") == "1" {
		s.cfg.MerchantDemoPassword = "Local-Only-QA-2026-Test"
		if err := db.SeedMerchant(context.Background(), s.pool.(*pgxpool.Pool)); err != nil {
			t.Fatal(err)
		}
		if err := BootstrapMerchants(context.Background(), s.pool.(*pgxpool.Pool), s.cfg); err != nil {
			t.Fatal(err)
		}
	}
	s.cfg.WebURL = "http://localhost:3101"
	s.cfg.Env = "development"
	s.cfg.StripeSecret = os.Getenv("QA_STRIPE_KEY")
	s.cfg.PaystackSecret = os.Getenv("QA_PAYSTACK_KEY")
	if os.Getenv("QA_MERCHANT") == "1" {
		s.cfg.StripeSecret = ""
		s.cfg.PaystackSecret = ""
	}
	for _, key := range []string{s.cfg.StripeSecret, s.cfg.PaystackSecret} {
		if key != "" && !strings.HasPrefix(key, "sk_test_") {
			t.Fatal("only test payment keys are permitted")
		}
	}
	ts := httptest.NewUnstartedServer(New(s.cfg, s.pool.(*pgxpool.Pool)))
	listener, err := net.Listen("tcp", "127.0.0.1:18081")
	if err != nil {
		t.Fatal(err)
	}
	ts.Listener = listener
	ts.Start()
	defer ts.Close()
	t.Log("Isolated customer QA API ready on 18081; emails are logged")
	for {
		if _, err := os.Stat(stop); err == nil {
			break
		}
		time.Sleep(time.Second)
	}
}
