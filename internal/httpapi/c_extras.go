package httpapi

import (
	"context"
	"crypto/rand"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
)

// The parts of the customer web that the design drew and nothing real stood behind:
// how fast a business replies, the languages it speaks, its delivery and returns
// policy, ingredients, picking an order up today or at the next visit, saved
// products, photos on reviews, and referral credit.

// rowQuerier is the pool or a transaction.
type rowQuerier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// ---------- platform settings ----------

func (s *Server) platformInt(ctx context.Context, key string) int {
	var n int
	_ = s.pool.QueryRow(ctx, `select (value #>> '{}')::int from platform_settings where key=$1`, key).Scan(&n)
	return n
}

// ---------- what a business says about itself ----------

// bizExtras is added to a business page: languages, policies, and how fast it usually replies.
func (s *Server) bizExtras(ctx context.Context, bizID string) M {
	out, err := row(ctx, s.pool, `select languages, returns_days, returns_note, ship_days_min, ship_days_max, pickup_ready_mins from businesses where id=$1`, bizID)
	if err != nil {
		return M{}
	}
	// The usual wait for a reply: for each time a client wrote and the business then answered, the
	// minutes in between, over the last 90 days. Shown only when there are enough to mean something.
	var mins *float64
	var n int
	_ = s.pool.QueryRow(ctx, `select percentile_cont(0.5) within group (order by x.mins), count(*) from (
		select extract(epoch from (r.created_at - m.created_at)) / 60 as mins
		from thread_messages m join threads t on t.id = m.thread_id
		join lateral (select created_at from thread_messages r where r.thread_id = m.thread_id and r.from_business and r.created_at > m.created_at order by r.created_at limit 1) r on true
		where t.business_id = $1 and not m.from_business and m.created_at > now() - interval '90 days'
		  and not exists (select 1 from thread_messages p where p.thread_id = m.thread_id and not p.from_business and p.created_at < m.created_at
		        and p.created_at > coalesce((select max(q.created_at) from thread_messages q where q.thread_id = m.thread_id and q.from_business and q.created_at < m.created_at), '-infinity'))
	) x`, bizID).Scan(&mins, &n)
	if mins != nil && n >= 3 {
		out["reply_minutes"] = int(*mins + 0.5)
		out["reply_sample"] = n
	}
	return out
}

var knownLanguages = []string{"English", "Yoruba", "Igbo", "Hausa", "Pidgin", "Spanish", "French", "Arabic", "Amharic", "Somali", "Swahili", "Portuguese", "Haitian Creole", "American Sign Language"}

// GET /v1/m/shop-policy
func (s *Server) mShopPolicy(w http.ResponseWriter, r *http.Request) {
	out, err := row(r.Context(), s.pool, `select languages, returns_days, returns_note, ship_days_min, ship_days_max, pickup_ready_mins from businesses where id=$1`, mc(r).BusinessID)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	out["language_choices"] = knownLanguages
	writeJSON(w, 200, out)
}

// PUT /v1/m/shop-policy   {languages, returns_days, returns_note, ship_days_min, ship_days_max, pickup_ready_mins}
// A number left out (null) means "we do not state this", and the storefront then says nothing about it.
func (s *Server) mShopPolicySave(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Languages       []string `json:"languages"`
		ReturnsDays     *int     `json:"returns_days"`
		ReturnsNote     string   `json:"returns_note"`
		ShipDaysMin     *int     `json:"ship_days_min"`
		ShipDaysMax     *int     `json:"ship_days_max"`
		PickupReadyMins *int     `json:"pickup_ready_mins"`
	}
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, "invalid json")
		return
	}
	langs := []string{}
	for _, l := range req.Languages {
		for _, k := range knownLanguages {
			if strings.EqualFold(strings.TrimSpace(l), k) {
				langs = append(langs, k)
			}
		}
	}
	req.ReturnsNote = strings.TrimSpace(req.ReturnsNote)
	switch {
	case req.ReturnsDays != nil && (*req.ReturnsDays < 0 || *req.ReturnsDays > 90):
		writeErr(w, 400, "returns can be accepted for 0 to 90 days")
		return
	case len(req.ReturnsNote) > 300:
		writeErr(w, 400, "keep the returns note under 300 characters")
		return
	case (req.ShipDaysMin == nil) != (req.ShipDaysMax == nil):
		writeErr(w, 400, "give both the shortest and the longest delivery time, or neither")
		return
	case req.ShipDaysMin != nil && (*req.ShipDaysMin < 0 || *req.ShipDaysMax < *req.ShipDaysMin || *req.ShipDaysMax > 30):
		writeErr(w, 400, "delivery takes between 0 and 30 days, shortest first")
		return
	case req.PickupReadyMins != nil && (*req.PickupReadyMins < 0 || *req.PickupReadyMins > 480):
		writeErr(w, 400, "an order can be ready for pick-up in 0 to 480 minutes")
		return
	}
	if _, err := s.pool.Exec(r.Context(), `update businesses set languages=$2, returns_days=$3, returns_note=$4, ship_days_min=$5, ship_days_max=$6, pickup_ready_mins=$7 where id=$1`,
		mc(r).BusinessID, langs, req.ReturnsDays, req.ReturnsNote, req.ShipDaysMin, req.ShipDaysMax, req.PickupReadyMins); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, M{"ok": true})
}

