package httpapi

import (
	"context"
	"encoding/csv"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
)

// Money: what the business has earned, what LogaLuxe owes it, and where that
// money is sent. Everything here is read from the ledger.

type balances struct {
	Available int `json:"available_cents"` // can be paid out now
	Pending   int `json:"pending_cents"`   // paid by clients, still settling
	Held      int `json:"held_cents"`      // deposits for visits that have not happened yet
}

func (s *Server) balancesOf(ctx context.Context, businessID string) balances {
	var b balances
	_ = s.pool.QueryRow(ctx, `select
		coalesce(sum(amount_cents) filter (where in_balance and status='settled'),0),
		coalesce(sum(amount_cents) filter (where in_balance and status='pending'),0),
		coalesce(sum(amount_cents) filter (where status='held'),0)
		from ledger where business_id=$1`, businessID).Scan(&b.Available, &b.Pending, &b.Held)
	return b
}

func nextPayout(schedule string, loc *time.Location) *string {
	now := time.Now().In(loc)
	var d time.Time
	switch schedule {
	case "daily":
		d = now.AddDate(0, 0, 1)
	case "weekly":
		d = now.AddDate(0, 0, (8-int(now.Weekday()))%7)
		if d.YearDay() == now.YearDay() {
			d = d.AddDate(0, 0, 7)
		}
	default:
		return nil
	}
	out := d.Format("2006-01-02")
	return &out
}

// GET /v1/m/money?from=&to=&kind=&staff=
func (s *Server) mMoney(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	m := mc(r)
	q := r.URL.Query()
	now := time.Now().In(m.Loc)
	monthStart := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, m.Loc)
	prevStart := monthStart.AddDate(0, -1, 0)
	from := parseDay(q.Get("from"), m.Loc)
	to := from.Add(24 * time.Hour)
	if q.Get("to") != "" {
		to = parseDay(q.Get("to"), m.Loc).Add(24 * time.Hour)
	}
	if !to.After(from) || to.Sub(from) > 100*24*time.Hour {
		to = from.Add(24 * time.Hour)
	}

	var schedule string
	_ = s.pool.QueryRow(ctx, `select payout_schedule from businesses where id=$1`, m.BusinessID).Scan(&schedule)
	month, _ := row(ctx, s.pool, `select
		coalesce(sum(amount_cents) filter (where kind in ('charge','deposit','tip') and status <> 'held' and created_at >= $2),0) as processed_cents,
		coalesce(sum(amount_cents) filter (where kind in ('charge','deposit','tip') and status <> 'held' and created_at >= $3 and created_at < $2),0) as processed_prev_cents,
		coalesce(sum(amount_cents) filter (where kind = 'tip' and created_at >= $2),0) as tips_cents,
		coalesce(-sum(amount_cents) filter (where kind in ('fee','payout_fee') and created_at >= $2),0) as fees_cents,
		coalesce(-sum(amount_cents) filter (where kind = 'refund' and created_at >= $2),0) as refunds_cents,
		count(*) filter (where kind = 'refund' and created_at >= $2) as refunds,
		(select count(*) from bookings where business_id=$1 and deposit_paid and starts_at > now() and status in ('requested','confirmed')) as deposits_for
		from ledger where business_id=$1`, m.BusinessID, monthStart, prevStart)
	tx, err := rows(ctx, s.pool, `select l.id, l.kind, l.amount_cents, l.method, l.status, l.in_balance, l.description, l.created_at, l.settles_at, l.sale_id, st.name as staff
		from ledger l left join staff st on st.id = l.staff_id
		where l.business_id=$1 and l.created_at >= $2 and l.created_at < $3 and ($4 = '' or l.kind = $4) and ($5 = '' or l.staff_id::text = $5)
		order by l.created_at desc limit 300`, m.BusinessID, from, to, q.Get("kind"), q.Get("staff"))
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	payouts, _ := rows(ctx, s.pool, `select p.id, p.amount_cents, p.currency, p.status, p.kind, p.fee_cents, p.provider, p.reference, p.failure_reason, p.scheduled_for, p.paid_at, a.bank_name, a.account_last4
		from payouts p left join payout_accounts a on a.id = p.account_id where p.business_id=$1 order by p.created_at desc limit 8`, m.BusinessID)
	methods, _ := rows(ctx, s.pool, `select method, count(*) as n, coalesce(sum(total_cents),0) as cents from sales where business_id=$1 and created_at >= $2 group by method order by cents desc`, m.BusinessID, monthStart)
	split, _ := row(ctx, s.pool, `select
		coalesce(sum(case when si.kind = 'product' then si.unit_cents * si.qty * coalesce(st.retail_commission_pct,0) / 100 else si.unit_cents * si.qty * coalesce(st.commission_pct,0) / 100 end),0)::int as commission_cents,
		coalesce(sum(case when si.kind = 'product' then si.qty * coalesce(p.cost_cents,0) else 0 end),0)::int as retail_cost_cents
		from sale_items si join sales sa on sa.id = si.sale_id left join staff st on st.id = si.staff_id left join products p on p.id = si.product_id
		where sa.business_id=$1 and sa.created_at >= $2`, m.BusinessID, monthStart)
	tax, _ := row(ctx, s.pool, `select coalesce(sum(tax_cents),0) as tax_cents from sales where business_id=$1 and created_at >= $2`, m.BusinessID, monthStart)
	account, _ := row(ctx, s.pool, `select id, provider, status, mode, bank_name, account_last4, account_name from payout_accounts where business_id=$1 and is_default order by created_at desc limit 1`, m.BusinessID)
	staff, _ := rows(ctx, s.pool, `select id, name from staff where business_id=$1 and not archived order by name`, m.BusinessID)
	writeJSON(w, 200, M{"balances": s.balancesOf(ctx, m.BusinessID), "schedule": schedule, "next_payout": nextPayout(schedule, m.Loc), "month": month, "transactions": tx,
		"from": from.Format("2006-01-02"), "to": to.Add(-time.Second).Format("2006-01-02"), "payouts": payouts, "methods": methods, "split": split, "tax": tax, "account": account, "staff": staff,
		"payments_mode": s.payMode(m.Market), "provider": providerFor(m.Market), "month_label": monthStart.Format("January")})
}

