package httpapi

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
)

// Checkout: taking payment at the end of a visit, or a quick sale at the desk.
// Every sale writes its lines to the ledger, which is what the Money page reads.

var payMethods = map[string]bool{"card": true, "tap": true, "cash": true, "transfer": true, "wallet": true}

const settleAfter = "2 days" // card money reaches the payout balance after this

// feeFor returns LogaLuxe's fee on a non-cash payment, from the fee schedule in force.
func feeFor(ctx context.Context, q interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, market, plan string, amount int) int {
	if amount <= 0 {
		return 0
	}
	var pct float64
	var fixed int
	var cap *int
	err := q.QueryRow(ctx, `select transaction_pct::float8, transaction_fixed_cents, transaction_cap_cents from fees
		where market=$1 and plan=$2 and status='approved' and effective_from <= current_date order by effective_from desc limit 1`, market, plan).Scan(&pct, &fixed, &cap)
	if err != nil {
		return 0
	}
	fee := int(float64(amount)*pct/100+0.5) + fixed
	if cap != nil && fee > *cap {
		fee = *cap
	}
	if fee > amount {
		fee = amount
	}
	return fee
}

// GET /v1/m/checkout   who is ready to pay, and what can be sold
func (s *Server) mCheckout(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	m := mc(r)
	now := time.Now().In(m.Loc)
	day := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, m.Loc)
	queue, err := rows(ctx, s.pool, `select `+bookingCols+` from bookings bk join staff st on st.id = bk.staff_id
		where bk.business_id=$1 and bk.paid_at is null
		  and ((bk.status in ('checked_in','in_progress','completed') and bk.starts_at > now() - interval '3 days')
		    or (bk.status = 'confirmed' and bk.starts_at >= $2 and bk.starts_at < $3))
		order by (bk.status = 'completed') desc, (bk.status = 'in_progress') desc, (bk.status = 'checked_in') desc, bk.starts_at`, m.BusinessID, day, day.Add(24*time.Hour))
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	services, _ := rows(ctx, s.pool, `select id, name, category, price_cents, duration_min from services where business_id=$1 and not archived order by sort, name`, m.BusinessID)
	products, _ := rows(ctx, s.pool, `select id, name, price_cents, stock, sku,
		(select coalesce(json_agg(json_build_object('location_id', ls.location_id, 'qty', ls.qty)), '[]') from location_stock ls where ls.product_id = products.id) as by_location
		from products where business_id=$1 and kind in ('retail','both') and stock > 0 order by name`, m.BusinessID)
	staff, _ := rows(ctx, s.pool, `select id, name, initials, tone from staff where business_id=$1 and not archived order by role='owner' desc, name`, m.BusinessID)
	today, _ := rows(ctx, s.pool, `select sa.id, sa.client_name, sa.total_cents, sa.tip_cents, sa.method, sa.status, sa.refunded_cents, sa.created_at, st.name as staff,
		(select string_agg(si.name, ', ') from sale_items si where si.sale_id = sa.id) as items
		from sales sa left join staff st on st.id = sa.staff_id where sa.business_id=$1 and sa.created_at >= $2 order by sa.created_at desc limit 60`, m.BusinessID, day)
	var taxBP int
	_ = s.pool.QueryRow(ctx, `select sales_tax_bp from businesses where id=$1`, m.BusinessID).Scan(&taxBP)
	packages, _ := rows(ctx, s.pool, `select p.id, p.name, p.description, p.price_cents, p.valid_days,
		(select coalesce(json_agg(json_build_object('service_id', pi.service_id, 'name', sv.name, 'qty', pi.qty) order by sv.name), '[]') from package_items pi join services sv on sv.id = pi.service_id where pi.package_id = p.id) as items
		from packages p where p.business_id=$1 and p.active order by p.name`, m.BusinessID)
	memberships, _ := rows(ctx, s.pool, `select ms.id, ms.name, ms.description, ms.price_cents, ms.service_discount_pct, ms.retail_discount_pct,
		(select coalesce(json_agg(json_build_object('service_id', mi.service_id, 'name', sv.name, 'qty', mi.qty) order by sv.name), '[]') from membership_items mi join services sv on sv.id = mi.service_id where mi.membership_id = ms.id) as items
		from memberships ms where ms.business_id=$1 and ms.active order by ms.name`, m.BusinessID)
	locations, _ := rows(ctx, s.pool, `select id, name, is_primary from locations where business_id=$1 order by is_primary desc, name`, m.BusinessID)
	writeJSON(w, 200, M{"queue": queue, "services": services, "products": products, "staff": staff, "sales": today, "tax_bp": taxBP, "payments_mode": s.payMode(m.Market),
		"packages": packages, "memberships": memberships, "can_take_payments": perm(m, "take_payments"), "locations": locations, "loyalty": loyaltyFor(ctx, s.pool, m.BusinessID)})
}