type productDetails struct {
	Ingredients string `json:"ingredients"`
	HowToUse    string `json:"how_to_use"`
	ShipDaysMin *int   `json:"ship_days_min"`
	ShipDaysMax *int   `json:"ship_days_max"`
	ReturnsDays *int   `json:"returns_days"`
}

func (d *productDetails) problem() string {
	d.Ingredients, d.HowToUse = strings.TrimSpace(d.Ingredients), strings.TrimSpace(d.HowToUse)
	switch {
	case len(d.Ingredients) > 3000 || len(d.HowToUse) > 3000:
		return "keep ingredients and directions under 3,000 characters each"
	case (d.ShipDaysMin == nil) != (d.ShipDaysMax == nil):
		return "give both the shortest and the longest delivery time, or neither"
	case d.ShipDaysMin != nil && (*d.ShipDaysMin < 0 || *d.ShipDaysMax < *d.ShipDaysMin || *d.ShipDaysMax > 30):
		return "delivery takes between 0 and 30 days, shortest first"
	case d.ReturnsDays != nil && (*d.ReturnsDays < 0 || *d.ReturnsDays > 90):
		return "returns can be accepted for 0 to 90 days"
	}
	return ""
}

// PUT /v1/m/products/{id}/details   {ingredients, how_to_use}
// A business's delivery and returns come from its shop policy, so only the text is saved here.
func (s *Server) mProductDetails(w http.ResponseWriter, r *http.Request) {
	var req productDetails
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, "invalid json")
		return
	}
	if why := req.problem(); why != "" {
		writeErr(w, 400, why)
		return
	}
	tag, err := s.pool.Exec(r.Context(), `update products set ingredients=$3, how_to_use=$4 where id=$1 and business_id=$2`, chi.URLParam(r, "id"), mc(r).BusinessID, req.Ingredients, req.HowToUse)
	if err != nil || tag.RowsAffected() == 0 {
		writeErr(w, 404, "product not found")
		return
	}
	writeJSON(w, 200, M{"ok": true})
}

// PUT /v1/admin/products/{id}/details   the same, plus delivery and returns for a brand's product
func (s *Server) adminProductDetails(w http.ResponseWriter, r *http.Request) {
	var req productDetails
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, "invalid json")
		return
	}
	if why := req.problem(); why != "" {
		writeErr(w, 400, why)
		return
	}
	var slug string
	if err := s.pool.QueryRow(r.Context(), `update products set ingredients=$2, how_to_use=$3, ship_days_min=$4, ship_days_max=$5, returns_days=$6 where id=$1 returning slug`,
		chi.URLParam(r, "id"), req.Ingredients, req.HowToUse, req.ShipDaysMin, req.ShipDaysMax, req.ReturnsDays).Scan(&slug); err != nil {
		writeErr(w, 404, "product not found")
		return
	}
	s.audit(r, "product.details", slug, nil, nil)
	writeJSON(w, 200, M{"ok": true})
}

// ---------- what a shopper is told about getting a product ----------

var weekdayKey = []string{"sun", "mon", "tue", "wed", "thu", "fri", "sat"}

