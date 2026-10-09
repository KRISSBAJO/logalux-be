package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/jackc/pgx/v5"
	"logaluxe/api/internal/mail"
)

func TestCampaignLegacySendingMigration(t *testing.T) {
	s := securityServer(t)
	_, id := campaignFixture(t, s)
	securityExec(t, s, `update campaigns set status='sending' where id=$1`, id)
	// Recreate only the pre-0038 schema in this disposable database.
	securityExec(t, s, `drop table campaign_jobs; alter table campaigns drop column uncertain, drop column pending, drop column stalled_reason`)
	body, err := os.ReadFile("../db/migrations/0038_campaign_jobs.sql")
	if err != nil {
		t.Fatal(err)
	}
	securityExec(t, s, string(body))
	s.runCampaignJobs(context.Background())
	var status, reason string
	if err := s.pool.QueryRow(context.Background(), `select status,stalled_reason from campaigns where id=$1`, id).Scan(&status, &reason); err != nil {
		t.Fatal(err)
	}
	if status != "stalled" || reason == "" {
		t.Fatalf("legacy delivery replayed: %s %s", status, reason)
	}
}

func campaignFixture(t *testing.T, s *Server) (string, string) {
	biz, _, client, _, _ := securityFixture(t, s)
	securityExec(t, s, `update clients set marketing_opt_in=true,email='campaign@example.test' where id=$1`, client)
	id := securityID(t, s, `insert into campaigns(business_id,name,audience,channel,subject,message,created_by) values($1,'Durable','all','email','Hello','Hello {first name}, book again.','fixture') returning id::text`, biz)
	return biz, id
}

func TestCampaignAtomicSnapshotAndConcurrentClaims(t *testing.T) {
	s := securityServer(t)
	biz, id := campaignFixture(t, s)
	ctx := context.Background()
	securityExec(t, s, `insert into clients(business_id,name,email,phone,marketing_opt_in) select $1,'Recipient '||n,'fixture@example.test','fixture-phone-'||n,true from generate_series(1,5001) n`, biz)
	var wg sync.WaitGroup
	var successes atomic.Int32
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if s.snapshotCampaign(ctx, id, biz, "Fixture", "fixture") == nil {
				successes.Add(1)
			}
		}()
	}
	wg.Wait()
	if successes.Load() != 1 {
		t.Fatalf("claims=%d", successes.Load())
	}
	var count int
	if err := s.pool.QueryRow(ctx, `select count(*) from campaign_jobs where campaign_id=$1`, id).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 5002 {
		t.Fatalf("snapshot truncated: %d", count)
	}
	seen := sync.Map{}
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			j, err := s.claimCampaignJob(ctx)
			if err != nil {
				t.Error(err)
				return
			}
			if _, dup := seen.LoadOrStore(j.id, true); dup {
				t.Error("duplicate worker claim")
			}
		}()
	}
	wg.Wait()
	// A claimed attempt with no accepted record is quarantined on restart.
	securityExec(t, s, `update campaign_jobs set lease_until=now()-interval '1 second' where status='sending'`)
	// Avoid delivering the remaining 4990 recipients in this recovery assertion.
	securityExec(t, s, `update campaign_jobs set status='skipped' where status='pending'`)
	restarted := &Server{pool: s.pool, cfg: s.cfg, mail: s.mail}
	restarted.runCampaignJobs(ctx)
	var unknown int
	var status string
	if err := s.pool.QueryRow(ctx, `select status,uncertain from campaigns where id=$1`, id).Scan(&status, &unknown); err != nil {
		t.Fatal(err)
	}
	if status != "stalled" || unknown != 12 {
		t.Fatalf("restart status=%s unknown=%d", status, unknown)
	}
}

func TestCampaignSnapshotRollback(t *testing.T) {
	s := securityServer(t)
	biz, id := campaignFixture(t, s)
	securityExec(t, s, `create function campaign_fail() returns trigger language plpgsql as $$ begin raise exception 'injected'; end $$;create trigger campaign_fail before insert on campaign_jobs for each row execute function campaign_fail()`)
	if s.snapshotCampaign(context.Background(), id, biz, "Fixture", "fixture") == nil {
		t.Fatal("expected snapshot error")
	}
	var status string
	var count int
	if err := s.pool.QueryRow(context.Background(), `select status,(select count(*) from campaign_jobs) from campaigns where id=$1`, id).Scan(&status, &count); err != nil {
		t.Fatal(err)
	}
	if status != "draft" || count != 0 {
		t.Fatalf("partial snapshot: %s %d", status, count)
	}
}