type saleLine struct {
	Kind      string `json:"kind"`
	ServiceID string `json:"service_id"`
	ProductID string `json:"product_id"`
	StaffID   string `json:"staff_id"`
	Name      string `json:"name"`
	Qty       int    `json:"qty"`
	UnitCents *int   `json:"unit_cents"`
	// kind "package" or "membership": what is being sold to the client.
	PackageID    string `json:"package_id"`
	MembershipID string `json:"membership_id"`
	// A service line paid for with a credit from the client's package or membership.
	Redeem bool `json:"redeem"`
}

// POST /v1/m/checkout   take payment for a booking, or make a quick sale
func (s *Server) mCheckoutPay(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	m := mc(r)
	if !perm(m, "take_payments") {
		writeErr(w, 403, "your sign-in cannot take payments; ask the owner")
		return
	}
	var req struct {
		BookingID     string     `json:"booking_id"`
		ClientID      string     `json:"client_id"`
		ClientName    string     `json:"client_name"`
		StaffID       string     `json:"staff_id"`
		Items         []saleLine `json:"items"`
		TipCents      int        `json:"tip_cents"`
		DiscountCents int        `json:"discount_cents"`
		Method        string     `json:"method"`
		Note          string     `json:"note"`
		PromoCode     string     `json:"promo_code"`    // one of the business's own codes, or a LogaLuxe one
		RedeemPoints  int        `json:"redeem_points"` // loyalty points the client is spending
		LocationID    string     `json:"location_id"`   // where the sale happens; the booking's location, or the main one, when left out
	}
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, "invalid json")
		return
	}
	// "link" is only ever set by the pay-link flow itself: to price a sale, and to record it once the client has paid.
	dryRun, linkPaid := ctx.Value(dryRunKey{}) != nil, ctx.Value(linkPaidKey{}) != nil
	if !payMethods[req.Method] && !(req.Method == "link" && (dryRun || linkPaid)) {
		writeErr(w, 400, "choose how the client is paying")
		return
	}
	if req.TipCents < 0 || req.DiscountCents < 0 || len(req.Items) > 40 {
		writeErr(w, 400, "the tip and discount cannot be negative")
		return
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	defer tx.Rollback(ctx)

	var bookingID, clientID, staffID *string
	clientName := strings.TrimSpace(req.ClientName)
	deposit := 0
	if req.BookingID != "" {
		var status, st, name string
		var cid *string
		var dep int
		var depPaid bool
		var paidAt *time.Time
		if err := tx.QueryRow(ctx, `select status, staff_id::text, client_id::text, client_name, deposit_cents, deposit_paid, paid_at from bookings where id=$1 and business_id=$2 for update`, req.BookingID, m.BusinessID).
			Scan(&status, &st, &cid, &name, &dep, &depPaid, &paidAt); err != nil {
			writeErr(w, 404, "booking not found")
			return
		}
		if paidAt != nil || status == "paid" {
			writeErr(w, 409, "this booking has already been paid")
			return
		}
		if status == "cancelled_client" || status == "cancelled_business" || status == "no_show" || status == "rescheduled" {
			writeErr(w, 409, "this booking was cancelled, so it cannot be checked out")
			return
		}
		bookingID, clientID, staffID, clientName = &req.BookingID, cid, &st, name
		if depPaid {
			deposit = dep
		}
		if len(req.Items) == 0 { // nothing changed at the desk: charge what was booked
			_ = tx.QueryRow(ctx, `select discount_cents from bookings where id=$1`, req.BookingID).Scan(&req.DiscountCents) // a promo code used when booking still counts
			its, _ := tx.Query(ctx, `select service_id::text, name, price_cents from booking_items where booking_id=$1`, req.BookingID)
			for its.Next() {
				var sid *string
				var n string
				var p int
				if its.Scan(&sid, &n, &p) == nil {
					l := saleLine{Kind: "service", Name: n, Qty: 1, UnitCents: &p, StaffID: st}
					if sid != nil {
						l.ServiceID = *sid
					}
					req.Items = append(req.Items, l)
				}
			}
			its.Close()
		}
	} else {
		if req.ClientID != "" {
			var id, name string
			if err := tx.QueryRow(ctx, `select id::text, name from clients where id=$1 and business_id=$2`, req.ClientID, m.BusinessID).Scan(&id, &name); err != nil {
				writeErr(w, 400, "that client is not in your list")
				return
			}
			clientID, clientName = &id, name
		}
		if req.StaffID != "" {
			staffID = &req.StaffID
		}
	}
	if clientName == "" {
		clientName = "Walk-in"
	}
	if staffID != nil {
		var ok bool
		_ = tx.QueryRow(ctx, `select exists(select 1 from staff where id=$1 and business_id=$2)`, *staffID, m.BusinessID).Scan(&ok)
		if !ok {
			writeErr(w, 400, "that person is not on your team")
			return
		}
	}
	if len(req.Items) == 0 {
		writeErr(w, 400, "add at least one service or product")
		return
	}

	// Stock comes off the shelves of the place where the sale happens.
	bookedAt := ""
	if bookingID != nil {
		_ = tx.QueryRow(ctx, `select coalesce(location_id::text,'') from bookings where id=$1`, *bookingID).Scan(&bookedAt)
	}
	saleLoc := locationFor(ctx, tx, m.BusinessID, firstNonEmpty(req.LocationID, bookedAt))
	atLocation(ctx, tx, saleLoc)
	var places int
	_ = tx.QueryRow(ctx, `select count(*) from locations where business_id=$1`, m.BusinessID).Scan(&places)

	// Price every line from the menu unless the desk set a price on purpose.
	type line struct {
		saleLine
		unit      int
		productID *string
		serviceID *string
		staffID   *string
		creditID  *string // the package or membership credit that paid for this line
		list      *int    // what the line would have cost without the credit
	}
	prices := s.loadPricing(ctx, m.BusinessID)
	now := time.Now().In(m.Loc)
	needClient := func() bool {
		if clientID == nil {
			writeErr(w, 400, "choose the client first: packages and memberships belong to a client")
			return false
		}
		return true
	}
	var lines []line
	subtotal, productTotal := 0, 0
	for _, it := range req.Items {
		if it.Qty <= 0 {
			it.Qty = 1
		}
		l := line{saleLine: it}
		if it.StaffID != "" {
			sid := it.StaffID
			l.staffID = &sid
		} else {
			l.staffID = staffID
		}
		switch it.Kind {
		case "service":
			var name string
			var price int
			if it.ServiceID != "" {
				if err := tx.QueryRow(ctx, `select name, price_cents from services where id=$1 and business_id=$2`, it.ServiceID, m.BusinessID).Scan(&name, &price); err != nil {
					writeErr(w, 400, "one of those services is not on your menu")
					return
				}
				sid := it.ServiceID
				l.serviceID = &sid
				l.Name = name
				// A booked visit costs what was agreed when it was booked. Anything added at the desk is priced for now.
				who := ""
				if l.staffID != nil {
					who = *l.staffID
				}
				booked := false
				if bookingID != nil {
					var bp int
					if tx.QueryRow(ctx, `select price_cents from booking_items where booking_id=$1 and service_id=$2 limit 1`, *bookingID, it.ServiceID).Scan(&bp) == nil {
						price, booked = bp, true
					}
				}
				if !booked {
					price, _ = prices.price(it.ServiceID, who, now)
				}
				if it.Redeem {
					if !needClient() {
						return
					}
					var cid string
					if err := tx.QueryRow(ctx, `update client_credits set used = used + 1 where id = (
						select cc.id from client_credits cc join client_plans cp on cp.id = cc.plan_id
						where cp.client_id=$1 and cp.business_id=$2 and cp.status='active' and cc.service_id=$3 and cc.used < cc.total and (cc.expires_at is null or cc.expires_at > now())
						order by cc.expires_at nulls last limit 1 for update of cc) returning id::text`, *clientID, m.BusinessID, it.ServiceID).Scan(&cid); err != nil {
						writeErr(w, 409, "this client has no credit left for "+name)
						return
					}
					value := price
					l.creditID, l.list = &cid, &value
					price, it.UnitCents = 0, nil
					l.Qty = 1
				}
			}
			l.unit = price
		case "product":
			var name string
			var price, stock int
			if err := tx.QueryRow(ctx, `select name, price_cents, stock from products where id=$1 and business_id=$2 for update`, it.ProductID, m.BusinessID).Scan(&name, &price, &stock); err != nil {
				writeErr(w, 400, "one of those products is not in your inventory")
				return
			}
			if places > 1 { // with more than one location, what counts is what is on this location's shelf
				stock = 0
				_ = tx.QueryRow(ctx, `select qty from location_stock where product_id=$1 and location_id=$2`, it.ProductID, saleLoc).Scan(&stock)
			}
			if stock < it.Qty {
				writeErr(w, 409, "only "+itoa(stock)+" of "+name+" left in stock")
				return
			}
			pid := it.ProductID
			l.productID, l.Name, l.unit = &pid, name, price
		case "package":
			if !needClient() {
				return
			}
			var name string
			var price int
			if err := tx.QueryRow(ctx, `select name, price_cents from packages where id=$1 and business_id=$2 and active`, it.PackageID, m.BusinessID).Scan(&name, &price); err != nil {
				writeErr(w, 400, "that package is not on sale")
				return
			}
			l.Name, l.unit, l.Qty, it.UnitCents = name, price, 1, nil
		case "membership":
			if !needClient() {
				return
			}
			var name string
			var price int
			if err := tx.QueryRow(ctx, `select name, price_cents from memberships where id=$1 and business_id=$2 and active`, it.MembershipID, m.BusinessID).Scan(&name, &price); err != nil {
				writeErr(w, 400, "that membership is not on sale")
				return
			}
			var has bool
			_ = tx.QueryRow(ctx, `select exists(select 1 from client_plans where client_id=$1 and membership_id=$2 and status='active')`, *clientID, it.MembershipID).Scan(&has)
			if has {
				writeErr(w, 409, "this client is already a member of "+name)
				return
			}
			l.Name, l.unit, l.Qty, it.UnitCents = name+" · first month", price, 1, nil
		case "custom":
		default:
			writeErr(w, 400, "each line must be a service, a product, a package, a membership or a custom amount")
			return
		}
		if it.UnitCents != nil {
			l.unit = *it.UnitCents
		}
		l.Name = strings.TrimSpace(l.Name)
		if l.Name == "" || l.unit < 0 || len(l.Name) > 120 {
			writeErr(w, 400, "each line needs a name and a price that is not negative")
			return
		}
		subtotal += l.unit * l.Qty
		if l.Kind == "product" {
			productTotal += l.unit * l.Qty
		}
		lines = append(lines, l)
	}
	// A promo code, checked the same way as on the booking page.
	promoID, promoOff := "", 0
	if strings.TrimSpace(req.PromoCode) != "" {
		id, off, why := promoDiscount(ctx, tx, req.PromoCode, "bookings", m.Currency, subtotal, true, m.BusinessID)
		if why != "" {
			writeErr(w, 400, why)
			return
		}
		promoID, promoOff = id, off
		req.DiscountCents += off
	}
	// A member's discount comes off by itself: services and retail at the rates of their membership.
	memberDiscount := 0
	if clientID != nil {
		var sp, rp int
		if tx.QueryRow(ctx, `select coalesce(max(ms.service_discount_pct),0), coalesce(max(ms.retail_discount_pct),0) from client_plans cp join memberships ms on ms.id = cp.membership_id
			where cp.client_id=$1 and cp.kind='membership' and cp.status='active'`, *clientID).Scan(&sp, &rp) == nil {
			for _, l := range lines {
				switch {
				case l.Kind == "service" && l.creditID == nil:
					memberDiscount += l.unit * l.Qty * sp / 100
				case l.Kind == "product":
					memberDiscount += l.unit * l.Qty * rp / 100
				}
			}
		}
	}
	req.DiscountCents += memberDiscount
	// Loyalty points the client chose to spend.
	loyalty := loyaltyFor(ctx, tx, m.BusinessID)
	pointsUsed, pointsOff := 0, 0
	if req.RedeemPoints > 0 {
		if !needClient() {
			return
		}
		have := pointsOf(ctx, tx, m.BusinessID, *clientID)
		switch {
		case !loyalty.Enabled:
			writeErr(w, 400, "loyalty points are switched off for this business")
			return
		case req.RedeemPoints < loyalty.MinRedeem:
			writeErr(w, 400, "the fewest points that can be spent at once is "+itoa(loyalty.MinRedeem))
			return
		case have < req.RedeemPoints:
			writeErr(w, 409, "this client has "+itoa(have)+" points")
			return
		}
		pointsUsed = req.RedeemPoints
		if room := subtotal - req.DiscountCents; pointsUsed*loyalty.PointValue > room { // never take more points than the bill can use
			pointsUsed = room / loyalty.PointValue
		}
		if pointsUsed < 0 {
			pointsUsed = 0
		}
		pointsOff = pointsUsed * loyalty.PointValue
		req.DiscountCents += pointsOff
	}
	if req.DiscountCents > subtotal {
		req.DiscountCents = subtotal
	}
	// Sales tax applies to retail products only, after its share of any discount.
	var taxBP int
	_ = tx.QueryRow(ctx, `select sales_tax_bp from businesses where id=$1`, m.BusinessID).Scan(&taxBP)
	tax := 0
	if subtotal > 0 && productTotal > 0 && taxBP > 0 {
		taxable := productTotal - req.DiscountCents*productTotal/subtotal
		tax = (taxable*taxBP + 5000) / 10000
	}
	if deposit > subtotal-req.DiscountCents+tax {
		deposit = subtotal - req.DiscountCents + tax
	}
	due := subtotal - req.DiscountCents + tax + req.TipCents - deposit

	var saleID string
	if err := tx.QueryRow(ctx, `insert into sales (business_id, booking_id, client_id, client_name, staff_id, subtotal_cents, discount_cents, tax_cents, tip_cents, deposit_cents, total_cents, method, note, created_by)
		values ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14) returning id::text`,
		m.BusinessID, bookingID, clientID, clientName, staffID, subtotal, req.DiscountCents, tax, req.TipCents, deposit, due, req.Method, strings.TrimSpace(req.Note), m.Email).Scan(&saleID); err != nil {
		if isCode(err, "23505") {
			writeErr(w, 409, "this booking has already been paid")
			return
		}
		writeErr(w, 500, err.Error())
		return
	}
	for _, l := range lines {
		if _, err := tx.Exec(ctx, `insert into sale_items (sale_id, kind, service_id, product_id, staff_id, name, qty, unit_cents, credit_id, list_cents) values ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`,
			saleID, l.Kind, l.serviceID, l.productID, l.staffID, l.Name, l.Qty, l.unit, l.creditID, l.list); err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		if l.serviceID != nil {
			useBackbar(ctx, tx, m.BusinessID, *l.serviceID, l.Qty, clientName, m.Email)
		}
		if l.Kind == "package" {
			var planID string
			if err := tx.QueryRow(ctx, `insert into client_plans (business_id, client_id, kind, package_id, name, price_cents, sale_id, expires_at)
				select $1, $2, 'package', p.id, p.name, p.price_cents, $4, now() + make_interval(days => p.valid_days) from packages p where p.id=$3 returning id::text`, m.BusinessID, *clientID, l.PackageID, saleID).Scan(&planID); err != nil {
				writeErr(w, 500, err.Error())
				return
			}
			_, _ = tx.Exec(ctx, `insert into client_credits (plan_id, service_id, service_name, total, expires_at)
				select $1, sv.id, sv.name, pi.qty, (select expires_at from client_plans where id=$1) from package_items pi join services sv on sv.id = pi.service_id where pi.package_id=$2`, planID, l.PackageID)
		}
		if l.Kind == "membership" {
			var planID string
			if err := tx.QueryRow(ctx, `insert into client_plans (business_id, client_id, kind, membership_id, name, price_cents, sale_id, renews_on)
				select $1, $2, 'membership', ms.id, ms.name, ms.price_cents, $4, (current_date + interval '1 month')::date from memberships ms where ms.id=$3 returning id::text`, m.BusinessID, *clientID, l.MembershipID, saleID).Scan(&planID); err != nil {
				writeErr(w, 500, err.Error())
				return
			}
			_, _ = tx.Exec(ctx, `insert into client_credits (plan_id, service_id, service_name, total, expires_at)
				select $1, sv.id, sv.name, mi.qty, (select renews_on from client_plans where id=$1)::timestamptz from membership_items mi join services sv on sv.id = mi.service_id where mi.membership_id=$2`, planID, l.MembershipID)
		}
		if l.productID != nil {
			if _, err := tx.Exec(ctx, `update products set stock = stock - $2, sold = sold + $2 where id=$1`, *l.productID, l.Qty); err != nil {
				writeErr(w, 500, err.Error())
				return
			}
			_, _ = tx.Exec(ctx, `insert into stock_movements (business_id, product_id, delta, reason, note, actor) values ($1,$2,$3,'sale',$4,$5)`, m.BusinessID, *l.productID, -l.Qty, "Sold to "+clientName, m.Email)
			_, _ = tx.Exec(ctx, `update stock_movements set location_id = nullif($2,'')::uuid where id = (select id from stock_movements where product_id=$1 and reason='sale' order by created_at desc, id desc limit 1) and location_id is null`, *l.productID, saleLoc)
		}
	}

	_, _ = tx.Exec(ctx, `update sales set location_id = nullif($2,'')::uuid, promo_code = $3 where id=$1`, saleID, saleLoc, strings.ToUpper(strings.TrimSpace(req.PromoCode)))
	if promoID != "" {
		_, _ = tx.Exec(ctx, `update promo_codes set used = used + 1 where id=$1`, promoID)
	}
	pointsEarned := 0
	if clientID != nil && loyalty.Enabled {
		if pointsUsed > 0 {
			_, _ = tx.Exec(ctx, `insert into loyalty_points (business_id, client_id, points, reason, sale_id, actor) values ($1,$2,$3,'redeem',$4,$5)`, m.BusinessID, *clientID, -pointsUsed, saleID, m.Email)
		}
		// Points are earned on what the client actually paid for goods and services, not on tips or tax.
		if paid := subtotal - req.DiscountCents; paid > 0 && loyalty.PerCents > 0 {
			if pointsEarned = paid / loyalty.PerCents * loyalty.EarnPoints; pointsEarned > 0 {
				_, _ = tx.Exec(ctx, `insert into loyalty_points (business_id, client_id, points, reason, sale_id, actor) values ($1,$2,$3,'earn',$4,$5)`, m.BusinessID, *clientID, pointsEarned, saleID, m.Email)
			}
		}
	}
	if clientID != nil {
		// A package with nothing left on it is finished.
		_, _ = tx.Exec(ctx, `update client_plans cp set status='used' where cp.client_id=$1 and cp.kind='package' and cp.status='active' and not exists (select 1 from client_credits cc where cc.plan_id = cp.id and cc.used < cc.total)`, *clientID)
	}

	// The ledger. Cash is recorded but never passes through LogaLuxe, so it stays out of the payout balance.
	// Money LogaLuxe never held stays out of the payout balance: cash always, and, once real payments are on,
	// anything taken on the business's own card machine or by transfer. Only a paid link went through LogaLuxe.
	cash := req.Method == "cash" || (s.payMode(m.Market) == "live" && req.Method != "link")
	status, inBalance := "pending", true
	if cash {
		status, inBalance = "settled", false
	}
	add := func(kind string, amount int, desc string) error {
		if amount == 0 {
			return nil
		}
		_, err := tx.Exec(ctx, `insert into ledger (business_id, kind, amount_cents, currency, method, status, in_balance, sale_id, booking_id, staff_id, description, settles_at)
			values ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11, case when $6 = 'pending' then now() + interval '`+settleAfter+`' end)`,
			m.BusinessID, kind, amount, m.Currency, req.Method, status, inBalance, saleID, bookingID, staffID, desc)
		return err
	}
	names := make([]string, 0, len(lines))
	for _, l := range lines {
		names = append(names, l.Name)
	}
	what := clientName + " · " + strings.Join(names, ", ")
	if err := add("charge", due-req.TipCents, what); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if err := add("tip", req.TipCents, "Tip · "+clientName); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if !cash {
		if fee := feeFor(ctx, tx, m.Market, m.Plan, due); fee > 0 {
			if err := add("fee", -fee, "LogaLuxe fee · "+clientName); err != nil {
				writeErr(w, 500, err.Error())
				return
			}
		}
	}
	leadFee := 0
	if bookingID != nil {
		// If LogaLuxe brought this client, the first visit carries the new-client fee: a share of the services paid for.
		services := 0
		for _, l := range lines {
			if l.Kind == "service" && l.creditID == nil {
				services += l.unit * l.Qty
			}
		}
		if subtotal > 0 {
			services -= req.DiscountCents * services / subtotal
		}
		leadFee = settleLead(ctx, tx, m.BusinessID, m.Currency, *bookingID, saleID, clientName, services, map[bool]string{true: "settled", false: "pending"}[cash])
		// The visit happened, so a held deposit is now earned.
		if _, err := tx.Exec(ctx, `update ledger set status='pending', settles_at = now() + interval '`+settleAfter+`' where booking_id=$1 and kind='deposit' and status='held'`, *bookingID); err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		if _, err := tx.Exec(ctx, `update bookings set status='paid', paid_at=now(), completed_at=coalesce(completed_at, now()), checked_in_at=coalesce(checked_in_at, now()), tip_cents=$2, total_cents=$3, discount_cents=$4 where id=$1`,
			*bookingID, req.TipCents, subtotal-req.DiscountCents, req.DiscountCents); err != nil {
			writeErr(w, 500, err.Error())
			return
		}
	}
	if dryRun {
		// Only pricing and checking: nothing is kept.
		writeJSON(w, 200, M{"ok": true, "subtotal_cents": subtotal, "discount_cents": req.DiscountCents, "tax_cents": tax, "tip_cents": req.TipCents, "deposit_cents": deposit, "total_cents": due, "member_discount_cents": memberDiscount,
			"promo_discount_cents": promoOff, "points_used": pointsUsed, "points_discount_cents": pointsOff, "points_earned": pointsEarned})
		return
	}
	if err := tx.Commit(ctx); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	s.lowStockNotice(m.BusinessID)
	writeJSON(w, 201, M{"ok": true, "sale_id": saleID, "subtotal_cents": subtotal, "discount_cents": req.DiscountCents, "tax_cents": tax, "tip_cents": req.TipCents, "deposit_cents": deposit, "total_cents": due, "member_discount_cents": memberDiscount, "lead_fee_cents": leadFee,
		"promo_discount_cents": promoOff, "points_used": pointsUsed, "points_discount_cents": pointsOff, "points_earned": pointsEarned})
}

