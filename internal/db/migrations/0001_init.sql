-- LogaLuxe core schema. Money is integer minor units (cents, kobo). Times are timestamptz.
create extension if not exists btree_gist;
create extension if not exists pgcrypto;

create table users (
  id uuid primary key default gen_random_uuid(),
  phone text unique,
  email text,
  first_name text not null default '',
  last_name text not null default '',
  locale text not null default 'en',
  preferred_channel text not null default 'whatsapp',
  created_at timestamptz not null default now()
);

create table businesses (
  id uuid primary key default gen_random_uuid(),
  slug text not null unique,
  name text not null,
  tagline text not null default '',
  about text not null default '',
  category text not null,
  market text not null check (market in ('US','NG')),
  currency text not null,
  timezone text not null,
  phone text not null default '',
  email text not null default '',
  instagram text not null default '',
  status text not null default 'live' check (status in ('pending','live','paused','suspended')),
  verification_status text not null default 'unverified' check (verification_status in ('unverified','pending','verified','rejected')),
  plan text not null default 'free' check (plan in ('free','pro')),
  rating numeric(3,2) not null default 0,
  review_count int not null default 0,
  tone text not null default '#3B1D22',
  highlights text[] not null default '{}',
  owner_name text not null default '',
  created_at timestamptz not null default now()
);

create table locations (
  id uuid primary key default gen_random_uuid(),
  business_id uuid not null references businesses(id) on delete cascade,
  name text not null,
  address text not null default '',
  city text not null default '',
  region text not null default '',
  country text not null,
  lat double precision,
  lng double precision,
  timezone text not null,
  is_primary boolean not null default false,
  -- {"mon":null,"tue":["09:00","18:00"],...}
  hours jsonb not null default '{}'::jsonb,
  arrival_notes text not null default ''
);

create table staff (
  id uuid primary key default gen_random_uuid(),
  business_id uuid not null references businesses(id) on delete cascade,
  name text not null,
  initials text not null,
  role text not null default 'staff',
  level text not null default 'senior',
  tone text not null default '#7A1F2B',
  bookable boolean not null default true,
  hours jsonb,
  rating numeric(3,2) not null default 0,
  created_at timestamptz not null default now()
);

create table services (
  id uuid primary key default gen_random_uuid(),
  business_id uuid not null references businesses(id) on delete cascade,
  name text not null,
  category text not null,
  description text not null default '',
  duration_min int not null,
  processing_min int not null default 0,
  buffer_min int not null default 0,
  price_cents int not null,
  deposit_cents int not null default 0,
  online boolean not null default true,
  sort int not null default 0,
  created_at timestamptz not null default now()
);

create table staff_services (
  staff_id uuid not null references staff(id) on delete cascade,
  service_id uuid not null references services(id) on delete cascade,
  price_cents int,
  primary key (staff_id, service_id)
);

create table clients (
  id uuid primary key default gen_random_uuid(),
  business_id uuid not null references businesses(id) on delete cascade,
  user_id uuid references users(id),
  name text not null,
  phone text not null default '',
  notes text not null default '',
  tags text[] not null default '{}',
  no_show_count int not null default 0,
  created_at timestamptz not null default now(),
  unique (business_id, phone)
);

create table bookings (
  id uuid primary key default gen_random_uuid(),
  business_id uuid not null references businesses(id) on delete cascade,
  location_id uuid references locations(id),
  staff_id uuid not null references staff(id),
  client_id uuid references clients(id),
  client_name text not null,
  client_phone text not null default '',
  status text not null default 'confirmed' check (status in ('requested','confirmed','checked_in','in_progress','completed','paid','cancelled_client','cancelled_business','no_show','rescheduled')),
  starts_at timestamptz not null,
  ends_at timestamptz not null,
  source text not null default 'web',
  total_cents int not null default 0,
  deposit_cents int not null default 0,
  deposit_paid boolean not null default false,
  notes text not null default '',
  created_at timestamptz not null default now(),
  check (ends_at > starts_at),
  -- The database itself refuses a double booking for one staff member.
  exclude using gist (staff_id with =, tstzrange(starts_at, ends_at) with &&)
    where (status in ('confirmed','checked_in','in_progress'))
);
create index bookings_business_day on bookings (business_id, starts_at);

create table booking_items (
  id uuid primary key default gen_random_uuid(),
  booking_id uuid not null references bookings(id) on delete cascade,
  service_id uuid references services(id),
  name text not null,
  price_cents int not null,
  duration_min int not null
);

