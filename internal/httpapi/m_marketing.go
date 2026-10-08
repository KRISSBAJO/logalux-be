package httpapi

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"logaluxe/api/internal/mail"
)

// Marketing: the messages a business sends by itself (reminders, review
// requests, win-backs) and one-off campaigns. A client gets at most four
// marketing messages a month. Reminders and confirmations do not count.

const marketingCap = 4

var automationInfo = []struct {
	Key, Name, When string
	Marketing       bool
}{
	{"confirmation", "Booking confirmation", "As soon as a client books", false},
	{"reminder_24h", "Reminder, the day before", "24 hours before the visit", false},
	{"reminder_2h", "Reminder, same day", "2 hours before the visit", false},
	{"review_request", "Review request", "2 hours after checkout", false},
	{"rebook", "Rebook prompt", "4 weeks after a visit, if nothing is booked", true},
	{"win_back", "Win-back", "60 days after the last visit", true},
	{"birthday", "Birthday", "On the client's birthday", true},
}

var audienceWhere = map[string]string{
	"all":      "true",
	"lapsed":   lapsedWhere,
	"new":      clientSegments["new"],
	"regulars": clientSegments["regulars"],
	"birthday": "c.birthday is not null and extract(month from c.birthday) = extract(month from now())",
}

// fill replaces the {placeholders} a business can use in a message.
func fill(tpl string, v map[string]string) string {
	for k, val := range v {
		tpl = strings.ReplaceAll(tpl, "{"+k+"}", val)
	}
	return tpl
}

func firstName(full string) string {
	if f := strings.Fields(full); len(f) > 0 {
		return f[0]
	}
	return "there"
}

// sendToClient delivers one message to a client on the best channel that can
// reach them, records it, and returns what happened: delivered, logged or skipped.
func (s *Server) sendToClient(ctx context.Context, businessID, businessName, clientID, email, phone, subject, body, automationKey string, bookingID, campaignID *string, marketing bool, channelWanted string) string {
	channel, status := "", "skipped"
	switch {
	case (channelWanted == "" || channelWanted == "email") && mail.Valid(email):
		channel = "email"
		st, err := s.mail.Send(ctx, email, subject, body+"\n\n"+businessName+" · sent with LogaLuxe")
		switch {
		case err != nil:
			status = "failed"
			s.logMailFailure("client message", email, err)
		case st == "sent":
			status = "delivered"
		default:
			status = "logged"
		}
	case channelWanted != "email" && phone != "":
		channel = firstNonEmpty(channelWanted, "sms")
		if channel == "sms" {
			status = s.sendSMS(ctx, businessID, phone, body) // really sent only when texts are switched on
		} else {
			status = "logged" // WhatsApp is not connected yet
			slog.Info("message recorded, the channel is not connected", "channel", channel, "to", phone, "body", body)
		}
	default:
		channel = firstNonEmpty(channelWanted, "email")
	}
	var cid *string
	if clientID != "" {
		cid = &clientID
	}
	_, _ = s.pool.Exec(ctx, `insert into message_sends (business_id, client_id, booking_id, campaign_id, automation_key, marketing, channel, status) values ($1,$2,$3,$4,$5,$6,$7,$8) on conflict do nothing`,
		businessID, cid, bookingID, campaignID, automationKey, marketing, channel, status)
	return status
}

