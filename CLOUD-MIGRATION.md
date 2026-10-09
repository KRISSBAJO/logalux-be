# Renviq and RelyKit handover

Completed October 8, 2026 (Chicago time).

## Database

The running API uses the LogaLuxe database on Renviq. Credentials remain in the ignored `.env`. TLS hostname and certificate verification are enabled (`sslmode=verify-full`). Automatic sample seeding is disabled.

All 96 tables matched the original database by row count and content fingerprint immediately before cutover. Migration 0033 subsequently added email queue counters on Renviq. The original local database has been retained unchanged.

The protected `E:\LogaLuxe-backups` folder contains the custom-format PostgreSQL backup, source counts, migration fingerprints, financial audit, and pre-cutover environment file. These files include private data; do not commit or distribute them.

Recovery: stop the API before changing its database configuration. Restore the backup into an empty PostgreSQL database with `pg_restore --single-transaction --exit-on-error --no-owner --no-acl`, then configure its connection privately and restart the API. To return to the retained local database, restore only the old database configuration from the protected environment backup, preserving the current email configuration. Cloud writes made after cutover must be reconciled before a rollback; the old local database is no longer current.

This migration does not establish an ongoing backup schedule. Configure and verify managed backups before public launch.

## Email and DNS

The five requested RelyKit DNS records are saved. RelyKit confirms DKIM, SPF, MX and DMARC verification. The existing stronger DMARC policy was retained.

The backend uses RelyKit with the verified `logaluxe.com` sender and the privately stored GatherPlux API key. Provider authentication and mocked send/error handling passed. No live email was sent during verification. RelyKit acceptance is counted as queued, not delivered.

## Verification and remaining findings

- Go tests, static checks, web/mobile TypeScript checks and security regression checks passed.
- API health, public pages and admin pagination smoke checks passed after cutover.
- The mobile certificate blocker was resolved using the trusted local certificate and a separate npm cache on E:. ESLint now runs, but reports 86 existing React hook/compiler errors and 44 warnings. Lint is not passing.
- The read-only financial audit found nine paid sample bookings for Ada's `Test Client` without sales records, consistent with the previously reported sample cleanup. No financial records were altered.
- Other stored-record checks found no duplicate deposit ledger entries, invalid refund totals, unfinished refund intents or sale-charge mismatches. The payments table contains zero rows, so this is not reconciliation against Stripe or Paystack statements.

Audit details are in the protected `financial-audit.json`. Migration tooling is in `scripts/renviq-migrate.cjs`; it reads credentials privately from `.env` and refuses restoration into a nonempty destination.
