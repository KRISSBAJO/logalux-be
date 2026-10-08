package httpapi

import (
	"context"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"logaluxe/api/internal/mail"
)

// The inbox. A thread is a conversation between a business and one client.
// In-app threads are real end to end: the client writes from their LogaLuxe
// account and reads the reply there. Email goes out through the mail provider.
// WhatsApp and SMS are recorded, and say so, until those channels are connected.

func preview(body string) string {
	body = strings.Join(strings.Fields(body), " ")
	if len(body) > 120 {
		return body[:117] + "..."
	}
	return body
}

// GET /v1/m/inbox?filter=open|unread|mine|closed&q=
func (s *Server) mInbox(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	m := mc(r)
	q := r.URL.Query()
	where := map[string]string{"unread": "t.status='open' and t.unread_business > 0", "mine": "t.status='open' and t.assigned_staff_id::text = $3", "closed": "t.status='closed'"}[q.Get("filter")]
	if where == "" {
		where = "t.status='open'"
	}
	threads, err := rows(ctx, s.pool, `select t.id, t.client_id, t.client_name, t.channel, t.status, t.unread_business, t.last_preview, t.last_message_at, st.name as assignee
		from threads t left join staff st on st.id = t.assigned_staff_id
		where t.business_id=$1 and (`+where+`) and ($2 = '' or t.client_name ilike '%'||$2||'%' or t.last_preview ilike '%'||$2||'%') and ($3 = '' or true)
		order by t.last_message_at desc limit 100`, m.BusinessID, strings.TrimSpace(q.Get("q")), m.StaffID)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	counts, _ := row(ctx, s.pool, `select count(*) filter (where status='open') as open, count(*) filter (where status='open' and unread_business > 0) as unread,
		count(*) filter (where status='open' and assigned_staff_id::text = $2) as mine, count(*) filter (where status='closed') as closed from threads where business_id=$1`, m.BusinessID, m.StaffID)
	writeJSON(w, 200, M{"threads": threads, "counts": counts})
}

// GET /v1/m/inbox/{id}   opening a thread marks it read
func (s *Server) mThread(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	m := mc(r)
	id := chi.URLParam(r, "id")
	t, err := row(ctx, s.pool, `select t.id, t.client_id, t.client_name, t.channel, t.status, t.assigned_staff_id, t.booking_id, t.created_at, t.user_id is not null as has_account from threads t where t.id=$1 and t.business_id=$2`, id, m.BusinessID)
	if err != nil {
		writeErr(w, 404, "conversation not found")
		return
	}
	_, _ = s.pool.Exec(ctx, `update threads set unread_business=0 where id=$1`, id)
	messages, _ := rows(ctx, s.pool, `select id, from_business, author, body, delivery, created_at from thread_messages where thread_id=$1 order by created_at`, id)
	var client M
	if t["client_id"] != nil {
		client, _ = row(ctx, s.pool, `select c.id, c.name, c.phone, c.email, c.preferred_channel, c.no_show_count, c.created_at,`+clientStats+` from clients c where c.id=$1`, t["client_id"])
	}
	var next M
	if t["client_id"] != nil {
		next, _ = row(ctx, s.pool, `select bk.id, bk.starts_at, bk.status, st.name as staff, (select string_agg(name, ' + ') from booking_items where booking_id = bk.id) as services
			from bookings bk join staff st on st.id = bk.staff_id where bk.client_id=$1 and bk.starts_at > now() - interval '12 hours' and bk.status in ('requested','confirmed','checked_in','in_progress') order by bk.starts_at limit 1`, t["client_id"])
	}
	saved, _ := rows(ctx, s.pool, `select id, title, body from saved_replies where business_id=$1 order by sort, title`, m.BusinessID)
	staff, _ := rows(ctx, s.pool, `select id, name from staff where business_id=$1 and not archived order by name`, m.BusinessID)
	writeJSON(w, 200, M{"thread": t, "messages": messages, "client": client, "booking": next, "saved_replies": saved, "staff": staff, "mail_mode": s.mail.Mode()})
}

