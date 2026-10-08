-- Real payments through Stripe (United States) and Paystack (Nigeria).
-- One row per thing a client is asked to pay online: a booking deposit, a
-- shop order, or a sale the desk sent as a pay link.
create table if not exists payments (
  id uuid primary key default gen_random_uuid(),
  reference text not null unique,
  provider text not null check (provider in ('stripe', 'paystack')),
  purpose text not null check (purpose in ('deposit', 'order', 'sale')),
  business_id uuid references businesses(id) on delete cascade,
  booking_id uuid references bookings(id) on delete set null,
  order_id uuid references orders(id) on delete set null,
  sale_id uuid references sales(id) on delete set null,
  -- For a pay link: the checkout request to run once the money has arrived, and who asked for it.
  payload jsonb,
  merchant_id uuid,
  amount_cents int not null check (amount_cents > 0),
  currency text not null,
  email text not null default '',
  description text not null default '',
  status text not null default 'pending' check (status in ('pending', 'paid', 'failed', 'expired', 'refunded')),
  provider_id text not null default '',
  payment_intent text not null default '',
  url text not null default '',
  refunded_cents int not null default 0,
  problem text not null default '',
  created_at timestamptz not null default now(),
  paid_at timestamptz,
  expires_at timestamptz not null default now() + interval '30 minutes'
);
create index if not exists payments_pending on payments (status, created_at) where status = 'pending';
create index if not exists payments_booking on payments (booking_id) where booking_id is not null;
create index if not exists payments_business on payments (business_id, created_at desc);

-- A payout that the provider is still sending.
alter table payouts drop constraint if exists payouts_status_check;
alter table payouts add constraint payouts_status_check check (status in ('scheduled', 'sending', 'paid', 'failed', 'held'));

-- Deleting a whole business must not try to move its stock to a location that is going too.
create or replace function move_stock_on_location_delete() returns trigger language plpgsql as $$
declare main uuid;
begin
  if not exists (select 1 from businesses where id = old.business_id) then
    return old;
  end if;
  select id into main from locations where business_id = old.business_id and is_primary and id <> old.id limit 1;
  if main is not null then
    insert into location_stock (product_id, location_id, qty)
    select product_id, main, qty from location_stock where location_id = old.id
    on conflict (product_id, location_id) do update set qty = location_stock.qty + excluded.qty;
  end if;
  return old;
end $$;
