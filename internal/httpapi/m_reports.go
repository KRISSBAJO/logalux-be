package httpapi

import (
	"encoding/csv"
	"github.com/jackc/pgx/v5"
	"net/http"
	"time"
)

// Reports and the home screen. Every number here is counted from bookings,
// sales and the ledger. Nothing is estimated.

const liveBooking = `bk.status not in ('cancelled_client','cancelled_business','rescheduled')`

func reportRange(v string, loc *time.Location) (from, to time.Time, days int, label string) {
	now := time.Now().In(loc)
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc)
	to = today.AddDate(0, 0, 1)
	switch v {
	case "7d":
		days, label = 7, "Last 7 days"
	case "90d":
		days, label = 90, "Last 90 days"
	case "month":
		from = time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, loc)
		return from, to, int(to.Sub(from).Hours()/24 + 0.5), now.Format("January")
	default:
		days, label = 30, "Last 30 days"
	}
	return to.AddDate(0, 0, -days), to, days, label
}

// GET /v1/m/reports?range=7d|30d|90d|month
func (s *Server) mReports(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	m := mc(r)
	from, to, days, label := reportRange(r.URL.Query().Get("range"), m.Loc)
	prev := from.AddDate(0, 0, -days)

	kpi := func(a, b time.Time) M {
		out, _ := row(ctx, s.pool, `select
			(select coalesce(sum(sa.subtotal_cents - sa.discount_cents - sa.refunded_cents),0) from sales sa where sa.business_id=$1 and sa.created_at >= $2 and sa.created_at < $3)::int as revenue_cents,
			(select coalesce(sum(sa.tip_cents),0) from sales sa where sa.business_id=$1 and sa.created_at >= $2 and sa.created_at < $3)::int as tips_cents,
			(select count(*) from sales sa where sa.business_id=$1 and sa.created_at >= $2 and sa.created_at < $3) as sales,
			(select count(*) from bookings bk where bk.business_id=$1 and bk.starts_at >= $2 and bk.starts_at < $3 and `+liveBooking+`) as bookings,
			(select count(*) from bookings bk where bk.business_id=$1 and bk.starts_at >= $2 and bk.starts_at < $3 and bk.status = 'no_show') as no_shows,
			(select count(*) from clients c where c.business_id=$1 and c.created_at >= $2 and c.created_at < $3 and not customer_business_member(c.user_id,c.business_id) and not (exists(select 1 from bookings ib where ib.client_id=c.id and ib.is_internal) and not exists(select 1 from bookings eb where eb.client_id=c.id and not eb.is_internal))) as new_clients,
			(select count(distinct bk.client_id) from bookings bk where bk.business_id=$1 and bk.starts_at >= $2 and bk.starts_at < $3 and bk.status in ('completed','paid') and bk.client_id is not null) as visitors,
			(select count(distinct bk.client_id) from bookings bk where bk.business_id=$1 and bk.starts_at >= $2 and bk.starts_at < $3 and bk.status in ('completed','paid') and bk.client_id is not null
			   and exists (select 1 from bookings b2 where b2.client_id = bk.client_id and b2.starts_at > bk.starts_at and b2.status not in ('cancelled_client','cancelled_business','no_show'))) as rebooked`, m.BusinessID, a, b)
		return out
	}
	byDay, _ := rows(ctx, s.pool, `select d::date as day,
		(select coalesce(sum(sa.subtotal_cents - sa.discount_cents - sa.refunded_cents),0) from sales sa where sa.business_id=$1 and (sa.created_at at time zone $4)::date = d::date)::int as revenue_cents,
		(select coalesce(sum(sa.tip_cents),0) from sales sa where sa.business_id=$1 and (sa.created_at at time zone $4)::date = d::date)::int as tips_cents,
		(select coalesce(sum(sa.subtotal_cents - sa.discount_cents - sa.refunded_cents),0) from sales sa where sa.business_id=$1 and (sa.created_at at time zone $4)::date = (d - make_interval(days => $5::int))::date)::int as previous_cents
		from generate_series($2::date, ($3::date - 1), interval '1 day') d order by d`, m.BusinessID, from.Format("2006-01-02"), to.Format("2006-01-02"), m.Timezone, days)
	sources, _ := rows(ctx, s.pool, `select bk.source, count(*) as n from bookings bk where bk.business_id=$1 and bk.starts_at >= $2 and bk.starts_at < $3 and `+liveBooking+` group by bk.source order by n desc`, m.BusinessID, from, to)
	staff, _ := rows(ctx, s.pool, `select st.id, st.name, st.initials, st.tone, st.rating,
		(select coalesce(sum(si.unit_cents * si.qty),0) from sale_items si join sales sa on sa.id = si.sale_id where si.staff_id = st.id and sa.created_at >= $2 and sa.created_at < $3 and sa.status <> 'refunded')::int as revenue_cents,
		(select coalesce(sum(sa.tip_cents),0) from sales sa where sa.staff_id = st.id and sa.created_at >= $2 and sa.created_at < $3)::int as tips_cents,
		(select count(*) from bookings bk where bk.staff_id = st.id and bk.starts_at >= $2 and bk.starts_at < $3 and `+liveBooking+`) as bookings,
		(select coalesce(sum(extract(epoch from (bk.ends_at - bk.starts_at))/60),0) from bookings bk where bk.staff_id = st.id and bk.starts_at >= $2 and bk.starts_at < $3 and `+liveBooking+` and bk.status <> 'no_show')::int as booked_min,
		(select count(distinct bk.client_id) from bookings bk where bk.staff_id = st.id and bk.starts_at >= $2 and bk.starts_at < $3 and bk.status in ('completed','paid') and bk.client_id is not null) as visitors,
		(select count(distinct bk.client_id) from bookings bk where bk.staff_id = st.id and bk.starts_at >= $2 and bk.starts_at < $3 and bk.status in ('completed','paid') and bk.client_id is not null
		   and exists (select 1 from bookings b2 where b2.client_id = bk.client_id and b2.starts_at > bk.starts_at and b2.status not in ('cancelled_client','cancelled_business','no_show'))) as rebooked
		from staff st where st.business_id=$1 and not st.archived and st.pay_type <> 'renter' order by revenue_cents desc`, m.BusinessID, from, to)
	// Busy hours: bookings that start in each two-hour band of each weekday.
	heat, _ := rows(ctx, s.pool, `select extract(isodow from bk.starts_at at time zone $4)::int as dow, (extract(hour from bk.starts_at at time zone $4)::int / 2) * 2 as hour, count(*) as n
		from bookings bk where bk.business_id=$1 and bk.starts_at >= $2 and bk.starts_at < $3 and `+liveBooking+` and bk.status <> 'no_show' group by 1, 2`, m.BusinessID, from, to, m.Timezone)
	top, _ := rows(ctx, s.pool, `select si.name, sum(si.qty) as sold, sum(si.unit_cents * si.qty)::int as revenue_cents from sale_items si join sales sa on sa.id = si.sale_id
		where sa.business_id=$1 and sa.created_at >= $2 and sa.created_at < $3 and si.kind <> 'product' group by si.name order by revenue_cents desc limit 6`, m.BusinessID, from, to)
	mix, _ := row(ctx, s.pool, `select
		count(*) filter (where prior.n > 0) as returning_visits,
		count(*) filter (where prior.n = 0) as new_visits,
		count(*) filter (where bk.deposit_paid) as with_deposit,
		count(*) as visits,
		(select count(*) from reviews rv where rv.business_id=$1 and rv.created_at >= $2 and rv.created_at < $3) as reviews,
		(select count(distinct sa.id) from sales sa join sale_items si on si.sale_id = sa.id where sa.business_id=$1 and sa.created_at >= $2 and sa.created_at < $3 and si.kind = 'product') as sales_with_retail,
		(select count(*) from sales sa where sa.business_id=$1 and sa.created_at >= $2 and sa.created_at < $3) as sales,
		(select coalesce(sum(sa.tip_cents),0) from sales sa where sa.business_id=$1 and sa.created_at >= $2 and sa.created_at < $3)::int as tips_cents,
		(select coalesce(sum(sa.subtotal_cents - sa.discount_cents),0) from sales sa where sa.business_id=$1 and sa.created_at >= $2 and sa.created_at < $3)::int as sold_cents
		from bookings bk left join lateral (select count(*) as n from bookings p where p.client_id = bk.client_id and p.starts_at < bk.starts_at and p.status in ('completed','paid')) prior on true
		where bk.business_id=$1 and bk.starts_at >= $2 and bk.starts_at < $3 and bk.status in ('completed','paid')`, m.BusinessID, from, to)
	var openMin int // minutes the location is open across the period, for "share of time booked"
	_ = s.pool.QueryRow(ctx, `select coalesce(sum(extract(epoch from ((l.hours->lower(to_char(d, 'dy'))->>1)::time - (l.hours->lower(to_char(d, 'dy'))->>0)::time))/60),0)::int
		from generate_series($2::date, ($3::date - 1), interval '1 day') d join locations l on l.business_id=$1 and l.is_primary
		where jsonb_typeof(l.hours->lower(to_char(d, 'dy'))) = 'array'`, m.BusinessID, from.Format("2006-01-02"), to.Format("2006-01-02")).Scan(&openMin)

	writeJSON(w, 200, M{"range": label, "from": from.Format("2006-01-02"), "to": to.Add(-time.Second).Format("2006-01-02"), "days": days,
		"now": kpi(from, to), "before": kpi(prev, from), "by_day": byDay, "sources": sources, "staff": staff, "heat": heat, "top_services": top, "mix": mix, "open_min": openMin})
}

