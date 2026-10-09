package httpapi

import (
	"context"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"logaluxe/api/internal/mail"
)

func (s *Server) logMailFailure(what, to string, err error) {
	slog.Error("email failed", "what", what, "to", to, "err", err)
}

// ---------- support inbox ----------

// POST /v1/support   public: the contact form
func (s *Server) supportCreate(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var req struct {
		Name         string `json:"name"`
		Email        string `json:"email"`
		Phone        string `json:"phone"`
		Role         string `json:"role"`
		BusinessSlug string `json:"business_slug"`
		Subject      string `json:"subject"`
		Message      string `json:"message"`
	}
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, "invalid json")
		return
	}
	req.Name, req.Email, req.Phone = strings.TrimSpace(req.Name), strings.TrimSpace(req.Email), strings.TrimSpace(req.Phone)
	req.Subject, req.Message = strings.TrimSpace(req.Subject), strings.TrimSpace(req.Message)
	if req.Role != "business" && req.Role != "other" {
		req.Role = "client"
	}
	switch {
	case req.Name == "" || len(req.Name) > 80:
		writeErr(w, 400, "tell us your name")
		return
	case req.Email == "" && req.Phone == "":
		writeErr(w, 400, "add an email or a phone number so we can reply")
		return
	case req.Email != "" && !mail.Valid(req.Email):
		writeErr(w, 400, "that email address does not look right")
		return
	case len(req.Phone) > 24:
		writeErr(w, 400, "that phone number is too long")
		return
	case len(req.Subject) < 3 || len(req.Subject) > 140:
		writeErr(w, 400, "add a short subject")
		return
	case len(req.Message) < 10 || len(req.Message) > 4000:
		writeErr(w, 400, "tell us a little more, in up to 4,000 characters")
		return
	}
	// A simple brake on floods: five open tickets from one contact is enough.
	var open int
	_ = s.pool.QueryRow(ctx, `select count(*) from support_tickets where status <> 'closed' and created_at > now() - interval '1 day' and ((email <> '' and email = $1) or (phone <> '' and phone = $2))`, req.Email, req.Phone).Scan(&open)
	if open >= 5 {
		writeErr(w, 429, "we already have your messages and will reply soon")
		return
	}
	var bizID *string
	if req.BusinessSlug != "" {
		var id string
		if err := s.pool.QueryRow(ctx, `select id::text from businesses where slug=$1`, req.BusinessSlug).Scan(&id); err == nil {
			bizID = &id
		}
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	defer tx.Rollback(ctx)
	var id, ref string
	if err := tx.QueryRow(ctx, `insert into support_tickets (name, email, phone, role, business_id, subject) values ($1,$2,$3,$4,$5,$6) returning id::text, ref`,
		req.Name, strings.ToLower(req.Email), req.Phone, req.Role, bizID, req.Subject).Scan(&id, &ref); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if _, err := tx.Exec(ctx, `insert into support_messages (ticket_id, author, body) values ($1,$2,$3)`, id, req.Name, req.Message); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if err := tx.Commit(ctx); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 201, M{"ok": true, "ref": ref})
}

// GET /v1/admin/support?status=open|waiting|closed&q=&mine=1
func (s *Server) adminSupport(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	mine := ""
	if q.Get("mine") == "1" {
		mine = currentAdmin(r).Email
	}
	out, pagination, err := s.adminRows(r, `
		select t.id, t.ref, t.name, t.email, t.phone, t.role, t.subject, t.status, t.priority, t.assigned_to, t.created_at, t.updated_at, b.name as business,
		  (select count(*) from support_messages m where m.ticket_id = t.id) as messages,
		  (select left(m.body, 140) from support_messages m where m.ticket_id = t.id and not m.internal order by m.created_at desc limit 1) as preview
		from support_tickets t left join businesses b on b.id = t.business_id
		where ($1 = '' or t.status = $1)
		  and ($2 = '' or t.ref ilike '%'||$2||'%' or t.name ilike '%'||$2||'%' or t.email ilike '%'||$2||'%' or t.phone like '%'||$2||'%' or t.subject ilike '%'||$2||'%')
		  and ($3 = '' or t.assigned_to = $3)
		order by t.status = 'open' desc, t.priority = 'urgent' desc, t.updated_at desc limit 200`, q.Get("status"), q.Get("q"), mine)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	counts, _ := row(r.Context(), s.pool, `select count(*) filter (where status='open') as open, count(*) filter (where status='waiting') as waiting, count(*) filter (where status='closed') as closed from support_tickets`)
	writeJSON(w, 200, M{"pagination": pagination, "tickets": out, "counts": counts, "mail_mode": s.mail.Mode()})
}

