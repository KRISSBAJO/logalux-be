# LogaLuxe API

Go 1.27, chi, pgx, Postgres 16. One binary, one Docker image.

## Run everything in Docker

```bash
cp .env.example .env     # then set POSTGRES_PASSWORD, ADMIN_TOKEN, ADMIN_EMAIL and ADMIN_PASSWORD
docker compose up --build
curl http://127.0.0.1:18080/health
```

Ports on the host: API 18080, Postgres 15433, Redis 16380. All bound to 127.0.0.1.

## Run the API locally against the Docker database

```bash
docker compose up -d db redis
go run ./cmd/api
go test ./...
```

## Routes

Public

- `GET /health`
- `GET /v1/businesses?q=&category=&market=US|NG`
- `GET /v1/businesses/{slug}` business, locations, staff, services, reviews, products
- `GET /v1/businesses/{slug}/availability?date=YYYY-MM-DD&services=id,id&staff=id|any`
- `POST /v1/bookings` `{business_slug, staff_id, starts_at, service_ids[], client_name, client_phone, notes, promo_code}`
- `GET /v1/bookings/{id}`
- `POST /v1/waitlist`
- `GET /v1/products?category=&q=&seller=` · `GET /v1/products/{slug}`
- `POST /v1/orders` `{customer_name, customer_phone, fulfilment: pickup|ship, address, promo_code, gift_code, items:[{product_slug, size_label, qty}]}`
- `GET /v1/orders/{id}`
- `POST /v1/checkout/check` `{scope: orders|bookings, currency, subtotal_cents, promo_code, gift_code}` what a code is worth, for showing in the cart
- Customer accounts: `POST /v1/auth/signup` `{first_name, last_name, email, phone, password}` · `POST /v1/auth/login` · `POST /v1/auth/logout` · `POST /v1/auth/forgot` · `POST /v1/auth/reset`. With the returned token as `Authorization: Bearer`: `GET /v1/auth/me` (add `?brief=1` for the name only) · `PUT /v1/auth/me` · `POST /v1/auth/password` · `POST /v1/auth/bookings/{id}/cancel`. A booking or order sent with a customer token is saved to that account. Customer and console tokens are separate and never interchangeable.
- `POST /v1/support` `{name, email, phone, role, business_slug, subject, message}` the contact form · `GET /v1/pages/{slug}` a published legal or help page
- `POST /v1/admin/forgot` `{email}` · `POST /v1/admin/reset` `{token, password}` password reset by email
- `GET /v1/site/media?slot=hero|category|business|product&ref=` images now showing; leave `ref` out to get the whole slot · `GET /v1/media/{id}` the image itself, streamed from the private S3 bucket

Admin. Sign in with `POST /v1/admin/login` `{email, password}` and send the returned token as `Authorization: Bearer <token>`. Sessions last 12 hours and only a hash of the token is stored. Five wrong passwords lock the account for 15 minutes. `ADMIN_TOKEN` is still accepted as a super admin, for scripts. The first super admin is created from `ADMIN_EMAIL` and `ADMIN_PASSWORD` when the admin table is empty.

Any signed-in admin

- `POST /v1/admin/logout` · `GET /v1/admin/me` · `GET /v1/admin/account`
- `POST /v1/admin/password` `{current, new}` · `POST /v1/admin/2fa/setup` · `POST /v1/admin/2fa/enable` `{code}` · `POST /v1/admin/2fa/disable` `{password}`
- `GET /v1/admin/support?status=&q=&mine=1` · `GET /v1/admin/support/{id}` · `POST /v1/admin/support/{id}/reply` `{body, internal, close}` · `PUT /v1/admin/support/{id}` `{status, priority, assigned_to}`
- `GET /v1/admin/promos` · `GET /v1/admin/gift-cards?q=` (codes are masked below operations) · `GET /v1/admin/broadcasts` · `GET /v1/admin/pages`
- `GET /v1/admin/overview?market=` · `GET /v1/admin/health` · `GET /v1/admin/search?q=`
- `GET /v1/admin/verification?status=` · `GET /v1/admin/moderation?status=flagged` · `GET /v1/admin/disputes?status=`
- `GET /v1/admin/businesses?q=&status=&market=` · `GET /v1/admin/businesses/{id}` · `POST /v1/admin/businesses/{id}/notes` `{body}`
- `GET /v1/admin/bookings?q=&status=&business=&from=&to=` · `POST /v1/admin/bookings/{id}/action` `{action: cancel|no_show|complete|confirm|reschedule, starts_at, reason}`
- `GET /v1/admin/clients?q=&filter=blocked|no_show` · `GET /v1/admin/orders?q=&status=` · `GET /v1/admin/products`
- `GET /v1/admin/payouts?status=` · `GET /v1/admin/fees` · `GET /v1/admin/flags` · `GET /v1/admin/audit?q=&actor=&action=`
- `GET /v1/admin/media` site images, slots and whether storage is set up

Operations and above