// GET /v1/m/marketing
func (s *Server) mMarketing(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	m := mc(r)
	kpis, _ := row(ctx, s.pool, `with visited as (
		  select distinct on (bk.client_id) bk.client_id, bk.starts_at from bookings bk
		  where bk.business_id=$1 and bk.client_id is not null and bk.status in ('completed','paid') and bk.starts_at > now() - interval '30 days' order by bk.client_id, bk.starts_at desc)
		select (select count(*) from visited) as visited,
		  (select count(*) from visited v where exists (select 1 from bookings b2 where b2.client_id = v.client_id and b2.starts_at > v.starts_at and b2.status not in ('cancelled_client','cancelled_business','no_show'))) as rebooked,
		  (select count(*) from bookings bk where bk.business_id=$1 and bk.status in ('completed','paid') and bk.starts_at > now() - interval '30 days') as visits,
		  (select count(*) from reviews rv where rv.business_id=$1 and rv.created_at > now() - interval '30 days') as reviews,
		  (select count(*) from message_sends ms where ms.business_id=$1 and ms.created_at > date_trunc('month', now()) and ms.status in ('delivered','logged')) as sent_month,
		  (select count(*) from message_sends ms where ms.business_id=$1 and ms.marketing and ms.created_at > date_trunc('month', now()) and ms.status in ('delivered','logged')) as marketing_month,
		  (select count(*) from clients c where c.business_id=$1 and not c.marketing_opt_in) as opted_out,
		  (select count(*) from clients c where c.business_id=$1) as clients,
		  (select count(distinct bk.id) from message_sends ms join bookings bk on bk.client_id = ms.client_id and bk.created_at > ms.created_at and bk.created_at < ms.created_at + interval '14 days'
		     where ms.business_id=$1 and ms.automation_key='win_back' and ms.created_at > now() - interval '30 days') as win_back_bookings,
		  (select coalesce(sum(bk.total_cents),0) from message_sends ms join bookings bk on bk.client_id = ms.client_id and bk.created_at > ms.created_at and bk.created_at < ms.created_at + interval '14 days'
		     where ms.business_id=$1 and ms.automation_key='win_back' and ms.created_at > now() - interval '30 days') as win_back_cents`, m.BusinessID)
	stored, _ := rows(ctx, s.pool, `select a.key, a.enabled, a.message,
		(select count(*) from message_sends ms where ms.business_id = a.business_id and ms.automation_key = a.key and ms.created_at > now() - interval '30 days' and ms.status in ('delivered','logged')) as sent_30d,
		(select count(*) from message_sends ms where ms.business_id = a.business_id and ms.automation_key = a.key and ms.created_at > now() - interval '30 days' and ms.status = 'delivered') as delivered_30d
		from automations a where a.business_id=$1`, m.BusinessID)
	byKey := map[string]M{}
	for _, a := range stored {
		byKey[a["key"].(string)] = a
	}
	autos := []M{}
	for _, info := range automationInfo {
		a := byKey[info.Key]
		if a == nil {
			a = M{"key": info.Key, "enabled": false, "message": defaultAutomations[info.Key], "sent_30d": 0, "delivered_30d": 0}
		}
		a["name"], a["when"], a["marketing"] = info.Name, info.When, info.Marketing
		autos = append(autos, a)
	}
	campaigns, _ := rows(ctx, s.pool, `select c.*, (select count(distinct bk.id) from message_sends ms join bookings bk on bk.client_id = ms.client_id and bk.created_at > ms.created_at and bk.created_at < ms.created_at + interval '14 days' where ms.campaign_id = c.id) as booked,
		(select coalesce(sum(bk.total_cents),0) from message_sends ms join bookings bk on bk.client_id = ms.client_id and bk.created_at > ms.created_at and bk.created_at < ms.created_at + interval '14 days' where ms.campaign_id = c.id) as booked_cents
		from campaigns c where c.business_id=$1 order by c.created_at desc limit 30`, m.BusinessID)
	audiences := M{}
	for key, where := range audienceWhere {
		var total, email, phone int
		_ = s.pool.QueryRow(ctx, `select count(*), count(*) filter (where c.email <> ''), count(*) filter (where c.phone <> '') from clients c where c.business_id=$1 and c.marketing_opt_in and (`+where+`)`, m.BusinessID).Scan(&total, &email, &phone)
		audiences[key] = M{"total": total, "email": email, "phone": phone}
	}
	writeJSON(w, 200, M{"kpis": kpis, "automations": autos, "campaigns": campaigns, "audiences": audiences, "cap": marketingCap,
		"modes": M{"email": s.mail.Mode(), "whatsapp": "log", "sms": s.smsMode(m.Market)}, "booking_link": strings.TrimRight(s.cfg.WebURL, "/") + "/b/" + m.Slug})
}