// GET /v1/admin/support/{id}
func (s *Server) adminSupportTicket(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id := chi.URLParam(r, "id")
	t, err := row(ctx, s.pool, `select t.*, b.name as business, b.id::text as business_ref from support_tickets t left join businesses b on b.id = t.business_id where t.id=$1`, id)
	if err != nil {
		writeErr(w, 404, "ticket not found")
		return
	}
	msgs, _ := rows(ctx, s.pool, `select id, author, from_staff, internal, delivery, body, created_at from support_messages where ticket_id=$1 order by created_at`, id)
	staff, _ := rows(ctx, s.pool, `select email, name from admin_users where active order by name`)
	writeJSON(w, 200, M{"ticket": t, "messages": msgs, "staff": staff})
}

// POST /v1/admin/support/{id}/reply   {body, internal, close}
func (s *Server) adminSupportReply(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id := chi.URLParam(r, "id")
	var req struct {
		Body     string `json:"body"`
		Internal bool   `json:"internal"`
		Close    bool   `json:"close"`
	}
	if err := readJSON(r, &req); err != nil || strings.TrimSpace(req.Body) == "" {
		writeErr(w, 400, "write a reply first")
		return
	}
	if len(req.Body) > 6000 {
		writeErr(w, 400, "keep the reply under 6,000 characters")
		return
	}
	var ref, name, email, subject string
	if err := s.pool.QueryRow(ctx, `select ref, name, email, subject from support_tickets where id=$1`, id).Scan(&ref, &name, &email, &subject); err != nil {
		writeErr(w, 404, "ticket not found")
		return
	}
	me := currentAdmin(r)
	delivery := ""
	if !req.Internal {
		if email == "" {
			delivery = "no email on file"
		} else {
			text := "Hello " + name + ",\n\n" + strings.TrimSpace(req.Body) + "\n\n" + me.Name + "\nLogaLuxe support\n\nYour reference is " + ref + ". Reply to this email or use " + strings.TrimRight(s.cfg.WebURL, "/") + "/help if you need anything else."
			var err error
			if delivery, err = s.mail.Send(ctx, email, "Re: ["+ref+"] "+subject, text); err != nil {
				delivery = "failed"
				s.logMailFailure("support reply", email, err)
			}
		}
	}
	if _, err := s.pool.Exec(ctx, `insert into support_messages (ticket_id, author, from_staff, internal, delivery, body) values ($1,$2,true,$3,$4,$5)`, id, me.Email, req.Internal, delivery, strings.TrimSpace(req.Body)); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	status := "waiting" // waiting on the customer
	if req.Close {
		status = "closed"
	}
	if req.Internal && !req.Close {
		_, _ = s.pool.Exec(ctx, `update support_tickets set updated_at=now() where id=$1`, id)
	} else {
		_, _ = s.pool.Exec(ctx, `update support_tickets set status=$2, updated_at=now(), assigned_to = case when assigned_to = '' then $3 else assigned_to end where id=$1`, id, status, me.Email)
	}
	s.audit(r, "support.reply", ref, nil, M{"internal": req.Internal, "closed": req.Close, "email": delivery})
	writeJSON(w, 200, M{"ok": true, "email": delivery})
}

// PUT /v1/admin/support/{id}   {status, priority, assigned_to}
func (s *Server) adminSupportUpdate(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id := chi.URLParam(r, "id")
	var req struct {
		Status     *string `json:"status"`
		Priority   *string `json:"priority"`
		AssignedTo *string `json:"assigned_to"`
	}
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, "invalid json")
		return
	}
	if req.Status != nil && *req.Status != "open" && *req.Status != "waiting" && *req.Status != "closed" {
		writeErr(w, 400, "status must be open, waiting or closed")
		return
	}
	if req.Priority != nil && *req.Priority != "normal" && *req.Priority != "urgent" {
		writeErr(w, 400, "priority must be normal or urgent")
		return
	}
	if req.AssignedTo != nil && *req.AssignedTo != "" {
		var ok bool
		_ = s.pool.QueryRow(ctx, `select exists(select 1 from admin_users where email=$1 and active)`, *req.AssignedTo).Scan(&ok)
		if !ok {
			writeErr(w, 400, "that person is not on the team")
			return
		}
	}
	var ref string
	if err := s.pool.QueryRow(ctx, `update support_tickets set status=coalesce($2,status), priority=coalesce($3,priority), assigned_to=coalesce($4,assigned_to), updated_at=now() where id=$1 returning ref`,
		id, req.Status, req.Priority, req.AssignedTo).Scan(&ref); err != nil {
		writeErr(w, 404, "ticket not found")
		return
	}
	s.audit(r, "support.update", ref, nil, req)
	writeJSON(w, 200, M{"ok": true})
}