- `POST /v1/admin/media` multipart `file`, `slot` (hero, category, business or product), `ref` (the category id or the business or product slug; empty for hero), `alt`. JPEG, PNG or WebP up to 8 MB, checked by content, not by file name.
- `PUT /v1/admin/media/{id}` `{active, alt, sort}` · `DELETE /v1/admin/media/{id}` also removes the file from S3
- `POST /v1/admin/verification/{id}/decide` `{decision: approve|needs_info|reject, note}`
- `POST /v1/admin/moderation/{id}/decide` `{action: publish|hide|remove, reason}`
- `POST /v1/admin/disputes/{id}/resolve` `{outcome: full|partial|credit|decline|out_of_scope, amount_cents, reason}`
- `POST /v1/admin/businesses/{id}/status` `{status: live|paused|suspended, reason}` · `PATCH /v1/admin/businesses/{id}` `{plan: free|pro}`
- `POST /v1/admin/businesses/{id}/payout-hold` `{hold, reason}` · `POST /v1/admin/businesses/{id}/credits` `{amount_cents, reason, client_phone}`
- `POST /v1/admin/clients/block` `{phone, blocked, reason}`
- `POST /v1/admin/orders/{id}/status` `{status, reason}` · `POST /v1/admin/products/{id}/active` `{active, reason}`
- `POST /v1/admin/payouts/{id}/action` `{action: retry|hold|release|mark_paid, reason}`
- `GET /v1/admin/export/{bookings|orders|payouts|clients|businesses|products|gift-cards|audit}` CSV with the list's own filters. `audit` is super admin only.
- `POST /v1/admin/businesses` · `PUT /v1/admin/businesses/{id}/profile` · `PUT /v1/admin/locations/{id}` `{name, address, city, region, arrival_notes, hours}`
- `POST /v1/admin/businesses/{id}/services` · `PUT|DELETE /v1/admin/services/{id}` · `POST /v1/admin/businesses/{id}/staff` · `PUT|DELETE /v1/admin/staff/{id}`
- `POST /v1/admin/products` · `PUT /v1/admin/products/{id}`
- `POST /v1/admin/promos` · `PUT /v1/admin/promos/{id}` `{active}` · `DELETE /v1/admin/promos/{id}` unused codes only
- `POST /v1/admin/gift-cards` `{amount_cents, currency, recipient_name, recipient_email, note, expires_on, send_email}` · `POST /v1/admin/gift-cards/{id}/void` `{reason}`
- `POST /v1/admin/broadcasts` saves a draft · `DELETE /v1/admin/broadcasts/{id}` drafts only

Super admin only

- `POST /v1/admin/team/{id}/reset-2fa` · `POST /v1/admin/broadcasts/{id}/send` · `PUT /v1/admin/pages/{slug}` `{title, body, published}`
- `POST /v1/admin/fees` proposes a change · `POST /v1/admin/fees/decide` `{market, plan, effective_from, decision: approve|reject}`. The approver must be a different person from the proposer.
- `POST /v1/admin/flags` · `PUT /v1/admin/flags/{key}` `{enabled, rollout_pct, market, plan}` · `DELETE /v1/admin/flags/{key}`
- `GET /v1/admin/team` · `POST /v1/admin/team` `{email, name, role, password}` · `PUT /v1/admin/team/{id}` `{role, active, password}`

## Merchant web API

For the people who run a business. Everything is under `/v1/m`. Sign in with `POST /v1/m/login` `{email, password}` and send the token as `Authorization: Bearer`. Sessions last 14 days and only a hash of the token is stored. Merchant, customer and console tokens are separate and never interchangeable. A person can belong to several businesses; `POST /v1/m/switch` `{business_id}` changes which one the session is working in. Every query is scoped to that business.

No session needed: `POST /v1/m/signup` `{name, email, phone, password, business, category, market: US|NG, city, region, address}` creates the business (status `pending` until the console verifies it), its first location, the owner as a bookable team member, default automations and saved replies · `POST /v1/m/login` · `POST /v1/m/logout` · `POST /v1/m/forgot` · `POST /v1/m/reset` `{token, password}`.

Roles are `staff` < `manager` < `owner`.

Everyone on the team

