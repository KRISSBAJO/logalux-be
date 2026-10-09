package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"

	"logaluxe/api/internal/booking"
)

// The deeper parts of running a salon: rooms and chairs that limit how many
// clients can be served at once, prices that change by day or by who does the
// work, packages and memberships, chair rental, and what each service uses up.

// ---------- permissions for team members ----------

// Defaults for a team member's sign-in. Managers and the owner can always do all three.
var staffPermDefaults = map[string]bool{"see_all_calendars": true, "take_payments": true, "see_reports": false}

// perm says whether the signed-in person may do something the owner can switch on or off per team member.
func perm(m Merchant, key string) bool {
	if merchantRank[m.Role] >= merchantRank["manager"] {
		return true
	}
	if v, ok := m.Permissions[key]; ok {
		return v
	}
	return staffPermDefaults[key]
}

// mNeedPerm lets through the given role and above, and team members who were given the permission.
func (s *Server) mNeedPerm(role, key string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			m := mc(r)
			if merchantRank[m.Role] < merchantRank[role] && !(m.Permissions[key]) {
				writeErr(w, http.StatusForbidden, "your sign-in does not include this; ask the owner")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// ---------- pricing ----------

type priceRule struct {
	Name, ServiceID, Level, From, To, StartsOn, EndsOn, Kind string
	Days                                                     []string
	Value                                                    int
}

// pricing answers "what does this service cost with this person at this time?".
type pricing struct {
	base     map[string]int            // service -> menu price
	personal map[string]map[string]int // staff -> service -> their own price
	levels   map[string]string         // staff -> level
	rules    []priceRule
}

func (s *Server) loadPricing(ctx context.Context, businessID string) *pricing {
	p := &pricing{base: map[string]int{}, personal: map[string]map[string]int{}, levels: map[string]string{}}
	svcs, _ := rows(ctx, s.pool, `select id, price_cents from services where business_id=$1`, businessID)
	for _, sv := range svcs {
		p.base[fmt.Sprint(sv["id"])] = int(toInt(sv["price_cents"]))
	}
	own, _ := rows(ctx, s.pool, `select ss.staff_id, ss.service_id, ss.price_cents from staff_services ss join staff st on st.id = ss.staff_id where st.business_id=$1 and ss.price_cents is not null`, businessID)
	for _, o := range own {
		st := fmt.Sprint(o["staff_id"])
		if p.personal[st] == nil {
			p.personal[st] = map[string]int{}
		}
		p.personal[st][fmt.Sprint(o["service_id"])] = int(toInt(o["price_cents"]))
	}
	staff, _ := rows(ctx, s.pool, `select id, level from staff where business_id=$1`, businessID)
	for _, st := range staff {
		p.levels[fmt.Sprint(st["id"])] = fmt.Sprint(st["level"])
	}
	rules, _ := rows(ctx, s.pool, `select name, coalesce(service_id::text,'') as service_id, days, coalesce(to_char(from_time,'HH24:MI'),'') as from_time, coalesce(to_char(to_time,'HH24:MI'),'') as to_time,
		level, coalesce(starts_on::text,'') as starts_on, coalesce(ends_on::text,'') as ends_on, adjust_kind, adjust_value
		from price_rules where business_id=$1 and active order by sort, created_at`, businessID)
	for _, r := range rules {
		p.rules = append(p.rules, priceRule{Name: fmt.Sprint(r["name"]), ServiceID: fmt.Sprint(r["service_id"]), Level: fmt.Sprint(r["level"]), From: fmt.Sprint(r["from_time"]), To: fmt.Sprint(r["to_time"]),
			StartsOn: fmt.Sprint(r["starts_on"]), EndsOn: fmt.Sprint(r["ends_on"]), Kind: fmt.Sprint(r["adjust_kind"]), Value: int(toInt(r["adjust_value"])), Days: toStrings(r["days"])})
	}
	return p
}

var dowKeys = []string{"sun", "mon", "tue", "wed", "thu", "fri", "sat"}

// price returns the price of one service for one person at a local time, and the rules that changed it.
// A price the person has for themselves replaces the menu price; rules then adjust it in the order the business put them in.
func (p *pricing) price(serviceID, staffID string, at time.Time) (int, []string) {
	price, ok := p.personal[staffID][serviceID]
	if !ok {
		price = p.base[serviceID]
	}
	var applied []string
	day, clock, date := dowKeys[int(at.Weekday())], at.Format("15:04"), at.Format("2006-01-02")
	for _, r := range p.rules {
		switch {
		case r.ServiceID != "" && r.ServiceID != serviceID:
			continue
		case len(r.Days) > 0 && !contains(r.Days, day):
			continue
		case r.From != "" && clock < r.From, r.To != "" && clock >= r.To:
			continue
		case r.Level != "" && r.Level != p.levels[staffID]:
			continue
		case r.StartsOn != "" && date < r.StartsOn, r.EndsOn != "" && date > r.EndsOn:
			continue
		}
		if r.Kind == "percent" {
			price += int(math.Round(float64(price) * float64(r.Value) / 100))
		} else {
			price += r.Value
		}
		applied = append(applied, r.Name)
	}
	if price < 0 {
		price = 0
	}
	return price, applied
}

func (p *pricing) total(serviceIDs []string, staffID string, at time.Time) int {
	t := 0
	for _, id := range serviceIDs {
		c, _ := p.price(id, staffID, at)
		t += c
	}
	return t
}

func contains(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

// GET /v1/m/menu   everything on the menu besides the services themselves
func (s *Server) mMenu(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	m := mc(r)
	resources, _ := rows(ctx, s.pool, `select re.id, re.name, re.qty,
		(select coalesce(array_agg(sr.service_id::text), '{}') from service_resources sr where sr.resource_id = re.id) as service_ids
		from resources re where re.business_id=$1 order by re.name`, m.BusinessID)
	rules, _ := rows(ctx, s.pool, `select pr.id, pr.name, pr.service_id, sv.name as service, pr.days, to_char(pr.from_time,'HH24:MI') as from_time, to_char(pr.to_time,'HH24:MI') as to_time, pr.level,
		pr.starts_on, pr.ends_on, pr.adjust_kind, pr.adjust_value, pr.active, pr.created_at
		from price_rules pr left join services sv on sv.id = pr.service_id where pr.business_id=$1 order by pr.sort, pr.created_at`, m.BusinessID)
	packages, _ := rows(ctx, s.pool, `select p.id, p.name, p.description, p.price_cents, p.valid_days, p.active, p.created_at,
		(select coalesce(json_agg(json_build_object('service_id', pi.service_id, 'name', sv.name, 'qty', pi.qty, 'price_cents', sv.price_cents) order by sv.sort, sv.name), '[]') from package_items pi join services sv on sv.id = pi.service_id where pi.package_id = p.id) as items,
		(select count(*) from client_plans cp where cp.package_id = p.id) as sold,
		(select count(*) from client_plans cp where cp.package_id = p.id and cp.status = 'active') as active_holders
		from packages p where p.business_id=$1 order by p.active desc, p.name`, m.BusinessID)
	memberships, _ := rows(ctx, s.pool, `select ms.id, ms.name, ms.description, ms.price_cents, ms.service_discount_pct, ms.retail_discount_pct, ms.active, ms.created_at,
		(select coalesce(json_agg(json_build_object('service_id', mi.service_id, 'name', sv.name, 'qty', mi.qty, 'price_cents', sv.price_cents) order by sv.sort, sv.name), '[]') from membership_items mi join services sv on sv.id = mi.service_id where mi.membership_id = ms.id) as items,
		(select count(*) from client_plans cp where cp.membership_id = ms.id and cp.status = 'active') as members,
		(select count(*) from client_plans cp where cp.membership_id = ms.id and cp.status = 'past_due') as past_due
		from memberships ms where ms.business_id=$1 order by ms.active desc, ms.name`, m.BusinessID)
	holders, holdersPage, holdersErr := s.historyRows(r, "holders", `select cp.id, cp.kind, cp.name, cp.status, cp.started_at, cp.expires_at, cp.renews_on, cp.client_id, c.name as client,
		(select coalesce(sum(cc.total - cc.used),0) from client_credits cc where cc.plan_id = cp.id and (cc.expires_at is null or cc.expires_at > now())) as credits_left
		from client_plans cp join clients c on c.id = cp.client_id and c.business_id = cp.business_id where cp.business_id=$1 and ($2 = '' or cp.kind=$2) order by (cp.status in ('active','past_due')) desc, cp.started_at desc`, "(status in ('active','past_due')) desc, started_at desc", "started_at client name kind status credits_left", m.BusinessID, r.URL.Query().Get("holders_kind"))
	if holdersErr != nil {
		writeErr(w, 500, holdersErr.Error())
		return
	}
	writeJSON(w, 200, M{"resources": resources, "price_rules": rules, "packages": packages, "memberships": memberships, "holders": holders, "holders_pagination": holdersPage, "auto_renew": s.payMode(m.Market) == "simulation"})
}

// ---------- rooms, chairs and stations ----------

// POST /v1/m/resources   {name, qty, service_ids}    PUT /v1/m/resources/{id}    DELETE /v1/m/resources/{id}
func (s *Server) mResourceSave(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	m := mc(r)
	var req struct {
		Name       string   `json:"name"`
		Qty        int      `json:"qty"`
		ServiceIDs []string `json:"service_ids"`
	}
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, "invalid json")
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	if len(req.Name) < 2 || len(req.Name) > 60 || req.Qty < 1 || req.Qty > 50 {
		writeErr(w, 400, "give it a name and say how many you have, from 1 to 50")
		return
	}
	id := chi.URLParam(r, "id")
	var err error
	if id == "" {
		err = s.pool.QueryRow(ctx, `insert into resources (business_id, name, qty) values ($1,$2,$3) returning id::text`, m.BusinessID, req.Name, req.Qty).Scan(&id)
	} else {
		err = s.pool.QueryRow(ctx, `update resources set name=$3, qty=$4 where id=$1 and business_id=$2 returning id::text`, id, m.BusinessID, req.Name, req.Qty).Scan(&id)
	}
	if err != nil {
		if isCode(err, "23505") {
			writeErr(w, 409, "you already have one with that name")
			return
		}
		writeErr(w, 404, "not found")
		return
	}
	if req.ServiceIDs != nil {
		_, _ = s.pool.Exec(ctx, `delete from service_resources where resource_id=$1`, id)
		_, _ = s.pool.Exec(ctx, `insert into service_resources (service_id, resource_id) select sv.id, $1 from services sv where sv.business_id=$2 and sv.id::text = any($3) on conflict do nothing`, id, m.BusinessID, req.ServiceIDs)
	}
	writeJSON(w, 200, M{"ok": true, "id": id})
}

func (s *Server) mResourceDelete(w http.ResponseWriter, r *http.Request) {
	tag, err := s.pool.Exec(r.Context(), `delete from resources where id=$1 and business_id=$2`, chi.URLParam(r, "id"), mc(r).BusinessID)
	if err != nil || tag.RowsAffected() == 0 {
		writeErr(w, 404, "not found")
		return
	}
	writeJSON(w, 200, M{"ok": true})
}

// PUT /v1/m/services/{id}/resources   {ids}   what a service needs to be free
func (s *Server) mServiceResources(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	m := mc(r)
	id := chi.URLParam(r, "id")
	var req struct {
		IDs []string `json:"ids"`
	}
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, "invalid json")
		return
	}
	var ok bool
	_ = s.pool.QueryRow(ctx, `select exists(select 1 from services where id=$1 and business_id=$2)`, id, m.BusinessID).Scan(&ok)
	if !ok {
		writeErr(w, 404, "service not found")
		return
	}
	_, _ = s.pool.Exec(ctx, `delete from service_resources where service_id=$1`, id)
	_, _ = s.pool.Exec(ctx, `insert into service_resources (service_id, resource_id) select $1, re.id from resources re where re.business_id=$2 and re.id::text = any($3) on conflict do nothing`, id, m.BusinessID, req.IDs)
	writeJSON(w, 200, M{"ok": true})
}

