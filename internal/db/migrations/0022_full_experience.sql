-- The design items that had nothing real behind them get something real.

-- "1,040 sold" on sample products was an invented number. Count what was really sold:
-- units in shop orders that were paid for, plus units sold at the desk.
update products p set sold =
  coalesce((select sum(oi.qty) from order_items oi join orders o on o.id = oi.order_id
            where oi.product_id = p.id and o.status in ('paid','ready','shipped','delivered')), 0)
  + coalesce((select sum(si.qty) from sale_items si where si.product_id = p.id), 0);

-- What a business tells shoppers and clients about itself.
alter table businesses
  add column if not exists languages text[] not null default '{}',
  add column if not exists returns_days int,                       -- null: no returns policy stated
  add column if not exists returns_note text not null default '',
  add column if not exists ship_days_min int,                      -- null: no delivery estimate stated
  add column if not exists ship_days_max int,
  add column if not exists pickup_ready_mins int;                  -- null: pick-up on the same day is not offered

-- A product's own details. Delivery and returns here are for brand products, which have no business behind them.
alter table products
  add column if not exists ingredients text not null default '',
  add column if not exists ship_days_min int,
  add column if not exists ship_days_max int,
  add column if not exists returns_days int;

-- Products a customer saved.
create table if not exists user_favourite_products (
  user_id    uuid not null references users(id) on delete cascade,
  product_id uuid not null references products(id) on delete cascade,
  created_at timestamptz not null default now(),
  primary key (user_id, product_id)
);

-- A note on one seller's part of an order, such as "collect at your visit on 10 Oct".
alter table order_shipments add column if not exists note text not null default '';

-- Referral credit. Each customer has a code; when a friend who joined with it pays for a first
-- visit or order, both are credited. The amount is set by LogaLuxe staff and starts at nothing,
-- so the programme is off until someone switches it on.
alter table users
  add column if not exists referral_code text,
  add column if not exists referred_by uuid references users(id) on delete set null,
  add column if not exists referral_paid_at timestamptz;
create unique index if not exists users_referral_code on users (referral_code) where referral_code is not null;

create table if not exists user_credits (
  id           uuid primary key default gen_random_uuid(),
  user_id      uuid not null references users(id) on delete cascade,
  amount_cents int not null,              -- positive: earned; negative: spent
  currency     text not null default 'USD',
  reason       text not null,
  order_id     uuid references orders(id) on delete set null,
  about_user   uuid references users(id) on delete set null,
  created_at   timestamptz not null default now()
);
create index if not exists user_credits_user on user_credits (user_id, created_at desc);

create table if not exists platform_settings (
  key        text primary key,
  value      jsonb not null,
  updated_by text not null default '',
  updated_at timestamptz not null default now()
);
insert into platform_settings (key, value) values ('referral_credit_cents', '0') on conflict do nothing;

-- The sample businesses state the kind of policy a real one would, so the screens have something to show.
update businesses set languages = '{English}', returns_days = 14, returns_note = 'Unopened items only, refunded to the way you paid.', ship_days_min = 2, ship_days_max = 4, pickup_ready_mins = 30
  where slug = 'ada' and exists (select 1 from sales where created_by = 'sample data');
update businesses set languages = '{English}' where slug in ('nia','barberloft','glow','freedomway') and languages = '{}' and exists (select 1 from sales where created_by = 'sample data');
update businesses set languages = '{English,Yoruba,Pidgin}' where slug in ('mnm','tiwa') and languages = '{}' and exists (select 1 from sales where created_by = 'sample data');
