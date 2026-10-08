# End-to-end checks

Bash scripts that exercise the running local API with curl. They need Docker, Node and the stack from `docker compose up`.

- `admin-core.sh .` sign-in, roles, bookings, payouts, fees, flags, team. Leaves test rows behind.
- `admin-growth.sh .` account, two-step sign-in, catalog, exports, promo codes, gift cards, support, messages, pages. Run `admin-growth-cleanup.sh` after it.
- `customer-accounts.sh .` sign up, sign in, book while signed in, cancel, reset the password. Cleans up after itself.
- `media.sh .` upload, read back, hide and delete one image in S3.
- `node scripts/e2e/merchant.js` signs up a throwaway business and runs a whole working day through the merchant API: menu, team, booking, checkout, stock, payout, refund, clients, inbox, marketing, roles, settings. Deletes the business afterwards.
- `node scripts/e2e/merchant-extras.js` rooms and chairs, pricing rules, breaks, packages, memberships, back-bar use, pay types, chair rent, permissions, and moving bookings when time off is approved. Deletes its business afterwards.
- `node scripts/e2e/merchant-leads.js` the lead system: opening, charging, the cap, voiding, promotion and its budget, the funnel, disputes and the console decision.
- `node scripts/e2e/merchant-more.js` stock per location, loyalty points, a business own promo codes, statements.
- `node scripts/e2e/payments-live.js` pay links, webhook protection, payout accounts and payouts against the Stripe and Paystack TEST modes. Refuses to run with live keys.
- `node scripts/e2e/customer-web.js` what the customer web relies on: openings, the month calendar, saved businesses, reviews, moving a booking, shop filters, an order with two sellers and each seller pay and fulfilment, product reviews.
- `node scripts/e2e/merchant-billing-ai.js` the Pro plan fee and the AI drafts (calls OpenAI twice when the key is set).
- `node scripts/e2e/geo.js` places, the place picker, a point to a place, search by distance with the widening radius and its notices, by place, by country and by map area, travelling businesses, the first guess from an internet address, gift cards in either country, and a business's country, pin and time zone at sign-up and when its address changes. Needs no payment keys; leaves nothing behind.
- `merchant-pages.sh` opens every merchant screen and tab as three sample owners.
- `merchant-smoke.sh` signs in as a sample owner and reads the data for every merchant screen.
- `merchant-public.sh` the public booking page, a client writing to a business, and the reply. Needs the web app on port 3100. Cleans up after itself.

Most suites expect simulated payments. Start the API for them with `STRIPE_SECRET_KEY= PAYSTACK_SECRET_KEY= MAIL_PROVIDER=log docker compose up -d api` (only `payments-live.js` wants the keys), so no real email is sent and the reset link can be read from the log. The scripts use throwaway accounts on the reserved `logaluxe.test` domain. Never run them against production.