type resourceUse struct {
	Name  string
	Qty   int
	Taken []booking.Busy
}

// resourcesFor loads the rooms and chairs a set of services needs, with when each is already in use that day.
func (s *Server) resourcesFor(ctx context.Context, businessID string, serviceIDs []string, from, to time.Time, excludeBooking string) []resourceUse {
	need, _ := rows(ctx, s.pool, `select distinct re.id, re.name, re.qty from resources re join service_resources sr on sr.resource_id = re.id
		where re.business_id=$1 and sr.service_id = any($2::uuid[])`, businessID, serviceIDs)
	out := make([]resourceUse, 0, len(need))
	for _, n := range need {
		u := resourceUse{Name: fmt.Sprint(n["name"]), Qty: int(toInt(n["qty"]))}
		busy, _ := rows(ctx, s.pool, `select distinct bk.id, bk.starts_at, bk.ends_at from bookings bk join booking_items bi on bi.booking_id = bk.id join service_resources sr on sr.service_id = bi.service_id
			where sr.resource_id=$1 and bk.status in ('requested','confirmed','checked_in','in_progress') and bk.starts_at < $3 and bk.ends_at > $2 and ($4 = '' or bk.id::text <> $4)`, n["id"], from, to, excludeBooking)
		for _, b := range busy {
			u.Taken = append(u.Taken, booking.Busy{Start: b["starts_at"].(time.Time), End: b["ends_at"].(time.Time)})
		}
		out = append(out, u)
	}
	return out
}

// clash names the first room or chair that is fully taken during a visit, or "" when there is room.
func clash(need []resourceUse, start, end time.Time) string {
	for _, u := range need {
		// The most that overlap at any one moment inside the visit.
		points := []time.Time{start}
		for _, t := range u.Taken {
			if t.Start.After(start) && t.Start.Before(end) {
				points = append(points, t.Start)
			}
		}
		for _, at := range points {
			n := 0
			for _, t := range u.Taken {
				if !t.Start.After(at) && t.End.After(at) && t.Start.Before(end) && t.End.After(start) {
					n++
				}
			}
			if n >= u.Qty {
				return u.Name
			}
		}
	}
	return ""
}

// breaksOn returns a person's breaks on one day as busy time.
func breaksOn(raw any, day time.Time, loc *time.Location) []booking.Busy {
	b, err := json.Marshal(raw)
	if err != nil {
		return nil
	}
	var all map[string][][]string
	if json.Unmarshal(b, &all) != nil {
		return nil
	}
	var out []booking.Busy
	for _, pair := range all[dowKeys[int(day.Weekday())]] {
		if len(pair) != 2 {
			continue
		}
		a, e1 := time.ParseInLocation("2006-01-02 15:04", day.Format("2006-01-02")+" "+pair[0], loc)
		z, e2 := time.ParseInLocation("2006-01-02 15:04", day.Format("2006-01-02")+" "+pair[1], loc)
		if e1 == nil && e2 == nil && z.After(a) {
			out = append(out, booking.Busy{Start: a, End: z})
		}
	}
	return out
}

