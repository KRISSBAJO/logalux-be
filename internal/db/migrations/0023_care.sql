-- After the sale: returns, problems with a visit, tips, gift cards bought by customers, and tax by state.

alter table payments drop constraint if exists payments_purpose_check;
alter table payments add constraint payments_purpose_check check (purpose in ('deposit', 'order', 'sale', 'gift', 'tip'));

-- Sales tax where LogaLuxe itself is the seller (brand products), by the state an order ships to.
-- A business's own products use that business's rate (businesses.sales_tax_bp). A state not listed is not taxed.
create table if not exists tax_rates (
  region     text primary key,            -- two-letter state code, upper case
  name       text not null default '',
  bp         int not null check (bp between 0 and 2500),  -- hundredths of a percent: 925 is 9.25%
  updated_by text not null default '',
  updated_at timestamptz not null default now()
);
insert into tax_rates (region, name, bp) values ('TN', 'Tennessee', 925) on conflict do nothing;

-- A customer asks to send back one seller's part of an order.
create table if not exists order_returns (
  id           uuid primary key default gen_random_uuid(),
  order_id     uuid not null references orders(id) on delete cascade,
  shipment_id  uuid not null references order_shipments(id) on delete cascade,
  business_id  uuid references businesses(id) on delete cascade,   -- null: a brand's product, decided by LogaLuxe staff
  user_id      uuid references users(id) on delete set null,
  reason       text not null check (reason in ('damaged', 'wrong_item', 'not_as_described', 'changed_mind', 'other')),
  note         text not null default '',
  status       text not null default 'requested' check (status in ('requested', 'approved', 'refused')),
  refund_cents int not null default 0,
  credit_cents int not null default 0,     -- the part given back as store credit, when it was paid with credit or a gift card
  restocked    boolean not null default false,
  reply        text not null default '',
  decided_by   text not null default '',
  decided_at   timestamptz,
  created_at   timestamptz not null default now()
);
create unique index if not exists order_returns_one on order_returns (shipment_id);
create index if not exists order_returns_business on order_returns (business_id, status, created_at desc);

-- A problem a client reports about a visit goes into the disputes the console already handles.
alter table disputes add column if not exists user_id uuid references users(id) on delete set null;
alter table disputes add column if not exists raised_by text not null default 'staff';