// deliver sends one outgoing message on the thread's channel and reports what happened.
func (s *Server) deliver(ctx context.Context, channel, businessName, clientEmail, body string, hasAccount bool) string {
	switch channel {
	case "in_app":
		if hasAccount {
			return "delivered" // the client reads it in their LogaLuxe account
		}
		return "logged"
	case "email":
		if !mail.Valid(clientEmail) {
			return "no email on file"
		}
		status, err := s.mail.Send(ctx, clientEmail, "A message from "+businessName, body+"\n\n"+businessName+" · sent with LogaLuxe")
		if err != nil {
			s.logMailFailure("inbox reply", clientEmail, err)
			return "failed"
		}
		return status // sent or logged
	}
	return "logged" // WhatsApp and SMS are not connected yet
}

// POST /v1/m/inbox/{id}/reply   {body}
func (s *Server) mThreadReply(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	m := mc(r)
	id := chi.URLParam(r, "id")
	var req struct {
		Body string `json:"body"`
	}
	if err := readJSON(r, &req); err != nil || strings.TrimSpace(req.Body) == "" || len(req.Body) > 4000 {
		writeErr(w, 400, "write a message of up to 4,000 characters")
		return
	}
	var channel string
	var hasAccount bool
	var email *string
	if err := s.pool.QueryRow(ctx, `select t.channel, t.user_id is not null, (select c.email from clients c where c.id = t.client_id) from threads t where t.id=$1 and t.business_id=$2`, id, m.BusinessID).Scan(&channel, &hasAccount, &email); err != nil {
		writeErr(w, 404, "conversation not found")
		return
	}
	body := strings.TrimSpace(req.Body)
	to := ""
	if email != nil {
		to = *email
	}
	delivery := s.deliver(ctx, channel, m.Business, to, body, hasAccount)
	if channel == "sms" {
		var phone string
		_ = s.pool.QueryRow(ctx, `select coalesce(c.phone,'') from threads t join clients c on c.id = t.client_id where t.id=$1`, id).Scan(&phone)
		delivery = s.sendSMS(ctx, m.BusinessID, phone, body+"\n"+m.Business)
	}
	if _, err := s.pool.Exec(ctx, `insert into thread_messages (thread_id, from_business, author, body, delivery) values ($1,true,$2,$3,$4)`, id, m.Name, body, delivery); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	_, _ = s.pool.Exec(ctx, `update threads set last_preview=$2, last_message_at=now(), unread_business=0, unread_client=unread_client+1, status='open',
		assigned_staff_id = coalesce(assigned_staff_id, nullif($3,'')::uuid) where id=$1`, id, preview(body), m.StaffID)
	writeJSON(w, 201, M{"ok": true, "delivery": delivery})
}

// PUT /v1/m/inbox/{id}   {status, assigned_staff_id}
func (s *Server) mThreadUpdate(w http.ResponseWriter, r *http.Request) {
	m := mc(r)
	var req struct {
		Status   *string `json:"status"`
		Assigned *string `json:"assigned_staff_id"`
	}
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, "invalid json")
		return
	}
	if req.Status != nil && *req.Status != "open" && *req.Status != "closed" {
		writeErr(w, 400, "status must be open or closed")
		return
	}
	tag, err := s.pool.Exec(r.Context(), `update threads set status = coalesce($3, status),
		assigned_staff_id = case when $4::text is null then assigned_staff_id when $4 = '' then null else (select id from staff where id::text = $4 and business_id=$2) end
		where id=$1 and business_id=$2`, chi.URLParam(r, "id"), m.BusinessID, req.Status, req.Assigned)
	if err != nil || tag.RowsAffected() == 0 {
		writeErr(w, 404, "conversation not found")
		return
	}
	writeJSON(w, 200, M{"ok": true})
}