create table reviews (
  id uuid primary key default gen_random_uuid(),
  business_id uuid not null references businesses(id) on delete cascade,
  booking_id uuid unique references bookings(id),
  author_name text not null,
  service_name text not null default '',
  rating int not null check (rating between 1 and 5),
  body text not null,
  status text not null default 'published' check (status in ('published','flagged','hidden','removed')),
  flag_reason text not null default '',
  reply text not null default '',
  created_at timestamptz not null default now()
);

create table products (
  id uuid primary key default gen_random_uuid(),
  business_id uuid references businesses(id) on delete set null,
  seller_name text not null,
  slug text not null unique,
  name text not null,
  description text not null default '',
  how_to_use text not null default '',
  category text not null,
  price_cents int not null,
  compare_cents int,
  stock int not null default 0,
  -- [{"label":"60 ml","price_cents":1800}]
  sizes jsonb not null default '[]'::jsonb,
  tone text not null default '#3B1D22',
  tags text[] not null default '{}',
  rating numeric(3,2) not null default 0,
  review_count int not null default 0,
  sold int not null default 0,
  pickup boolean not null default true,
  shipping boolean not null default true,
  shipping_cents int not null default 499,
  created_at timestamptz not null default now()
);

create table orders (
  id uuid primary key default gen_random_uuid(),
  user_id uuid references users(id),
  customer_name text not null,
  customer_phone text not null default '',
  status text not null default 'paid' check (status in ('pending','paid','ready','shipped','delivered','cancelled','refunded')),
  fulfilment text not null check (fulfilment in ('pickup','ship')),
  subtotal_cents int not null,
  shipping_cents int not null default 0,
  tax_cents int not null default 0,
  credit_cents int not null default 0,
  total_cents int not null,
  address jsonb,
  created_at timestamptz not null default now()
);

create table order_items (
  id uuid primary key default gen_random_uuid(),
  order_id uuid not null references orders(id) on delete cascade,
  product_id uuid references products(id),
  seller_name text not null,
  name text not null,
  size_label text not null default '',
  qty int not null check (qty > 0),
  unit_cents int not null
);

create table verification_requests (
  id uuid primary key default gen_random_uuid(),
  business_id uuid not null references businesses(id) on delete cascade,
  status text not null default 'pending' check (status in ('pending','needs_info','approved','rejected')),
  risk_score int not null default 0,
  id_type text not null default '',
  id_provider text not null default '',
  licence_status text not null default 'not_required',
  portfolio_note text not null default '',
  decision_note text not null default '',
  decided_by text,
  decided_at timestamptz,
  created_at timestamptz not null default now()
);

create table disputes (
  id uuid primary key default gen_random_uuid(),
  ref text not null unique,
  business_id uuid not null references businesses(id) on delete cascade,
  booking_id uuid references bookings(id),
  client_name text not null,
  amount_cents int not null,
  currency text not null,
  reason text not null,
  client_statement text not null default '',
  business_statement text not null default '',
  status text not null default 'with_business' check (status in ('with_business','needs_decision','resolved','out_of_scope')),
  outcome text,
  outcome_cents int,
  decision_note text not null default '',
  business_deadline timestamptz not null default now() + interval '48 hours',
  created_at timestamptz not null default now(),
  resolved_at timestamptz
);

create table fees (
  market text not null,
  plan text not null,
  transaction_pct numeric(5,2) not null,
  transaction_fixed_cents int not null default 0,
  transaction_cap_cents int,
  new_client_pct numeric(5,2) not null,
  instant_payout_pct numeric(5,2) not null default 1,
  instant_payout_min_cents int not null default 50,
  marketplace_pct numeric(5,2) not null,
  chargeback_cents int not null,
  plan_price_cents int not null default 0,
  effective_from date not null default current_date,
  approved_by text,
  primary key (market, plan, effective_from)
);

create table feature_flags (
  key text primary key,
  name text not null,
  description text not null default '',
  rollout_pct int not null default 0 check (rollout_pct between 0 and 100),
  market text,
  plan text,
  enabled boolean not null default false,
  updated_at timestamptz not null default now()
);

create table audit_log (
  id bigserial primary key,
  actor text not null,
  action text not null,
  target text not null default '',
  before jsonb,
  after jsonb,
  created_at timestamptz not null default now()
);

create table payment_events (
  id uuid primary key default gen_random_uuid(),
  provider text not null,
  kind text not null,
  booking_id uuid references bookings(id),
  order_id uuid references orders(id),
  amount_cents int not null,
  currency text not null,
  status text not null,
  reference text not null default '',
  created_at timestamptz not null default now()
);