// productExtras answers, for one product: ingredients, how long delivery takes, the returns policy,
// whether it can be picked up today, the shopper's next visit to the seller, and whether they saved it.
func (s *Server) productExtras(ctx context.Context, slug string, uid *string) M {
	var id, ingredients, returnsNote, tz string
	var bizID *string
	var pShipMin, pShipMax, pReturns, bShipMin, bShipMax, bReturns, readyMins *int
	var pickup bool
	var stock int
	var hours map[string]*[2]string
	if err := s.pool.QueryRow(ctx, `select p.id::text, p.ingredients, p.ship_days_min, p.ship_days_max, p.returns_days, p.pickup and p.business_id is not null, p.stock,
		b.id::text, b.ship_days_min, b.ship_days_max, b.returns_days, coalesce(b.returns_note,''), b.pickup_ready_mins, coalesce(b.timezone,'UTC'),
		(select l.hours from locations l where l.business_id = b.id and l.is_primary)
		from products p left join businesses b on b.id = p.business_id where p.slug=$1 and p.active`, slug).
		Scan(&id, &ingredients, &pShipMin, &pShipMax, &pReturns, &pickup, &stock, &bizID, &bShipMin, &bShipMax, &bReturns, &returnsNote, &readyMins, &tz, &hours); err != nil {
		return nil
	}
	out := M{"ingredients": ingredients}
	shipMin, shipMax, returns := pShipMin, pShipMax, pReturns
	if bizID != nil { // a business's policy covers everything it sells
		shipMin, shipMax, returns = bShipMin, bShipMax, bReturns
	}
	if shipMin != nil && shipMax != nil {
		out["delivery"] = M{"days_min": *shipMin, "days_max": *shipMax}
	}
	if returns != nil {
		out["returns"] = M{"days": *returns, "note": returnsNote}
	}
	if bizID != nil && pickup && stock > 0 && readyMins != nil {
		loc, err := time.LoadLocation(tz)
		if err != nil {
			loc = time.UTC
		}
		now := time.Now().In(loc)
		if span := hours[weekdayKey[int(now.Weekday())]]; span != nil {
			open, e1 := time.ParseInLocation("2006-01-02 15:04", now.Format("2006-01-02")+" "+span[0], loc)
			shut, e2 := time.ParseInLocation("2006-01-02 15:04", now.Format("2006-01-02")+" "+span[1], loc)
			if e1 == nil && e2 == nil {
				from := now
				if open.After(from) {
					from = open
				}
				ready := from.Add(time.Duration(*readyMins) * time.Minute)
				if ready.Before(shut) {
					out["pickup_today"] = M{"ready_at": ready.Format("15:04"), "until": span[1], "ready_mins": *readyMins, "open_now": !now.Before(open)}
				}
			}
		}
	}
	if uid != nil {
		var saved bool
		_ = s.pool.QueryRow(ctx, `select exists(select 1 from user_favourite_products where user_id=$1 and product_id=$2)`, *uid, id).Scan(&saved)
		out["saved"] = saved
		if bizID != nil && pickup {
			if v, err := row(ctx, s.pool, `select bk.id, bk.starts_at, $3::text as timezone from bookings bk where bk.user_id=$1 and bk.business_id=$2 and bk.status in ('requested','confirmed') and bk.starts_at > now() order by bk.starts_at limit 1`, *uid, *bizID, tz); err == nil {
				out["next_visit"] = v
			}
		}
	}
	return out
}

// GET /v1/products-extras?slugs=a,b   the same answers for several products at once, for the cart
func (s *Server) productsExtras(w http.ResponseWriter, r *http.Request) {
	uid := s.customerID(r)
	out := M{}
	for i, slug := range splitCSV(r.URL.Query().Get("slugs")) {
		if i >= 40 {
			break
		}
		if x := s.productExtras(r.Context(), slug, uid); x != nil {
			out[slug] = x
		}
	}
	writeJSON(w, 200, M{"extras": out})
}