// GET /v1/m/reports/export?range=   one row per sale
func (s *Server) mReportsExport(w http.ResponseWriter, r *http.Request) {
	m := mc(r)
	from, to, _, _ := reportRange(r.URL.Query().Get("range"), m.Loc)
	out, err := s.pool.Query(r.Context(), `select sa.created_at, sa.client_name, st.name as staff, (select string_agg(si.name, '; ') from sale_items si where si.sale_id = sa.id) as items,
		sa.subtotal_cents, sa.discount_cents, sa.tax_cents, sa.tip_cents, sa.deposit_cents, sa.total_cents, sa.refunded_cents, sa.method, sa.status
		from sales sa left join staff st on st.id = sa.staff_id and st.business_id = sa.business_id where sa.business_id=$1 and sa.created_at >= $2 and sa.created_at < $3 order by sa.created_at`, m.BusinessID, from, to)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	defer out.Close()
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="sales-`+from.Format("2006-01-02")+`-to-`+to.Add(-time.Second).Format("2006-01-02")+`.csv"`)
	_, _ = w.Write([]byte("\xEF\xBB\xBF"))
	cw := csv.NewWriter(w)
	cols := []string{"created_at", "client_name", "staff", "items", "subtotal_cents", "discount_cents", "tax_cents", "tip_cents", "deposit_cents", "total_cents", "refunded_cents", "method", "status"}
	_ = cw.Write(cols)
	rec := make([]string, len(cols))
	for out.Next() {
		sa, scanErr := pgx.RowToMap(out)
		if scanErr != nil {
			panic(http.ErrAbortHandler)
		}
		tidy(sa)
		for i, k := range cols {
			rec[i] = csvCell(sa[k])
		}
		_ = cw.Write(rec)
	}
	if out.Err() != nil {
		panic(http.ErrAbortHandler)
	}
	cw.Flush()
}

// GET /v1/m/home   the first screen of the day
func (s *Server) mHome(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	m := mc(r)
	now := time.Now().In(m.Loc)
	day := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, m.Loc)
	end := day.Add(24 * time.Hour)
	monday := day.AddDate(0, 0, -((int(day.Weekday()) + 6) % 7))

	today, _ := row(ctx, s.pool, `select
		count(*) filter (where `+liveBooking+` and bk.status <> 'no_show') as bookings,
		coalesce(sum(bk.total_cents) filter (where `+liveBooking+` and bk.status <> 'no_show'),0)::int as expected_cents,
		coalesce(sum(case when bk.paid_at is not null then bk.total_cents when bk.deposit_paid then bk.deposit_cents else 0 end) filter (where `+liveBooking+`),0)::int as paid_cents,
		coalesce(sum(extract(epoch from (bk.ends_at - bk.starts_at))/60) filter (where `+liveBooking+` and bk.status <> 'no_show'),0)::int as booked_min,
		count(*) filter (where bk.status in ('checked_in','in_progress')) as in_chair,
		count(*) filter (where bk.status = 'completed' and bk.paid_at is null) as to_check_out
		from bookings bk where bk.business_id=$1 and bk.starts_at >= $2 and bk.starts_at < $3`, m.BusinessID, day, end)
	upNext, _ := rows(ctx, s.pool, `select `+bookingCols+` from bookings bk join staff st on st.id = bk.staff_id
		where bk.business_id=$1 and bk.starts_at >= $2 and bk.starts_at < $3 and bk.status in ('requested','confirmed','checked_in','in_progress','completed') and bk.paid_at is null
		order by (bk.status in ('checked_in','in_progress','completed')) desc, bk.starts_at limit 7`, m.BusinessID, day, end)
	team, _ := rows(ctx, s.pool, `select st.id, st.name, st.initials, st.tone,
		(select count(*) from bookings bk where bk.staff_id = st.id and bk.starts_at >= $2 and bk.starts_at < $3 and `+liveBooking+` and bk.status <> 'no_show') as bookings,
		(select coalesce(sum(bk.total_cents),0) from bookings bk where bk.staff_id = st.id and bk.starts_at >= $2 and bk.starts_at < $3 and `+liveBooking+` and bk.status <> 'no_show')::int as cents,
		exists (select 1 from time_off t where t.staff_id = st.id and t.status = 'approved' and $2::date between t.starts_on and t.ends_on) as off
		from staff st where st.business_id=$1 and not st.archived and st.bookable order by st.role='owner' desc, st.name`, m.BusinessID, day, end)
	week, _ := rows(ctx, s.pool, `select d::date as day,
		(select coalesce(sum(sa.subtotal_cents - sa.discount_cents - sa.refunded_cents),0) from sales sa where sa.business_id=$1 and (sa.created_at at time zone $3)::date = d::date)::int as cents,
		(select coalesce(sum(bk.total_cents),0) from bookings bk where bk.business_id=$1 and (bk.starts_at at time zone $3)::date = d::date and bk.paid_at is null and `+liveBooking+` and bk.status <> 'no_show')::int as booked_cents
		from generate_series($2::date, $2::date + 6, interval '1 day') d order by d`, m.BusinessID, monday.Format("2006-01-02"), m.Timezone)
	var lastWeek int
	_ = s.pool.QueryRow(ctx, `select coalesce(sum(subtotal_cents - discount_cents - refunded_cents),0)::int from sales where business_id=$1 and created_at >= $2 and created_at < $3`, m.BusinessID, monday.AddDate(0, 0, -7), monday.AddDate(0, 0, -7).Add(now.Sub(monday))).Scan(&lastWeek)
	inbox, _ := rows(ctx, s.pool, `select id, client_name, channel, last_preview, last_message_at, unread_business from threads where business_id=$1 and status='open' order by (unread_business > 0) desc, last_message_at desc limit 3`, m.BusinessID)
	counts, _ := row(ctx, s.pool, `select
		(select coalesce(sum(unread_business),0) from threads where business_id=$1 and status='open')::int as unread,
		(select count(*) from time_off where business_id=$1 and status='requested') as time_off,
		(select count(*) from products where business_id=$1 and reorder_at > 0 and stock <= reorder_at) as low_stock,
		(select count(*) from waitlist_entries where business_id=$1 and status='waiting') as waitlist,
		(select count(*) from reviews where business_id=$1 and status='published' and reply = '') as unreplied_reviews,
		(select count(*) from bookings where business_id=$1 and status='requested' and starts_at > now()) as to_confirm,
		(select count(*) from clients c where c.business_id=$1 and `+lapsedWhere+`) as lapsed,
		(select count(*) from services where business_id=$1 and not archived) as services,
		(select count(*) from payout_accounts where business_id=$1 and status='verified') as payout_accounts,
		(select count(*) from site_media where slot='business' and ref=$2) as photos,
		(select verification_status from businesses where id=$1) as verification`, m.BusinessID, m.Slug)
	timeOff, _ := rows(ctx, s.pool, `select t.id, st.name as staff, t.starts_on, t.ends_on,
		(select count(*) from bookings bk where bk.staff_id = t.staff_id and bk.starts_at::date between t.starts_on and t.ends_on and bk.status in ('requested','confirmed')) as bookings_affected
		from time_off t join staff st on st.id = t.staff_id where t.business_id=$1 and t.status='requested' order by t.starts_on limit 3`, m.BusinessID)
	lowStock, _ := rows(ctx, s.pool, `select id, name, stock, reorder_at from products where business_id=$1 and reorder_at > 0 and stock <= reorder_at order by stock limit 3`, m.BusinessID)
	var openToday any
	_ = s.pool.QueryRow(ctx, `select hours->$2 from locations where business_id=$1 and is_primary`, m.BusinessID, dayKey(day)).Scan(&openToday)

	writeJSON(w, 200, M{"date": day.Format("2006-01-02"), "today": today, "up_next": upNext, "team": team, "week": week, "last_week_cents": lastWeek, "inbox": inbox, "counts": counts,
		"time_off": timeOff, "low_stock": lowStock, "balances": s.balancesOf(ctx, m.BusinessID), "open_today": openToday, "first_name": firstName(m.Name)})
}

func dayKey(t time.Time) string {
	return [...]string{"sun", "mon", "tue", "wed", "thu", "fri", "sat"}[t.Weekday()]
}
