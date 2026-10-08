package httpapi

import (
	"encoding/csv"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"logaluxe/api/internal/mail"
)

// The client book. A client is "lapsed" when their last visit was more than
// 60 days ago and nothing is booked ahead.

const clientStats = `
	(select count(*) from bookings bk where bk.client_id = c.id and bk.status in ('completed','paid')) as visits,
	(select coalesce(sum(bk.total_cents),0) from bookings bk where bk.client_id = c.id and bk.status in ('completed','paid')) as spent_cents,
	(select coalesce(sum(bk.tip_cents),0) from bookings bk where bk.client_id = c.id) as tips_cents,
	(select max(bk.starts_at) from bookings bk where bk.client_id = c.id and bk.status in ('completed','paid')) as last_visit,
	(select min(bk.starts_at) from bookings bk where bk.client_id = c.id and bk.starts_at > now() and bk.status in ('requested','confirmed')) as next_visit`

const lapsedWhere = `exists (select 1 from bookings bk where bk.client_id = c.id and bk.status in ('completed','paid'))
	and not exists (select 1 from bookings bk where bk.client_id = c.id and bk.status in ('completed','paid') and bk.starts_at > now() - interval '60 days')
	and not exists (select 1 from bookings bk where bk.client_id = c.id and bk.starts_at > now() and bk.status in ('requested','confirmed'))`

var clientSegments = map[string]string{
	"":         "true",
	"new":      "c.created_at > now() - interval '30 days'",
	"regulars": "(select count(*) from bookings bk where bk.client_id = c.id and bk.status in ('completed','paid')) >= 3",
	"lapsed":   lapsedWhere,
	"upcoming": "exists (select 1 from bookings bk where bk.client_id = c.id and bk.starts_at > now() and bk.status in ('requested','confirmed'))",
	"no_show":  "c.no_show_count > 0",
	"waitlist": "'waitlist' = any(c.tags)",
}

// GET /v1/m/clients?q=&segment=&sort=&page=
func (s *Server) mClients(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	m := mc(r)
	q := r.URL.Query()
	seg, ok := clientSegments[q.Get("segment")]
	if !ok {
		seg = "true"
	}
	order := map[string]string{"name": "c.name", "spent": "spent_cents desc", "visits": "visits desc", "new": "c.created_at desc"}[q.Get("sort")]
	if order == "" {
		order = "last_visit desc nulls last, c.created_at desc"
	}
	page, _ := strconv.Atoi(q.Get("page"))
	if page < 1 {
		page = 1
	}
	const per = 25
	out, err := rows(ctx, s.pool, `select c.id, c.name, c.phone, c.email, c.tags, c.no_show_count, c.preferred_channel, c.marketing_opt_in, c.created_at, count(*) over() as total,`+clientStats+`
		from clients c where c.business_id=$1 and (`+seg+`)
		  and ($2 = '' or c.name ilike '%'||$2||'%' or c.phone like '%'||$2||'%' or c.email ilike '%'||$2||'%')
		order by `+order+` limit `+strconv.Itoa(per)+` offset $3`, m.BusinessID, strings.TrimSpace(q.Get("q")), (page-1)*per)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	total := 0
	for _, c := range out {
		if n, ok := c["total"].(int64); ok {
			total = int(n)
		}
		delete(c, "total")
	}
	counts, _ := row(ctx, s.pool, `select count(*) as all,
		count(*) filter (where `+clientSegments["new"]+`) as new,
		count(*) filter (where `+clientSegments["regulars"]+`) as regulars,
		count(*) filter (where `+lapsedWhere+`) as lapsed,
		count(*) filter (where `+clientSegments["upcoming"]+`) as upcoming,
		count(*) filter (where c.no_show_count > 0) as no_show
		from clients c where c.business_id=$1`, m.BusinessID)
	writeJSON(w, 200, M{"clients": out, "total": total, "page": page, "per_page": per, "counts": counts})
}

