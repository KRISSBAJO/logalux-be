package httpapi

import (
	"github.com/jackc/pgx/v5"
	"net/http"
	"strings"
	"time"
)

// An authenticated merchant can create their personal customer profile. Existing
// profiles require a separately authenticated customer session, never email alone.
func (s *Server) mCustomer(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	m := mc(r)
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		writeErr(w, 500, "could not open your customer profile")
		return
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `select pg_advisory_xact_lock(hashtext($1))`, "merchant-customer:"+strings.ToLower(m.Email)); err != nil {
		writeErr(w, 500, "could not open your customer profile")
		return
	}
	var uid string
	var deleted bool
	err = tx.QueryRow(ctx, `select u.id::text,u.deleted_at is not null from merchant_customer_links l join users u on u.id=l.user_id where l.merchant_id=$1`, m.ID).Scan(&uid, &deleted)
	if err == nil && deleted {
		writeErr(w, 409, "your linked customer profile was deleted; contact support before creating another")
		return
	}
	if err != nil && err != pgx.ErrNoRows {
		writeErr(w, 500, "could not read your customer profile")
		return
	}
	if uid == "" {
		err = tx.QueryRow(ctx, `select id::text,deleted_at is not null from users where lower(email)=lower($1)`, m.Email).Scan(&uid, &deleted)
		if err == nil {
			c, ok := s.customerFrom(ctx, r.Header.Get("X-Customer-Token"))
			if deleted || !ok || c.ID != uid {
				writeJSON(w, 409, M{"error": "Sign in to your existing customer account once to link it securely.", "existing_customer": true})
				return
			}
		} else if err == pgx.ErrNoRows {
			parts := strings.Fields(m.Name)
			first, last := "Customer", ""
			if len(parts) > 0 {
				first = parts[0]
				last = strings.Join(parts[1:], " ")
			}
			err = tx.QueryRow(ctx, `insert into users(email,first_name,last_name,preferred_channel) values(lower($1),$2,$3,'email') returning id::text`, m.Email, first, last).Scan(&uid)
			if err != nil {
				writeErr(w, 500, "could not create your customer profile")
				return
			}
		} else {
			writeErr(w, 500, "could not read your customer profile")
			return
		}
		if _, err = tx.Exec(ctx, `insert into merchant_customer_links(merchant_id,user_id) values($1,$2)`, m.ID, uid); err != nil {
			writeErr(w, 409, "this customer profile is already linked to another merchant login")
			return
		}
	}
	tok, err := newToken()
	if err != nil {
		writeErr(w, 500, "could not sign you in")
		return
	}
	if _, err = tx.Exec(ctx, `insert into user_sessions(token_hash,user_id,expires_at,device_name) values($1,$2,$3,$4)`, hashToken(tok), uid, time.Now().Add(userSessionTTL), deviceLabel(r.UserAgent())); err != nil {
		writeErr(w, 500, "could not sign you in")
		return
	}
	if err = tx.Commit(ctx); err != nil {
		writeErr(w, 500, "could not open your customer profile")
		return
	}
	writeJSON(w, 200, M{"token": tok, "expires_in": int(userSessionTTL.Seconds())})
}