// POST /v1/m/sales/{id}/refund   {amount_cents, reason, restock}
func (s *Server) mSaleRefund(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	m := mc(r)
	id := chi.URLParam(r, "id")
	var req struct {
		AmountCents int    `json:"amount_cents"`
		Reason      string `json:"reason"`
		Restock     bool   `json:"restock"`
	}
	if err := readJSON(r, &req); err != nil || strings.TrimSpace(req.Reason) == "" {
		writeErr(w, 400, "give a reason for the refund")
		return
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	defer tx.Rollback(ctx)
	var total, refunded int
	var method, client string
	var bookingID, staffID *string
	if err := tx.QueryRow(ctx, `select total_cents, refunded_cents, method, client_name, booking_id::text, staff_id::text from sales where id=$1 and business_id=$2 for update`, id, m.BusinessID).
		Scan(&total, &refunded, &method, &client, &bookingID, &staffID); err != nil {
		writeErr(w, 404, "sale not found")
		return
	}
	left := total - refunded
	if req.AmountCents <= 0 {
		req.AmountCents = left
	}
	if left <= 0 || req.AmountCents > left {
		writeErr(w, 400, "you can refund up to "+formatMoney(left, m.Currency)+" on this sale")
		return
	}
	var inBalance bool
	_ = tx.QueryRow(ctx, `select exists(select 1 from ledger where sale_id=$1 and kind='charge' and in_balance)`, id).Scan(&inBalance)
	newStatus := "part_refunded"
	if req.AmountCents == left {
		newStatus = "refunded"
	}
	if _, err := tx.Exec(ctx, `update sales set refunded_cents = refunded_cents + $2, status=$3 where id=$1`, id, req.AmountCents, newStatus); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if _, err := tx.Exec(ctx, `insert into ledger (business_id, kind, amount_cents, currency, method, status, in_balance, sale_id, booking_id, staff_id, description)
		values ($1,'refund',$2,$3,$4,'settled',$5,$6,$7,$8,$9)`, m.BusinessID, -req.AmountCents, m.Currency, method, inBalance, id, bookingID, staffID, "Refund · "+client+" · "+strings.TrimSpace(req.Reason)); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if newStatus == "refunded" {
		// A sale that is fully refunded gives back the points spent on it and takes back the points it earned.
		_, _ = tx.Exec(ctx, `insert into loyalty_points (business_id, client_id, points, reason, sale_id, note, actor)
			select business_id, client_id, -sum(points), 'adjust', sale_id, 'Sale refunded', $2 from loyalty_points where sale_id=$1 group by business_id, client_id, sale_id having sum(points) <> 0`, id, m.Email)
	}
	if req.Restock {
		if _, err := tx.Exec(ctx, `with back as (
			update products p set stock = p.stock + si.qty, sold = greatest(p.sold - si.qty, 0) from sale_items si where si.sale_id=$1 and si.product_id = p.id returning p.id, si.qty)
			insert into stock_movements (business_id, product_id, delta, reason, note, actor) select $2, id, qty, 'return', $3, $4 from back`, id, m.BusinessID, "Refund · "+client, m.Email); err != nil {
			writeErr(w, 500, err.Error())
			return
		}
	}
	if err := tx.Commit(ctx); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	sent := ""
	if method == "link" {
		// The client paid online, so the money goes back the same way.
		var payID string
		if s.pool.QueryRow(ctx, `select id::text from payments where sale_id=$1 and status in ('paid','refunded') limit 1`, id).Scan(&payID) == nil {
			if err := s.refundPayment(ctx, payID, req.AmountCents); err != nil {
				sent = "recorded, but the provider did not send the money back: " + err.Error()
				_, _ = s.pool.Exec(ctx, `update payments set problem=$2 where id=$1`, payID, "A refund was recorded but not sent: "+err.Error())
			} else {
				sent = "sent back to the client's card or bank"
			}
		}
	}
	writeJSON(w, 200, M{"ok": true, "status": newStatus, "provider_refund": sent})
}

// GET /v1/m/checkout/day?date=   the end-of-day count
func (s *Server) mCheckoutDay(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	m := mc(r)
	day := parseDay(r.URL.Query().Get("date"), m.Loc)
	end := day.Add(24 * time.Hour)
	totals, _ := row(ctx, s.pool, `select count(*) as sales, coalesce(sum(total_cents),0) as taken_cents, coalesce(sum(tip_cents),0) as tips_cents,
		coalesce(sum(tax_cents),0) as tax_cents, coalesce(sum(discount_cents),0) as discounts_cents, coalesce(sum(refunded_cents),0) as refunded_cents
		from sales where business_id=$1 and created_at >= $2 and created_at < $3`, m.BusinessID, day, end)
	byMethod, _ := rows(ctx, s.pool, `select method, count(*) as n, coalesce(sum(total_cents - refunded_cents),0) as cents from sales where business_id=$1 and created_at >= $2 and created_at < $3 group by method order by cents desc`, m.BusinessID, day, end)
	byStaff, _ := rows(ctx, s.pool, `select coalesce(st.name, 'No one') as staff, count(distinct sa.id) as sales, coalesce(sum(sa.tip_cents),0) as tips_cents, coalesce(sum(sa.subtotal_cents - sa.discount_cents),0) as revenue_cents
		from sales sa left join staff st on st.id = sa.staff_id where sa.business_id=$1 and sa.created_at >= $2 and sa.created_at < $3 group by st.name order by revenue_cents desc`, m.BusinessID, day, end)
	unpaid, _ := row(ctx, s.pool, `select count(*) as n from bookings where business_id=$1 and starts_at >= $2 and starts_at < $3 and paid_at is null and status in ('checked_in','in_progress','completed')`, m.BusinessID, day, end)
	writeJSON(w, 200, M{"date": day.Format("2006-01-02"), "totals": totals, "by_method": byMethod, "by_staff": byStaff, "unpaid": unpaid})
}