- `GET /v1/m/me` who is signed in, their businesses, and the menu badges · `POST /v1/m/password` · `PUT /v1/m/account` `{name, phone}`
- `GET /v1/m/home` the first screen of the day
- `GET /v1/m/calendar?date=&days=1|7|month` · `GET /v1/m/availability?date=&services=&staff=id|any&exclude=` · `GET /v1/m/bookings?q=` find a booking by client name or phone
- `POST /v1/m/bookings` a booking taken by phone or for a walk-in · `GET /v1/m/bookings/{id}` · `POST /v1/m/bookings/{id}/action` `{action: confirm|check_in|start|complete|no_show|cancel|reschedule, starts_at, staff_id, reason}`
- `POST /v1/m/blocks` `{staff_id, starts_at, ends_at, reason}` · `DELETE /v1/m/blocks/{id}` · `GET /v1/m/waitlist` · `PUT /v1/m/waitlist/{id}` `{status}`
- `GET /v1/m/clients?q=&segment=new|regulars|lapsed|upcoming|no_show|waitlist&sort=&page=` · `GET /v1/m/clients/{id}` · `POST /v1/m/clients` · `PUT /v1/m/clients/{id}`
- `GET /v1/m/checkout` · `POST /v1/m/checkout` `{booking_id | client_name, items:[{kind: service|product|custom, ...}], tip_cents, discount_cents, method: card|tap|cash|transfer|wallet}` · `GET /v1/m/checkout/day?date=`
- `GET /v1/m/inbox?filter=&q=` · `GET /v1/m/inbox/{id}` · `POST /v1/m/inbox/{id}/reply` `{body}` · `PUT /v1/m/inbox/{id}` `{status, assigned_staff_id}` · `POST /v1/m/inbox` `{client_id, channel, body}`
- `GET /v1/m/services` · `GET /v1/m/staff?week=` · `POST /v1/m/time-off` `{staff_id, starts_on, ends_on, reason}` (a team member asks for their own; a manager's is approved as it is made)

Managers and the owner

- `POST /v1/m/clients/import` `{rows}` · `GET /v1/m/clients/export` · `POST /v1/m/sales/{id}/refund` `{amount_cents, reason, restock}` · `PUT /v1/m/saved-replies`
- `POST /v1/m/services` · `PUT /v1/m/services/{id}` (with `staff_prices` for a price per person) · `POST /v1/m/services/{id}/action` `{action: archive|restore|duplicate|delete}` · `PUT /v1/m/services/order`
- `POST /v1/m/staff` · `PUT /v1/m/staff/{id}` · `POST /v1/m/staff/{id}/action` `{action: archive|restore}` · `POST /v1/m/time-off/{id}` `{decision: approve|decline|cancel}` · `GET /v1/m/payroll?from=&to=`
- `GET /v1/m/inventory?q=&filter=` · `POST /v1/m/products` · `PUT /v1/m/products/{id}` · `POST /v1/m/products/{id}/stock` `{delta, reason: restock|adjust|backbar|count, note}` · `GET /v1/m/products/{id}/history` · `POST /v1/m/suppliers` · `DELETE /v1/m/suppliers/{id}` · `POST /v1/m/purchase-orders` (`{suggest: true}` builds one from everything at its reorder level) · `POST /v1/m/purchase-orders/{id}` `{action: order|receive|cancel}`
- `GET /v1/m/marketing` · `PUT /v1/m/automations/{key}` `{enabled, message}` · `POST /v1/m/campaigns` · `DELETE /v1/m/campaigns/{id}` · `POST /v1/m/campaigns/{id}/test` · `POST /v1/m/campaigns/{id}/send`
- `GET /v1/m/reports?range=7d|30d|90d|month` · `GET /v1/m/reports/export`
- `GET /v1/m/storefront` · `PUT /v1/m/storefront` · `POST /v1/m/storefront/photos` (multipart `file`, `alt`) · `PUT /v1/m/storefront/photos` `{ids}` · `PUT|DELETE /v1/m/storefront/photos/{id}` · `POST /v1/m/reviews/{id}` `{reply}` or `{pinned}`
- `GET /v1/m/settings` · `PUT /v1/m/settings/profile` · `PUT /v1/m/settings/rules` `{booking, policy, notify}` · `POST /v1/m/locations` · `PUT /v1/m/locations/{id}` · `POST /v1/m/locations/{id}/action` `{action: primary|delete}`

The owner only

- `GET /v1/m/money?from=&to=&kind=&staff=` · `GET /v1/m/money/export` · `POST /v1/m/payouts` `{instant}` · `PUT /v1/m/payout-schedule` `{schedule: daily|weekly|manual}`
- `GET /v1/m/payout-account` · `POST /v1/m/payout-account/bank` (Nigeria; two steps, the second with `confirm`) · `POST /v1/m/payout-account/stripe` (United States) · `POST /v1/m/payout-account/{id}/default` · `DELETE /v1/m/payout-account/{id}`
- `POST /v1/m/staff/{id}/invite` `{email, role: manager|staff}` emails a link to choose a password · `DELETE /v1/m/staff/{id}/invite` · `POST /v1/m/plan` `{plan}` · `POST /v1/m/listing` `{paused}`

Clients meet the merchant web in three places, all with a customer token: `GET /v1/auth/threads` · `GET /v1/auth/threads/{id}` · `POST /v1/auth/threads` `{business_slug, body}` for in-app messages, and `POST /v1/auth/bookings/{id}/review` `{rating, body}` after a finished visit. `GET /v1/businesses/{slug}` also returns `display` (what the business chose to show) and `policy` (instant or approved bookings, the cancellation window).

### Pricing, rooms, packages and memberships

- **Price of a booking.** A person's own price for a service replaces the menu price. Pricing rules then adjust it, in the order they were made: a rule can be limited to a service, days of the week, a time window, a staff level and a date range, and changes the price by an amount or a percentage. `GET /v1/m/price-check?service=&staff=&at=` explains a price. Every free time the API returns (to the business and to the public) carries `price_cents` for that time and person, and a booking is stored at that price.
- **Rooms, chairs and stations.** A resource has a name and a quantity. A service can require resources; a time is offered only when the person and every required resource are free, and the booking endpoints refuse a time when one is fully taken. `POST|PUT|DELETE /v1/m/resources`, `PUT /v1/m/services/{id}/resources` `{ids}`.
- **Packages.** A package is a set of services with quantities, sold for one price and valid for a number of days. Selling one at checkout (`items: [{kind: "package", package_id}]`, a client is required) gives the client credits. A service line with `redeem: true` is paid with a credit. `POST|PUT|DELETE /v1/m/packages`.
- **Memberships.** A monthly price, a discount on services, a discount on retail, and services included each month. Selling one (`kind: "membership"`) makes the client a member; their discount then comes off every sale by itself (`member_discount_cents` in the answer). The worker renews memberships each month: in simulation it records the charge and tops up the included services; with live payments there is no saved card to charge, so the membership is marked `past_due` for the desk to collect. `POST|PUT|DELETE /v1/m/memberships`, `GET /v1/m/clients/{id}/plans`, `POST /v1/m/client-plans/{id}` `{action: cancel|reactivate}`.
- `GET /v1/m/menu` returns resources, pricing rules, packages, memberships and who holds them. `POST /v1/m/services/import` `{rows}` takes a pasted price list.

### Pay, breaks, chair rental and permissions

- A team member has a pay type: `commission`, `hourly`, `salary`, `owner` or `renter`. `GET /v1/m/payroll` works out rostered hours (their week, less breaks and approved time off), wage or salary for the period, commission and tips. A service paid with a package credit earns commission on its value; the sale of the package itself earns none.
- Breaks are stored per weekday (`breaks: {mon: [["13:00","13:30"]]}`) and are never offered to clients.
- A chair renter is not booked through the business. The worker opens a rent charge for each period; the business marks it paid, waived or due with `POST /v1/m/rent/{id}`. LogaLuxe records rent and does not collect it.
- Three things can be switched per team member: `see_all_calendars`, `take_payments`, `see_reports`. They are saved on the staff record and enforced by the API; managers and the owner can always do all three.
- `POST /v1/m/time-off/{id}` `{decision: "approve", reassign: true}` also moves that person's bookings to someone else who does the same services and is free, and answers with how many moved and how many are left.

### Stock

- A product can have a photo (`POST|DELETE /v1/m/products/{id}/photo`), a full-shelf level (`par_level`) and a list of services that use it (`PUT /v1/m/products/{id}/services` `{items: [{service_id, qty}]}`). Checkout takes what each paid service uses off the shelf. Amounts can be fractions: part-used units are remembered until they add up to a whole one.

### Business rules that are enforced

Booking: `instant` (off means a booking starts as a request), `lead_hours`, `max_days`, `anyone`, `multi_service`, `waitlist`, `on_search` (off hides the business from search; its link still works). Policy: `cancel_hours`, `late_cancel_fee`, `no_show_fee`, `new_client_deposit_pct`, `prepay_after_no_show`. Notify: `new_booking_email`, `cancellation_email`, `low_stock_email`, `daily_summary` (sent after 7am local time). Emails go to the business email, or the owner. Addresses ending in `.test` are never emailed.

### Customer web API

- **Openings.** `GET /v1/businesses/{slug}/openings?services=&staff=&limit=` the next free times of one business; `GET /v1/openings?slugs=a,b,c&q=` the next three of several, for search results; `GET /v1/businesses/{slug}/days?month=YYYY-MM&services=&staff=` which days of a month have room. All obey the business's lead time, booking horizon, breaks, rooms and pricing rules; every time carries its price.
- **Reviews.** `GET /v1/businesses/{slug}/reviews?page=&stars=` with the star breakdown. A business with five or more published reviews gets a two-sentence summary of what clients say (`review_summary`), written by the AI model from those reviews and refreshed when new ones arrive. It appears only when `OPENAI_API_KEY` is set.
- **Saved businesses.** `GET /v1/auth/favourites`, `PUT|DELETE /v1/auth/favourites/{slug}`. `GET /v1/businesses/{slug}` returns `saved` for a signed-in customer.
- **A booking afterwards.** `GET /v1/bookings/{id}/calendar.ics` a calendar file; `GET /v1/auth/bookings/{id}` with what the client may still do; `POST /v1/auth/bookings/{id}/reschedule` `{starts_at, staff_id}` moves it to another free time while the free cancellation window is still open. The price agreed when booking is kept.
- **What a client holds.** `GET /v1/auth/wallet`: packages, memberships and loyalty points at each business they have visited.
- **The shop.** `GET /v1/products` takes `q`, `category`, `seller` (a business handle, a seller name, `booked` or `brands`), `tag`, `delivery`, `min`, `max`, `sort`, `page`, `per`, and returns the values that can be filtered with counts. `GET /v1/products/{slug}` adds photos, reviews, related products and whether this customer may review it. `POST /v1/auth/products/{slug}/review` is for someone who bought it, once; ratings are counted from real reviews.
- **Orders by seller.** `POST /v1/orders` takes `fulfilment_by_seller`, so a studio's items can be collected while a brand's are shipped. Each seller in an order has a row in `order_shipments` with its own status. When an order is paid, each business in it is credited its items and shipping and charged the marketplace fee (`marketplace_pct`) in the ledger. An order that is never paid puts its stock back.
- **A business's online orders.** `GET /v1/m/orders?status=`, `POST /v1/m/orders/{id}` `{action: ready|shipped|delivered|collected, tracking}`.
- **Cancelling an unpaid order.** `POST /v1/auth/orders/{id}/cancel` asks the payment provider first (in case the customer just paid), closes the payment page, puts the stock back and returns any gift card money or promo use. An order that expires unpaid does the same.
- **What a business page tells the booking screen.** `GET /v1/businesses/{slug}` lists each person's `service_ids`, and its `policy` says whether deposits are paid online (`payments_live`), the first-visit deposit (`new_client_deposit_pct`) and `prepay_after_no_show`. `GET /v1/auth/bookings/{id}` includes `late_cancel_fee`.
- **Prices with sizes.** The shop's price filter and price sort use a product's lowest size price, which is the price its card shows.

The screens are in `logaluxe-web`: `/search`, `/b/{slug}`, `/b/{slug}/book`, `/shop`, `/shop/{slug}`, `/cart`, `/account`, and a business's online orders at `/business/inventory?tab=orders`.

### Customer extras

Each of these was drawn in the design and now has real data behind it (`c_extras.go`, migration 0022).

- **What a business says about itself.** `GET /v1/businesses/{slug}` returns `extras`: the languages it speaks, its returns policy, delivery time and pick-up promise, and `reply_minutes`, the median time it took to answer clients over 90 days (absent until there are three replies to measure). A business sets these with `GET|PUT /v1/m/shop-policy`; a number left `null` means "not stated" and the page then says nothing.
- **Product details.** `PUT /v1/m/products/{id}/details` `{ingredients, how_to_use}`; `PUT /v1/admin/products/{id}/details` adds delivery time and returns for brand products. `GET /v1/products/{slug}` returns `extras`: ingredients, delivery, returns, `pickup_today` (only while the seller is open and the item can be ready before closing), and for a signed-in customer `saved` and `next_visit`. `GET /v1/products-extras?slugs=` gives the same for a cart.
- **Pick up at the visit.** No new request field: when a customer who collects has a visit booked with that seller, the API writes it on the seller's part of the order (`note` in `GET /v1/m/orders`).
- **Saved products.** `GET /v1/auth/favourite-products`, `PUT|DELETE /v1/auth/favourite-products/{slug}`.
- **Photos on reviews.** `POST /v1/auth/reviews/{id}/photos` (the author, up to three, confirmed email), `DELETE /v1/auth/review-photos/{id}`. Review lists carry `photos`.
- **Referral credit.** Off until staff set an amount: `GET|PUT /v1/admin/settings/referral` `{credit_cents}` (0 to 10000). A customer's code and link: `GET /v1/auth/referral`. A friend signs up with `ref`, confirms their email and pays for a first visit or has a first order delivered; the worker then credits both once. Credit is in USD and `POST /v1/orders` spends it automatically after the promo code and gift card; an order it covers fully is paid at once. An unpaid order gives it back. LogaLuxe funds it: sellers are paid in full.
- **Sold counts** are real: units in paid shop orders plus units sold at the desk.
- **Distance** is worked out in the visitor's browser from their own position and is never sent to the API.
- Not built: order updates on WhatsApp.
- Test: `node scripts/e2e/customer-extras.js`.

### After the sale, setup, and the naira shop

Migrations 0023 to 0025; `c_care.go`, `m_onboarding.go`, `order_mail.go`.

- **Tax.** Each business charges its own rate (`businesses.sales_tax_bp`) on what it sells. Brand products are sold by LogaLuxe and taxed by the state they ship to, from `tax_rates` (`GET /v1/admin/tax-rates`, `PUT|DELETE /v1/admin/tax-rates/{region}`); a state not listed is not taxed. `POST /v1/orders/quote` takes the same body as an order, changes nothing, and answers the totals: the cart shows those and never works tax out itself.
- **Returns.** `POST /v1/auth/orders/{id}/returns` `{seller, reason, note}` while the seller's stated window is open (`can_return`, `return_until`, `return_why` on each part of `GET /v1/orders/{id}`). The seller answers at `GET /v1/m/returns`, `POST /v1/m/returns/{id}` `{action, reply, refund_cents, restock}`; staff answer for brand products at `/v1/admin/returns`. A refund goes to the card as far as a card paid and the rest as store credit; the seller's balance is debited and the marketplace fee on the refunded items is returned.
- **A problem with a visit.** `POST /v1/auth/bookings/{id}/problem` within 14 days opens a dispute. The business answers within 48 hours at `GET /v1/m/problems`, `POST /v1/m/problems/{id}`; staff then decide in the console's disputes queue.
- **A tip after the visit.** `POST /v1/auth/bookings/{id}/tip` `{amount_cents}`, up to 30 days after, at most the price of the visit.
- **Gift cards bought online.** `POST /v1/gift-cards/buy`, US dollars, $10 to $500. The code is emailed to the recipient (or the buyer) and never returned to the browser.
- **Order emails.** The customer is emailed when an order is placed, ready or shipped, and when a return is answered; each seller is emailed about a new order and a return request.
- **Getting a business ready.** `GET /v1/m/onboarding` lists seven setup steps, each worked out from what is really there. Identity papers: `POST /v1/m/verification/documents` (photo or PDF, 10 MB, stored privately), `POST /v1/m/verification/submit`. Staff read them at `GET /v1/admin/verification/{id}/documents` and `GET /v1/admin/verification-documents/{id}`; every view is in the audit log.
- **The shop in naira.** A product is priced in its seller's currency (brands in dollars). `GET /v1/products?currency=NGN` lists the naira shop; an order is in one currency and is paid through that market's provider. Dollar gift cards and store credit cannot pay for a naira order.
- Tests: `node scripts/e2e/customer-care.js`, `node scripts/e2e/setup-and-naira.js`.

### Experience extras

Migration 0026; `c_more.go`, `ics.go`.

- **Booking for someone else.** `POST /v1/bookings` takes `guest_name`; the contact stays the person who booked. Bookings carry it everywhere.
- **Questions at booking.** A business keeps up to 20 (`GET|POST /v1/m/intake`, `PUT|DELETE /v1/m/intake/{id}`): a short answer, yes or no, a choice, or a box that must be ticked, for every booking or one service. `GET /v1/businesses/{slug}` returns them as `intake`; `POST /v1/bookings` takes `answers` and refuses with a sentence naming the question. The wording is stored with each answer.
- **Repeat appointments.** `POST /v1/auth/bookings/{id}/repeat` `{every_weeks, times}` books the same visit at the same clock time, each through the normal rules, and answers what was made and what was skipped and why. The bookings share a `series_id`.
- **Calendar sync, per person.** Out: `POST /v1/m/calendar-sync/feed` gives a private address (`GET /v1/cal/{token}.ics`) that Google, Apple and Outlook calendars subscribe to; set `API_PUBLIC_URL` so the address is one they can reach. In: `PUT /v1/m/calendar-sync/import` `{url}` takes the private iCal address of the person's own calendar, read every ten minutes; their busy times become blocks that cannot be booked. Only times are kept. Daily and weekly repeats are understood; other rules are counted and reported, never guessed. The address must be https on the public internet.
- **A person's own day.** `GET /v1/m/my-day?date=`: their visits in order with the guest, answers, notes and visit count, and their blocks.
- **A booking must be free.** `POST /v1/bookings` now refuses a time the person has blocked off or is on approved time off, not only a time another booking holds.
- **Embedding.** `/embed/{slug}` is the booking page made for an iframe and `/embed.js` adds a "Book now" button to a business's own site. Those bookings use `source: "link"`.
- Tests: `node scripts/e2e/experience.js`, and `go test ./internal/httpapi -run TestParseICS`.

### Launch safety

- **Customers confirm their email.** Sign-up emails a link to `/verify?token=` (48 hours). `POST /v1/auth/verify` `{token}` confirms the address the link was sent to; `POST /v1/auth/verify/send` sends it again, at most every two minutes. `user.email_verified` is in every account answer. Reviews of a business or a product need a confirmed email. Using a password reset link also confirms it.
- **Request limits.** One connection may make only so many tries at sign-in, sign-up, reset emails, promo and gift codes, bookings, orders, the waitlist and the help form (`safety.go`); past that the API answers 429 with `Retry-After`. This is on top of the 15 minute lock each account already gets after repeated wrong passwords. Counts are kept in memory per API process. The web app passes the visitor's address in `X-Visitor-IP`; set the same `WEB_API_KEY` on the API and the web app so the API believes it (required in production). `RATE_LIMITS=off` turns the limits off for the end-to-end suites, which make many accounts from one address.
- **Two-step sign-in for businesses.** `GET /v1/m/security`, `POST /v1/m/2fa/setup`, `/2fa/enable` `{code}` (returns eight recovery codes once), `/2fa/disable` `{password}`. `POST /v1/m/login` then needs `code` and answers `need_code: true` without one. A super admin can reset it for someone who lost their phone: `POST /v1/admin/merchants/reset-2fa` `{email}`. In the web app it is under Settings, Your account.
- **Search engines.** The web app serves `/robots.txt`, `/sitemap.xml`, a web app manifest, structured data on business, product and listing pages, and a page per city and service (`/nashville`, `/lagos/barbers`). Set `NEXT_PUBLIC_SITE_URL` to the public address. `GET /v1/businesses?quiet=1` lists without counting views, for the sitemap.
- Test: `node scripts/e2e/safety.js` with mail logged and the limits on. Every other suite: start the API with `RATE_LIMITS=off` as well.

### The lead system

LogaLuxe earns a share of the first visit of a client it brought to a business. It is the `new_client_pct` in the fee schedule, per market and plan, with a cap (`new_client_cap_cents`) and an optional adjustment per kind of business (`lead_category_rates`).

- A lead opens when a public booking arrives with `source` set to `search`, `category` or `app` and the client has never booked, bought from, or been a lead of that business. The website sets the source: a link from LogaLuxe search carries `?src=search`; someone who opens the business's own link books with source `link` and is never a lead.
- The lead is charged when that booking is checked out: a share of the services paid for on that visit (not tips, tax or retail), up to the cap. It is a `lead_fee` line in the ledger and comes out of the payout balance however the client paid.
- A cancelled booking or a no-show voids the lead (a database trigger does it, so no code path can forget). Nothing is charged.
- The business can dispute a charge for 14 days (`POST /v1/m/leads/{id}/dispute`). The console decides: a refund puts the fee back as an `adjustment` line.
- **Promotion.** A business can bid extra percentage points on new clients (up to 20, in half points) with a monthly budget (`PUT /v1/m/leads/settings`). While its month's lead fees are under the budget it is listed first in search and marked `promoted`; leads opened then pay the base rate plus the bid. When the budget is spent, promotion stops by itself. Sorting search by price or reviews ignores promotion.
- `GET /v1/m/leads` gives the business its leads, the month's fees, what those clients have spent since, how many came back, the 30-day funnel (shown in search, page opened from search, first bookings) and where it ranks among similar businesses in its city.
- Console: `GET /v1/admin/leads` (lead revenue by market, promotion revenue, disputes), `POST /v1/admin/leads/{id}/resolve` `{outcome: refund|uphold, note}` (operations), `PUT /v1/admin/leads/rates` `{market, category, delta_pct}` (super admin).

### Real payments

Stripe is used for businesses in the United States and Paystack for businesses in Nigeria. Each market is live when its secret key is set (`STRIPE_SECRET_KEY`, `PAYSTACK_SECRET_KEY`) and simulated when it is not. Card details are only ever typed on the provider's own page.

- **Deposits.** A public booking that needs a deposit answers with `booking.payment.url`. The time is held for 30 minutes. When the payment arrives the deposit is marked paid and held in the ledger; if it never arrives the booking is cancelled and the time released.
- **Shop orders** work the same way (`order.payment.url`).
- **Pay links.** `POST /v1/m/checkout/link` takes the same body as the checkout, prices and checks the sale without recording it, and answers with a payment page for the client. When they pay, the sale is recorded exactly as it was rung up. `GET /v1/m/payments/{ref}` tells the till whether it has been paid.
- **Confirming.** `POST /v1/payments/{ref}/confirm` (the client coming back from the provider), `POST /v1/webhooks/stripe` (signed with `STRIPE_WEBHOOK_SECRET`), `POST /v1/webhooks/paystack` (signed with the Paystack secret key), and the worker asking the provider about open payments every minute. All four end in one function that acts once, so it works without a public address for webhooks.
- **At the desk.** Once a market is live, money taken on the business's own card machine, by bank transfer or in cash is recorded but stays out of the payout balance and carries no LogaLuxe fee. Only deposits and pay links went through LogaLuxe.
- **Refunds.** Cancelling a booking in the client's favour, or refunding a sale that was paid by link, sends the money back through the provider.
- **Payouts.** A payout to a real payout account is sent through the provider: a Stripe transfer to the business's connected account, or a Paystack transfer to its bank. It becomes `paid`, or `failed` with the provider's reason and the money returned to the balance. Paystack only allows transfers from a registered (not starter) business account.
- To check a setup against the providers' test modes: `node scripts/e2e/payments-live.js` (refuses to run with live keys), and `node scripts/e2e/pay-start.js ada` to make a deposit to pay by hand with a test card.

### Stock per location, loyalty, promo codes, statements, text messages

- **Stock per location.** `products.stock` is the total; `location_stock` says where it sits. Sales, counts, deliveries and adjustments name a location (`location_id`), or use the main one. `POST /v1/m/products/{id}/transfer` moves stock between locations. Closing a location moves its stock to the main one.
- **Loyalty.** `GET /v1/m/loyalty`, `PUT /v1/m/loyalty/settings` `{enabled, earn_points, per_cents, point_value_cents, min_redeem}`, `POST /v1/m/loyalty/adjust`. Points are earned at checkout on what the client paid for goods and services, and spent there with `redeem_points`.
- **Promo codes.** A business makes its own codes (`GET|POST /v1/m/promos`, `PUT|DELETE /v1/m/promos/{id}`). They work on its booking page and at its checkout (`promo_code`), and nowhere else.
- **Statements.** `GET /v1/m/statements` lists the months; `GET /v1/m/statements/{YYYY-MM}` gives opening and closing balance, each total, the payouts and every line, and `?format=csv` downloads it.
- **Logo.** `POST|DELETE /v1/m/storefront/logo`. It is shown on the public page.
- **Text messages.** Twilio (United States) and Termii (Nigeria) send reminders, campaign messages and inbox replies on the `sms` channel. Nothing is texted unless `SMS_ENABLED=true` as well as the keys, and the fictional number ranges used by the sample data are never texted.

### How money moves

Every amount a business is owed or pays lives in the `ledger` table: `charge`, `tip`, `deposit`, `refund`, `fee`, `payout`, `payout_fee`. A line is `held` (a deposit for a visit that has not happened), `pending` (paid, settling for two days) or `settled`. The payout balance is the sum of settled lines that count towards it; cash never does. A payout takes the whole available balance and writes a negative line, under a lock so two cannot run at once.

A background worker in the API process runs every minute: it settles lines whose time has come, sends the automatic messages, and every ten minutes runs the daily or weekly payouts for businesses whose local time is past 6am. Each job takes a database lock, so several copies of the API do not repeat the work.

### What is simulated

- **Payments in a market without its key.** A payment is recorded as taken and a payout is marked paid with a `sim_` reference. No money moves. Payout accounts added then are labelled simulated and cannot receive a real payout later.
- **WhatsApp.** No provider is connected: a message on that channel is saved with delivery `logged`. In-app messages are real, email is real when `MAIL_PROVIDER` is set, and texts are real when `SMS_ENABLED=true`.
- **Plans.** Changing between Free and Pro changes the fees charged. There is no subscription billing.
- **Membership renewals with live payments.** There is no saved card to charge, so a membership that is due is marked as owing and the desk collects it.

### Sample owners

With `SEED=true` and `MERCHANT_DEMO_PASSWORD` set, each sample business gets an owner who can sign in at `/business/signin` as `<handle>@logaluxe.test`, for example `ada@logaluxe.test` (United States) or `mnm@logaluxe.test` (Nigeria). Six weeks of sample visits, sales and payouts are loaded once. Leave the password empty in production.

## Added for the mobile app

- `POST /v1/m/checkout/quote` takes the same body as `POST /v1/m/checkout` and answers `{quote}` with the totals the sale would have. Nothing is kept. The till uses it so the price on screen is the price charged, promo codes included.
- `POST /v1/checkout/check` accepts `business_slug`, so a code that belongs to one business can be checked for a booking with it.
- `GET /v1/m/calendar` marks each block with `external` when it came from a person's own calendar.
- A client now gets an email when a booking is confirmed or sent as a request. When a deposit is paid online, the email goes once the money has arrived.
- `CORS_ORIGIN` must include the address the app is served from when it runs in a browser (port 8097 in development).

## Email

`MAIL_PROVIDER` is `resend`, `smtp` or `log`. With `log`, or with a provider that is missing its key, messages are written to the API log and not sent, and the console says so. Email is used for password reset links, support replies, gift card codes and bulk messages. Links in emails point at `WEB_URL`. WhatsApp and SMS are not connected: a send on those channels is recorded only.

To test email flows without sending anything: `MAIL_PROVIDER=log docker compose up -d api`, then read the message in `docker logs logaluxe-api`.

## Image storage

Set `AWS_REGION`, `AWS_S3_BUCKET`, `AWS_ACCESS_KEY_ID` and `AWS_SECRET_ACCESS_KEY`. The bucket can stay private: the API signs its own requests and serves the bytes. The key needs `s3:PutObject`, `s3:GetObject` and `s3:DeleteObject` on the bucket. Files are stored under `site/<slot>/<ref>/<id>.<ext>`. Without these settings uploads return 503 and the console says so.

## Rules the code keeps

- Money is integer minor units. Times are `timestamptz`; every business has a timezone.
- The `bookings` table carries an exclusion constraint: the database refuses two active bookings for one staff member that overlap. The API turns that into a 409.
- Without payment keys the API runs in simulation mode: deposits and orders are marked `simulated`.
- Every admin action writes to `audit_log` with the admin's email and the before and after values.
- Checkout never trusts an amount from the browser. Promo discounts and gift card balances are worked out and locked on the server inside the order's transaction.
- A refunded or cancelled order returns gift card money to the card. Every balance change is a ledger row.
- Two-step sign-in is TOTP (RFC 6238). A wrong code counts towards the lockout. Recovery codes and reset tokens are stored only as hashes.
- A service or team member with bookings on record is retired, not deleted.
- A blocked phone number cannot book. Refunding or cancelling an order puts its stock back.
