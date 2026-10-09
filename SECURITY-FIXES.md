# Account, payment, list and calendar hardening

Implemented for the six findings reviewed on 8 October 2026.

## Ownership and receipts

Merchant wallet entries require a verified matching phone. A booking's user link or a typed number never grants access to existing packages, memberships, or points. Public booking creation only links an existing client record when the signed-in customer's verified number matches. New contacts can still be created for guest bookings. Public requests cannot overwrite an existing client's name or notes. Messaging only matches verified phone/email fields.

Public booking/order receipts expose status, prices and service/product information, but redact customer names, contact information, private notes, delivery addresses, tracking and return details. Full personal fields require the owning customer's session, except the immediate creation response. Orders use an explicit field list instead of `select *`. Receipt responses are not cacheable.

## Payments and refunds

Settlement locks the payment row, performs the booking/order/sale/gift and ledger work in one transaction, and commits the payment status with those records. Checkout runs within a nested savepoint. Database failure rolls everything back; semantic sale/gift failures leave the payment pending with a reconciliation message. Notifications are deferred until after commit. The existing sweep retries pending payments.

Migration `0032_payment_safety.sql` adds durable refund intents and cancellation refund jobs. A per-payment advisory lock serializes refund calls. Intents survive a lost response or process restart. Stripe retries use the same intent's idempotency key. Paystack uncertain responses are looked up by transaction and the persisted merchant note; they are not submitted again blindly. Accepted refunds and refunded totals are recorded together. Reconciliation runs with the payment sweep. Provider acceptance means the refund is submitted, not that the bank has completed it.

An uncertain Paystack refund that cannot be found, or a Stripe retry beyond its idempotency window, requires provider reconciliation rather than risking a duplicate. This is deliberate. Historical paid rows are not automatically replayed: existing records need a separate reconciliation audit before any financial repair.

Provider references: [Stripe idempotent requests](https://docs.stripe.com/api/idempotent_requests), [Paystack refunds API](https://paystack.com/docs/api/refund/).

## Admin lists

Bookings, clients, orders, payouts, audit and support now accept `page`, `per_page` (maximum 200), `sort` and `direction`, in addition to their existing filters. Responses include matching totals and paging metadata. Sort columns have a per-endpoint allowlist. The web pages request the selected server page and expose row count, sorting and previous/next controls. The 200 limit is now a page-size ceiling, not a cap on reachable records.

## Calendar import

The connection itself resolves and validates every address, then dials a validated numeric IP. TLS still checks the original hostname. Private, loopback, link-local and shared-address targets are refused, including mixed DNS answers. Environment proxies are disabled for these fetches; redirect targets undergo the same connection checks.

## Verification

`node scripts/security-regressions.cjs` reads local database credentials privately, creates isolated disposable databases, runs ownership/privacy, settlement rollback/concurrency, refund lost-response/concurrency, paging beyond 200 and DNS-pinning tests, then removes only those test databases. Seeded application records are never used as test fixtures. Payment providers are mocked: these tests cannot charge or refund real money.

`node scripts/security-smoke.cjs` checks the running API and rendered admin paging controls using read-only requests. The Docker build runs Go tests and vet before compiling the API. Web TypeScript is checked separately.

`.dockerignore` excludes environment secrets from build context. No new payment keys are required.

Final local verification: all eight security regression groups passed, the Docker build passed Go tests and vet, and the read-only running API/admin smoke checks passed. Web and mobile TypeScript checks passed. Mobile lint could not complete because Expo's automatic ESLint setup encountered a local TLS certificate error. Native-device behaviour and historical financial reconciliation are not covered by these checks.
