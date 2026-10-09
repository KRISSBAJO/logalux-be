package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Three things that run on keys the owner has added: the monthly Pro fee,
// membership renewals charged to the card a member saved, and drafts written
// by an AI model that a person always reads and sends themselves.

// ---------- the Pro plan's monthly fee ----------

const planGraceDays = 14 // how long a Pro business can owe its fee before it is moved back to Free

// billPlans takes the month's Pro fee out of each Pro business's payout balance. When there is not
// enough in the balance it waits and tries again each run; after the grace period the business
// moves back to the Free plan, and is told.
func (s *Server) billPlans(ctx context.Context) {
	due, err := rows(ctx, s.pool, `select b.id, b.name, b.currency, b.plan_paid_through, b.plan_due_since, (now() at time zone b.timezone)::date as today,
		(select f.plan_price_cents from fees f where f.market = b.market and f.plan = 'pro' and f.status = 'approved' and f.effective_from <= current_date order by f.effective_from desc limit 1) as price,
		(select coalesce(sum(l.amount_cents),0) from ledger l where l.business_id = b.id and l.in_balance and l.status = 'settled')::int as available
		from businesses b where b.plan = 'pro' and b.status in ('live','paused') and (b.plan_paid_through is null or b.plan_paid_through < (now() at time zone b.timezone)::date) limit 500`)
	if err != nil {
		return
	}
	for _, b := range due {
		price := int(toInt(b["price"]))
		today, _ := b["today"].(time.Time)
		if price <= 0 {
			continue
		}
		if int(toInt(b["available"])) >= price {
			tx, err := s.pool.Begin(ctx)
			if err != nil {
				return
			}
			// Claim the month first, so two runs cannot both take it.
			tag, err := tx.Exec(ctx, `update businesses set plan_paid_through = (greatest(coalesce(plan_paid_through, $2::date - 1), $2::date - 1) + interval '1 month')::date, plan_due_since = null
				where id=$1 and plan = 'pro' and (plan_paid_through is null or plan_paid_through < $2::date)`, b["id"], today)
			if err == nil && tag.RowsAffected() == 1 {
				_, err = tx.Exec(ctx, `insert into ledger (business_id, kind, amount_cents, currency, method, status, in_balance, description) values ($1,'plan_fee',$2,$3,'logaluxe','settled',true,$4)`,
					b["id"], -price, b["currency"], "LogaLuxe Pro · "+today.Format("January 2006"))
			}
			if err != nil {
				_ = tx.Rollback(ctx)
				continue
			}
			_ = tx.Commit(ctx)
			continue
		}
		since, owing := b["plan_due_since"].(time.Time)
		if !owing {
			_, _ = s.pool.Exec(ctx, `update businesses set plan_due_since = $2 where id=$1 and plan_due_since is null`, b["id"], today)
			continue
		}
		if today.Sub(since) > planGraceDays*24*time.Hour {
			_, _ = s.pool.Exec(ctx, `update businesses set plan = 'free', plan_due_since = null where id=$1`, b["id"])
			_, _ = s.pool.Exec(ctx, `insert into audit_log (actor, action, target, before, after) values ('billing','merchant.plan',$1,$2,$3)`, fmt.Sprint(b["id"]), M{"plan": "pro"}, M{"plan": "free", "why": "the Pro fee could not be taken"})
			s.notifyBusiness(fmt.Sprint(b["id"]), "new_booking_email", "Your LogaLuxe plan is now Free",
				"We could not take the Pro fee from your LogaLuxe balance for "+itoa(planGraceDays)+" days, so your plan has moved to Free. Nothing is owed. You can switch back to Pro at any time: "+strings.TrimRight(s.cfg.WebURL, "/")+"/business/settings?tab=plan")
		}
	}
}

// ---------- a saved card for membership renewals ----------