func TestCampaignAcceptedDeliveryAndMidSendRestart(t *testing.T) {
	for _, crash := range []bool{false, true} {
		t.Run(map[bool]string{false: "accepted", true: "mid-send"}[crash], func(t *testing.T) {
			s := securityServer(t)
			biz, id := campaignFixture(t, s)
			ctx := context.Background()
			var sends atomic.Int32
			accepted := make(chan struct{})
			release := make(chan struct{})
			stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				sends.Add(1)
				close(accepted)
				if crash {
					<-release
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"id":"fixture-accepted","status":"queued"}`))
			}))
			defer stub.Close()
			s.mail = mail.New(mail.Config{Provider: "relykit", RelyKitKey: "stub", From: "fixture@example.test", RelyKitURL: stub.URL})
			if err := s.snapshotCampaign(ctx, id, biz, "Fixture", "fixture"); err != nil {
				t.Fatal(err)
			}
			j, err := s.claimCampaignJob(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if crash {
				done := make(chan string, 1)
				go func() { done <- s.deliverCampaignJob(ctx, j) }()
				<-accepted
				securityExec(t, s, `update campaign_jobs set lease_until=now()-interval '1 second' where id=$1`, j.id)
				restarted := &Server{pool: s.pool, cfg: s.cfg, mail: s.mail}
				restarted.runCampaignJobs(ctx)
				close(release)
				<-done // old process never persisted the provider response
				if err := s.finishCampaignJob(ctx, j, "queued"); err == nil {
					t.Fatal("stale worker overwrote recovery")
				}
			} else {
				status := s.deliverCampaignJob(ctx, j)
				if status != "queued" {
					t.Fatalf("status=%s", status)
				}
				if err := s.finishCampaignJob(ctx, j, status); err != nil {
					t.Fatal(err)
				}
			}
			restarted := &Server{pool: s.pool, cfg: s.cfg, mail: s.mail}
			restarted.runCampaignJobs(ctx)
			restarted.runCampaignJobs(ctx)
			if sends.Load() != 1 {
				t.Fatalf("accepted delivery replayed: %d", sends.Load())
			}
			var status string
			if err := s.pool.QueryRow(ctx, `select status from campaign_jobs where id=$1`, j.id).Scan(&status); err != nil {
				t.Fatal(err)
			}
			expected := "queued"
			if crash {
				expected = "uncertain"
			}
			if status != expected {
				t.Fatalf("status=%s", status)
			}
			if _, err := s.claimCampaignJob(ctx); err != pgx.ErrNoRows {
				t.Fatalf("terminal reclaimed: %v", err)
			}
		})
	}
}

func TestCampaignRecoveryUsesRecordedAcceptance(t *testing.T) {
	s := securityServer(t)
	biz, id := campaignFixture(t, s)
	ctx := context.Background()
	if err := s.snapshotCampaign(ctx, id, biz, "Fixture", "fixture"); err != nil {
		t.Fatal(err)
	}
	j, err := s.claimCampaignJob(ctx)
	if err != nil {
		t.Fatal(err)
	}
	securityExec(t, s, `insert into message_sends(business_id,client_id,campaign_id,marketing,channel,status) values($1,$2,$3,true,'email','queued')`, biz, j.client, id)
	securityExec(t, s, `update campaign_jobs set lease_until=now()-interval '1 second' where id=$1`, j.id)
	s.runCampaignJobs(ctx)
	var status string
	if err := s.pool.QueryRow(ctx, `select status from campaign_jobs where id=$1`, j.id).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "queued" {
		t.Fatalf("acceptance lost: %s", status)
	}
}

func TestCampaignRestartResumesPendingAndRechecksConsent(t *testing.T) {
	s := securityServer(t)
	biz, id := campaignFixture(t, s)
	ctx := context.Background()
	securityExec(t, s, `insert into clients(business_id,name,email,phone,marketing_opt_in) values($1,'Second','second@example.test','second-fixture',true),($1,'Third','third@example.test','third-fixture',true)`, biz)
	if err := s.snapshotCampaign(ctx, id, biz, "Fixture", "fixture"); err != nil {
		t.Fatal(err)
	}
	j, err := s.claimCampaignJob(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.finishCampaignJob(ctx, j, s.deliverCampaignJob(ctx, j)); err != nil {
		t.Fatal(err)
	}
	securityExec(t, s, `update clients set marketing_opt_in=false where id=(select client_id from campaign_jobs where campaign_id=$1 and status='pending' order by id limit 1)`, id)
	restarted := &Server{pool: s.pool, cfg: s.cfg, mail: s.mail}
	restarted.runCampaignJobs(ctx)
	var status string
	var logged, skipped, pending int
	if err = s.pool.QueryRow(ctx, `select status,logged,skipped,pending from campaigns where id=$1`, id).Scan(&status, &logged, &skipped, &pending); err != nil {
		t.Fatal(err)
	}
	if status != "sent" || logged != 2 || skipped != 1 || pending != 0 {
		t.Fatalf("resume: %s logged=%d skipped=%d pending=%d", status, logged, skipped, pending)
	}
}