// ---------- pricing rules ----------

type priceRuleReq struct {
	Name      string   `json:"name"`
	ServiceID string   `json:"service_id"`
	Days      []string `json:"days"`
	From      string   `json:"from_time"`
	To        string   `json:"to_time"`
	Level     string   `json:"level"`
	StartsOn  string   `json:"starts_on"`
	EndsOn    string   `json:"ends_on"`
	Kind      string   `json:"adjust_kind"`  // amount (minor units) or percent
	Value     int      `json:"adjust_value"` // signed: +1500 is $15 more, -15 with percent is 15% off
	Active    *bool    `json:"active"`
}

func (v *priceRuleReq) check() string {
	v.Name = strings.TrimSpace(v.Name)
	days := []string{}
	for _, d := range v.Days {
		if contains(weekDays, d) && !contains(days, d) {
			days = append(days, d)
		}
	}
	v.Days = days
	switch {
	case len(v.Name) < 2 || len(v.Name) > 80:
		return "give the rule a name"
	case v.Kind != "amount" && v.Kind != "percent":
		return "choose an amount or a percentage"
	case v.Value == 0:
		return "the change cannot be zero"
	case v.Kind == "percent" && (v.Value < -90 || v.Value > 200):
		return "a percentage change must be between 90% off and 200% more"
	case (v.From != "" && !clockRe.MatchString(v.From)) || (v.To != "" && !clockRe.MatchString(v.To)):
		return "times look like 09:00"
	case v.From != "" && v.To != "" && v.To <= v.From:
		return "the end time must be after the start time"
	case v.Level != "" && v.Level != "junior" && v.Level != "senior" && v.Level != "master":
		return "level must be junior, senior or master"
	}
	for _, d := range []string{v.StartsOn, v.EndsOn} {
		if d != "" {
			if _, err := time.Parse("2006-01-02", d); err != nil {
				return "dates look like 2026-12-24"
			}
		}
	}
	if v.StartsOn != "" && v.EndsOn != "" && v.EndsOn < v.StartsOn {
		return "the last day must not be before the first"
	}
	return ""
}

// POST /v1/m/price-rules    PUT /v1/m/price-rules/{id}    DELETE /v1/m/price-rules/{id}
func (s *Server) mPriceRuleSave(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	m := mc(r)
	var req priceRuleReq
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, "invalid json")
		return
	}
	if msg := req.check(); msg != "" {
		writeErr(w, 400, msg)
		return
	}
	active := req.Active == nil || *req.Active
	id := chi.URLParam(r, "id")
	const vals = `$2, (select id from services where id::text = $3 and business_id = $1), $4, nullif($5,'')::time, nullif($6,'')::time, $7, nullif($8,'')::date, nullif($9,'')::date, $10, $11, $12`
	args := []any{m.BusinessID, req.Name, req.ServiceID, req.Days, req.From, req.To, req.Level, req.StartsOn, req.EndsOn, req.Kind, req.Value, active}
	var err error
	if id == "" {
		err = s.pool.QueryRow(ctx, `insert into price_rules (business_id, name, service_id, days, from_time, to_time, level, starts_on, ends_on, adjust_kind, adjust_value, active, sort) values ($1, `+vals+`, (select coalesce(max(sort),0)+1 from price_rules where business_id=$1)) returning id::text`, args...).Scan(&id)
	} else {
		err = s.pool.QueryRow(ctx, `update price_rules set (name, service_id, days, from_time, to_time, level, starts_on, ends_on, adjust_kind, adjust_value, active) = (`+vals+`) where business_id=$1 and id=$13 returning id::text`, append(args, id)...).Scan(&id)
	}
	if err != nil {
		writeErr(w, 404, "rule not found")
		return
	}
	writeJSON(w, 200, M{"ok": true, "id": id})
}

// PUT /v1/m/price-rules/order   {ids: [...]}   the order the rules are applied in, first to last
func (s *Server) mPriceRuleOrder(w http.ResponseWriter, r *http.Request) {
	var req struct {
		IDs []string `json:"ids"`
	}
	if err := readJSON(r, &req); err != nil || len(req.IDs) == 0 || len(req.IDs) > 200 {
		writeErr(w, 400, "send the rules in the order you want them")
		return
	}
	ctx := r.Context()
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	defer tx.Rollback(ctx)
	for i, id := range req.IDs {
		if _, err := tx.Exec(ctx, `update price_rules set sort=$3 where id::text=$1 and business_id=$2`, id, mc(r).BusinessID, i+1); err != nil {
			writeErr(w, 500, err.Error())
			return
		}
	}
	if err := tx.Commit(ctx); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, M{"ok": true})
}

func (s *Server) mPriceRuleDelete(w http.ResponseWriter, r *http.Request) {
	tag, err := s.pool.Exec(r.Context(), `delete from price_rules where id=$1 and business_id=$2`, chi.URLParam(r, "id"), mc(r).BusinessID)
	if err != nil || tag.RowsAffected() == 0 {
		writeErr(w, 404, "rule not found")
		return
	}
	writeJSON(w, 200, M{"ok": true})
}

// GET /v1/m/price-check?service=&staff=&at=2026-10-10T10:00   what one booking would cost, and why
func (s *Server) mPriceCheck(w http.ResponseWriter, r *http.Request) {
	m := mc(r)
	q := r.URL.Query()
	at, ok := parseLocal(q.Get("at"), m.Loc)
	if !ok {
		at = time.Now()
	}
	p := s.loadPricing(r.Context(), m.BusinessID)
	base, known := p.base[q.Get("service")]
	if !known {
		writeErr(w, 404, "service not found")
		return
	}
	price, applied := p.price(q.Get("service"), q.Get("staff"), at.In(m.Loc))
	if applied == nil {
		applied = []string{}
	}
	writeJSON(w, 200, M{"menu_cents": base, "price_cents": price, "rules": applied})
}

// ---------- packages and memberships ----------

type planItem struct {
	ServiceID string `json:"service_id"`
	Qty       int    `json:"qty"`
}

type planReq struct {
	Name        string     `json:"name"`
	Description string     `json:"description"`
	PriceCents  int        `json:"price_cents"`
	ValidDays   int        `json:"valid_days"`           // packages
	ServicePct  int        `json:"service_discount_pct"` // memberships
	RetailPct   int        `json:"retail_discount_pct"`  // memberships
	Items       []planItem `json:"items"`
	Active      *bool      `json:"active"`
}

func (v *planReq) check(pkg bool) string {
	v.Name, v.Description = strings.TrimSpace(v.Name), strings.TrimSpace(v.Description)
	switch {
	case len(v.Name) < 2 || len(v.Name) > 80:
		return "give it a name of 2 to 80 characters"
	case len(v.Description) > 600:
		return "keep the description under 600 characters"
	case v.PriceCents < 0:
		return "the price cannot be negative"
	case pkg && len(v.Items) == 0:
		return "a package needs at least one service in it"
	case pkg && (v.ValidDays < 1 || v.ValidDays > 1825):
		return "say how many days the package can be used for, up to five years"
	case !pkg && (v.ServicePct < 0 || v.ServicePct > 100 || v.RetailPct < 0 || v.RetailPct > 100):
		return "a discount is a percentage between 0 and 100"
	case !pkg && v.ServicePct == 0 && v.RetailPct == 0 && len(v.Items) == 0:
		return "a membership needs a benefit: a discount or an included service"
	}
	for _, it := range v.Items {
		if it.Qty < 1 || it.Qty > 100 {
			return "each included service needs a number from 1 to 100"
		}
	}
	return ""
}

