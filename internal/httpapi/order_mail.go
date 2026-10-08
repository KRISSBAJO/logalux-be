package httpapi

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// Emails about a shop order: to the customer when it is placed and at each step,
// and to each business that has something to hand over or send.

func (s *Server) mailOrder(orderID, subject string, body func(o M, lines string) string) {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		o, err := row(ctx, s.pool, `select id::text as id, customer_name, customer_email, total_cents, credit_cents, gift_cents from orders where id=$1`, orderID)
		if err != nil {
			return
		}
		to := strings.TrimSpace(fmt.Sprint(o["customer_email"]))
		if to == "" || strings.HasSuffix(to, ".test") {
			return
		}
		items, _ := rows(ctx, s.pool, `select seller_name, name, size_label, qty, unit_cents from order_items where order_id=$1 order by seller_name, name`, orderID)
		var sb strings.Builder
		for _, it := range items {
			size := ""
			if fmt.Sprint(it["size_label"]) != "" {
				size = " (" + fmt.Sprint(it["size_label"]) + ")"
			}
			sb.WriteString(fmt.Sprintf("  %v x %v%s, from %v: %s\n", it["qty"], it["name"], size, it["seller_name"], formatMoney(int(toInt(it["unit_cents"])*toInt(it["qty"])), "USD")))
		}
		if _, err := s.mail.Send(ctx, to, subject, body(o, sb.String())); err != nil {
			s.logMailFailure("order email", to, err)
		}
	}()
}

func orderRef(o M) string { return strings.ToUpper(fmt.Sprint(o["id"]))[:8] }

// orderPlaced is sent once an order is paid for: what was bought, how each part reaches the customer, and where to follow it.
func (s *Server) orderPlaced(orderID string) {
	account := strings.TrimRight(s.cfg.WebURL, "/") + "/account?tab=orders"
	s.mailOrder(orderID, "Your LogaLuxe order", func(o M, lines string) string {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		parts, _ := rows(ctx, s.pool, `select seller_name, fulfilment from order_shipments where order_id=$1 order by seller_name`, orderID)
		var how strings.Builder
		for _, p := range parts {
			if p["fulfilment"] == "pickup" {
				how.WriteString("  " + fmt.Sprint(p["seller_name"]) + ": you collect it. We email you when it is ready.\n")
			} else {
				how.WriteString("  " + fmt.Sprint(p["seller_name"]) + ": shipped to you. We email you when it is on its way.\n")
			}
		}
		paid := "You paid " + formatMoney(int(toInt(o["total_cents"])), "USD") + "."
		if toInt(o["total_cents"]) == 0 {
			paid = "Nothing was charged."
		}
		if c := int(toInt(o["credit_cents"])); c > 0 {
			paid += " " + formatMoney(c, "USD") + " came from your store credit."
		}
		return "Hello " + firstNonEmpty(strings.Fields(fmt.Sprint(o["customer_name"]) + " there")[0], "there") + ",\n\nThank you. Your order " + orderRef(o) + " is placed. " + paid + "\n\n" + lines + "\n" + how.String() +
			"\nFollow it here: " + account + "\n\nLogaLuxe"
	})
	// Each business with a part in it hears about it too.
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		parts, _ := rows(ctx, s.pool, `select sh.business_id::text as business_id, sh.fulfilment, sh.note, o.customer_name from order_shipments sh join orders o on o.id = sh.order_id where sh.order_id=$1 and sh.business_id is not null`, orderID)
		for _, p := range parts {
			what := "to send"
			if p["fulfilment"] == "pickup" {
				what = "to get ready for collection"
			}
			note := ""
			if n := fmt.Sprint(p["note"]); n != "" {
				note = "\n\n" + n
			}
			s.notifyBusiness(fmt.Sprint(p["business_id"]), "order_email", "A new shop order", fmt.Sprint(p["customer_name"])+" ordered from your shop on LogaLuxe. You have items "+what+"."+note+
				"\n\nOpen your online orders: "+strings.TrimRight(s.cfg.WebURL, "/")+"/business/inventory?tab=orders")
		}
	}()
}

// orderStep tells the customer that one seller's part of their order moved on.
func (s *Server) orderStep(orderID, seller, step, tracking string) {
	var subject, line string
	switch step {
	case "ready":
		subject, line = "Your order is ready to collect", "Your items from "+seller+" are ready to collect."
	case "shipped":
		subject, line = "Your order is on its way", "Your items from "+seller+" have been sent."
		if strings.TrimSpace(tracking) != "" {
			line += " Tracking: " + strings.TrimSpace(tracking) + "."
		}
	default:
		return
	}
	account := strings.TrimRight(s.cfg.WebURL, "/") + "/account?tab=orders"
	s.mailOrder(orderID, subject, func(o M, _ string) string {
		return "Hello " + firstNonEmpty(strings.Fields(fmt.Sprint(o["customer_name"]) + " there")[0], "there") + ",\n\n" + line + "\n\nOrder " + orderRef(o) + ": " + account + "\n\nLogaLuxe"
	})
}