// rememberCard keeps the provider's reference to the card a membership was bought with, so the next
// month can be charged without the client. It runs after a pay link that sold a membership is paid.
func (s *Server) rememberCard(ctx context.Context, paymentID string) {
	var provider, ref, intent, email string
	var saleID *string
	if err := s.pool.QueryRow(ctx, `select provider, reference, payment_intent, email, sale_id::text from payments where id=$1 and save_card and status='paid'`, paymentID).Scan(&provider, &ref, &intent, &email, &saleID); err != nil || saleID == nil {
		return
	}
	customer, method := "", ""
	switch provider {
	case "stripe":
		var pi struct {
			Customer      string `json:"customer"`
			PaymentMethod string `json:"payment_method"`
		}
		if providerJSON(ctx, http.MethodGet, "https://api.stripe.com/v1/payment_intents/"+url.PathEscape(intent), s.cfg.StripeSecret, nil, nil, &pi) != nil {
			return
		}
		customer, method = pi.Customer, pi.PaymentMethod
	case "paystack":
		var out struct {
			Data struct {
				Authorization struct {
					Code     string `json:"authorization_code"`
					Reusable bool   `json:"reusable"`
				} `json:"authorization"`
				Customer struct {
					Email string `json:"email"`
				} `json:"customer"`
			} `json:"data"`
		}
		if providerJSON(ctx, http.MethodGet, "https://api.paystack.co/transaction/verify/"+url.PathEscape(ref), s.cfg.PaystackSecret, nil, nil, &out) != nil || !out.Data.Authorization.Reusable {
			return
		}
		method, email = out.Data.Authorization.Code, firstNonEmpty(out.Data.Customer.Email, email)
	}
	if method == "" {
		return
	}
	_, _ = s.pool.Exec(ctx, `update client_plans set pay_provider=$2, pay_customer=$3, pay_method=$4, pay_email=$5 where sale_id=$1 and kind='membership'`, *saleID, provider, customer, method, email)
}

// chargeSavedCard takes one month's membership from the card on file.
func (s *Server) chargeSavedCard(ctx context.Context, planID, provider, customer, method, email, currency, what string, amount int) error {
	if method == "" {
		return errors.New("there is no card on file for this membership")
	}
	if amount <= 0 {
		return nil
	}
	switch provider {
	case "stripe":
		var pi struct {
			Status string `json:"status"`
		}
		err := providerJSON(ctx, http.MethodPost, "https://api.stripe.com/v1/payment_intents", s.cfg.StripeSecret, url.Values{
			"amount": {strconv.Itoa(amount)}, "currency": {strings.ToLower(currency)}, "customer": {customer}, "payment_method": {method},
			"off_session": {"true"}, "confirm": {"true"}, "description": {what}, "metadata[plan]": {planID}}, nil, &pi)
		if err != nil {
			return err
		}
		if pi.Status != "succeeded" {
			return errors.New("the card needs the client to approve the payment")
		}
		return nil
	case "paystack":
		var out struct {
			Data struct {
				Status  string `json:"status"`
				Message string `json:"gateway_response"`
			} `json:"data"`
		}
		err := providerJSON(ctx, http.MethodPost, "https://api.paystack.co/transaction/charge_authorization", s.cfg.PaystackSecret, nil,
			M{"email": email, "amount": amount, "currency": currency, "authorization_code": method, "reference": "lxm" + strings.ReplaceAll(planID, "-", "")[:20] + strconv.FormatInt(time.Now().Unix(), 36)}, &out)
		if err != nil {
			return err
		}
		if out.Data.Status != "success" {
			return errors.New(firstNonEmpty(out.Data.Message, "the bank declined the card"))
		}
		return nil
	}
	return errors.New("there is no card on file for this membership")
}

// ---------- AI drafts ----------

const aiDailyLimit = 100 // drafts per business per day

var aiHTTP = &http.Client{Timeout: 40 * time.Second}

// aiWrite asks the model for text. system sets the rules; user carries the facts. With asJSON the
// answer is a JSON object. Every draft is counted against the business's daily limit.
func (s *Server) aiWrite(ctx context.Context, businessID, system, user string, asJSON bool) (string, error) {
	if s.cfg.OpenAIKey == "" {
		return "", errors.New("AI drafting is not set up")
	}
	var used int
	if err := s.pool.QueryRow(ctx, `insert into ai_usage (business_id, day, n) values ($1, current_date, 1) on conflict (business_id, day) do update set n = ai_usage.n + 1 returning n`, businessID).Scan(&used); err == nil && used > aiDailyLimit {
		return "", errors.New("you have reached today's limit of " + itoa(aiDailyLimit) + " drafts; it resets tomorrow")
	}
	return s.aiAsk(ctx, system, user, asJSON)
}