// GET /v1/m/clients/{id}
func (s *Server) mClient(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	m := mc(r)
	id := chi.URLParam(r, "id")
	c, err := row(ctx, s.pool, `select c.id, c.name, c.phone, c.email, c.notes, c.tags, c.no_show_count, c.preferred_channel, c.marketing_opt_in, c.birthday, c.consent, c.created_at,`+clientStats+`,
		exists(select 1 from blocked_contacts bc where bc.phone = c.phone and c.phone <> '') as blocked
		from clients c where c.id=$1 and c.business_id=$2`, id, m.BusinessID)
	if err != nil {
		writeErr(w, 404, "client not found")
		return
	}
	visits, _ := rows(ctx, s.pool, `select bk.id, bk.status, bk.starts_at, bk.total_cents, bk.tip_cents, st.name as staff,
		(select string_agg(name, ' + ') from booking_items where booking_id = bk.id) as services
		from bookings bk join staff st on st.id = bk.staff_id where bk.client_id=$1 order by bk.starts_at desc limit 12`, id)
	writeJSON(w, 200, M{"client": c, "visits": visits})
}

type clientReq struct {
	Name     string   `json:"name"`
	Phone    string   `json:"phone"`
	Email    string   `json:"email"`
	Notes    string   `json:"notes"`
	Tags     []string `json:"tags"`
	Birthday string   `json:"birthday"`
	Channel  string   `json:"preferred_channel"`
	OptIn    *bool    `json:"marketing_opt_in"`
}

func (c *clientReq) check() (phone string, birthday *time.Time, msg string) {
	c.Name, c.Email = strings.TrimSpace(c.Name), strings.ToLower(strings.TrimSpace(c.Email))
	phone, ok := cleanPhone(c.Phone)
	switch {
	case c.Name == "" || len(c.Name) > 80:
		return "", nil, "the client needs a name"
	case !ok:
		return "", nil, "that phone number does not look right; include the country code"
	case c.Email != "" && !mail.Valid(c.Email):
		return "", nil, "that email address does not look right"
	case len(c.Notes) > 4000:
		return "", nil, "keep the notes under 4,000 characters"
	}
	if c.Channel != "email" && c.Channel != "sms" {
		c.Channel = "whatsapp"
	}
	if c.Birthday != "" {
		t, err := time.Parse("2006-01-02", c.Birthday)
		if err != nil {
			return "", nil, "the birthday is not a date"
		}
		birthday = &t
	}
	return phone, birthday, ""
}

// POST /v1/m/clients
func (s *Server) mClientCreate(w http.ResponseWriter, r *http.Request) {
	var req clientReq
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, "invalid json")
		return
	}
	phone, birthday, msg := req.check()
	if msg != "" {
		writeErr(w, 400, msg)
		return
	}
	if phone == "" && req.Email == "" {
		writeErr(w, 400, "add a phone number or an email so you can reach them")
		return
	}
	var id string
	err := s.pool.QueryRow(r.Context(), `insert into clients (business_id, name, phone, email, notes, tags, birthday, preferred_channel, marketing_opt_in)
		values ($1,$2,$3,$4,$5,$6,$7,$8,$9) returning id::text`, mc(r).BusinessID, req.Name, phone, req.Email, req.Notes, cleanList(req.Tags, 10), birthday, req.Channel, req.OptIn == nil || *req.OptIn).Scan(&id)
	if err != nil {
		if isCode(err, "23505") {
			writeErr(w, 409, "you already have a client with that phone number")
			return
		}
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 201, M{"ok": true, "id": id})
}

