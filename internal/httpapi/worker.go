package httpapi

import (
	"context"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"logaluxe/api/internal/config"
	"logaluxe/api/internal/mail"
	"logaluxe/api/internal/storage"
)

// Background work that keeps a business running without anyone pressing a
// button: money settling into the payout balance, the daily payout run, and
// the automatic messages (confirmations, reminders, review requests).
//
// Each job takes a database lock, so if the API ever runs as several copies
// only one of them does the work.

func StartWorker(ctx context.Context, cfg config.Config, pool *pgxpool.Pool) {
	s := &Server{cfg: cfg, pool: pool, store: storage.New(cfg.AWSRegion, cfg.AWSBucket, cfg.AWSAccessKey, cfg.AWSSecretKey),
		mail: mail.New(mail.Config{Provider: cfg.MailProvider, From: cfg.MailFrom, ResendKey: cfg.ResendKey, RelyKitKey: cfg.RelyKitKey, RelyKitURL: cfg.RelyKitURL,
			SMTPHost: cfg.SMTPHost, SMTPPort: cfg.SMTPPort, SMTPUser: cfg.SMTPUser, SMTPPass: cfg.SMTPPass, SMTPSecure: cfg.SMTPSecure})}
	go func() {
		tick := time.NewTicker(time.Minute)
		defer tick.Stop()
		n := 0
		for {
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
				n++
				s.once(ctx, 7301, func(c context.Context) { s.settleLedger(c) })
				s.once(ctx, 7302, func(c context.Context) { s.runAutomations(c) })
				s.once(ctx, 7307, func(c context.Context) { s.sweepPayments(c) })
				s.once(ctx, 7312, func(c context.Context) { s.runCampaignJobs(c) })
				if n%10 == 1 {
					s.once(ctx, 7303, func(c context.Context) { s.runPayouts(c) })
					s.once(ctx, 7304, func(c context.Context) { s.renewMemberships(c) })
					s.once(ctx, 7305, func(c context.Context) { s.chargeRent(c) })
					s.once(ctx, 7306, func(c context.Context) { s.morningSummaries(c) })
					s.once(ctx, 7308, func(c context.Context) { s.billPlans(c) })
					s.once(ctx, 7309, func(c context.Context) { s.summariseReviews(c) })
					s.once(ctx, 7310, func(c context.Context) { s.awardReferrals(c) })
					s.once(ctx, 7311, func(c context.Context) { s.importCalendars(c) })
				}
			}
		}
	}()
	slog.Info("worker started", "email", s.mail.Mode())
}

// once runs a job only if no other copy of the API is running it right now.
func (s *Server) once(ctx context.Context, lock int64, job func(context.Context)) {
	c, cancel := context.WithTimeout(ctx, 4*time.Minute)
	defer cancel()
	conn, err := s.pool.Acquire(c)
	if err != nil {
		return
	}
	defer conn.Release()
	var got bool
	if err := conn.QueryRow(c, `select pg_try_advisory_lock($1)`, lock).Scan(&got); err != nil || !got {
		return
	}
	defer func() {
		_, _ = conn.Exec(context.Background(), `select pg_advisory_unlock($1)`, lock)
		if r := recover(); r != nil {
			slog.Error("worker job failed", "lock", lock, "panic", r)
		}
	}()
	job(c)
}

// settleLedger moves money that has finished settling into the payout balance.
func (s *Server) settleLedger(ctx context.Context) {
	if tag, err := s.pool.Exec(ctx, `update ledger set status='settled' where status='pending' and settles_at <= now()`); err == nil && tag.RowsAffected() > 0 {
		slog.Info("ledger settled", "lines", tag.RowsAffected())
	}
}

// runPayouts sends each business its available balance once a day (or once a
// week on Monday), after 6 in the morning its own time.
func (s *Server) runPayouts(ctx context.Context) {
	due, err := rows(ctx, s.pool, `select b.id, b.market, b.plan, b.currency from businesses b
		where b.payout_schedule in ('daily','weekly') and not b.payout_hold
		  and extract(hour from now() at time zone b.timezone) >= 6
		  and (b.payout_schedule = 'daily' or extract(isodow from now() at time zone b.timezone) = 1)
		  and exists (select 1 from payout_accounts a where a.business_id = b.id and a.is_default and a.status = 'verified')
		  and not exists (select 1 from payouts p where p.business_id = b.id and p.kind = 'automatic' and (p.created_at at time zone b.timezone)::date = (now() at time zone b.timezone)::date)
		  and (select coalesce(sum(l.amount_cents),0) from ledger l where l.business_id = b.id and l.in_balance and l.status = 'settled') >= 100
		limit 500`)
	if err != nil {
		slog.Error("payout run", "err", err)
		return
	}
	for _, b := range due {
		if id, reason := s.createPayout(ctx, b["id"].(string), b["market"].(string), b["plan"].(string), b["currency"].(string), "automatic"); reason == "" {
			slog.Info("automatic payout", "business", b["id"], "payout", id)
		}
	}
}
