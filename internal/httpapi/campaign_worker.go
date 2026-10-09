package httpapi

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"logaluxe/api/internal/mail"
)

// Claim and snapshot share a repeatable-read transaction: no partially scheduled
// campaign is visible. Keyset pages bound memory, not the size of the audience.
func (s *Server) snapshotCampaign(ctx context.Context, id, biz, name, slug string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `set transaction isolation level repeatable read`); err != nil {
		return err
	}
	var audience, channel, subject, message string
	err = tx.QueryRow(ctx, `update campaigns set status='waiting',sent_at=now() where id=$1 and business_id=$2 and status='draft' returning audience,channel,subject,message`, id, biz).Scan(&audience, &channel, &subject, &message)
	if err != nil {
		return err
	}
	where, ok := audienceWhere[audience]
	if !ok {
		return fmt.Errorf("invalid audience")
	}
	cursor := "00000000-0000-0000-0000-000000000000"
	total := 0
	for {
		rs, err := tx.Query(ctx, `select c.id::text,c.name,c.email,c.phone,coalesce((select bi.name from bookings bk join booking_items bi on bi.booking_id=bk.id where bk.client_id=c.id and bk.status in ('completed','paid') order by bk.starts_at desc limit 1),'your last visit') from clients c where c.business_id=$1 and c.marketing_opt_in and c.id>$2::uuid and (`+where+`) order by c.id limit 500`, biz, cursor)
		if err != nil {
			return err
		}
		type recipient struct{ id, name, email, phone, last string }
		batch := []recipient{}
		for rs.Next() {
			var c recipient
			if err = rs.Scan(&c.id, &c.name, &c.email, &c.phone, &c.last); err != nil {
				rs.Close()
				return err
			}
			batch = append(batch, c)
		}
		err = rs.Err()
		rs.Close()
		if err != nil {
			return err
		}
		if len(batch) == 0 {
			break
		}
		for _, c := range batch {
			vals := map[string]string{"first name": firstName(c.name), "last service": c.last, "booking link": strings.TrimRight(s.cfg.WebURL, "/") + "/b/" + slug, "staff": "the team", "business": name, "time": ""}
			_, err = tx.Exec(ctx, `insert into campaign_jobs(campaign_id,client_id,email,phone,subject,body,business_name) values($1,$2,$3,$4,$5,$6,$7)`, id, c.id, c.email, c.phone, firstNonEmpty(fill(subject, vals), "News from "+name), fill(message, vals), name)
			if err != nil {
				return err
			}
			cursor = c.id
			total++
		}
	}
	_, err = tx.Exec(ctx, `update campaigns set recipients=$2,pending=$2,status=case when $2=0 then 'sent' else 'waiting' end where id=$1`, id, total)
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}

type campaignJob struct{ id, campaign, business, client, email, phone, subject, body, name, channel string }

// A sending lease is an attempt marker, not permission to replay an external
// side effect. After expiration recovery is terminal and conservative.
func (s *Server) claimCampaignJob(ctx context.Context) (campaignJob, error) {
	var j campaignJob
	err := s.pool.QueryRow(ctx, `with candidate as (select j.id from campaign_jobs j join campaigns c on c.id=j.campaign_id where j.status='pending' and c.status in ('waiting','sending') order by j.id for update of j skip locked limit 1), claimed as (update campaign_jobs j set status='sending',lease_until=now()+interval '10 minutes' from candidate x where j.id=x.id returning j.*) select j.id::text,j.campaign_id::text,c.business_id::text,j.client_id::text,j.email,j.phone,j.subject,j.body,j.business_name,c.channel from claimed j join campaigns c on c.id=j.campaign_id`).Scan(&j.id, &j.campaign, &j.business, &j.client, &j.email, &j.phone, &j.subject, &j.body, &j.name, &j.channel)
	return j, err
}