// PUT /v1/m/clients/{id}
func (s *Server) mClientUpdate(w http.ResponseWriter, r *http.Request) {
	var req clientReq
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, "invalid json")
		return
	}
	phone, birthday, msg := req.check()
	if msg != "" {
		writeErr(w, 400, msg)
		return
	}
	tag, err := s.pool.Exec(r.Context(), `update clients set name=$3, phone=$4, email=$5, notes=$6, tags=$7, birthday=$8, preferred_channel=$9, marketing_opt_in=coalesce($10, marketing_opt_in)
		where id=$1 and business_id=$2`, chi.URLParam(r, "id"), mc(r).BusinessID, req.Name, phone, req.Email, req.Notes, cleanList(req.Tags, 10), birthday, req.Channel, req.OptIn)
	if err != nil {
		if isCode(err, "23505") {
			writeErr(w, 409, "another client already has that phone number")
			return
		}
		writeErr(w, 500, err.Error())
		return
	}
	if tag.RowsAffected() == 0 {
		writeErr(w, 404, "client not found")
		return
	}
	writeJSON(w, 200, M{"ok": true})
}

// POST /v1/m/clients/import   {rows: [{name, phone, email, notes}]}
// Rows that match an existing phone number are skipped, not overwritten.
func (s *Server) mClientImport(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	m := mc(r)
	var req struct {
		Rows []clientReq `json:"rows"`
	}
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, "invalid json")
		return
	}
	if len(req.Rows) == 0 || len(req.Rows) > 2000 {
		writeErr(w, 400, "paste between 1 and 2,000 clients at a time")
		return
	}
	added, skipped := 0, 0
	problems := []string{}
	for i, c := range req.Rows {
		phone, _, msg := c.check()
		if msg == "" && phone == "" && c.Email == "" {
			msg = "no phone or email"
		}
		if msg != "" {
			skipped++
			if len(problems) < 8 {
				problems = append(problems, fmt.Sprintf("row %d (%s): %s", i+1, firstNonEmpty(c.Name, "no name"), msg))
			}
			continue
		}
		tag, err := s.pool.Exec(ctx, `insert into clients (business_id, name, phone, email, notes, tags) select $1,$2,$3,$4,$5,'{imported}'
			where not exists (select 1 from clients where business_id=$1 and (($3 <> '' and phone=$3) or ($4 <> '' and email=$4)))`, m.BusinessID, c.Name, phone, c.Email, c.Notes)
		if err != nil || tag.RowsAffected() == 0 {
			skipped++
			continue
		}
		added++
	}
	writeJSON(w, 200, M{"ok": true, "added": added, "skipped": skipped, "problems": problems})
}

// GET /v1/m/clients/export
func (s *Server) mClientExport(w http.ResponseWriter, r *http.Request) {
	m := mc(r)
	out, err := rows(r.Context(), s.pool, `select c.name, c.phone, c.email, c.tags, c.no_show_count, c.preferred_channel, c.marketing_opt_in, c.created_at,`+clientStats+` from clients c where c.business_id=$1 order by c.name limit 20000`, m.BusinessID)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="clients-`+time.Now().Format("2006-01-02")+`.csv"`)
	_, _ = w.Write([]byte("\xEF\xBB\xBF"))
	cw := csv.NewWriter(w)
	cols := []string{"name", "phone", "email", "tags", "visits", "spent_cents", "tips_cents", "no_show_count", "last_visit", "next_visit", "preferred_channel", "marketing_opt_in", "created_at"}
	_ = cw.Write(cols)
	rec := make([]string, len(cols))
	for _, c := range out {
		for i, k := range cols {
			if k == "tags" {
				rec[i] = csvCell(strings.Join(toStrings(c[k]), "; "))
				continue
			}
			rec[i] = csvCell(c[k])
		}
		_ = cw.Write(rec)
	}
	cw.Flush()
}

func toStrings(v any) []string {
	switch x := v.(type) {
	case []string:
		return x
	case []any:
		out := make([]string, 0, len(x))
		for _, i := range x {
			out = append(out, fmt.Sprint(i))
		}
		return out
	}
	return nil
}

// toInt reads a whole number out of a database value, whatever width it came back as.
func toInt(v any) int64 {
	switch x := v.(type) {
	case int:
		return int64(x)
	case int16:
		return int64(x)
	case int32:
		return int64(x)
	case int64:
		return x
	case float64:
		return int64(x)
	}
	return 0
}