// ---------- bulk messages ----------

type recipient struct{ name, email, phone string }

// audienceOf lists who a message would reach. Clients are counted once per
// phone number, however many businesses they book with.
func (s *Server) audienceOf(ctx context.Context, audience, market, plan string) ([]recipient, error) {
	var sql string
	args := []any{market}
	if audience == "businesses" {
		sql = `select owner_name, email, phone from businesses where status in ('live','pending','paused') and ($1 = '' or market = $1) and ($2 = '' or plan = $2) order by name`
		args = append(args, plan)
	} else {
		sql = `select distinct on (c.phone) c.name, '' as email, c.phone from clients c join businesses b on b.id = c.business_id
			where c.phone <> '' and ($1 = '' or b.market = $1) and not exists (select 1 from blocked_contacts bc where bc.phone = c.phone) order by c.phone, c.created_at desc`
	}
	rs, err := s.pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	defer rs.Close()
	var out []recipient
	for rs.Next() {
		var x recipient
		if err := rs.Scan(&x.name, &x.email, &x.phone); err != nil {
			return nil, err
		}
		out = append(out, x)
	}
	return out, rs.Err()
}

func reachable(list []recipient, channel string) int {
	n := 0
	for _, x := range list {
		if (channel == "email" && mail.Valid(x.email)) || (channel != "email" && x.phone != "") {
			n++
		}
	}
	return n
}

// How a message on this channel would go out today.
func (s *Server) channelMode(channel string) string {
	if channel == "email" {
		return s.mail.Mode()
	}
	return "log" // WhatsApp and SMS are not connected yet
}

// GET /v1/admin/broadcasts
func (s *Server) adminBroadcasts(w http.ResponseWriter, r *http.Request) {
	out, err := rows(r.Context(), s.pool, `select * from broadcasts order by created_at desc limit 100`)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, M{"broadcasts": out, "modes": M{"email": s.channelMode("email"), "whatsapp": s.channelMode("whatsapp"), "sms": s.channelMode("sms")}})
}

// POST /v1/admin/broadcasts   saves a draft and says how many people it would reach
func (s *Server) adminBroadcastCreate(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var req struct {
		Audience string `json:"audience"`
		Market   string `json:"market"`
		Plan     string `json:"plan"`
		Channel  string `json:"channel"`
		Subject  string `json:"subject"`
		Body     string `json:"body"`
	}
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, "invalid json")
		return
	}
	req.Subject, req.Body = strings.TrimSpace(req.Subject), strings.TrimSpace(req.Body)
	switch {
	case req.Audience != "businesses" && req.Audience != "clients":
		writeErr(w, 400, "choose businesses or clients")
		return
	case req.Market != "" && req.Market != "US" && req.Market != "NG":
		writeErr(w, 400, "market must be US, NG or empty for both")
		return
	case req.Plan != "" && req.Plan != "free" && req.Plan != "pro":
		writeErr(w, 400, "plan must be free, pro or empty for both")
		return
	case req.Channel != "email" && req.Channel != "whatsapp" && req.Channel != "sms":
		writeErr(w, 400, "choose email, WhatsApp or SMS")
		return
	case req.Channel == "email" && req.Subject == "":
		writeErr(w, 400, "an email needs a subject")
		return
	case len(req.Body) < 10 || len(req.Body) > 4000:
		writeErr(w, 400, "the message must be 10 to 4,000 characters")
		return
	case req.Channel == "sms" && len(req.Body) > 320:
		writeErr(w, 400, "keep a text message under 320 characters")
		return
	}
	if req.Audience == "clients" {
		req.Plan = ""
	}
	list, err := s.audienceOf(ctx, req.Audience, req.Market, req.Plan)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	var id string
	if err := s.pool.QueryRow(ctx, `insert into broadcasts (audience, market, plan, channel, subject, body, recipients, created_by) values ($1,$2,$3,$4,$5,$6,$7,$8) returning id::text`,
		req.Audience, req.Market, req.Plan, req.Channel, req.Subject, req.Body, len(list), s.actor(r)).Scan(&id); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	s.audit(r, "broadcast.draft", id, nil, M{"audience": req.Audience, "channel": req.Channel, "recipients": len(list)})
	writeJSON(w, 201, M{"ok": true, "id": id, "recipients": len(list), "reachable": reachable(list, req.Channel)})
}