// PUT /v1/m/automations/{key}   {enabled, message}
func (s *Server) mAutomationUpdate(w http.ResponseWriter, r *http.Request) {
	m := mc(r)
	key := chi.URLParam(r, "key")
	if _, ok := defaultAutomations[key]; !ok {
		writeErr(w, 404, "there is no automation called "+key)
		return
	}
	var req struct {
		Enabled *bool   `json:"enabled"`
		Message *string `json:"message"`
	}
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, "invalid json")
		return
	}
	if req.Message != nil {
		msg := strings.TrimSpace(*req.Message)
		if len(msg) < 10 || len(msg) > 600 {
			writeErr(w, 400, "the message must be 10 to 600 characters")
			return
		}
		req.Message = &msg
	}
	if _, err := s.pool.Exec(r.Context(), `insert into automations (business_id, key, enabled, message) values ($1,$2,coalesce($3,true),coalesce($4,$5))
		on conflict (business_id, key) do update set enabled = coalesce($3, automations.enabled), message = coalesce($4, automations.message)`, m.BusinessID, key, req.Enabled, req.Message, defaultAutomations[key]); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, M{"ok": true})
}

// POST /v1/m/campaigns   {name, audience, channel, subject, message}
func (s *Server) mCampaignCreate(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	m := mc(r)
	var req struct {
		Name     string `json:"name"`
		Audience string `json:"audience"`
		Channel  string `json:"channel"`
		Subject  string `json:"subject"`
		Message  string `json:"message"`
	}
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, "invalid json")
		return
	}
	req.Name, req.Subject, req.Message = strings.TrimSpace(req.Name), strings.TrimSpace(req.Subject), strings.TrimSpace(req.Message)
	where, ok := audienceWhere[req.Audience]
	switch {
	case req.Name == "" || len(req.Name) > 80:
		writeErr(w, 400, "give the campaign a name")
		return
	case !ok:
		writeErr(w, 400, "choose who it goes to")
		return
	case req.Channel != "email" && req.Channel != "whatsapp" && req.Channel != "sms":
		writeErr(w, 400, "choose email, WhatsApp or SMS")
		return
	case req.Channel == "email" && req.Subject == "":
		writeErr(w, 400, "an email needs a subject")
		return
	case len(req.Message) < 10 || len(req.Message) > 1200:
		writeErr(w, 400, "the message must be 10 to 1,200 characters")
		return
	}
	var n int
	_ = s.pool.QueryRow(ctx, `select count(*) from clients c where c.business_id=$1 and c.marketing_opt_in and (`+where+`)`, m.BusinessID).Scan(&n)
	var id string
	if err := s.pool.QueryRow(ctx, `insert into campaigns (business_id, name, audience, channel, subject, message, recipients, created_by) values ($1,$2,$3,$4,$5,$6,$7,$8) returning id::text`,
		m.BusinessID, req.Name, req.Audience, req.Channel, req.Subject, req.Message, n, m.Email).Scan(&id); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 201, M{"ok": true, "id": id, "recipients": n})
}

// DELETE /v1/m/campaigns/{id}   drafts only
func (s *Server) mCampaignDelete(w http.ResponseWriter, r *http.Request) {
	tag, err := s.pool.Exec(r.Context(), `delete from campaigns where id=$1 and business_id=$2 and status='draft'`, chi.URLParam(r, "id"), mc(r).BusinessID)
	if err != nil || tag.RowsAffected() == 0 {
		writeErr(w, 409, "only a draft can be deleted")
		return
	}
	writeJSON(w, 200, M{"ok": true})
}

// POST /v1/m/campaigns/{id}/test   sends the message to the person signed in
func (s *Server) mCampaignTest(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	m := mc(r)
	var subject, message string
	if err := s.pool.QueryRow(ctx, `select subject, message from campaigns where id=$1 and business_id=$2`, chi.URLParam(r, "id"), m.BusinessID).Scan(&subject, &message); err != nil {
		writeErr(w, 404, "campaign not found")
		return
	}
	body := fill(message, map[string]string{"first name": firstName(m.Name), "last service": "your last service", "booking link": strings.TrimRight(s.cfg.WebURL, "/") + "/b/" + m.Slug, "staff": firstName(m.Name), "business": m.Business, "time": "Saturday at 10:00"})
	status, err := s.mail.Send(ctx, m.Email, firstNonEmpty(subject, "Test: a message from "+m.Business), "[Test] "+body)
	if err != nil {
		writeErr(w, 502, "the test could not be sent: "+err.Error())
		return
	}
	writeJSON(w, 200, M{"ok": true, "status": status, "to": m.Email})
}

