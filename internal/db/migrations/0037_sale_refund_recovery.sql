-- Reserve merchant refunds and recovery work atomically with sale accounting.
create table sale_refund_jobs (
 id uuid primary key default gen_random_uuid(),
 sale_id uuid not null references sales(id),
 payment_id uuid not null references payments(id),
 amount_cents integer not null check(amount_cents>0),
 target_refunded_cents integer not null,
 status text not null default 'pending' check(status in ('pending','accepted')),
 problem text not null default '',
 created_at timestamptz not null default now(),
 updated_at timestamptz not null default now()
);
create unique index sale_refund_pending on sale_refund_jobs(sale_id) where status='pending';
alter table sales add column provider_refund_status text not null default '';
alter table ledger add column sale_refund_job_id uuid references sale_refund_jobs(id);

alter table order_returns add column provider_refund_status text not null default '';
create table order_return_refund_jobs (
 return_id uuid primary key references order_returns(id),
 payment_id uuid not null references payments(id),
 target_refunded_cents integer not null,
 status text not null default 'pending' check(status in ('pending','accepted')),
 problem text not null default '',
 created_at timestamptz not null default now()
);

create table sale_refund_requests (
 sale_id uuid not null references sales(id),
 request_key text not null,
 payload_hash text not null,
 response_body text not null,
 response_code integer not null,
 primary key(sale_id,request_key)
);
