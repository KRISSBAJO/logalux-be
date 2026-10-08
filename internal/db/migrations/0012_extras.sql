-- The parts of the merchant design that had nothing behind them: rooms and
-- chairs, pricing rules, packages and memberships, pay types, breaks, chair
-- rental, and which products a service uses up.

-- ---------- rooms, chairs and stations ----------
create table if not exists resources (
  id uuid primary key default gen_random_uuid(),
  business_id uuid not null references businesses(id) on delete cascade,
  name text not null,
  qty int not null default 1 check (qty between 1 and 50),
  created_at timestamptz not null default now(),
  unique (business_id, name)
);
create table if not exists service_resources (
  service_id uuid not null references services(id) on delete cascade,
  resource_id uuid not null references resources(id) on delete cascade,
  primary key (service_id, resource_id)
);

-- ---------- pricing rules ----------
-- A rule nudges the menu price up or down when its conditions match: certain
-- days, a time window, a staff level, a date range. Empty means "any".
create table if not exists price_rules (
  id uuid primary key default gen_random_uuid(),
  business_id uuid not null references businesses(id) on delete cascade,
  name text not null,
  service_id uuid references services(id) on delete cascade,
  days text[] not null default '{}',
  from_time time,
  to_time time,
  level text not null default '' check (level in ('', 'junior', 'senior', 'master')),
  starts_on date,
  ends_on date,
  adjust_kind text not null check (adjust_kind in ('amount', 'percent')),
  adjust_value int not null check (adjust_value <> 0),
  active boolean not null default true,
  created_at timestamptz not null default now()
);
create index if not exists price_rules_business on price_rules (business_id) where active;

-- ---------- packages and memberships ----------
create table if not exists packages (
  id uuid primary key default gen_random_uuid(),
  business_id uuid not null references businesses(id) on delete cascade,
  name text not null,
  description text not null default '',
  price_cents int not null check (price_cents >= 0),
  valid_days int not null default 365 check (valid_days between 1 and 1825),
  active boolean not null default true,
  created_at timestamptz not null default now()
);
create table if not exists package_items (
  package_id uuid not null references packages(id) on delete cascade,
  service_id uuid not null references services(id) on delete cascade,
  qty int not null check (qty between 1 and 100),
  primary key (package_id, service_id)
);
create table if not exists memberships (
  id uuid primary key default gen_random_uuid(),
  business_id uuid not null references businesses(id) on delete cascade,
  name text not null,
  description text not null default '',
  price_cents int not null check (price_cents >= 0),
  service_discount_pct int not null default 0 check (service_discount_pct between 0 and 100),
  retail_discount_pct int not null default 0 check (retail_discount_pct between 0 and 100),
  active boolean not null default true,
  created_at timestamptz not null default now()
);
-- What a member may have each month at no extra charge.
create table if not exists membership_items (
  membership_id uuid not null references memberships(id) on delete cascade,
  service_id uuid not null references services(id) on delete cascade,
  qty int not null check (qty between 1 and 100),
  primary key (membership_id, service_id)
);
-- A package or membership that one client holds.
create table if not exists client_plans (
  id uuid primary key default gen_random_uuid(),
  business_id uuid not null references businesses(id) on delete cascade,
  client_id uuid not null references clients(id) on delete cascade,
  kind text not null check (kind in ('package', 'membership')),
  package_id uuid references packages(id) on delete set null,
  membership_id uuid references memberships(id) on delete set null,
  name text not null,
  price_cents int not null default 0,
  status text not null default 'active' check (status in ('active', 'used', 'expired', 'cancelled', 'past_due')),
  sale_id uuid references sales(id) on delete set null,
  started_at timestamptz not null default now(),
  expires_at timestamptz,
  renews_on date,
  cancelled_at timestamptz
);
create index if not exists client_plans_client on client_plans (client_id, status);
create index if not exists client_plans_renewal on client_plans (renews_on) where kind = 'membership' and status = 'active';
create table if not exists client_credits (
  id uuid primary key default gen_random_uuid(),
  plan_id uuid not null references client_plans(id) on delete cascade,
  service_id uuid references services(id) on delete set null,
  service_name text not null default '',
  total int not null check (total > 0),
  used int not null default 0 check (used >= 0),
  expires_at timestamptz,
  check (used <= total)
);
create index if not exists client_credits_plan on client_credits (plan_id);

alter table sale_items drop constraint if exists sale_items_kind_check;
alter table sale_items add constraint sale_items_kind_check check (kind in ('service', 'product', 'custom', 'package', 'membership'));
alter table sale_items add column if not exists credit_id uuid references client_credits(id) on delete set null;
alter table sale_items add column if not exists list_cents int;

-- ---------- how people are paid, breaks, and chair rental ----------
alter table staff add column if not exists pay_type text not null default 'commission' check (pay_type in ('commission', 'hourly', 'salary', 'owner', 'renter'));
alter table staff add column if not exists hourly_cents int not null default 0 check (hourly_cents >= 0);
alter table staff add column if not exists salary_cents int not null default 0 check (salary_cents >= 0);
alter table staff add column if not exists breaks jsonb not null default '{}';
alter table staff add column if not exists rent_cents int not null default 0 check (rent_cents >= 0);
alter table staff add column if not exists rent_period text not null default 'weekly' check (rent_period in ('weekly', 'monthly'));
alter table staff add column if not exists rent_days text[] not null default '{}';
alter table staff add column if not exists trading_name text not null default '';
update staff set pay_type = 'owner' where role = 'owner' and pay_type = 'commission';

create table if not exists rent_charges (
  id uuid primary key default gen_random_uuid(),
  business_id uuid not null references businesses(id) on delete cascade,
  staff_id uuid not null references staff(id) on delete cascade,
  period_start date not null,
  period_end date not null,
  amount_cents int not null,
  status text not null default 'due' check (status in ('due', 'paid', 'waived')),
  method text not null default '',
  note text not null default '',
  paid_at timestamptz,
  created_at timestamptz not null default now(),
  unique (staff_id, period_start)
);

-- ---------- what a service uses up ----------
create table if not exists service_products (
  service_id uuid not null references services(id) on delete cascade,
  product_id uuid not null references products(id) on delete cascade,
  qty numeric(8,2) not null default 1 check (qty > 0 and qty <= 100),
  primary key (service_id, product_id)
);
-- A full shelf, for the stock meter. And the part of an opened unit already used.
alter table products add column if not exists par_level int not null default 0 check (par_level >= 0);
alter table products add column if not exists backbar_open numeric(8,2) not null default 0;

-- The day the morning summary email last went out, so it goes once a day.
alter table businesses add column if not exists last_summary_on date;
alter table products add column if not exists low_notified_on date;