func (s *Server) savePlan(w http.ResponseWriter, r *http.Request, pkg bool) {
	ctx := r.Context()
	m := mc(r)
	var req planReq
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, "invalid json")
		return
	}
	if pkg && req.ValidDays == 0 {
		req.ValidDays = 365
	}
	if msg := req.check(pkg); msg != "" {
		writeErr(w, 400, msg)
		return
	}
	active := req.Active == nil || *req.Active
	id := chi.URLParam(r, "id")
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	defer tx.Rollback(ctx)
	table, items, key := "memberships", "membership_items", "membership_id"
	if pkg {
		table, items, key = "packages", "package_items", "package_id"
	}
	switch {
	case pkg && id == "":
		err = tx.QueryRow(ctx, `insert into packages (business_id, name, description, price_cents, valid_days, active) values ($1,$2,$3,$4,$5,$6) returning id::text`, m.BusinessID, req.Name, req.Description, req.PriceCents, req.ValidDays, active).Scan(&id)
	case pkg:
		err = tx.QueryRow(ctx, `update packages set name=$3, description=$4, price_cents=$5, valid_days=$6, active=$7 where id=$1 and business_id=$2 returning id::text`, id, m.BusinessID, req.Name, req.Description, req.PriceCents, req.ValidDays, active).Scan(&id)
	case id == "":
		err = tx.QueryRow(ctx, `insert into memberships (business_id, name, description, price_cents, service_discount_pct, retail_discount_pct, active) values ($1,$2,$3,$4,$5,$6,$7) returning id::text`, m.BusinessID, req.Name, req.Description, req.PriceCents, req.ServicePct, req.RetailPct, active).Scan(&id)
	default:
		err = tx.QueryRow(ctx, `update memberships set name=$3, description=$4, price_cents=$5, service_discount_pct=$6, retail_discount_pct=$7, active=$8 where id=$1 and business_id=$2 returning id::text`, id, m.BusinessID, req.Name, req.Description, req.PriceCents, req.ServicePct, req.RetailPct, active).Scan(&id)
	}
	if err != nil {
		writeErr(w, 404, "not found")
		return
	}
	if _, err := tx.Exec(ctx, `delete from `+items+` where `+key+`=$1`, id); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	for _, it := range req.Items {
		tag, err := tx.Exec(ctx, `insert into `+items+` (`+key+`, service_id, qty) select $1, sv.id, $3 from services sv where sv.id::text=$2 and sv.business_id=$4 on conflict do nothing`, id, it.ServiceID, it.Qty, m.BusinessID)
		if err != nil || tag.RowsAffected() == 0 {
			writeErr(w, 400, "one of those services is not on your menu")
			return
		}
	}
	if err := tx.Commit(ctx); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	_ = table
	writeJSON(w, 200, M{"ok": true, "id": id})
}

// POST /v1/m/packages   PUT /v1/m/packages/{id}   {name, description, price_cents, valid_days, items:[{service_id, qty}], active}
func (s *Server) mPackageSave(w http.ResponseWriter, r *http.Request) { s.savePlan(w, r, true) }

// POST /v1/m/memberships   PUT /v1/m/memberships/{id}   {name, description, price_cents (a month), service_discount_pct, retail_discount_pct, items, active}
func (s *Server) mMembershipSave(w http.ResponseWriter, r *http.Request) { s.savePlan(w, r, false) }

// DELETE /v1/m/packages/{id}   DELETE /v1/m/memberships/{id}
// One that a client has ever held is switched off instead of deleted, so their record stays whole.
func (s *Server) deletePlan(w http.ResponseWriter, r *http.Request, pkg bool) {
	ctx := r.Context()
	m := mc(r)
	id := chi.URLParam(r, "id")
	table, key := "memberships", "membership_id"
	if pkg {
		table, key = "packages", "package_id"
	}
	var held bool
	_ = s.pool.QueryRow(ctx, `select exists(select 1 from client_plans where `+key+`=$1)`, id).Scan(&held)
	if held {
		tag, _ := s.pool.Exec(ctx, `update `+table+` set active=false where id=$1 and business_id=$2`, id, m.BusinessID)
		if tag.RowsAffected() == 0 {
			writeErr(w, 404, "not found")
			return
		}
		writeJSON(w, 200, M{"ok": true, "archived": true})
		return
	}
	tag, err := s.pool.Exec(ctx, `delete from `+table+` where id=$1 and business_id=$2`, id, m.BusinessID)
	if err != nil || tag.RowsAffected() == 0 {
		writeErr(w, 404, "not found")
		return
	}
	writeJSON(w, 200, M{"ok": true})
}
func (s *Server) mPackageDelete(w http.ResponseWriter, r *http.Request)    { s.deletePlan(w, r, true) }
func (s *Server) mMembershipDelete(w http.ResponseWriter, r *http.Request) { s.deletePlan(w, r, false) }

const clientPlansHistorySQL = `select cp.id, cp.kind, cp.name, cp.status, cp.price_cents, cp.started_at, cp.expires_at, cp.renews_on, cp.package_id, cp.membership_id, (cp.pay_method <> '') as card_on_file, cp.charge_problem,
		ms.service_discount_pct, ms.retail_discount_pct,
		(select coalesce(json_agg(json_build_object('id', cc.id, 'service_id', cc.service_id, 'service', cc.service_name, 'total', cc.total, 'used', cc.used, 'left', cc.total - cc.used,
			'expires_at', cc.expires_at, 'usable', (cc.used < cc.total and (cc.expires_at is null or cc.expires_at > now()) and cp.status = 'active')) order by cc.service_name), '[]') from client_credits cc where cc.plan_id = cp.id) as credits
		from client_plans cp left join memberships ms on ms.id = cp.membership_id
		where cp.client_id=$1 and cp.business_id=$2 order by (cp.status in ('active','past_due')) desc, cp.started_at desc`

// clientPlans lists what one client holds, with the credits that can still be used.
func (s *Server) clientPlans(ctx context.Context, businessID, clientID string) (plans []M, member M) {
	plans, _ = rows(ctx, s.pool, clientPlansHistorySQL, clientID, businessID)
	if plans == nil {
		plans = []M{}
	}
	for _, p := range plans {
		if p["kind"] == "membership" && p["status"] == "active" {
			if member == nil || toInt(p["service_discount_pct"]) > toInt(member["service_discount_pct"]) {
				member = M{"name": p["name"], "service_discount_pct": p["service_discount_pct"], "retail_discount_pct": p["retail_discount_pct"]}
			}
		}
	}
	return plans, member
}

// GET /v1/m/clients/{id}/plans   the packages and memberships a client holds
func (s *Server) mClientPlans(w http.ResponseWriter, r *http.Request) {
	m := mc(r)
	id := chi.URLParam(r, "id")
	var ok bool
	_ = s.pool.QueryRow(r.Context(), `select exists(select 1 from clients where id=$1 and business_id=$2)`, id, m.BusinessID).Scan(&ok)
	if !ok {
		writeErr(w, 404, "client not found")
		return
	}
	plans, plansPage, err := s.historyRows(r, "plans", clientPlansHistorySQL, "(status in ('active','past_due')) desc, started_at desc", "started_at name kind status price_cents", id, m.BusinessID)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	member, _ := row(r.Context(), s.pool, `select cp.name, ms.service_discount_pct, ms.retail_discount_pct from client_plans cp join memberships ms on ms.id=cp.membership_id where cp.client_id=$1 and cp.business_id=$2 and cp.kind='membership' and cp.status='active' order by ms.service_discount_pct desc, cp.id limit 1`, id, m.BusinessID)

	writeJSON(w, 200, M{"plans": plans, "plans_pagination": plansPage, "member": member, "points": pointsOf(r.Context(), s.pool, m.BusinessID, id), "loyalty": loyaltyFor(r.Context(), s.pool, m.BusinessID)})
}