func (s *Server) refreshCampaignProgress(ctx context.Context) error {
	_, err := s.pool.Exec(ctx, `with counts as (select campaign_id,count(*) filter(where status in ('pending','sending'))::int pending,count(*) filter(where status='delivered')::int delivered,count(*) filter(where status='queued')::int queued,count(*) filter(where status='logged')::int logged,count(*) filter(where status='skipped')::int skipped,count(*) filter(where status='failed')::int failed,count(*) filter(where status='uncertain')::int uncertain,bool_or(status='sending') active from campaign_jobs group by campaign_id) update campaigns c set pending=x.pending,delivered=x.delivered,queued=x.queued,logged=x.logged,skipped=x.skipped,failed=x.failed,uncertain=x.uncertain,status=case when x.pending>0 then case when x.active then 'sending' else 'waiting' end when x.uncertain>0 then 'stalled' else 'sent' end,stalled_reason=case when x.uncertain>0 then 'Some delivery outcomes are unknown and will not be retried automatically.' else '' end from counts x where c.id=x.campaign_id`)
	return err
}

func (s *Server) finishCampaignJob(ctx context.Context, j campaignJob, status string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	tag, err := tx.Exec(ctx, `update campaign_jobs set status=$2,completed_at=now(),lease_until=null where id=$1 and status='sending' and lease_until>now()`, j.id, status)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("campaign lease expired")
	}
	_, err = tx.Exec(ctx, `insert into message_sends(business_id,client_id,campaign_id,marketing,channel,status) values($1,(select id from clients where id=$2 and business_id=$1),$3,true,$4,$5)`, j.business, j.client, j.campaign, j.channel, status)
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Server) deliverCampaignJob(ctx context.Context, j campaignJob) string {
	var eligible bool
	err := s.pool.QueryRow(ctx, `select exists(select 1 from clients c where c.id=$1 and c.business_id=$2 and c.marketing_opt_in and (select count(*) from message_sends m where m.client_id=c.id and m.marketing and m.created_at>now()-interval '30 days' and m.status in ('delivered','queued','logged','uncertain')) + (select count(*) from campaign_jobs j join campaigns jc on jc.id=j.campaign_id where jc.sent_at>now()-interval '30 days' and j.client_id=c.id and j.id<>$4::uuid and j.status in ('sending','uncertain') and not exists(select 1 from message_sends ms where ms.campaign_id=j.campaign_id and ms.client_id=j.client_id)) <$3)`, j.client, j.business, marketingCap, j.id).Scan(&eligible)
	if err != nil {
		return "uncertain"
	}
	if !eligible {
		return "skipped"
	}
	if j.channel == "email" {
		if !mail.Valid(j.email) {
			return "skipped"
		}
		status, err := s.mail.Send(ctx, j.email, j.subject, j.body+"\n\n"+j.name+" · sent with LogaLuxe")
		if err != nil {
			return "uncertain"
		}
		if status == "sent" {
			return "delivered"
		}
		if status == "queued" {
			return "queued"
		}
		return "logged"
	}
	if j.phone == "" {
		return "skipped"
	}
	status := "uncertain"
	if j.channel == "sms" {
		status = s.sendSMS(ctx, j.business, j.phone, j.body)
	} else {
		status = s.sendWhatsApp(ctx, j.phone, j.body)
	}
	if status == "failed" {
		return "uncertain"
	}
	return status
}

// Add s.runCampaignJobs(ctx) to the worker tick. Row locks support multiple
// API workers without a global advisory lock. Each invocation bounds work.
func (s *Server) runCampaignJobs(ctx context.Context) {
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	_, err := s.pool.Exec(ctx, `update campaign_jobs j set status=coalesce((select m.status from message_sends m where m.campaign_id=j.campaign_id and m.client_id=j.client_id and m.status in ('delivered','queued','logged','skipped','failed') order by m.created_at desc limit 1),'uncertain'),completed_at=now(),lease_until=null where j.status='sending' and j.lease_until<=now()`)
	if err != nil {
		slog.Error("campaign recovery", "err", err)
		return
	}
	defer func() {
		if err := s.refreshCampaignProgress(ctx); err != nil {
			slog.Error("campaign progress", "err", err)
		}
	}()
	for n := 0; n < 100 && ctx.Err() == nil; n++ {
		j, err := s.claimCampaignJob(ctx)
		if err == pgx.ErrNoRows {
			return
		}
		if err != nil {
			slog.Error("campaign claim", "err", err)
			return
		}
		if err = s.refreshCampaignProgress(ctx); err != nil {
			return
		}
		status := s.deliverCampaignJob(ctx, j)
		if err = s.finishCampaignJob(ctx, j, status); err != nil {
			slog.Error("campaign completion", "err", err)
			return
		}
	}
}