// POST /v1/m/inbox   {client_id, channel, body}   start a conversation with a client
func (s *Server) mThreadCreate(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	m := mc(r)
	var req struct {
		ClientID string `json:"client_id"`
		Channel  string `json:"channel"`
		Body     string `json:"body"`
	}
	if err := readJSON(r, &req); err != nil || strings.TrimSpace(req.Body) == "" || len(req.Body) > 4000 {
		writeErr(w, 400, "write a message of up to 4,000 characters")
		return
	}
	var name, email, phone, pref string
	var userID *string
	if err := s.pool.QueryRow(ctx, `select name, email, phone, preferred_channel, user_id::text from clients where id=$1 and business_id=$2`, req.ClientID, m.BusinessID).Scan(&name, &email, &phone, &pref, &userID); err != nil {
		writeErr(w, 400, "choose one of your clients")
		return
	}
	if req.Channel == "" { // the best channel that can actually reach them today
		switch {
		case userID != nil:
			req.Channel = "in_app"
		case mail.Valid(email):
			req.Channel = "email"
		default:
			req.Channel = firstNonEmpty(map[string]string{"sms": "sms", "whatsapp": "whatsapp"}[pref], "whatsapp")
		}
	}
	if req.Channel != "in_app" && req.Channel != "email" && req.Channel != "whatsapp" && req.Channel != "sms" {
		writeErr(w, 400, "unknown channel")
		return
	}
	if req.Channel == "email" && !mail.Valid(email) {
		writeErr(w, 400, "this client has no email address on file")
		return
	}
	if (req.Channel == "whatsapp" || req.Channel == "sms") && phone == "" {
		writeErr(w, 400, "this client has no phone number on file")
		return
	}
	body := strings.TrimSpace(req.Body)
	// One open thread per client and channel.
	var id string
	if err := s.pool.QueryRow(ctx, `select id::text from threads where business_id=$1 and client_id=$2 and channel=$3 order by last_message_at desc limit 1`, m.BusinessID, req.ClientID, req.Channel).Scan(&id); err != nil {
		if err := s.pool.QueryRow(ctx, `insert into threads (business_id, client_id, user_id, client_name, channel, assigned_staff_id) values ($1,$2,$3,$4,$5, nullif($6,'')::uuid) returning id::text`,
			m.BusinessID, req.ClientID, userID, name, req.Channel, m.StaffID).Scan(&id); err != nil {
			writeErr(w, 500, err.Error())
			return
		}
	}
	delivery := s.deliver(ctx, req.Channel, m.Business, email, body, userID != nil)
	if req.Channel == "sms" {
		var phone string
		_ = s.pool.QueryRow(ctx, `select coalesce(phone,'') from clients where id=$1 and business_id=$2`, req.ClientID, m.BusinessID).Scan(&phone)
		delivery = s.sendSMS(ctx, m.BusinessID, phone, body+"\n"+m.Business)
	}
	_, _ = s.pool.Exec(ctx, `insert into thread_messages (thread_id, from_business, author, body, delivery) values ($1,true,$2,$3,$4)`, id, m.Name, body, delivery)
	_, _ = s.pool.Exec(ctx, `update threads set last_preview=$2, last_message_at=now(), status='open', unread_client=unread_client+1 where id=$1`, id, preview(body))
	writeJSON(w, 201, M{"ok": true, "id": id, "delivery": delivery})
}

// PUT /v1/m/saved-replies   {replies: [{title, body}]}   replaces the whole list
func (s *Server) mSavedReplies(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	m := mc(r)
	var req struct {
		Replies []struct {
			Title string `json:"title"`
			Body  string `json:"body"`
		} `json:"replies"`
	}
	if err := readJSON(r, &req); err != nil || len(req.Replies) > 30 {
		writeErr(w, 400, "send up to 30 saved replies")
		return
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	defer tx.Rollback(ctx)
	_, _ = tx.Exec(ctx, `delete from saved_replies where business_id=$1`, m.BusinessID)
	n := 0
	for _, rp := range req.Replies {
		t, b := strings.TrimSpace(rp.Title), strings.TrimSpace(rp.Body)
		if t == "" || b == "" || len(t) > 40 || len(b) > 1000 {
			continue
		}
		_, _ = tx.Exec(ctx, `insert into saved_replies (business_id, title, body, sort) values ($1,$2,$3,$4)`, m.BusinessID, t, b, n)
		n++
	}
	if err := tx.Commit(ctx); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, M{"ok": true, "saved": n})
}