// pickupNote is written on a seller's part of an order: when the customer already has a visit booked there, that is when they collect.
func (s *Server) pickupNote(ctx context.Context, q rowQuerier, userID, businessID string) string {
	var at time.Time
	var tz string
	if err := q.QueryRow(ctx, `select bk.starts_at, b.timezone from bookings bk join businesses b on b.id = bk.business_id
		where bk.user_id=$1 and bk.business_id=$2 and bk.status in ('requested','confirmed') and bk.starts_at > now() order by bk.starts_at limit 1`, userID, businessID).Scan(&at, &tz); err != nil {
		return ""
	}
	loc, err := time.LoadLocation(tz)
	if err != nil {
		loc = time.UTC
	}
	return "Has a visit booked on " + at.In(loc).Format("Mon 2 Jan at 15:04") + ". Have it ready then."
}

// ---------- saved products ----------

// GET /v1/auth/favourite-products
func (s *Server) authFavProducts(w http.ResponseWriter, r *http.Request) {
	out, err := rows(r.Context(), s.pool, `select p.slug, p.name, p.seller_name, p.price_cents, p.stock, p.tone, p.sizes, p.rating::float8 as rating, p.review_count, f.created_at as saved_at,
		(select sm.id from site_media sm where sm.slot='product' and sm.ref = p.slug and sm.active order by sm.sort, sm.created_at desc limit 1) as photo_id
		from user_favourite_products f join products p on p.id = f.product_id where f.user_id=$1 and p.active order by f.created_at desc`, currentCustomer(r).ID)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, M{"products": out})
}

// PUT|DELETE /v1/auth/favourite-products/{slug}
func (s *Server) authFavProductSet(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var id string
	if err := s.pool.QueryRow(ctx, `select id::text from products where slug=$1 and active`, chi.URLParam(r, "slug")).Scan(&id); err != nil {
		writeErr(w, 404, "product not found")
		return
	}
	if r.Method == http.MethodDelete {
		_, _ = s.pool.Exec(ctx, `delete from user_favourite_products where user_id=$1 and product_id=$2`, currentCustomer(r).ID, id)
		writeJSON(w, 200, M{"ok": true, "saved": false})
		return
	}
	var n int
	_ = s.pool.QueryRow(ctx, `select count(*) from user_favourite_products where user_id=$1`, currentCustomer(r).ID).Scan(&n)
	if n >= 200 {
		writeErr(w, 409, "you have saved 200 products, which is the most we keep; remove one first")
		return
	}
	_, _ = s.pool.Exec(ctx, `insert into user_favourite_products (user_id, product_id) values ($1,$2) on conflict do nothing`, currentCustomer(r).ID, id)
	writeJSON(w, 200, M{"ok": true, "saved": true})
}

// ---------- photos on a review ----------

const maxReviewPhotos = 3