// POST /v1/m/campaigns/{id}/send
func (s *Server) mCampaignSend(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	m := mc(r)
	id := chi.URLParam(r, "id")
	var audience, channel, subject, message string
	// Claim the draft, so two clicks cannot send it twice.
	if err := s.pool.QueryRow(ctx, `update campaigns set status='sending', sent_at=now() where id=$1 and business_id=$2 and status='draft' returning audience, channel, subject, message`, id, m.BusinessID).
		Scan(&audience, &channel, &subject, &message); err != nil {
		writeErr(w, 409, "this campaign was already sent, or does not exist")
		return
	}
	link := strings.TrimRight(s.cfg.WebURL, "/") + "/b/" + m.Slug
	biz, bizName := m.BusinessID, m.Business
	go func() {
		bg, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
		defer cancel()
		clients, err := rows(bg, s.pool, `select c.id, c.name, c.email, c.phone,
			(select bi.name from bookings bk join booking_items bi on bi.booking_id = bk.id where bk.client_id = c.id and bk.status in ('completed','paid') order by bk.starts_at desc limit 1) as last_service,
			(select count(*) from message_sends ms where ms.client_id = c.id and ms.marketing and ms.created_at > now() - interval '30 days' and ms.status in ('delivered','logged')) as recent
			from clients c where c.business_id=$1 and c.marketing_opt_in and (`+audienceWhere[audience]+`) order by c.name limit 5000`, biz)
		var delivered, logged, skipped, failed int
		if err == nil {
			for _, c := range clients {
				if toInt(c["recent"]) >= marketingCap { // already had four this month
					skipped++
					continue
				}
				name, _ := c["name"].(string)
				last, _ := c["last_service"].(string)
				body := fill(message, map[string]string{"first name": firstName(name), "last service": firstNonEmpty(last, "your last visit"), "booking link": link, "staff": "the team", "business": bizName, "time": ""})
				email, _ := c["email"].(string)
				phone, _ := c["phone"].(string)
				switch s.sendToClient(bg, biz, bizName, c["id"].(string), email, phone, firstNonEmpty(subject, "News from "+bizName), body, "", nil, &id, true, channel) {
				case "delivered":
					delivered++
				case "logged":
					logged++
				case "failed":
					failed++
				default:
					skipped++
				}
			}
		}
		_, _ = s.pool.Exec(bg, `update campaigns set status='sent', recipients=$2, delivered=$3, logged=$4, skipped=$5, failed=$6 where id=$1`, id, len(clients), delivered, logged, skipped, failed)
	}()
	writeJSON(w, 202, M{"ok": true})
}

// ---------- automations, run by the worker ----------

type due struct {
	key      string
	business string
	bizName  string
	slug     string
	message  string
	clientID string
	name     string
	email    string
	phone    string
	service  string
	staff    string
	when     string
	booking  *string
}

