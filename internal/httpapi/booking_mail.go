package httpapi

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// bookingPlaced tells the client by email that a booking is made: once it is confirmed or sent
// as a request, and for a deposit paid online, once the money has arrived. fallback is the
// address typed at booking, used when neither the booking nor an account holds one.
func (s *Server) bookingPlaced(bookingID, fallback string) {
	if s.afterPaymentCommit(func(parent *Server) { parent.bookingPlaced(bookingID, fallback) }) {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		b, err := row(ctx, s.pool, `select bk.status, bk.starts_at, bk.client_name, bk.guest_name, bk.total_cents, bk.deposit_cents, bk.deposit_paid,
			coalesce(nullif(bk.client_email,''), u.email, nullif(cl.email,''), '') as email, coalesce(bk.client_phone,'') as phone, coalesce(u.preferred_channel, cl.preferred_channel, '') as prefer, bk.business_id::text as business_id,
			b.name as business, b.slug, b.timezone, b.currency, st.name as staff,
			coalesce((select string_agg(bi.name, ', ') from booking_items bi where bi.booking_id = bk.id), '') as services
			from bookings bk join businesses b on b.id = bk.business_id join staff st on st.id = bk.staff_id
			left join users u on u.id = bk.user_id left join clients cl on cl.id = bk.client_id where bk.id=$1`, bookingID)
		if err != nil {
			return
		}
		// A short word on the phone as well, when WhatsApp or texts are switched on and the person did not choose email only.
		if st := fmt.Sprint(b["status"]); st == "confirmed" || st == "requested" {
			if at, ok := b["starts_at"].(time.Time); ok {
				zone, zerr := time.LoadLocation(fmt.Sprint(b["timezone"]))
				if zerr != nil {
					zone = time.UTC
				}
				word := map[bool]string{true: "You are booked at ", false: "Your request is with "}[st == "confirmed"]
				s.tellPhone(ctx, fmt.Sprint(b["business_id"]), fmt.Sprint(b["phone"]), fmt.Sprint(b["prefer"]), word+fmt.Sprint(b["business"])+": "+at.In(zone).Format("Mon 2 Jan at 3:04 PM")+". See or change it: "+strings.TrimRight(s.cfg.WebURL, "/")+"/b/"+fmt.Sprint(b["slug"])+"/book?booking="+bookingID)
			}
		}
		to := strings.TrimSpace(firstNonEmpty(fmt.Sprint(b["email"]), strings.TrimSpace(strings.ReplaceAll(fallback, "<nil>", ""))))
		status := fmt.Sprint(b["status"])
		if to == "" || strings.HasSuffix(to, ".test") || (status != "confirmed" && status != "requested") {
			return
		}
		loc, err := time.LoadLocation(fmt.Sprint(b["timezone"]))
		if err != nil {
			loc = time.UTC
		}
		start, _ := b["starts_at"].(time.Time)
		when := start.In(loc).Format("Monday 2 January at 3:04 PM")
		cur, business := fmt.Sprint(b["currency"]), fmt.Sprint(b["business"])
		subject, lead := "You are booked at "+business, "Your booking is confirmed."
		if status == "requested" {
			subject, lead = "Your request to "+business, business+" has your request and will confirm it or suggest another time. Nothing is booked until they do."
		}
		var sb strings.Builder
		sb.WriteString("Hello " + firstNonEmpty(strings.Fields(fmt.Sprint(b["client_name"]) + " there")[0], "there") + ",\n\n" + lead + "\n\n")
		sb.WriteString("  " + fmt.Sprint(b["services"]) + "\n  " + when + "\n  With " + fmt.Sprint(b["staff"]) + " at " + business + "\n")
		if g := strings.TrimSpace(fmt.Sprint(b["guest_name"])); g != "" && g != "<nil>" {
			sb.WriteString("  For " + g + "\n")
		}
		sb.WriteString("  Total " + formatMoney(int(toInt(b["total_cents"])), cur) + "\n")
		if dep := int(toInt(b["deposit_cents"])); dep > 0 {
			if paid, _ := b["deposit_paid"].(bool); paid {
				sb.WriteString("  Deposit paid " + formatMoney(dep, cur) + "\n")
			} else {
				sb.WriteString("  Deposit " + formatMoney(dep, cur) + ", not charged online\n")
			}
		}
		sb.WriteString("\nSee, change or cancel it: " + strings.TrimRight(s.cfg.WebURL, "/") + "/b/" + fmt.Sprint(b["slug"]) + "/book?booking=" + bookingID + "\n")
		if _, err := s.mail.Send(ctx, to, subject, sb.String()); err != nil {
			s.logMailFailure("booking email", to, err)
		}
	}()
}