// GET /v1/m/money/export?from=&to=   the ledger as a spreadsheet
func (s *Server) mMoneyExport(w http.ResponseWriter, r *http.Request) {
	m := mc(r)
	q := r.URL.Query()
	now := time.Now().In(m.Loc)
	from := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, m.Loc)
	if q.Get("from") != "" {
		from = parseDay(q.Get("from"), m.Loc)
	}
	to := now.Add(24 * time.Hour)
	if q.Get("to") != "" {
		to = parseDay(q.Get("to"), m.Loc).Add(24 * time.Hour)
	}
	out, err := rows(r.Context(), s.pool, `select l.created_at, l.kind, l.description, l.method, l.amount_cents, l.currency, l.status, l.in_balance, st.name as staff
		from ledger l left join staff st on st.id = l.staff_id where l.business_id=$1 and l.created_at >= $2 and l.created_at < $3 order by l.created_at limit 50000`, m.BusinessID, from, to)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="ledger-`+from.Format("2006-01-02")+`-to-`+to.Add(-time.Second).Format("2006-01-02")+`.csv"`)
	_, _ = w.Write([]byte("\xEF\xBB\xBF"))
	cw := csv.NewWriter(w)
	cols := []string{"created_at", "kind", "description", "staff", "method", "amount_cents", "currency", "status", "in_balance"}
	_ = cw.Write(append(cols[:0:0], "date", "type", "description", "staff", "method", "amount_cents", "currency", "status", "counts_towards_payout"))
	rec := make([]string, len(cols))
	for _, l := range out {
		for i, k := range cols {
			rec[i] = csvCell(l[k])
		}
		_ = cw.Write(rec)
	}
	cw.Flush()
}

// POST /v1/m/payouts   {instant}   send the available balance to the bank now
func (s *Server) mPayoutNow(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	m := mc(r)
	var req struct {
		Instant bool `json:"instant"`
	}
	_ = readJSON(r, &req)
	id, msg := s.createPayout(ctx, m.BusinessID, m.Market, m.Plan, m.Currency, map[bool]string{true: "instant", false: "manual"}[req.Instant])
	if msg != "" {
		writeErr(w, 409, msg)
		return
	}
	writeJSON(w, 201, M{"ok": true, "id": id})
}

// createPayout moves the whole available balance into one payout. It returns a
// plain reason when it cannot. Used by "pay out now" and by the daily run.
func (s *Server) createPayout(ctx context.Context, businessID, market, plan, currency, kind string) (id, reason string) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return "", "could not start the payout"
	}
	defer tx.Rollback(ctx)
	// One payout at a time per business.
	if _, err := tx.Exec(ctx, `select pg_advisory_xact_lock(hashtext($1))`, "payout:"+businessID); err != nil {
		return "", "could not start the payout"
	}
	var hold bool
	var holdReason string
	_ = tx.QueryRow(ctx, `select payout_hold, payout_hold_reason from businesses where id=$1`, businessID).Scan(&hold, &holdReason)
	if hold {
		return "", "payouts are on hold for this business: " + firstNonEmpty(holdReason, "contact support")
	}
	var accountID, provider, mode string
	if err := tx.QueryRow(ctx, `select id::text, provider, mode from payout_accounts where business_id=$1 and is_default and status='verified' order by created_at desc limit 1`, businessID).Scan(&accountID, &provider, &mode); err != nil {
		return "", "add and verify a payout account first"
	}
	var available int
	_ = tx.QueryRow(ctx, `select coalesce(sum(amount_cents),0) from ledger where business_id=$1 and in_balance and status='settled'`, businessID).Scan(&available)
	if s.payMode(market) == "live" && mode != "live" {
		return "", "this payout account was recorded before real payments were switched on; add your real account under Payout account"
	}
	if available < 100 {
		return "", "there is nothing to pay out yet; money becomes available two days after a client pays"
	}
	fee := 0
	if kind == "instant" {
		var pct float64
		var min int
		_ = tx.QueryRow(ctx, `select instant_payout_pct::float8, instant_payout_min_cents from fees where market=$1 and plan=$2 and status='approved' and effective_from <= current_date order by effective_from desc limit 1`, market, plan).Scan(&pct, &min)
		fee = int(float64(available)*pct/100 + 0.5)
		if pct > 0 && fee < min {
			fee = min
		}
	}
	// With no live key the transfer is simulated and marked paid at once. With a key it is queued for the provider.
	status, ref := "paid", "sim_"+time.Now().Format("20060102150405")
	if mode == "live" {
		status, ref = "scheduled", ""
	}
	if err := tx.QueryRow(ctx, `insert into payouts (business_id, amount_cents, currency, status, provider, reference, kind, fee_cents, account_id, paid_at)
		values ($1,$2,$3,$4,$5,$6,$7,$8,$9, case when $4 = 'paid' then now() end) returning id::text`, businessID, available-fee, currency, status, provider, ref, kind, fee, accountID).Scan(&id); err != nil {
		return "", "could not create the payout"
	}
	if _, err := tx.Exec(ctx, `insert into ledger (business_id, kind, amount_cents, currency, method, status, payout_id, description) values ($1,'payout',$2,$3,$4,'settled',$5,$6)`,
		businessID, -(available - fee), currency, provider, id, "Payout to bank"); err != nil {
		return "", "could not record the payout"
	}
	if fee > 0 {
		if _, err := tx.Exec(ctx, `insert into ledger (business_id, kind, amount_cents, currency, method, status, payout_id, description) values ($1,'payout_fee',$2,$3,$4,'settled',$5,'Instant payout fee')`,
			businessID, -fee, currency, provider, id); err != nil {
			return "", "could not record the payout"
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return "", "could not finish the payout"
	}
	if status == "scheduled" {
		s.sendPayout(id) // through Stripe or Paystack; it is marked paid, or failed and put back, when they answer
	}
	return id, ""
}

// PUT /v1/m/payout-schedule   {schedule: daily|weekly|manual}
func (s *Server) mPayoutSchedule(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Schedule string `json:"schedule"`
	}
	if err := readJSON(r, &req); err != nil || (req.Schedule != "daily" && req.Schedule != "weekly" && req.Schedule != "manual") {
		writeErr(w, 400, "choose daily, weekly or manual")
		return
	}
	_, _ = s.pool.Exec(r.Context(), `update businesses set payout_schedule=$2 where id=$1`, mc(r).BusinessID, req.Schedule)
	writeJSON(w, 200, M{"ok": true})
}

// ---------- payout account ----------

// GET /v1/m/payout-account
func (s *Server) mPayoutAccount(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	m := mc(r)
	// A Stripe account still being set up may have been finished since the last look.
	if s.cfg.StripeSecret != "" {
		pend, _ := rows(ctx, s.pool, `select id, external_id from payout_accounts where business_id=$1 and provider='stripe' and status='pending' and external_id <> ''`, m.BusinessID)
		for _, p := range pend {
			if ok, bank, last4, err := s.stripeStatus(ctx, p["external_id"].(string)); err == nil && ok {
				_, _ = s.pool.Exec(ctx, `update payout_accounts set status='verified', bank_name=$2, account_last4=$3 where id=$1`, p["id"], bank, last4)
			}
		}
	}
	accounts, err := rows(ctx, s.pool, `select id, provider, status, mode, bank_name, account_name, account_last4, currency, is_default, created_at from payout_accounts where business_id=$1 order by is_default desc, created_at desc`, m.BusinessID)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	var schedule, verification string
	_ = s.pool.QueryRow(ctx, `select payout_schedule, verification_status from businesses where id=$1`, m.BusinessID).Scan(&schedule, &verification)
	out := M{"accounts": accounts, "market": m.Market, "currency": m.Currency, "schedule": schedule, "verification": verification,
		"stripe_live": s.cfg.StripeSecret != "", "paystack_live": s.cfg.PaystackSecret != "", "flutterwave_live": s.cfg.FlutterwaveSecret != ""}
	if m.Market == "NG" {
		banks := nigerianBanks
		if s.cfg.PaystackSecret != "" {
			banks = s.paystackBanks(ctx)
		}
		list := make([]M, 0, len(banks))
		for _, b := range banks {
			list = append(list, M{"name": b[0], "code": b[1]})
		}
		out["banks"] = list
	}
	writeJSON(w, 200, out)
}

var digitsRe = regexp.MustCompile(`^\d+$`)

// POST /v1/m/payout-account/bank   Nigeria: {bank_code, bank_name, account_number, account_name, confirm}
// Step one (confirm false) checks the account with the bank and returns the
// name on it. Step two (confirm true) saves it. The full number is never stored here.
func (s *Server) mPayoutBank(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	m := mc(r)
	if m.Market != "NG" {
		writeErr(w, 400, "bank accounts are added this way in Nigeria only")
		return
	}
	var req struct {
		BankCode      string `json:"bank_code"`
		BankName      string `json:"bank_name"`
		AccountNumber string `json:"account_number"`
		AccountName   string `json:"account_name"`
		Confirm       bool   `json:"confirm"`
	}
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, "invalid json")
		return
	}
	req.AccountNumber = strings.ReplaceAll(strings.TrimSpace(req.AccountNumber), " ", "")
	if req.BankCode == "" || len(req.AccountNumber) != 10 || !digitsRe.MatchString(req.AccountNumber) {
		writeErr(w, 400, "choose a bank and enter the 10-digit account number")
		return
	}
	live := s.cfg.PaystackSecret != ""
	name := strings.TrimSpace(req.AccountName)
	// The bank is asked once, on the first step. On the confirm step the name it gave comes back with the request, and
	// Paystack checks the account again itself when the payout recipient is registered.
	if live && !(req.Confirm && name != "") {
		resolved, err := s.paystackResolve(ctx, req.BankCode, req.AccountNumber)
		if err != nil {
			writeErr(w, 400, "the bank could not find that account: "+err.Error())
			return
		}
		name = resolved
	} else if name == "" {
		writeErr(w, 400, "enter the name on the account")
		return
	}
	if !req.Confirm {
		writeJSON(w, 200, M{"ok": true, "account_name": name, "checked_with_bank": live})
		return
	}
	external, mode := "", "simulation"
	if live {
		code, err := s.paystackRecipient(ctx, name, req.BankCode, req.AccountNumber)
		if err != nil {
			writeErr(w, 502, "could not register the account for payouts: "+err.Error())
			return
		}
		external, mode = code, "live"
	}
	_, _ = s.pool.Exec(ctx, `update payout_accounts set is_default=false where business_id=$1`, m.BusinessID)
	if _, err := s.pool.Exec(ctx, `insert into payout_accounts (business_id, provider, status, mode, bank_name, bank_code, account_name, account_last4, currency, external_id) values ($1,'paystack','verified',$2,$3,$4,$5,$6,'NGN',$7)`,
		m.BusinessID, mode, strings.TrimSpace(req.BankName), req.BankCode, name, req.AccountNumber[6:], external); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 201, M{"ok": true, "account_name": name, "mode": mode})
}

// POST /v1/m/payout-account/stripe   United States
// With a Stripe key this returns a link to Stripe's own onboarding pages.
// Without one it records a simulated account from {bank_name, last4, account_name}.
func (s *Server) mPayoutStripe(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	m := mc(r)
	if m.Market != "US" {
		writeErr(w, 400, "Stripe is used for businesses in the United States")
		return
	}
	var req struct {
		BankName    string `json:"bank_name"`
		Last4       string `json:"last4"`
		AccountName string `json:"account_name"`
	}
	_ = readJSON(r, &req)
	if s.cfg.StripeSecret != "" {
		var rowID, external string
		_ = s.pool.QueryRow(ctx, `select id::text, external_id from payout_accounts where business_id=$1 and provider='stripe' order by created_at desc limit 1`, m.BusinessID).Scan(&rowID, &external)
		accountID, link, err := s.stripeOnboard(ctx, external, m.Email, m.Business)
		if err != nil {
			writeErr(w, 502, "Stripe could not start the setup: "+err.Error())
			return
		}
		if rowID == "" {
			_, _ = s.pool.Exec(ctx, `insert into payout_accounts (business_id, provider, status, mode, currency, external_id) values ($1,'stripe','pending','live','USD',$2)`, m.BusinessID, accountID)
		}
		writeJSON(w, 200, M{"ok": true, "url": link})
		return
	}
	req.Last4 = strings.TrimSpace(req.Last4)
	if strings.TrimSpace(req.BankName) == "" || len(req.Last4) != 4 || !digitsRe.MatchString(req.Last4) || strings.TrimSpace(req.AccountName) == "" {
		writeErr(w, 400, "enter the bank, the name on the account and the last four digits")
		return
	}
	_, _ = s.pool.Exec(ctx, `update payout_accounts set is_default=false where business_id=$1`, m.BusinessID)
	if _, err := s.pool.Exec(ctx, `insert into payout_accounts (business_id, provider, status, mode, bank_name, account_name, account_last4, currency) values ($1,'stripe','verified','simulation',$2,$3,$4,'USD')`,
		m.BusinessID, strings.TrimSpace(req.BankName), strings.TrimSpace(req.AccountName), req.Last4); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 201, M{"ok": true, "mode": "simulation"})
}

// POST /v1/m/payout-account/{id}/default
func (s *Server) mPayoutDefault(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	m := mc(r)
	tag, err := s.pool.Exec(ctx, `update payout_accounts set is_default = (id::text = $2) where business_id=$1 and exists (select 1 from payout_accounts where id::text=$2 and business_id=$1 and status='verified')`, m.BusinessID, chi.URLParam(r, "id"))
	if err != nil || tag.RowsAffected() == 0 {
		writeErr(w, 404, "no verified account with that id")
		return
	}
	writeJSON(w, 200, M{"ok": true})
}

// DELETE /v1/m/payout-account/{id}
func (s *Server) mPayoutAccountDelete(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	m := mc(r)
	var wasDefault bool
	if err := s.pool.QueryRow(ctx, `delete from payout_accounts where id=$1 and business_id=$2 returning is_default`, chi.URLParam(r, "id"), m.BusinessID).Scan(&wasDefault); err != nil {
		writeErr(w, 404, "account not found")
		return
	}
	if wasDefault { // promote the newest verified account that is left
		_, _ = s.pool.Exec(ctx, `update payout_accounts set is_default=true where id = (select id from payout_accounts where business_id=$1 and status='verified' order by created_at desc limit 1)`, m.BusinessID)
	}
	writeJSON(w, 200, M{"ok": true})
}