// DELETE /v1/admin/broadcasts/{id}   drafts only
func (s *Server) adminBroadcastDelete(w http.ResponseWriter, r *http.Request) {
	tag, err := s.pool.Exec(r.Context(), `delete from broadcasts where id=$1 and status='draft'`, chi.URLParam(r, "id"))
	if err != nil || tag.RowsAffected() == 0 {
		writeErr(w, 409, "only a draft can be deleted")
		return
	}
	s.audit(r, "broadcast.delete", chi.URLParam(r, "id"), nil, nil)
	writeJSON(w, 200, M{"ok": true})
}

// POST /v1/admin/broadcasts/{id}/send   super admin only: operations write the draft,
// a super admin decides it goes out.
func (s *Server) adminBroadcastSend(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id := chi.URLParam(r, "id")
	var audience, market, plan, channel, subject, body string
	// Claim the draft, so two clicks cannot send it twice.
	err := s.pool.QueryRow(ctx, `update broadcasts set status='sending', sent_by=$2, sent_at=now() where id=$1 and status='draft' returning audience, market, plan, channel, subject, body`, id, s.actor(r)).
		Scan(&audience, &market, &plan, &channel, &subject, &body)
	if err != nil {
		writeErr(w, 409, "this message was already sent, or does not exist")
		return
	}
	list, err := s.audienceOf(ctx, audience, market, plan)
	if err != nil {
		_, _ = s.pool.Exec(ctx, `update broadcasts set status='draft', sent_by='', sent_at=null where id=$1`, id)
		writeErr(w, 500, err.Error())
		return
	}
	s.audit(r, "broadcast.send", id, nil, M{"audience": audience, "channel": channel, "recipients": len(list)})

	// Sending can outlast the request, so it runs on its own clock.
	go func() {
		bg, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
		defer cancel()
		var delivered, logged, skipped, failed, queued int
		for _, x := range list {
			text := strings.ReplaceAll(body, "{{name}}", firstNonEmpty(x.name, "there"))
			switch {
			case channel == "email" && mail.Valid(x.email):
				status, err := s.mail.Send(bg, x.email, subject, text)
				switch {
				case err != nil:
					failed++
					s.logMailFailure("broadcast", x.email, err)
				case status == "sent":
					delivered++
				case status == "queued":
					queued++
				default:
					logged++
				}
			case channel != "email" && x.phone != "":
				slog.Info("message not sent, the channel is not connected", "channel", channel, "to", x.phone, "body", text)
				logged++
			default:
				skipped++
			}
		}
		_, _ = s.pool.Exec(bg, `update broadcasts set status='sent', recipients=$2, delivered=$3, logged=$4, skipped=$5, failed=$6, queued=$7 where id=$1`, id, len(list), delivered, logged, skipped, failed, queued)
	}()
	writeJSON(w, 202, M{"ok": true, "recipients": len(list), "reachable": reachable(list, channel), "mode": s.channelMode(channel)})
}

// ---------- legal and help pages ----------

// GET /v1/pages/{slug}   public
func (s *Server) sitePage(w http.ResponseWriter, r *http.Request) {
	p, err := row(r.Context(), s.pool, `select slug, title, body, updated_at from site_pages where slug=$1 and published`, chi.URLParam(r, "slug"))
	if err != nil {
		writeErr(w, 404, "page not found")
		return
	}
	writeJSON(w, 200, M{"page": p})
}

// GET /v1/admin/pages
func (s *Server) adminPages(w http.ResponseWriter, r *http.Request) {
	out, err := rows(r.Context(), s.pool, `select slug, title, body, published, updated_by, updated_at from site_pages order by slug`)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, M{"pages": out})
}

// PUT /v1/admin/pages/{slug}   {title, body, published}
func (s *Server) adminPageUpdate(w http.ResponseWriter, r *http.Request) {
	slug := chi.URLParam(r, "slug")
	var req struct {
		Title     string `json:"title"`
		Body      string `json:"body"`
		Published bool   `json:"published"`
	}
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, "invalid json")
		return
	}
	req.Title = strings.TrimSpace(req.Title)
	if req.Title == "" || len(req.Title) > 120 {
		writeErr(w, 400, "the page needs a title")
		return
	}
	if len(req.Body) > 60000 {
		writeErr(w, 400, "the page is too long; keep it under 60,000 characters")
		return
	}
	before, err := row(r.Context(), s.pool, `select title, published, length(body) as length from site_pages where slug=$1`, slug)
	if err != nil {
		writeErr(w, 404, "page not found")
		return
	}
	if _, err := s.pool.Exec(r.Context(), `update site_pages set title=$2, body=$3, published=$4, updated_by=$5, updated_at=now() where slug=$1`, slug, req.Title, req.Body, req.Published, s.actor(r)); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	s.audit(r, "page.update", slug, before, M{"title": req.Title, "published": req.Published, "length": len(req.Body)})
	writeJSON(w, 200, M{"ok": true})
}
