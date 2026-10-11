package httpapi

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"golang.org/x/crypto/bcrypt"
)

func customerPage(r *http.Request, key string) int {
	n, _ := strconv.Atoi(r.URL.Query().Get(key))
	if n < 1 {
		return 1
	}
	if n > 100000 {
		return 100000
	}
	return n
}

// GET /auth/sessions: opaque session IDs only; never expose bearer-token hashes.
func (s *Server) authSessions(w http.ResponseWriter, r *http.Request) {
	out, err := rows(r.Context(), s.pool, `select id, device_name, created_at, last_seen_at, expires_at, token_hash=$2 as current
 from user_sessions s where user_id=$1 and (expires_at>now() or exists(select 1 from customer_refresh_tokens rt where rt.session_id=s.id and rt.used_at is null and least(rt.idle_expires_at,rt.absolute_expires_at)>now())) order by last_seen_at desc, id`, currentCustomer(r).ID, hashToken(bearer(r)))
	if err != nil {
		writeErr(w, 500, "could not load your signed-in devices")
		return
	}
	writeJSON(w, 200, M{"sessions": out})
}

func (s *Server) authSessionRevoke(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	q := `delete from user_sessions where user_id=$1 and id::text=$2 and token_hash<>$3`
	if id == "others" {
		q = `delete from user_sessions where user_id=$1 and $2='others' and token_hash<>$3`
	}
	tag, err := s.pool.Exec(r.Context(), q, currentCustomer(r).ID, id, hashToken(bearer(r)))
	if err != nil {
		writeErr(w, 500, "could not sign out that device")
		return
	}
	if id != "others" && tag.RowsAffected() == 0 {
		writeErr(w, 404, "device not found; use Sign out for this device")
		return
	}
	writeJSON(w, 200, M{"ok": true})
}

// A complete export of this account, with no session tokens, reset codes or provider authorisations.
func (s *Server) authExport(w http.ResponseWriter, r *http.Request) {
	c := currentCustomer(r)
	ctx := r.Context()
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		writeErr(w, 500, "could not prepare export")
		return
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `set transaction isolation level repeatable read, read only`); err != nil {
		writeErr(w, 500, "could not prepare export")
		return
	}
	out := M{"profile": c, "exported_at": time.Now().UTC(), "format_version": 1}
	queries := map[string]string{
		"bookings":         `select id,business_id,starts_at,ends_at,status,total_cents,deposit_cents,deposit_paid,guest_name,notes from bookings where user_id=$1 order by starts_at,id`,
		"orders":           `select id,status,fulfilment,total_cents,currency,created_at,customer_name,customer_phone,customer_email,address,shipping_cents,tax_cents,discount_cents,gift_cents,credit_cents from orders where user_id=$1 order by created_at,id`,
		"order_items":      `select oi.order_id,oi.name,oi.qty,oi.unit_cents,oi.seller_name from order_items oi join orders o on o.id=oi.order_id where o.user_id=$1 order by oi.order_id,oi.id`,
		"messages":         `select tm.thread_id,tm.from_business,tm.body,tm.created_at from thread_messages tm join threads t on t.id=tm.thread_id where t.user_id=$1 order by tm.created_at,tm.id`,
		"reviews":          `select rv.id,rv.business_id,rv.rating,rv.body,rv.created_at from reviews rv join bookings bk on bk.id=rv.booking_id where bk.user_id=$1 order by rv.created_at,rv.id`,
		"saved_businesses": `select business_id,created_at from user_favourites where user_id=$1 order by created_at`,
		"saved_products":   `select product_id,created_at from user_favourite_products where user_id=$1 order by created_at`,
		"store_credit":     `select amount_cents,currency,reason,created_at from user_credits where user_id=$1 order by created_at,id`,
		"product_reviews":  `select product_id,rating,body,created_at from product_reviews where user_id=$1 order by created_at,id`,
		"intake_answers":   `select ba.booking_id,ba.label,ba.answer from booking_answers ba join bookings bk on bk.id=ba.booking_id where bk.user_id=$1 order by ba.booking_id,ba.sort`,
		"plans":            `select p.id,p.business_id,p.kind,p.name,p.price_cents,p.status,p.started_at,p.expires_at,p.renews_on from client_plans p join clients cl on cl.id=p.client_id where $1::uuid is not null and $2::boolean and $3<>'' and cl.phone=$3 order by p.started_at,p.id`,
		"loyalty":          `select lp.business_id,lp.points,lp.reason,lp.created_at from loyalty_points lp join clients cl on cl.id=lp.client_id where $1::uuid is not null and $2::boolean and $3<>'' and cl.phone=$3 order by lp.created_at,lp.id`,
	}
	for key, q := range queries {
		args := []any{c.ID}
		if key == "plans" || key == "loyalty" {
			args = append(args, c.PhoneVerified, c.Phone)
		}
		rs, e := tx.Query(ctx, q, args...)
		if e != nil {
			writeErr(w, 500, "could not export "+key)
			return
		}
		var data []M
		for rs.Next() {
			vals, e := rs.Values()
			if e != nil {
				rs.Close()
				writeErr(w, 500, "could not read export")
				return
			}
			item := M{}
			for i, f := range rs.FieldDescriptions() {
				item[string(f.Name)] = vals[i]
			}
			tidy(item)
			data = append(data, item)
		}
		e = rs.Err()
		rs.Close()
		if e != nil {
			writeErr(w, 500, "could not read export")
			return
		}
		if data == nil {
			data = []M{}
		}
		out[key] = data
	}
	if err = tx.Commit(ctx); err != nil {
		writeErr(w, 500, "could not finish export")
		return
	}
	w.Header().Set("Content-Disposition", `attachment; filename="logaluxe-account.json"`)
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, out)
}