func (s *Server) runAutomations(ctx context.Context) {
	link := func(slug string) string { return strings.TrimRight(s.cfg.WebURL, "/") + "/b/" + slug }
	send := func(d due, marketing bool) {
		body := fill(d.message, map[string]string{"first name": firstName(d.name), "last service": firstNonEmpty(d.service, "your appointment"), "booking link": link(d.slug),
			"staff": firstNonEmpty(firstName(d.staff), "the team"), "business": d.bizName, "time": d.when})
		subject := map[string]string{"confirmation": "You are booked at " + d.bizName, "reminder_24h": "Tomorrow at " + d.bizName, "reminder_2h": "See you soon at " + d.bizName,
			"review_request": "How was your visit to " + d.bizName + "?", "rebook": "Time for your next visit?", "win_back": "We miss you at " + d.bizName, "birthday": "Happy birthday from " + d.bizName}[d.key]
		s.sendToClient(ctx, d.business, d.bizName, d.clientID, d.email, d.phone, subject, body, d.key, d.booking, nil, marketing, "")
	}

	// Messages tied to one booking. A unique index makes sure each is sent once.
	bookingRules := map[string]string{
		"confirmation":   "bk.status = 'confirmed' and bk.created_at > now() - interval '2 hours' and bk.starts_at > now() and bk.source not in ('walk_in')",
		"reminder_24h":   "bk.status = 'confirmed' and bk.starts_at > now() + interval '3 hours' and bk.starts_at <= now() + interval '24 hours' and bk.created_at < bk.starts_at - interval '26 hours'",
		"reminder_2h":    "bk.status = 'confirmed' and bk.starts_at > now() + interval '15 minutes' and bk.starts_at <= now() + interval '2 hours' and bk.created_at < bk.starts_at - interval '3 hours'",
		"review_request": "bk.status = 'paid' and bk.paid_at < now() - interval '2 hours' and bk.paid_at > now() - interval '3 days' and not exists (select 1 from reviews rv where rv.booking_id = bk.id)",
	}
	for key, rule := range bookingRules {
		rs, err := s.pool.Query(ctx, `select b.id::text, b.name, b.slug, b.timezone, a.message, coalesce(bk.client_id::text,''), bk.client_name, coalesce(nullif(bk.client_email,''), c.email, ''), bk.client_phone,
			coalesce((select string_agg(name, ' + ') from booking_items where booking_id = bk.id), ''), st.name, bk.starts_at, bk.id::text
			from bookings bk join businesses b on b.id = bk.business_id join automations a on a.business_id = b.id and a.key = $1 and a.enabled
			join staff st on st.id = bk.staff_id left join clients c on c.id = bk.client_id
			where b.status = 'live' and `+rule+` and not exists (select 1 from message_sends ms where ms.booking_id = bk.id and ms.automation_key = $1) limit 200`, key)
		if err != nil {
			slog.Error("automation query", "key", key, "err", err)
			continue
		}
		var batch []due
		for rs.Next() {
			var d due
			var tz, bid string
			var at time.Time
			if rs.Scan(&d.business, &d.bizName, &d.slug, &tz, &d.message, &d.clientID, &d.name, &d.email, &d.phone, &d.service, &d.staff, &at, &bid) != nil {
				continue
			}
			loc, err := time.LoadLocation(tz)
			if err != nil {
				loc = time.UTC
			}
			d.key, d.booking, d.when = key, &bid, at.In(loc).Format("Monday 2 January at 3:04 PM")
			batch = append(batch, d)
		}
		rs.Close()
		for _, d := range batch {
			send(d, false)
		}
	}

	// Messages tied to the client, not a booking. Each respects the monthly cap.
	clientRules := map[string]string{
		"rebook": `last.at < now() - interval '28 days' and last.at > now() - interval '36 days'
			and not exists (select 1 from message_sends ms where ms.client_id = c.id and ms.automation_key = 'rebook' and ms.created_at > now() - interval '60 days')`,
		"win_back": `last.at < now() - interval '60 days' and last.at > now() - interval '75 days'
			and not exists (select 1 from message_sends ms where ms.client_id = c.id and ms.automation_key = 'win_back' and ms.created_at > now() - interval '90 days')`,
		"birthday": `c.birthday is not null and to_char(c.birthday, 'MM-DD') = to_char(now() at time zone b.timezone, 'MM-DD')
			and not exists (select 1 from message_sends ms where ms.client_id = c.id and ms.automation_key = 'birthday' and ms.created_at > now() - interval '300 days')`,
	}
	for key, rule := range clientRules {
		rs, err := s.pool.Query(ctx, fmt.Sprintf(`select b.id::text, b.name, b.slug, a.message, c.id::text, c.name, c.email, c.phone, coalesce(last.service, '')
			from clients c join businesses b on b.id = c.business_id join automations a on a.business_id = b.id and a.key = $1 and a.enabled
			left join lateral (select bk.starts_at as at, (select bi.name from booking_items bi where bi.booking_id = bk.id limit 1) as service
			  from bookings bk where bk.client_id = c.id and bk.status in ('completed','paid') order by bk.starts_at desc limit 1) last on true
			where b.status = 'live' and c.marketing_opt_in and %s
			  and not exists (select 1 from bookings nx where nx.client_id = c.id and nx.starts_at > now() and nx.status in ('requested','confirmed'))
			  and (select count(*) from message_sends ms where ms.client_id = c.id and ms.marketing and ms.created_at > now() - interval '30 days' and ms.status in ('delivered','logged')) < %d
			limit 200`, rule, marketingCap), key)
		if err != nil {
			slog.Error("automation query", "key", key, "err", err)
			continue
		}
		var batch []due
		for rs.Next() {
			d := due{key: key}
			if rs.Scan(&d.business, &d.bizName, &d.slug, &d.message, &d.clientID, &d.name, &d.email, &d.phone, &d.service) == nil {
				batch = append(batch, d)
			}
		}
		rs.Close()
		for _, d := range batch {
			send(d, true)
		}
	}
}