// POST /v1/m/client-plans/{id}   {action: cancel|reactivate}
// Cancelling a membership stops the next renewal; the client keeps this month's benefits until then.
// Cancelling a package ends it and forfeits what is left, so the screen asks first.
func (s *Server) mClientPlanAction(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	m := mc(r)
	var req struct {
		Action string `json:"action"`
	}
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, "invalid json")
		return
	}
	id := chi.URLParam(r, "id")
	var tagRows int64
	switch req.Action {
	case "cancel":
		tag, _ := s.pool.Exec(ctx, `update client_plans set status='cancelled', cancelled_at=now() where id=$1 and business_id=$2 and status in ('active','past_due')`, id, m.BusinessID)
		tagRows = tag.RowsAffected()
	case "reactivate":
		tag, _ := s.pool.Exec(ctx, `update client_plans set status='active', cancelled_at=null, renews_on = case when kind='membership' then greatest(renews_on, current_date) else renews_on end
			where id=$1 and business_id=$2 and status in ('cancelled','past_due') and (expires_at is null or expires_at > now())`, id, m.BusinessID)
		tagRows = tag.RowsAffected()
	default:
		writeErr(w, 400, "action must be cancel or reactivate")
		return
	}
	if tagRows == 0 {
		writeErr(w, 409, "that cannot be done to this plan in its current state")
		return
	}
	writeJSON(w, 200, M{"ok": true})
}

// renewMemberships runs once a day per membership: in simulation the monthly
// charge is recorded and the included services are topped up. With live
// payments there is no saved card to charge yet, so the membership is marked
// as owing and the desk takes the payment.
func (s *Server) renewMemberships(ctx context.Context) {
	due, err := rows(ctx, s.pool, `select cp.id, cp.business_id, cp.client_id, cp.membership_id, cp.name, cp.renews_on, cp.pay_provider, cp.pay_customer, cp.pay_method, cp.pay_email, c.name as client, b.currency, b.market, b.plan, ms.price_cents, ms.active
		from client_plans cp join clients c on c.id = cp.client_id join businesses b on b.id = cp.business_id left join memberships ms on ms.id = cp.membership_id
		where cp.kind='membership' and cp.status='active' and cp.renews_on <= (now() at time zone b.timezone)::date limit 300`)
	if err != nil {
		return
	}
	for _, p := range due {
		if p["membership_id"] == nil || p["active"] != true {
			_, _ = s.pool.Exec(ctx, `update client_plans set status='expired' where id=$1`, p["id"]) // the plan was withdrawn
			continue
		}
		method := "card" // simulated
		if s.payMode(fmt.Sprint(p["market"])) != "simulation" {
			// Charge the card the member saved when they joined by pay link. Without one, or if the bank says no, the desk collects.
			if err := s.chargeSavedCard(ctx, fmt.Sprint(p["id"]), fmt.Sprint(p["pay_provider"]), fmt.Sprint(p["pay_customer"]), fmt.Sprint(p["pay_method"]), fmt.Sprint(p["pay_email"]), fmt.Sprint(p["currency"]),
				fmt.Sprint(p["name"])+" membership", int(toInt(p["price_cents"]))); err != nil {
				_, _ = s.pool.Exec(ctx, `update client_plans set status='past_due', charge_problem=$2 where id=$1`, p["id"], err.Error())
				continue
			}
			method = "link"
		}
		tx, err := s.pool.Begin(ctx)
		if err != nil {
			return
		}
		price := int(toInt(p["price_cents"]))
		var saleID string
		err = tx.QueryRow(ctx, `insert into sales (business_id, client_id, client_name, subtotal_cents, total_cents, method, note, created_by) values ($1,$2,$3,$4,$4,$5,'Membership renewal','automatic') returning id::text`,
			p["business_id"], p["client_id"], p["client"], price, method).Scan(&saleID)
		if err == nil {
			_, err = tx.Exec(ctx, `insert into sale_items (sale_id, kind, name, qty, unit_cents) values ($1,'membership',$2,1,$3)`, saleID, p["name"], price)
		}
		if err == nil && price > 0 {
			_, err = tx.Exec(ctx, `insert into ledger (business_id, kind, amount_cents, currency, method, status, sale_id, description, settles_at) values ($1,'charge',$2,$3,'card','pending',$4,$5, now() + interval '`+settleAfter+`')`,
				p["business_id"], price, p["currency"], saleID, fmt.Sprint(p["client"])+" · "+fmt.Sprint(p["name"])+" renewal")
			if fee := feeFor(ctx, tx, fmt.Sprint(p["market"]), fmt.Sprint(p["plan"]), price); err == nil && fee > 0 {
				_, err = tx.Exec(ctx, `insert into ledger (business_id, kind, amount_cents, currency, method, status, sale_id, description, settles_at) values ($1,'fee',$2,$3,'card','pending',$4,'LogaLuxe fee · membership', now() + interval '`+settleAfter+`')`,
					p["business_id"], -fee, p["currency"], saleID)
			}
		}
		if err == nil {
			_, err = tx.Exec(ctx, `update client_plans set renews_on = (renews_on + interval '1 month')::date where id=$1`, p["id"])
		}
		if err == nil {
			_, err = tx.Exec(ctx, `delete from client_credits where plan_id=$1`, p["id"])
		}
		if err == nil {
			_, err = tx.Exec(ctx, `insert into client_credits (plan_id, service_id, service_name, total, expires_at)
				select $1, sv.id, sv.name, mi.qty, (select renews_on from client_plans where id=$1)::timestamptz from membership_items mi join services sv on sv.id = mi.service_id where mi.membership_id=$2`, p["id"], p["membership_id"])
		}
		if err != nil {
			_ = tx.Rollback(ctx)
			continue
		}
		_ = tx.Commit(ctx)
	}
	// Packages past their last day.
	_, _ = s.pool.Exec(ctx, `update client_plans set status='expired' where kind='package' and status='active' and expires_at < now()`)
}

// ---------- chair rental ----------

// chargeRent opens this period's rent for each renter. It never charges a card: the owner marks it paid.
func (s *Server) chargeRent(ctx context.Context) {
	_, _ = s.pool.Exec(ctx, `insert into rent_charges (business_id, staff_id, period_start, period_end, amount_cents)
		select st.business_id, st.id, p.start, p.finish, st.rent_cents
		from staff st join businesses b on b.id = st.business_id,
		lateral (select case when st.rent_period = 'monthly' then date_trunc('month', now() at time zone b.timezone)::date else date_trunc('week', now() at time zone b.timezone)::date end as start) s0,
		lateral (select s0.start as start, case when st.rent_period = 'monthly' then (s0.start + interval '1 month' - interval '1 day')::date else s0.start + 6 end as finish) p
		where st.pay_type = 'renter' and st.rent_cents > 0 and not st.archived
		on conflict (staff_id, period_start) do nothing`)
}

