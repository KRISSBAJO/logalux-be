package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"time"
)

var expoTokenPattern = regexp.MustCompile(`^(ExponentPushToken|ExpoPushToken)\[[A-Za-z0-9_-]+\]$`)

func (s *Server) authPush(w http.ResponseWriter, r *http.Request) {
	s.savePush(w, r, "customer", currentCustomer(r).ID)
}
func (s *Server) merchantPush(w http.ResponseWriter, r *http.Request) {
	s.savePush(w, r, "merchant", mc(r).ID)
}
func (s *Server) savePush(w http.ResponseWriter, r *http.Request, kind, id string) {
	hash := hashToken(bearer(r))
	if r.Method == http.MethodGet {
		var enabled bool
		if err := s.pool.QueryRow(r.Context(), `select exists(select 1 from push_devices where owner_kind=$1 and owner_id=$2 and session_hash=$3)`, kind, id, hash).Scan(&enabled); err != nil {
			writeErr(w, 500, "could not check notifications")
			return
		}
		writeJSON(w, 200, M{"enabled": enabled && os.Getenv("PUSH_ENABLED") == "true"})
		return
	}
	if r.Method == http.MethodDelete {
		if _, err := s.pool.Exec(r.Context(), `delete from push_devices where owner_kind=$1 and owner_id=$2 and session_hash=$3`, kind, id, hash); err != nil {
			writeErr(w, 500, "could not disable notifications")
			return
		}
		writeJSON(w, 200, M{"ok": true})
		return
	}
	var req struct {
		Token    string `json:"token"`
		Platform string `json:"platform"`
	}
	if readJSON(r, &req) != nil || !expoTokenPattern.MatchString(req.Token) || len(req.Token) > 256 || (req.Platform != "ios" && req.Platform != "android") {
		writeErr(w, 400, "invalid notification device")
		return
	}
	if os.Getenv("PUSH_ENABLED") != "true" {
		writeErr(w, 503, "phone notifications are not enabled on this server yet")
		return
	}
	_, err := s.pool.Exec(r.Context(), `insert into push_devices(owner_kind,owner_id,session_hash,token,platform) values($1,$2,$3,$4,$5)
 on conflict(owner_kind,token) do update set owner_id=excluded.owner_id,session_hash=excluded.session_hash,platform=excluded.platform,updated_at=now()`, kind, id, hash, req.Token, req.Platform)
	if err != nil {
		writeErr(w, 500, "could not register notifications")
		return
	}
	writeJSON(w, 200, M{"ok": true})
}

func expoPushRequest(ctx context.Context, path string, payload any, out any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://exp.host/--/api/v2/push/"+path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if key := os.Getenv("EXPO_ACCESS_TOKEN"); key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	res, err := (&http.Client{Timeout: 15 * time.Second}).Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return fmt.Errorf("push service returned %d", res.StatusCode)
	}
	return json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(out)
}

// Durable outbox: events are created in the booking/message transaction. Tickets
// are checked separately; a successful API response is not treated as delivery.
func (s *Server) runMobilePush(ctx context.Context) {
	if os.Getenv("PUSH_ENABLED") != "true" {
		return
	}
	_, err := s.pool.Exec(ctx, `delete from push_devices d where
 (d.owner_kind='customer' and not exists(select 1 from user_sessions ss join users u on u.id=ss.user_id where ss.token_hash=d.session_hash and ss.user_id=d.owner_id and (ss.expires_at>now() or exists(select 1 from customer_refresh_tokens rt where rt.session_id=ss.id and rt.used_at is null and least(rt.idle_expires_at,rt.absolute_expires_at)>now())) and u.deleted_at is null)) or
 (d.owner_kind='merchant' and not exists(select 1 from merchant_sessions ss where ss.token_hash=d.session_hash and ss.merchant_id=d.owner_id and ss.expires_at>now()))`)
	if err != nil {
		return
	}
	_, err = s.pool.Exec(ctx, `insert into push_events(event_key,owner_kind,owner_id,title,body,path)
 select 'reminder:'||b.id,'customer',b.user_id,'Appointment coming up','Open LogaLuxe to check your appointment details.','/client/bookings'
 from bookings b where b.user_id is not null and b.status='confirmed' and b.starts_at>now() and b.starts_at<now()+interval '2 hours'
 on conflict(event_key) do nothing`)
	if err != nil {
		return
	}
	_, err = s.pool.Exec(ctx, `insert into push_deliveries(event_id,device_id)
 select e.id,d.id from push_events e join push_devices d on d.owner_kind=e.owner_kind and d.owner_id=e.owner_id
 where e.created_at>greatest(d.updated_at,now()-interval '24 hours') on conflict do nothing`)
	if err != nil {
		return
	}
	pending, err := rows(ctx, s.pool, `select p.event_id,p.device_id,p.state,p.ticket_id,d.token,e.title,e.body,e.path
 from push_deliveries p join push_devices d on d.id=p.device_id join push_events e on e.id=p.event_id
 where e.owner_id=d.owner_id and e.owner_kind=d.owner_kind and p.state in ('pending','ticket') and p.next_at<=now() and p.attempts<8 order by p.next_at limit 100`)
	if err != nil {
		return
	}
	for _, p := range pending {
		state, next, ticket, reason := "pending", time.Now().Add(5*time.Minute), "", ""
		var status, problem string
		if p["state"] == "ticket" {
			ticket = fmt.Sprint(p["ticket_id"])
			var answer struct {
				Data map[string]struct {
					Status  string `json:"status"`
					Details struct {
						Error string `json:"error"`
					} `json:"details"`
				} `json:"data"`
			}
			err = expoPushRequest(ctx, "getReceipts", M{"ids": []string{ticket}}, &answer)
			if r, ok := answer.Data[ticket]; ok {
				status, problem = r.Status, r.Details.Error
			} else {
				state = "ticket"
			}
		} else {
			var answer struct {
				Data struct {
					Status  string `json:"status"`
					ID      string `json:"id"`
					Details struct {
						Error string `json:"error"`
					} `json:"details"`
				} `json:"data"`
			}
			err = expoPushRequest(ctx, "send", M{"to": p["token"], "title": p["title"], "body": p["body"], "data": M{"path": p["path"]}, "channelId": "default", "sound": "default"}, &answer)
			status, problem, ticket = answer.Data.Status, answer.Data.Details.Error, answer.Data.ID
		}
		if err != nil {
			reason = err.Error()
			if p["state"] == "ticket" {
				state = "ticket"
			}
		}
		if err == nil && status == "ok" {
			if p["state"] == "ticket" {
				state = "sent"
			} else if ticket != "" {
				state = "ticket"
				next = time.Now().Add(15 * time.Minute)
			}
		}
		if err == nil && status == "error" {
			reason = problem
			if problem != "MessageRateExceeded" {
				state = "failed"
			}
		}
		if problem == "DeviceNotRegistered" {
			_, _ = s.pool.Exec(ctx, `delete from push_devices where id=$1`, p["device_id"])
			continue
		}
		_, _ = s.pool.Exec(ctx, `update push_deliveries set state=$3,ticket_id=nullif($4,''),attempts=attempts+1,next_at=$5,error=nullif($6,'') where event_id=$1 and device_id=$2`, p["event_id"], p["device_id"], state, ticket, next, reason)
	}
	_, _ = s.pool.Exec(ctx, `update push_deliveries set state='failed',error=coalesce(error,'retry limit reached') where state in ('pending','ticket') and attempts>=8`)
	_, _ = s.pool.Exec(ctx, `delete from push_events where created_at<now()-interval '30 days'`)
}
