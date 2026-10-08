-- The lead system: how LogaLuxe earns when it brings a business a new client.
--
-- A lead is a client who found the business through LogaLuxe (search, a
-- category page, the app) and had never booked there before. The business
-- pays once, a share of that first visit, and only when the visit is paid
-- for. Clients who come through the business's own link are never leads.

alter table fees add column if not exists new_client_cap_cents int;
update fees set new_client_cap_cents = case when market = 'US' then 6000 else 2500000 end where new_client_cap_cents is null;

-- The share can differ by kind of business, on top of the rate for the plan.
create table if not exists lead_category_rates (
  market text not null,
  category text not null,
  delta_pct numeric(5,2) not null check (delta_pct between -20 and 30),
  updated_by text not null default '',
  updated_at timestamptz not null default now(),
  primary key (market, category)
);

create table if not exists leads (
  id uuid primary key default gen_random_uuid(),
  business_id uuid not null references businesses(id) on delete cascade,
  client_id uuid references clients(id) on delete set null,
  booking_id uuid references bookings(id) on delete set null,
  sale_id uuid references sales(id) on delete set null,
  client_name text not null default '',
  source text not null,
  status text not null default 'pending' check (status in ('pending', 'charged', 'void', 'disputed', 'refunded')),
  boosted boolean not null default false,
  base_pct numeric(5,2) not null,
  boost_pct numeric(5,2) not null default 0,
  cap_cents int,
  value_cents int not null default 0,
  fee_cents int not null default 0,
  void_reason text not null default '',
  dispute_reason text not null default '',
  disputed_at timestamptz,
  resolved_by text not null default '',
  resolved_at timestamptz,
  resolution_note text not null default '',
  charged_at timestamptz,
  created_at timestamptz not null default now()
);
create unique index if not exists leads_booking on leads (booking_id) where booking_id is not null;
-- A client can only ever be one live lead for a business.
create unique index if not exists leads_one_per_client on leads (business_id, client_id) where client_id is not null and status <> 'void';
create index if not exists leads_business on leads (business_id, created_at desc);

alter table ledger drop constraint if exists ledger_kind_check;
alter table ledger add constraint ledger_kind_check check (kind in ('charge', 'deposit', 'tip', 'refund', 'fee', 'payout', 'payout_fee', 'adjustment', 'lead_fee'));

-- A visit that does not happen is not a lead worth paying for.
create or replace function void_lead_on_cancel() returns trigger language plpgsql as $$
begin
  if new.status in ('cancelled_client', 'cancelled_business', 'no_show') and old.status is distinct from new.status then
    update leads set status = 'void', void_reason = case new.status when 'no_show' then 'The client did not come' else 'The booking was cancelled' end
    where booking_id = new.id and status = 'pending';
  end if;
  return new;
end $$;
drop trigger if exists bookings_void_lead on bookings;
create trigger bookings_void_lead after update of status on bookings for each row execute function void_lead_on_cancel();

-- How many people saw a business in LogaLuxe search and opened its page from
-- there, by day. This is the top of the funnel the business is shown.
create table if not exists lead_events (
  business_id uuid not null references businesses(id) on delete cascade,
  kind text not null check (kind in ('impression', 'view', 'promoted_impression')),
  day date not null default current_date,
  n int not null default 0,
  primary key (business_id, kind, day)
);