// aiAsk is the call itself, with no allowance: the console's Journal drafts use it, behind a staff sign-in.
func (s *Server) aiAsk(ctx context.Context, system, user string, asJSON bool) (string, error) {
	if s.cfg.OpenAIKey == "" {
		return "", errors.New("AI drafting is not set up")
	}
	body := M{"model": firstNonEmpty(s.cfg.OpenAIModel, "gpt-4o-mini"), "temperature": 0.5, "messages": []M{{"role": "system", "content": system}, {"role": "user", "content": user}}}
	if asJSON {
		body["response_format"] = M{"type": "json_object"}
	}
	raw, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.openai.com/v1/chat/completions", strings.NewReader(string(raw)))
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+s.cfg.OpenAIKey)
	req.Header.Set("Content-Type", "application/json")
	res, err := aiHTTP.Do(req)
	if err != nil {
		slog.Error("ai request failed", "err", err)
		return "", errors.New("the AI service did not answer; try again")
	}
	defer res.Body.Close()
	var out struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.NewDecoder(res.Body).Decode(&out); err != nil || res.StatusCode >= 300 || len(out.Choices) == 0 {
		return "", errors.New("the AI service could not write a draft: " + firstNonEmpty(out.Error.Message, "no answer"))
	}
	return strings.TrimSpace(out.Choices[0].Message.Content), nil
}

// businessFacts is what the model may rely on about a business: only what is on record.
func (s *Server) businessFacts(ctx context.Context, m Merchant) string {
	var b strings.Builder
	var category, city string
	var hours []byte
	_ = s.pool.QueryRow(ctx, `select b.category, coalesce(l.city,''), coalesce(l.hours::text,'{}') from businesses b left join locations l on l.business_id = b.id and l.is_primary where b.id=$1`, m.BusinessID).Scan(&category, &city, &hours)
	rules := s.bizSettings(ctx, m.BusinessID)
	fmt.Fprintf(&b, "Business: %s (%s) in %s. Currency %s.\nOpening hours by weekday (missing means closed): %s\n", m.Business, category, city, m.Currency, hours)
	fmt.Fprintf(&b, "Booking page: %s/b/%s\nFree cancellation until %d hours before. Bookings are %s.\nServices and prices:\n", strings.TrimRight(s.cfg.WebURL, "/"), m.Slug,
		settingInt(rules["policy"]["cancel_hours"], 24), map[bool]string{true: "confirmed at once", false: "confirmed by the business"}[settingBool(rules["booking"]["instant"], true)])
	svcs, _ := rows(ctx, s.pool, `select name, price_cents, duration_min, deposit_cents from services where business_id=$1 and not archived and online order by sort, name limit 60`, m.BusinessID)
	for _, sv := range svcs {
		fmt.Fprintf(&b, "- %v: %s, %v minutes", sv["name"], formatMoney(int(toInt(sv["price_cents"])), m.Currency), sv["duration_min"])
		if d := int(toInt(sv["deposit_cents"])); d > 0 {
			fmt.Fprintf(&b, ", deposit %s", formatMoney(d, m.Currency))
		}
		b.WriteString("\n")
	}
	return b.String()
}

// GET /v1/m/ai   whether drafting is available, and how much of today's allowance is used
func (s *Server) mAI(w http.ResponseWriter, r *http.Request) {
	var used int
	_ = s.pool.QueryRow(r.Context(), `select coalesce((select n from ai_usage where business_id=$1 and day=current_date),0)`, mc(r).BusinessID).Scan(&used)
	writeJSON(w, 200, M{"enabled": s.cfg.OpenAIKey != "", "used_today": used, "limit": aiDailyLimit})
}