// POST /v1/auth/reviews/{id}/photos   multipart "file"   the person who wrote the review adds up to three photos
func (s *Server) authReviewPhoto(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	c := currentCustomer(r)
	if s.store == nil {
		writeErr(w, 503, "photo storage is not set up yet")
		return
	}
	if !needVerified(w, c) {
		return
	}
	var reviewID string
	if err := s.pool.QueryRow(ctx, `select rv.id::text from reviews rv join bookings bk on bk.id = rv.booking_id where rv.id::text=$1 and bk.user_id=$2`, chi.URLParam(r, "id"), c.ID).Scan(&reviewID); err != nil {
		writeErr(w, 404, "review not found")
		return
	}
	var have int
	_ = s.pool.QueryRow(ctx, `select count(*) from site_media where slot='review' and ref=$1`, reviewID).Scan(&have)
	if have >= maxReviewPhotos {
		writeErr(w, 409, "a review can have three photos; remove one to add another")
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
	key := "site/review/" + reviewID + "/" + id + ext
	if err := s.store.Put(ctx, key, contentType, data); err != nil {
		writeErr(w, 502, "the upload failed; try again")
		return
	}
	if _, err := s.pool.Exec(ctx, `insert into site_media (id, slot, ref, storage_key, content_type, size_bytes, alt, uploaded_by, sort) values ($1,'review',$2,$3,$4,$5,'Photo from a client review',$6,$7)`,
		id, reviewID, key, contentType, len(data), "customer:"+c.ID, have); err != nil {
		_ = s.store.Delete(ctx, key)
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 201, M{"ok": true, "id": id})
}

// DELETE /v1/auth/review-photos/{id}
func (s *Server) authReviewPhotoDelete(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var key string
	if err := s.pool.QueryRow(ctx, `delete from site_media sm using reviews rv, bookings bk where sm.id::text=$1 and sm.slot='review' and rv.id::text = sm.ref and bk.id = rv.booking_id and bk.user_id=$2 returning sm.storage_key`,
		chi.URLParam(r, "id"), currentCustomer(r).ID).Scan(&key); err != nil {
		writeErr(w, 404, "photo not found")
		return
	}
	if s.store != nil {
		_ = s.store.Delete(ctx, key)
	}
	writeJSON(w, 200, M{"ok": true})
}

// ---------- referral credit ----------

// referralCode gives a customer their code, making one the first time it is asked for.
func (s *Server) referralCode(ctx context.Context, userID string) string {
	var code *string
	_ = s.pool.QueryRow(ctx, `select referral_code from users where id=$1`, userID).Scan(&code)
	if code != nil && *code != "" {
		return *code
	}
	for i := 0; i < 5; i++ {
		b := make([]byte, 7)
		if _, err := rand.Read(b); err != nil {
			return ""
		}
		var sb strings.Builder
		for _, c := range b {
			sb.WriteByte(codeAlphabet[int(c)%len(codeAlphabet)])
		}
		if tag, err := s.pool.Exec(ctx, `update users set referral_code=$2 where id=$1 and referral_code is null`, userID, sb.String()); err == nil && tag.RowsAffected() == 1 {
			return sb.String()
		}
		_ = s.pool.QueryRow(ctx, `select referral_code from users where id=$1`, userID).Scan(&code)
		if code != nil && *code != "" {
			return *code
		}
	}
	return ""
}

func creditBalance(ctx context.Context, q rowQuerier, userID string) int {
	return creditBalanceIn(ctx, q, userID, "USD")
}

func creditBalanceIn(ctx context.Context, q rowQuerier, userID, currency string) int {
	var n int
	_ = q.QueryRow(ctx, `select coalesce(sum(amount_cents),0)::int from user_credits where user_id=$1 and currency=$2`, userID, currency).Scan(&n)
	if n < 0 {
		return 0
	}
	return n
}

// GET /v1/auth/referral   the customer's code, what the programme pays, and their credit
func (s *Server) authReferral(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	c := currentCustomer(r)
	amount := s.platformInt(ctx, "referral_credit_cents")
	out := M{"on": amount > 0, "credit_cents": amount, "currency": "USD", "balance_cents": creditBalance(ctx, s.pool, c.ID)}
	if amount > 0 {
		code := s.referralCode(ctx, c.ID)
		out["code"] = code
		out["link"] = strings.TrimRight(s.cfg.WebURL, "/") + "/signup?ref=" + code
	}
	var joined, paid int
	_ = s.pool.QueryRow(ctx, `select count(*), count(*) filter (where referral_paid_at is not null) from users where referred_by=$1`, c.ID).Scan(&joined, &paid)
	out["friends_joined"], out["friends_paid"] = joined, paid
	history, _ := rows(ctx, s.pool, `select amount_cents, currency, reason, created_at from user_credits where user_id=$1 order by created_at desc limit 30`, c.ID)
	out["history"] = history
	writeJSON(w, 200, out)
}

// noteReferral ties a new account to the friend whose code it signed up with. A wrong code is ignored: it must not stop a sign-up.
func (s *Server) noteReferral(ctx context.Context, newUserID, code string) {
	code = strings.ToUpper(strings.TrimSpace(code))
	if code == "" || s.platformInt(ctx, "referral_credit_cents") <= 0 {
		return
	}
	_, _ = s.pool.Exec(ctx, `update users u set referred_by = f.id from users f where u.id=$1 and f.referral_code=$2 and f.id <> u.id and f.email_verified_at is not null`, newUserID, code)
}

// awardReferrals credits both people once the friend has confirmed their email and paid for a first visit or order.
// It runs from the worker, so a refund a minute after paying does not earn credit before anyone can look.
func (s *Server) awardReferrals(ctx context.Context) {
	amount := s.platformInt(ctx, "referral_credit_cents")
	if amount <= 0 {
		return
	}
	due, _ := rows(ctx, s.pool, `select u.id::text as id, u.referred_by::text as friend, u.first_name from users u
		where u.referred_by is not null and u.referral_paid_at is null and u.email_verified_at is not null
		  and (exists (select 1 from bookings bk where bk.user_id = u.id and bk.status = 'paid' and bk.paid_at < now() - interval '1 day')
		    or exists (select 1 from orders o where o.user_id = u.id and o.status in ('delivered') ))
		limit 200`)
	for _, d := range due {
		tx, err := s.pool.Begin(ctx)
		if err != nil {
			return
		}
		tag, err := tx.Exec(ctx, `update users set referral_paid_at = now() where id=$1 and referral_paid_at is null`, d["id"])
		if err == nil && tag.RowsAffected() == 1 {
			_, _ = tx.Exec(ctx, `insert into user_credits (user_id, amount_cents, reason, about_user) values ($1,$3,'Welcome credit: you joined with a friend''s link',$2), ($2,$3,'Thank you: a friend you invited made their first purchase',$1)`, d["id"], d["friend"], amount)
			_ = tx.Commit(ctx)
		} else {
			_ = tx.Rollback(ctx)
		}
	}
}

// GET /v1/admin/settings/referral
func (s *Server) adminReferral(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	s.awardReferrals(ctx) // the worker does this every ten minutes; staff looking at the numbers see them up to date
	out := M{"credit_cents": s.platformInt(ctx, "referral_credit_cents"), "currency": "USD"}
	stats, _ := row(ctx, s.pool, `select (select count(*) from users where referred_by is not null) as joined, (select count(*) from users where referral_paid_at is not null) as paid,
		(select coalesce(sum(amount_cents),0) from user_credits where amount_cents > 0)::int as given_cents, (select coalesce(-sum(amount_cents),0) from user_credits where amount_cents < 0)::int as spent_cents`)
	out["stats"] = stats
	writeJSON(w, 200, out)
}

// PUT /v1/admin/settings/referral   {credit_cents}   0 switches the programme off
func (s *Server) adminReferralSave(w http.ResponseWriter, r *http.Request) {
	var req struct {
		CreditCents int `json:"credit_cents"`
	}
	if err := readJSON(r, &req); err != nil || req.CreditCents < 0 || req.CreditCents > 10000 {
		writeErr(w, 400, "the credit is between $0 (off) and $100")
		return
	}
	before := s.platformInt(r.Context(), "referral_credit_cents")
	if _, err := s.pool.Exec(r.Context(), `insert into platform_settings (key, value, updated_by) values ('referral_credit_cents', to_jsonb($1::int), $2)
		on conflict (key) do update set value = excluded.value, updated_by = excluded.updated_by, updated_at = now()`, req.CreditCents, currentAdmin(r).Email); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	s.audit(r, "settings.referral_credit", fmt.Sprint(req.CreditCents), M{"credit_cents": before}, M{"credit_cents": req.CreditCents})
	writeJSON(w, 200, M{"ok": true})
}

// productOwnDetails is what the people who edit a product see: its own text and its own delivery and returns
// (a business's shop policy is not mixed in), whether or not it is on sale. The shape matches the shop's product answer.
func (s *Server) productOwnDetails(w http.ResponseWriter, r *http.Request, businessID string) {
	var how, ingredients string
	var shipMin, shipMax, returns *int
	if err := s.pool.QueryRow(r.Context(), `select how_to_use, ingredients, ship_days_min, ship_days_max, returns_days from products where id::text=$1 and ($2 = '' or business_id::text = $2)`,
		chi.URLParam(r, "id"), businessID).Scan(&how, &ingredients, &shipMin, &shipMax, &returns); err != nil {
		writeErr(w, 404, "product not found")
		return
	}
	extras := M{"ingredients": ingredients}
	if shipMin != nil && shipMax != nil {
		extras["delivery"] = M{"days_min": *shipMin, "days_max": *shipMax}
	}
	if returns != nil {
		extras["returns"] = M{"days": *returns, "note": ""}
	}
	writeJSON(w, 200, M{"product": M{"how_to_use": how}, "extras": extras})
}

// GET /v1/m/products/{id}/details
func (s *Server) mProductDetailsGet(w http.ResponseWriter, r *http.Request) {
	s.productOwnDetails(w, r, mc(r).BusinessID)
}

// GET /v1/admin/products/{id}/details
func (s *Server) adminProductDetailsGet(w http.ResponseWriter, r *http.Request) {
	s.productOwnDetails(w, r, "")
}