// POST /v1/m/rent/{id}   {action: paid|waive|reopen, method, note}
func (s *Server) mRentAction(w http.ResponseWriter, r *http.Request) {
	m := mc(r)
	var req struct {
		Action string `json:"action"`
		Method string `json:"method"`
		Note   string `json:"note"`
	}
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, "invalid json")
		return
	}
	status := map[string]string{"paid": "paid", "waive": "waived", "reopen": "due"}[req.Action]
	if status == "" {
		writeErr(w, 400, "action must be paid, waive or reopen")
		return
	}
	tag, err := s.pool.Exec(r.Context(), `update rent_charges set status=$3, method=$4, note=$5, paid_at = case when $3 = 'paid' then now() end where id=$1 and business_id=$2`,
		chi.URLParam(r, "id"), m.BusinessID, status, strings.TrimSpace(req.Method), strings.TrimSpace(req.Note))
	if err != nil || tag.RowsAffected() == 0 {
		writeErr(w, 404, "rent charge not found")
		return
	}
	writeJSON(w, 200, M{"ok": true})
}

// ---------- time off: approve and move the bookings ----------

// reassign moves each booking a person has during their time off to someone else who does the same
// services and is free. It reports how many moved and how many still need a person to sort out.
func (s *Server) reassign(ctx context.Context, m Merchant, timeOffID string) (moved, left int) {
	bks, _ := rows(ctx, s.pool, `select bk.id, bk.starts_at, bk.ends_at, bk.staff_id from bookings bk join time_off t on t.staff_id = bk.staff_id
		where t.id=$1 and t.business_id=$2 and (bk.starts_at at time zone $3)::date between t.starts_on and t.ends_on and bk.status in ('requested','confirmed') order by bk.starts_at`, timeOffID, m.BusinessID, m.Timezone)
	for _, b := range bks {
		start, end := b["starts_at"].(time.Time), b["ends_at"].(time.Time)
		day := start.In(m.Loc)
		cands, _ := rows(ctx, s.pool, `select st.id, st.hours, st.breaks from staff st
			where st.business_id=$1 and st.bookable and not st.archived and st.id <> $2 and st.pay_type <> 'renter'
			  and not exists (select 1 from booking_items bi where bi.booking_id=$3 and bi.service_id is not null and not exists (select 1 from staff_services ss where ss.staff_id=st.id and ss.service_id=bi.service_id))
			  and not exists (select 1 from time_off t where t.staff_id=st.id and t.status='approved' and $4::date between t.starts_on and t.ends_on)
			  and not exists (select 1 from calendar_blocks cb where cb.staff_id=st.id and cb.starts_at < $6 and cb.ends_at > $5)
			order by (select count(*) from bookings x where x.staff_id=st.id and x.starts_at::date = $4::date)`, m.BusinessID, b["staff_id"], b["id"], day.Format("2006-01-02"), start, end)
		done := false
		for _, c := range cands {
			if h := hoursOf(c["hours"]); h != nil { // someone with their own week must be working then
				day0 := time.Date(day.Year(), day.Month(), day.Day(), 0, 0, 0, 0, m.Loc)
				free := false
				for _, t := range booking.Slots(booking.Params{Day: day0, Loc: m.Loc, Hours: h, Busy: breaksOn(c["breaks"], day0, m.Loc), DurationMin: int(end.Sub(start).Minutes()), IntervalMin: 5}) {
					if t.Equal(start) {
						free = true
						break
					}
				}
				if !free {
					continue
				}
			}
			// The database refuses the move if it would double-book them.
			if tag, err := s.pool.Exec(ctx, `update bookings set staff_id=$2 where id=$1`, b["id"], c["id"]); err == nil && tag.RowsAffected() == 1 {
				done = true
				break
			}
		}
		if done {
			moved++
		} else {
			left++
		}
	}
	return moved, left
}

// ---------- the price list, pasted in ----------