// POST /v1/m/ai/reply   {thread_id, hint}   a draft answer to a client. Nothing is sent: a person reads it first.
func (s *Server) mAIReply(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	m := mc(r)
	var req struct {
		ThreadID string `json:"thread_id"`
		Hint     string `json:"hint"`
	}
	if err := readJSON(r, &req); err != nil || len(req.Hint) > 300 {
		writeErr(w, 400, "invalid json")
		return
	}
	var client, channel string
	var clientID *string
	if err := s.pool.QueryRow(ctx, `select client_name, channel, client_id::text from threads where id=$1 and business_id=$2`, req.ThreadID, m.BusinessID).Scan(&client, &channel, &clientID); err != nil {
		writeErr(w, 404, "conversation not found")
		return
	}
	msgs, _ := rows(ctx, s.pool, `select from_business, body, created_at from (select from_business, body, created_at from thread_messages where thread_id=$1 order by created_at desc limit 12) x order by created_at`, req.ThreadID)
	if len(msgs) == 0 {
		writeErr(w, 400, "there is nothing to answer yet")
		return
	}
	var talk strings.Builder
	for _, x := range msgs {
		who := "Client"
		if x["from_business"] == true {
			who = "Business"
		}
		fmt.Fprintf(&talk, "%s: %v\n", who, x["body"])
	}
	next := "none"
	if clientID != nil {
		var at time.Time
		var what string
		if s.pool.QueryRow(ctx, `select bk.starts_at, coalesce((select string_agg(name, ' + ') from booking_items where booking_id = bk.id),'a visit') from bookings bk
			where bk.client_id=$1 and bk.starts_at > now() and bk.status in ('requested','confirmed') order by bk.starts_at limit 1`, *clientID).Scan(&at, &what) == nil {
			next = what + " on " + at.In(m.Loc).Format("Monday 2 January at 15:04")
		}
	}
	system := `You draft replies for a beauty business to send to its clients. Write as the business, in the first person plural or singular as fits. Rules:
- Plain, warm and short: at most 70 words. No emojis unless the client used them. No exclamation marks unless the client did.
- Use only the facts given. Never invent a price, a time, an opening, a policy or a promise. If the answer depends on the calendar or on something not given, say you will check and come back, or point to the booking page.
- Do not say you are an AI. Do not add a subject line, a signature block or quotation marks. Output only the message text.
- Write in the language the client wrote in.`
	user := s.businessFacts(ctx, m) + "\nToday is " + time.Now().In(m.Loc).Format("Monday 2 January 2006, 15:04") + ".\nClient's name: " + client + ". Channel: " + channel + ". Their next booking: " + next + ".\n\nConversation so far:\n" + talk.String()
	if h := strings.TrimSpace(req.Hint); h != "" {
		user += "\nThe business wants the reply to say: " + h + "\n"
	}
	draft, err := s.aiWrite(ctx, m.BusinessID, system, user, false)
	if err != nil {
		writeErr(w, 503, err.Error())
		return
	}
	writeJSON(w, 200, M{"draft": strings.Trim(draft, "\"")})
}

// POST /v1/m/ai/campaign   {audience, channel, goal}   a draft campaign message with the business's placeholders
func (s *Server) mAICampaign(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	m := mc(r)
	var req struct {
		Audience string `json:"audience"`
		Channel  string `json:"channel"`
		Goal     string `json:"goal"`
	}
	if err := readJSON(r, &req); err != nil || len(strings.TrimSpace(req.Goal)) < 5 || len(req.Goal) > 400 {
		writeErr(w, 400, "say in a sentence what the message is for")
		return
	}
	who := map[string]string{"all": "every client who accepts marketing", "new": "clients who joined in the last 30 days", "regulars": "clients with three or more visits", "lapsed": "clients who have not visited in 60 days", "birthday": "clients whose birthday is this month"}[req.Audience]
	if who == "" {
		who = "the business's clients"
	}
	limit := "at most 300 characters"
	if req.Channel == "email" {
		limit = "at most 90 words, with a subject line of at most 8 words"
	}
	system := `You write marketing messages for a beauty business to send to its own clients. Rules:
- Plain, warm and specific. No hype, no all-caps, no emojis, at most one exclamation mark.
- Use only the facts given. Never invent a price, a discount, a date or a code: if the goal names one, use exactly that.
- Use these placeholders exactly as written where they help, and no others: {first name}, {business}, {booking link}, {last service}. Start by greeting {first name}. End with {booking link}.
- Answer with a JSON object: {"subject": "...", "message": "..."}. The subject is empty unless the channel is email.`
	user := s.businessFacts(ctx, m) + "\nAudience: " + who + ". Channel: " + firstNonEmpty(req.Channel, "whatsapp") + " (" + limit + ").\nWhat the business wants this message to do: " + strings.TrimSpace(req.Goal)
	raw, err := s.aiWrite(ctx, m.BusinessID, system, user, true)
	if err != nil {
		writeErr(w, 503, err.Error())
		return
	}
	var out struct {
		Subject string `json:"subject"`
		Message string `json:"message"`
	}
	if json.Unmarshal([]byte(raw), &out) != nil || strings.TrimSpace(out.Message) == "" {
		writeErr(w, 503, "the AI service returned something unusable; try again")
		return
	}
	// Models like to write the real name and link. Put the placeholders back, so one message works for every client and the link is always the current one.
	msg := strings.TrimSpace(out.Message)
	msg = strings.ReplaceAll(msg, strings.TrimRight(s.cfg.WebURL, "/")+"/b/"+m.Slug, "{booking link}")
	msg = strings.ReplaceAll(msg, m.Business, "{business}")
	if !strings.Contains(msg, "{booking link}") {
		msg = strings.TrimRight(msg, " ") + " {booking link}"
	}
	writeJSON(w, 200, M{"subject": strings.TrimSpace(strings.ReplaceAll(out.Subject, m.Business, "{business}")), "message": msg})
}