// Deletion removes the login/profile; statutory transaction records stay intact.
// Financial obligations and upcoming visits must be resolved first.
func (s *Server) authDelete(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Password string `json:"password"`
		Confirm  string `json:"confirm"`
	}
	if readJSON(r, &req) != nil || req.Confirm != "DELETE" {
		writeErr(w, 400, "type DELETE to confirm account deletion")
		return
	}
	c := currentCustomer(r)
	ctx := r.Context()
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		writeErr(w, 500, "could not delete account")
		return
	}
	defer tx.Rollback(ctx)
	var hash *string
	if err = tx.QueryRow(ctx, `select password_hash from users where id=$1 and deleted_at is null for update`, c.ID).Scan(&hash); err != nil {
		writeErr(w, 401, "sign in again")
		return
	}
	if hash != nil {
		if bcrypt.CompareHashAndPassword([]byte(*hash), []byte(req.Password)) != nil {
			writeErr(w, 403, "your current password is required")
			return
		}
	} else {
		var fresh bool
		err = tx.QueryRow(ctx, `select exists(select 1 from user_sessions where user_id=$1 and token_hash=$2 and created_at>now()-interval '10 minutes')`, c.ID, hashToken(bearer(r))).Scan(&fresh)
		if err != nil || !fresh || !c.PhoneVerified {
			writeErr(w, 403, "sign in again with a phone code before deleting your account")
			return
		}
	}
	var blocked bool
	err = tx.QueryRow(ctx, `select exists(select 1 from bookings where user_id=$1 and status in ('requested','confirmed','checked_in','in_progress') and ends_at>now())
	or exists(select 1 from payments p left join bookings bk on bk.id=p.booking_id where (p.user_id=$1 or bk.user_id=$1) and p.status='pending')
 or exists(select 1 from orders o where user_id=$1 and (status in ('pending','paid','ready','shipped') or exists(select 1 from order_shipments sh where sh.order_id=o.id and sh.status in ('new','ready','shipped'))))
 or exists(select 1 from disputes where user_id=$1 and status in ('with_business','needs_decision'))
 or exists(select 1 from order_returns where user_id=$1 and (status='requested' or provider_refund_status='pending'))
 or exists(select 1 from client_plans p join clients cl on cl.id=p.client_id where $2::boolean and cl.phone=$3 and p.status in ('active','past_due'))
 or coalesce((select sum(amount_cents) from user_credits where user_id=$1),0)>0`, c.ID, c.PhoneVerified, c.Phone).Scan(&blocked)
	if err != nil {
		writeErr(w, 500, "could not check outstanding activity")
		return
	}
	if blocked {
		writeErr(w, 409, "resolve upcoming visits, open orders, returns, disputes, unused plans and store credit before deleting; contact support if you need help")
		return
	}
	for _, table := range []string{"user_sessions", "user_password_resets", "user_email_tokens", "user_cards", "user_favourites", "user_favourite_products"} {
		if _, err = tx.Exec(ctx, "delete from "+table+" where user_id=$1", c.ID); err != nil {
			writeErr(w, 500, "could not remove account data")
			return
		}
	}
	_, err = tx.Exec(ctx, `update users set email=null,phone=null,first_name='Deleted account',last_name='',password_hash=null,email_verified_at=null,phone_verified_at=null,stripe_customer_id=null,referral_code=null,preferred_channel='email',deleted_at=now() where id=$1`, c.ID)
	if err != nil {
		writeErr(w, 500, "could not remove profile")
		return
	}
	if err = tx.Commit(ctx); err != nil {
		writeErr(w, 500, "could not finish deleting account")
		return
	}
	writeJSON(w, 200, M{"ok": true, "retained": "transaction and merchant records required for legal, financial and dispute purposes"})
}

func deviceLabel(ua string) string {
	browser := "Browser"
	for _, v := range []struct{ key, name string }{{"Edg/", "Edge"}, {"Firefox/", "Firefox"}, {"Chrome/", "Chrome"}, {"Safari/", "Safari"}, {"Expo", "LogaLuxe app"}} {
		if strings.Contains(ua, v.key) {
			browser = v.name
			break
		}
	}
	os := ""
	for _, v := range []struct{ key, name string }{{"Android", "Android"}, {"iPhone", "iPhone"}, {"iPad", "iPad"}, {"Windows", "Windows"}, {"Macintosh", "Mac"}, {"Linux", "Linux"}} {
		if strings.Contains(ua, v.key) {
			os = v.name
			break
		}
	}
	if os != "" {
		return browser + " on " + os
	}
	return browser
}