// POST /v1/m/services/import   {rows: [{name, category, duration_min, price_cents, deposit_cents}]}
func (s *Server) mServiceImport(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	m := mc(r)
	var req struct {
		Rows []struct {
			Name         string `json:"name"`
			Category     string `json:"category"`
			DurationMin  int    `json:"duration_min"`
			PriceCents   int    `json:"price_cents"`
			DepositCents int    `json:"deposit_cents"`
		} `json:"rows"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		writeErr(w, 400, "invalid json")
		return
	}
	if len(req.Rows) == 0 || len(req.Rows) > 300 {
		writeErr(w, 400, "paste between 1 and 300 services at a time")
		return
	}
	var next int
	_ = s.pool.QueryRow(ctx, `select coalesce(max(sort),0) from services where business_id=$1`, m.BusinessID).Scan(&next)
	added, skipped := 0, []string{}
	for i, row := range req.Rows {
		name, cat := strings.TrimSpace(row.Name), strings.TrimSpace(row.Category)
		if cat == "" {
			cat = "Services"
		}
		why := ""
		switch {
		case len(name) < 2 || len(name) > 100:
			why = "needs a name"
		case row.DurationMin < 5 || row.DurationMin > 720:
			why = "needs a length between 5 minutes and 12 hours"
		case row.PriceCents < 0 || row.DepositCents < 0 || row.DepositCents > row.PriceCents:
			why = "has a price or deposit that does not add up"
		}
		if why == "" {
			var dup bool
			_ = s.pool.QueryRow(ctx, `select exists(select 1 from services where business_id=$1 and lower(name)=lower($2) and not archived)`, m.BusinessID, name).Scan(&dup)
			if dup {
				why = "is already on your menu"
			}
		}
		if why != "" {
			skipped = append(skipped, "Line "+itoa(i+1)+" "+why)
			continue
		}
		next++
		var id string
		if err := s.pool.QueryRow(ctx, `insert into services (business_id, name, category, duration_min, price_cents, deposit_cents, sort) values ($1,$2,$3,$4,$5,$6,$7) returning id::text`,
			m.BusinessID, name, cat, row.DurationMin, row.PriceCents, row.DepositCents, next).Scan(&id); err != nil {
			skipped = append(skipped, "Line "+itoa(i+1)+" could not be saved")
			continue
		}
		_, _ = s.pool.Exec(ctx, `insert into staff_services (staff_id, service_id) select id, $2 from staff where business_id=$1 and bookable and not archived on conflict do nothing`, m.BusinessID, id)
		added++
	}
	writeJSON(w, 200, M{"ok": true, "added": added, "skipped": skipped})
}

// ---------- products: the photo, and which services use them ----------

// POST /v1/m/products/{id}/photo   multipart: file   one photo per product; a new one replaces the old
func (s *Server) mProductPhoto(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	m := mc(r)
	if s.store == nil {
		writeErr(w, 503, "photo storage is not set up yet; contact LogaLuxe support")
		return
	}
	var slug string
	if err := s.pool.QueryRow(ctx, `select slug from products where id=$1 and business_id=$2`, chi.URLParam(r, "id"), m.BusinessID).Scan(&slug); err != nil {
		writeErr(w, 404, "product not found")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxImageBytes+(1<<20))
	if err := r.ParseMultipartForm(maxImageBytes + (1 << 20)); err != nil {
		writeErr(w, 413, "the photo is too large; the limit is 8 MB")
		return
	}
	file, _, err := r.FormFile("file")
	if err != nil {
		writeErr(w, 400, "choose a photo to upload")
		return
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxImageBytes+1))
	if err != nil || len(data) == 0 || len(data) > maxImageBytes {
		writeErr(w, 413, "the photo could not be read, or is over 8 MB")
		return
	}
	contentType := http.DetectContentType(data) // trust the bytes, not the file name
	ext, ok := imageExt[contentType]
	if !ok {
		writeErr(w, 415, "use a JPEG, PNG or WebP photo")
		return
	}
	var id string
	_ = s.pool.QueryRow(ctx, `select gen_random_uuid()::text`).Scan(&id)
	key := "site/product/" + slug + "/" + id + ext
	if err := s.store.Put(ctx, key, contentType, data); err != nil {
		writeErr(w, 502, "the upload failed; try again")
		return
	}
	old, _ := rows(ctx, s.pool, `select id, storage_key from site_media where slot='product' and ref=$1`, slug)
	if _, err := s.pool.Exec(ctx, `insert into site_media (id, slot, ref, storage_key, content_type, size_bytes, alt, uploaded_by, sort) values ($1,'product',$2,$3,$4,$5,$6,$7,0)`,
		id, slug, key, contentType, len(data), strings.TrimSpace(r.FormValue("alt")), m.Email); err != nil {
		_ = s.store.Delete(ctx, key)
		writeErr(w, 500, err.Error())
		return
	}
	for _, o := range old {
		_, _ = s.pool.Exec(ctx, `delete from site_media where id=$1`, o["id"])
		_ = s.store.Delete(ctx, fmt.Sprint(o["storage_key"]))
	}
	writeJSON(w, 201, M{"ok": true, "id": id})
}

// DELETE /v1/m/products/{id}/photo
func (s *Server) mProductPhotoDelete(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	m := mc(r)
	old, _ := rows(ctx, s.pool, `select sm.id, sm.storage_key from site_media sm join products p on p.slug = sm.ref where sm.slot='product' and p.id=$1 and p.business_id=$2`, chi.URLParam(r, "id"), m.BusinessID)
	for _, o := range old {
		_, _ = s.pool.Exec(ctx, `delete from site_media where id=$1`, o["id"])
		if s.store != nil {
			_ = s.store.Delete(ctx, fmt.Sprint(o["storage_key"]))
		}
	}
	writeJSON(w, 200, M{"ok": true, "removed": len(old)})
}

// PUT /v1/m/products/{id}/services   {items: [{service_id, qty}]}
// How much of this product one service uses. Checkout takes it off the shelf as each service is paid for.
func (s *Server) mProductServices(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	m := mc(r)
	id := chi.URLParam(r, "id")
	var req struct {
		Items []struct {
			ServiceID string  `json:"service_id"`
			Qty       float64 `json:"qty"`
		} `json:"items"`
	}
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, "invalid json")
		return
	}
	var ok bool
	_ = s.pool.QueryRow(ctx, `select exists(select 1 from products where id=$1 and business_id=$2)`, id, m.BusinessID).Scan(&ok)
	if !ok {
		writeErr(w, 404, "product not found")
		return
	}
	for _, it := range req.Items {
		if it.Qty <= 0 || it.Qty > 100 {
			writeErr(w, 400, "the amount used per service must be more than 0 and at most 100")
			return
		}
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	defer tx.Rollback(ctx)
	_, _ = tx.Exec(ctx, `delete from service_products where product_id=$1`, id)
	for _, it := range req.Items {
		if _, err := tx.Exec(ctx, `insert into service_products (service_id, product_id, qty) select sv.id, $1, $3 from services sv where sv.id::text=$2 and sv.business_id=$4 on conflict (service_id, product_id) do update set qty = excluded.qty`,
			id, it.ServiceID, it.Qty, m.BusinessID); err != nil {
			writeErr(w, 500, err.Error())
			return
		}
	}
	if err := tx.Commit(ctx); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, M{"ok": true})
}

// useBackbar takes what a service uses off the shelf. Part-used units are remembered, so a service
// that uses a tenth of a bottle takes one bottle off the count on the tenth time.
func useBackbar(ctx context.Context, tx pgx.Tx, businessID, serviceID string, times int, who, actor string) {
	rs, err := tx.Query(ctx, `select sp.product_id::text, sp.qty::float8 from service_products sp join products p on p.id = sp.product_id where sp.service_id=$1 and p.business_id=$2`, serviceID, businessID)
	if err != nil {
		return
	}
	type use struct {
		id  string
		qty float64
	}
	var uses []use
	for rs.Next() {
		var u use
		if rs.Scan(&u.id, &u.qty) == nil {
			uses = append(uses, u)
		}
	}
	rs.Close()
	for _, u := range uses {
		var whole int
		if err := tx.QueryRow(ctx, `update products set backbar_open = backbar_open + $2 where id=$1 returning floor(backbar_open)::int`, u.id, u.qty*float64(times)).Scan(&whole); err != nil || whole <= 0 {
			continue
		}
		_, _ = tx.Exec(ctx, `update products set stock = greatest(stock - $2, 0), backbar_open = backbar_open - $2 where id=$1`, u.id, whole)
		_, _ = tx.Exec(ctx, `insert into stock_movements (business_id, product_id, delta, reason, note, actor) values ($1,$2,$3,'backbar',$4,$5)`, businessID, u.id, -whole, "Used for "+who, actor)
	}
}

// ---------- pay ----------

// rosteredMinutes adds up the time a person was down to work between two days, without breaks or approved time off.
func rosteredMinutes(own, location booking.Hours, breaks any, off [][2]string, from, to time.Time, loc *time.Location) int {
	hours := own
	if hours == nil {
		hours = location
	}
	if hours == nil {
		return 0
	}
	total := 0
	for d := from; d.Before(to); d = d.AddDate(0, 0, 1) {
		key := d.Format("2006-01-02")
		away := false
		for _, o := range off {
			if key >= o[0] && key <= o[1] {
				away = true
				break
			}
		}
		h := hours[dowKeys[int(d.Weekday())]]
		if away || h == nil {
			continue
		}
		a, e1 := time.Parse("15:04", h[0])
		z, e2 := time.Parse("15:04", h[1])
		if e1 != nil || e2 != nil || !z.After(a) {
			continue
		}
		mins := int(z.Sub(a).Minutes())
		for _, b := range breaksOn(breaks, d, loc) {
			mins -= int(b.End.Sub(b.Start).Minutes())
		}
		if mins > 0 {
			total += mins
		}
	}
	return total
}

// payroll works out what each person earned between two moments: wage or salary, commission and tips.
// Chair renters are not paid by the business, so they are listed apart with the rent they owe.
func (s *Server) payroll(ctx context.Context, m Merchant, from, to time.Time) (people []M, renters []M, err error) {
	people, err = rows(ctx, s.pool, `select st.id, st.name, st.pay_type, st.hourly_cents, st.salary_cents, st.commission_pct, st.retail_commission_pct, st.hours, st.breaks,
		coalesce(sum(case when si.credit_id is not null then coalesce(si.list_cents,0) else si.unit_cents * si.qty end) filter (where si.kind in ('service','custom')),0)::int as service_cents,
		coalesce(sum(si.unit_cents * si.qty) filter (where si.kind = 'product'),0)::int as retail_cents,
		coalesce(sum(si.unit_cents * si.qty) filter (where si.kind in ('package','membership')),0)::int as plan_cents,
		(select coalesce(sum(sa2.tip_cents),0) from sales sa2 where sa2.staff_id = st.id and sa2.created_at >= $2 and sa2.created_at < $3)::int as tips_cents,
		count(distinct si.sale_id) as sales
		from staff st left join (
		  select i.staff_id, i.sale_id, i.kind, i.unit_cents, i.qty, i.credit_id, i.list_cents from sale_items i join sales sa on sa.id = i.sale_id
		  where sa.business_id=$1 and sa.created_at >= $2 and sa.created_at < $3 and sa.status <> 'refunded') si on si.staff_id = st.id
		where st.business_id=$1 and st.pay_type <> 'renter' and (not st.archived or si.sale_id is not null) group by st.id order by st.name`, m.BusinessID, from, to)
	if err != nil {
		return nil, nil, err
	}
	var locRaw any
	_ = s.pool.QueryRow(ctx, `select hours from locations where business_id=$1 and is_primary`, m.BusinessID).Scan(&locRaw)
	locHours := hoursOf(locRaw)
	offRows, _ := rows(ctx, s.pool, `select staff_id, starts_on::text as a, ends_on::text as z from time_off where business_id=$1 and status='approved'`, m.BusinessID)
	off := map[string][][2]string{}
	for _, o := range offRows {
		id := fmt.Sprint(o["staff_id"])
		off[id] = append(off[id], [2]string{fmt.Sprint(o["a"]), fmt.Sprint(o["z"])})
	}
	day0 := time.Date(from.In(m.Loc).Year(), from.In(m.Loc).Month(), from.In(m.Loc).Day(), 0, 0, 0, 0, m.Loc)
	days := to.Sub(from).Hours() / 24
	for _, p := range people {
		id := fmt.Sprint(p["id"])
		commission := func(base, pct any) int {
			f, _ := pct.(float64)
			return int(math.Round(float64(toInt(base)) * f / 100))
		}
		pctOf := func(v any) float64 { // numeric columns arrive as float64 or as a pgtype; go through the text form
			var f float64
			_, _ = fmt.Sscan(fmt.Sprint(v), &f)
			return f
		}
		p["commission_pct"], p["retail_commission_pct"] = pctOf(p["commission_pct"]), pctOf(p["retail_commission_pct"])
		p["service_commission_cents"] = commission(p["service_cents"], p["commission_pct"])
		p["retail_commission_cents"] = commission(p["retail_cents"], p["retail_commission_pct"])
		mins := rosteredMinutes(hoursOf(p["hours"]), locHours, p["breaks"], off[id], day0, to, m.Loc)
		wage := 0
		switch p["pay_type"] {
		case "hourly":
			wage = int(math.Round(float64(mins) / 60 * float64(toInt(p["hourly_cents"]))))
		case "salary":
			wage = int(math.Round(float64(toInt(p["salary_cents"])) * days / 30.4375)) // a month's salary, by the day
		case "owner":
			p["service_commission_cents"], p["retail_commission_cents"] = 0, 0 // the owner takes what is left, not a commission
		}
		p["rostered_min"], p["wage_cents"] = mins, wage
		p["earned_cents"] = wage + p["service_commission_cents"].(int) + p["retail_commission_cents"].(int) + int(toInt(p["tips_cents"]))
		delete(p, "hours")
		delete(p, "breaks")
	}
	renters, _ = rows(ctx, s.pool, `select st.id, st.name, st.trading_name, st.rent_cents, st.rent_period, st.rent_days,
		(select coalesce(sum(rc.amount_cents),0) from rent_charges rc where rc.staff_id = st.id and rc.status = 'due')::int as owed_cents,
		(select coalesce(sum(rc.amount_cents),0) from rent_charges rc where rc.staff_id = st.id and rc.status = 'paid' and rc.paid_at >= $2 and rc.paid_at < $3)::int as paid_cents
		from staff st where st.business_id=$1 and st.pay_type='renter' and not st.archived order by st.name`, m.BusinessID, from, to)
	if renters == nil {
		renters = []M{}
	}
	sort.SliceStable(people, func(i, j int) bool { return fmt.Sprint(people[i]["name"]) < fmt.Sprint(people[j]["name"]) })
	return people, renters, nil
}

// ---------- emails to the business ----------

// notifyBusiness emails the business when its settings ask for that kind of notice. It never blocks the request.
func (s *Server) notifyBusiness(businessID, setting, subject, body string) {
	if s.afterPaymentCommit(func(parent *Server) { parent.notifyBusiness(businessID, setting, subject, body) }) {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		var email string
		_ = s.pool.QueryRow(ctx, `select coalesce(nullif(b.email,''), (select mu.email from merchant_members mm join merchant_users mu on mu.id = mm.merchant_id where mm.business_id = b.id and mm.role = 'owner' limit 1), '') from businesses b where b.id=$1`, businessID).Scan(&email)
		if email == "" || strings.HasSuffix(email, ".test") || !settingBool(s.bizSettings(ctx, businessID)["notify"][setting], setting != "daily_summary") {
			return
		}
		if _, err := s.mail.Send(ctx, email, subject, body+"\n\nYou can switch these emails off under Settings, Notifications."); err != nil {
			s.logMailFailure("business notice", email, err)
		}
	}()
}

// lowStockNotice tells the business once a day about products at their reorder level.
func (s *Server) lowStockNotice(businessID string) {
	if s.afterPaymentCommit(func(parent *Server) { parent.lowStockNotice(businessID) }) {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		low, _ := rows(ctx, s.pool, `select name, stock, reorder_at from products where business_id=$1 and reorder_at > 0 and stock <= reorder_at and (low_notified_on is null or low_notified_on < current_date) order by name limit 30`, businessID)
		if len(low) == 0 {
			return
		}
		lines := make([]string, 0, len(low))
		for _, p := range low {
			lines = append(lines, fmt.Sprintf("- %v: %v left (reorder at %v)", p["name"], p["stock"], p["reorder_at"]))
		}
		_, _ = s.pool.Exec(ctx, `update products set low_notified_on = current_date where business_id=$1 and reorder_at > 0 and stock <= reorder_at`, businessID)
		s.notifyBusiness(businessID, "low_stock_email", "Running low: "+itoa(len(low))+" product(s)", "These products are at or below their reorder level:\n\n"+strings.Join(lines, "\n")+"\n\nDraft an order: "+strings.TrimRight(s.cfg.WebURL, "/")+"/business/inventory?filter=low")
	}()
}

// morningSummaries sends the day's plan to businesses that asked for it, once a day after 7 in the morning their time.
func (s *Server) morningSummaries(ctx context.Context) {
	due, _ := rows(ctx, s.pool, `select b.id, b.name, b.timezone, b.currency from businesses b
		where b.status = 'live' and coalesce((b.settings->'notify'->>'daily_summary')::boolean, false)
		  and extract(hour from now() at time zone b.timezone) >= 7
		  and (b.last_summary_on is null or b.last_summary_on < (now() at time zone b.timezone)::date) limit 200`)
	for _, b := range due {
		_, _ = s.pool.Exec(ctx, `update businesses set last_summary_on = (now() at time zone timezone)::date where id=$1`, b["id"])
		day, err := row(ctx, s.pool, `select count(*) as n, coalesce(sum(total_cents),0)::int as cents,
			(select count(*) from threads t where t.business_id=$1 and t.status='open' and t.unread_business > 0) as unread,
			(select count(*) from products p where p.business_id=$1 and p.reorder_at > 0 and p.stock <= p.reorder_at) as low
			from bookings where business_id=$1 and (starts_at at time zone $2)::date = (now() at time zone $2)::date and status in ('requested','confirmed','checked_in','in_progress')`, b["id"], b["timezone"])
		if err != nil {
			continue
		}
		body := fmt.Sprintf("Good morning. Today at %v:\n\n- %v booking(s), %s expected\n- %v message(s) waiting for a reply\n- %v product(s) running low\n\nOpen your day: %s/business",
			b["name"], day["n"], formatMoney(int(toInt(day["cents"])), fmt.Sprint(b["currency"])), day["unread"], day["low"], strings.TrimRight(s.cfg.WebURL, "/"))
		s.notifyBusiness(fmt.Sprint(b["id"]), "daily_summary", "Your day at "+fmt.Sprint(b["name"]), body)
	}
}
