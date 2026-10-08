-- Admin console: real accounts with roles, sessions, and the operational tables.

create table admin_users (
  id uuid primary key default gen_random_uuid(),
  email text not null unique,
  name text not null,
  role text not null check (role in ('support','ops','super_admin')),
  password_hash text not null,
  active boolean not null default true,
  failed_logins int not null default 0,
  locked_until timestamptz,
  last_login_at timestamptz,
  created_at timestamptz not null default now()
);

-- Only a hash of the session token is stored; a database leak cannot be replayed.
create table admin_sessions (
  token_hash text primary key,
  admin_id uuid not null references admin_users(id) on delete cascade,
  expires_at timestamptz not null,
  created_at timestamptz not null default now()
);

create table admin_notes (
  id uuid primary key default gen_random_uuid(),
  target_type text not null,
  target_id uuid not null,
  author text not null,
  body text not null,
  created_at timestamptz not null default now()
);
create index admin_notes_target on admin_notes (target_type, target_id, created_at desc);

create table credits (
  id uuid primary key default gen_random_uuid(),
  business_id uuid references businesses(id) on delete set null,
  client_phone text not null default '',
  amount_cents int not null check (amount_cents > 0),
  currency text not null,
  reason text not null,
  issued_by text not null,
  created_at timestamptz not null default now()
);

create table blocked_contacts (
  phone text primary key,
  reason text not null default '',
  blocked_by text not null,
  created_at timestamptz not null default now()
);

create table payouts (
  id uuid primary key default gen_random_uuid(),
  business_id uuid not null references businesses(id) on delete cascade,
  amount_cents int not null,
  currency text not null,
  status text not null default 'scheduled' check (status in ('scheduled','paid','failed','held')),
  provider text not null,
  reference text not null default '',
  failure_reason text not null default '',
  scheduled_for date not null default current_date,
  paid_at timestamptz,
  created_at timestamptz not null default now()
);
create index payouts_business on payouts (business_id, created_at desc);

alter table businesses
  add column payout_hold boolean not null default false,
  add column payout_hold_reason text not null default '';

alter table products add column active boolean not null default true;

-- Fee changes are proposed by one admin and approved by another.
alter table fees
  add column status text not null default 'approved' check (status in ('pending','approved')),
  add column proposed_by text,
  add column note text not null default '';