// ---------- the client's side of in-app messages ----------

// GET /v1/auth/threads
func (s *Server) authThreads(w http.ResponseWriter, r *http.Request) {
	out, err := rows(r.Context(), s.pool, `select t.id, t.unread_client, t.last_preview, t.last_message_at, b.name as business, b.slug, b.tone
		from threads t join businesses b on b.id = t.business_id where t.user_id=$1 and t.channel='in_app' order by t.last_message_at desc limit 50`, currentCustomer(r).ID)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, M{"threads": out})
}

// GET /v1/auth/threads/{id}
func (s *Server) authThread(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id := chi.URLParam(r, "id")
	t, err := row(ctx, s.pool, `select t.id, b.name as business, b.slug, b.tone from threads t join businesses b on b.id = t.business_id where t.id=$1 and t.user_id=$2`, id, currentCustomer(r).ID)
	if err != nil {
		writeErr(w, 404, "conversation not found")
		return
	}
	_, _ = s.pool.Exec(ctx, `update threads set unread_client=0 where id=$1`, id)
	messages, _ := rows(ctx, s.pool, `select id, from_business, author, body, created_at from thread_messages where thread_id=$1 order by created_at`, id)
	writeJSON(w, 200, M{"thread": t, "messages": messages})
}

// POST /v1/auth/threads   {business_slug, body}   a signed-in client writes to a business
func (s *Server) authThreadSend(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	c := currentCustomer(r)
	var req struct {
		BusinessSlug string `json:"business_slug"`
		Body         string `json:"body"`
	}
	if err := readJSON(r, &req); err != nil || strings.TrimSpace(req.Body) == "" || len(req.Body) > 2000 {
		writeErr(w, 400, "write a message of up to 2,000 characters")
		return
	}
	var bizID string
	if err := s.pool.QueryRow(ctx, `select id::text from businesses where slug=$1 and status in ('live','paused')`, req.BusinessSlug).Scan(&bizID); err != nil {
		writeErr(w, 404, "business not found")
		return
	}
	// A brake on floods: 30 messages an hour from one account is plenty.
	var recent int
	_ = s.pool.QueryRow(ctx, `select count(*) from thread_messages tm join threads t on t.id = tm.thread_id where t.user_id=$1 and not tm.from_business and tm.created_at > now() - interval '1 hour'`, c.ID).Scan(&recent)
	if recent >= 30 {
		writeErr(w, 429, "you have sent a lot of messages; try again in a little while")
		return
	}
	name := strings.TrimSpace(c.FirstName + " " + c.LastName)
	body := strings.TrimSpace(req.Body)
	var id string
	if err := s.pool.QueryRow(ctx, `select id::text from threads where business_id=$1 and user_id=$2 and channel='in_app' limit 1`, bizID, c.ID).Scan(&id); err != nil {
		// Link to the business's own record of this client when there is one.
		var clientID *string
		_ = s.pool.QueryRow(ctx, `select id::text from clients where business_id=$1 and (user_id=$2 or ($3 <> '' and phone=$3) or ($4 <> '' and email=$4)) order by (user_id=$2) desc nulls last limit 1`, bizID, c.ID, c.Phone, c.Email).Scan(&clientID)
		if err := s.pool.QueryRow(ctx, `insert into threads (business_id, client_id, user_id, client_name, channel) values ($1,$2,$3,$4,'in_app') returning id::text`, bizID, clientID, c.ID, name).Scan(&id); err != nil {
			writeErr(w, 500, err.Error())
			return
		}
	}
	_, _ = s.pool.Exec(ctx, `insert into thread_messages (thread_id, from_business, author, body, delivery) values ($1,false,$2,$3,'delivered')`, id, name, body)
	_, _ = s.pool.Exec(ctx, `update threads set last_preview=$2, last_message_at=now(), status='open', unread_business=unread_business+1, unread_client=0 where id=$1`, id, preview(body))
	writeJSON(w, 201, M{"ok": true, "id": id})
}
