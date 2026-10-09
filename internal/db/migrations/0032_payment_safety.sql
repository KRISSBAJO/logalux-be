-- Durable provider refund intents survive process/network/database failures.
create table payment_refunds (
 id uuid primary key default gen_random_uuid(),
 payment_id uuid not null references payments(id),
 amount_cents integer not null check(amount_cents>0),
 base_refunded_cents integer not null,
 status text not null default 'created' check(status in ('created','sending','unknown','accepted','failed')),
 provider_ref text not null default '',
 problem text not null default '',
 created_at timestamptz not null default now(),
 updated_at timestamptz not null default now(),
 unique(payment_id,base_refunded_cents)
);
create table payment_refund_jobs (
 payment_id uuid primary key references payments(id),
 amount_cents integer not null check(amount_cents>0),
 created_at timestamptz not null default now(),
 completed_at timestamptz
);
