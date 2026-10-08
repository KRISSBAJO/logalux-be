-- Everything the merchant web needs: business accounts, rosters and time off,
-- checkout and the money ledger, payout accounts, inventory, the inbox, and
-- marketing.

-- ---------- business accounts ----------
create table merchant_users (
  id uuid primary key default gen_random_uuid(),
  email text not null,
  name text not null,
  phone text not null default '',
  password_hash text not null,
  failed_logins int not null default 0,
  locked_until timestamptz,
  last_login_at timestamptz,
  created_at timestamptz not null default now()
);
create unique index merchant_users_email on merchant_users (lower(email));

-- One person can belong to several businesses; a session remembers which one is open.
create table merchant_members (
  merchant_id uuid not null references merchant_users(id) on delete cascade,
  business_id uuid not null references businesses(id) on delete cascade,
  role text not null check (role in ('owner','manager','staff')),
  staff_id uuid references staff(id) on delete set null,
  created_at timestamptz not null default now(),
  primary key (merchant_id, business_id)
);
create table merchant_sessions (
  token_hash text primary key,
  merchant_id uuid not null references merchant_users(id) on delete cascade,
  business_id uuid references businesses(id) on delete cascade,
  expires_at timestamptz not null,
  created_at timestamptz not null default now()
);
create table merchant_password_resets (
  token_hash text primary key,
  merchant_id uuid not null references merchant_users(id) on delete cascade,
  expires_at timestamptz not null,
  used_at timestamptz,
  created_at timestamptz not null default now()
);

-- ---------- business settings ----------
alter table businesses
  add column settings jsonb not null default '{}'::jsonb,   -- policies, booking rules, storefront display, notifications
  add column tiktok text not null default '',
  add column website text not null default '',
  add column payout_schedule text not null default 'daily' check (payout_schedule in ('daily','weekly','manual')),
  add column sales_tax_bp int not null default 925;          -- basis points on retail; 925 = 9.25%

-- ---------- team, rosters, time off ----------
alter table staff
  add column email text not null default '',
  add column phone text not null default '',
  add column commission_pct numeric(5,2) not null default 40,
  add column retail_commission_pct numeric(5,2) not null default 10,
  add column permissions jsonb not null default '{}'::jsonb,
  add column archived boolean not null default false;

create table time_off (
  id uuid primary key default gen_random_uuid(),
  business_id uuid not null references businesses(id) on delete cascade,
  staff_id uuid not null references staff(id) on delete cascade,
  starts_on date not null,
  ends_on date not null,
  reason text not null default '',
  status text not null default 'requested' check (status in ('requested','approved','declined')),
  decided_by text not null default '',
  created_at timestamptz not null default now(),
  check (ends_on >= starts_on)
);
create index time_off_staff on time_off (staff_id, starts_on);

-- Time a person is not bookable inside working hours: lunch, training, a personal errand.
create table calendar_blocks (
  id uuid primary key default gen_random_uuid(),
  business_id uuid not null references businesses(id) on delete cascade,
  staff_id uuid not null references staff(id) on delete cascade,
  starts_at timestamptz not null,
  ends_at timestamptz not null,
  reason text not null default '',
  created_by text not null default '',
  created_at timestamptz not null default now(),
  check (ends_at > starts_at)
);
create index calendar_blocks_staff on calendar_blocks (staff_id, starts_at);

create table waitlist_entries (
  id uuid primary key default gen_random_uuid(),
  business_id uuid not null references businesses(id) on delete cascade,
  client_name text not null,
  client_phone text not null default '',
  service_id uuid references services(id) on delete set null,
  day date,
  time_of_day text not null default '',
  status text not null default 'waiting' check (status in ('waiting','offered','booked','removed')),
  created_at timestamptz not null default now()
);
create index waitlist_business on waitlist_entries (business_id, status, day);

alter table services add column archived boolean not null default false;

-- ---------- clients ----------
alter table clients
  add column email text not null default '',
  add column birthday date,
  add column marketing_opt_in boolean not null default true,
  add column preferred_channel text not null default 'whatsapp',
  add column consent jsonb not null default '{}'::jsonb;

-- ---------- the visit, from arrival to payment ----------
alter table bookings
  add column checked_in_at timestamptz,
  add column started_at timestamptz,
  add column completed_at timestamptz,
  add column paid_at timestamptz,
  add column tip_cents int not null default 0,
  add column cancel_reason text not null default '',
  add column client_email text not null default '';

create table sales (
  id uuid primary key default gen_random_uuid(),
  business_id uuid not null references businesses(id) on delete cascade,
  booking_id uuid references bookings(id) on delete set null,
  client_id uuid references clients(id) on delete set null,
  client_name text not null default 'Walk-in',
  staff_id uuid references staff(id) on delete set null,
  subtotal_cents int not null,
  discount_cents int not null default 0,
  tax_cents int not null default 0,
  tip_cents int not null default 0,
  deposit_cents int not null default 0,       -- already paid when booking, taken off what is due
  total_cents int not null,                   -- charged now
  method text not null check (method in ('card','tap','cash','transfer','wallet','gift')),
  status text not null default 'paid' check (status in ('paid','refunded','part_refunded')),
  refunded_cents int not null default 0,
  promo_code text not null default '',
  note text not null default '',
  created_by text not null,
  created_at timestamptz not null default now()
);
create index sales_business on sales (business_id, created_at desc);
create unique index sales_one_per_booking on sales (booking_id) where booking_id is not null;

create table sale_items (
  id uuid primary key default gen_random_uuid(),
  sale_id uuid not null references sales(id) on delete cascade,
  kind text not null check (kind in ('service','product','custom')),
  service_id uuid references services(id) on delete set null,
  product_id uuid references products(id) on delete set null,
  staff_id uuid references staff(id) on delete set null,
  name text not null,
  qty int not null default 1 check (qty > 0),
  unit_cents int not null
);
create index sale_items_sale on sale_items (sale_id);

-- Every movement of the business's money is one line here. amount_cents is
-- signed from the business's side: a charge is positive, a fee or payout negative.
-- Cash never reaches LogaLuxe, so cash lines are recorded with in_balance false.
create table ledger (
  id uuid primary key default gen_random_uuid(),
  business_id uuid not null references businesses(id) on delete cascade,
  kind text not null check (kind in ('charge','deposit','tip','refund','fee','payout','payout_fee','adjustment')),
  amount_cents int not null,
  currency text not null,
  method text not null default '',
  status text not null default 'pending' check (status in ('held','pending','settled')),
  in_balance boolean not null default true,
  sale_id uuid references sales(id) on delete set null,
  booking_id uuid references bookings(id) on delete set null,
  payout_id uuid references payouts(id) on delete set null,
  staff_id uuid references staff(id) on delete set null,
  description text not null default '',
  settles_at timestamptz,
  created_at timestamptz not null default now()
);
create index ledger_business on ledger (business_id, created_at desc);
create index ledger_settling on ledger (status, settles_at) where status = 'pending';

create table payout_accounts (
  id uuid primary key default gen_random_uuid(),
  business_id uuid not null references businesses(id) on delete cascade,
  provider text not null check (provider in ('stripe','paystack','flutterwave')),
  status text not null default 'pending' check (status in ('pending','verified','restricted')),
  mode text not null default 'simulation' check (mode in ('simulation','live')),
  bank_name text not null default '',
  bank_code text not null default '',
  account_name text not null default '',
  account_last4 text not null default '',
  currency text not null,
  external_id text not null default '',        -- the provider's own account or recipient id
  is_default boolean not null default true,
  created_at timestamptz not null default now()
);
create index payout_accounts_business on payout_accounts (business_id);

alter table payouts
  add column kind text not null default 'automatic' check (kind in ('automatic','instant','manual')),
  add column fee_cents int not null default 0,
  add column account_id uuid references payout_accounts(id) on delete set null;

-- ---------- inventory ----------
create table suppliers (
  id uuid primary key default gen_random_uuid(),
  business_id uuid not null references businesses(id) on delete cascade,
  name text not null,
  contact text not null default '',
  email text not null default '',
  phone text not null default '',
  created_at timestamptz not null default now()
);
alter table products
  add column sku text not null default '',
  add column cost_cents int not null default 0,
  add column reorder_at int not null default 0,
  add column kind text not null default 'retail' check (kind in ('retail','backbar','both')),
  add column supplier_id uuid references suppliers(id) on delete set null;

create table stock_movements (
  id uuid primary key default gen_random_uuid(),
  business_id uuid not null references businesses(id) on delete cascade,
  product_id uuid not null references products(id) on delete cascade,
  delta int not null,
  reason text not null check (reason in ('sale','restock','adjust','backbar','return','count')),
  note text not null default '',
  actor text not null default '',
  created_at timestamptz not null default now()
);
create index stock_movements_product on stock_movements (product_id, created_at desc);

create sequence purchase_order_seq start 401;
create table purchase_orders (
  id uuid primary key default gen_random_uuid(),
  business_id uuid not null references businesses(id) on delete cascade,
  supplier_id uuid references suppliers(id) on delete set null,
  ref text not null default ('PO-' || lpad(nextval('purchase_order_seq')::text, 4, '0')),
  status text not null default 'draft' check (status in ('draft','ordered','received','cancelled')),
  items jsonb not null default '[]'::jsonb,   -- [{product_id, name, qty, cost_cents}]
  total_cents int not null default 0,
  expected_on date,
  created_by text not null default '',
  created_at timestamptz not null default now(),
  received_at timestamptz
);
create index purchase_orders_business on purchase_orders (business_id, created_at desc);

-- ---------- inbox ----------
create table threads (
  id uuid primary key default gen_random_uuid(),
  business_id uuid not null references businesses(id) on delete cascade,
  client_id uuid references clients(id) on delete set null,
  user_id uuid references users(id) on delete set null,      -- the customer account, for in-app messages
  client_name text not null,
  channel text not null default 'in_app' check (channel in ('in_app','whatsapp','sms','email')),
  status text not null default 'open' check (status in ('open','closed')),
  assigned_staff_id uuid references staff(id) on delete set null,
  booking_id uuid references bookings(id) on delete set null,
  unread_business int not null default 0,
  unread_client int not null default 0,
  last_preview text not null default '',
  last_message_at timestamptz not null default now(),
  created_at timestamptz not null default now()
);
create index threads_business on threads (business_id, last_message_at desc);
create index threads_user on threads (user_id, last_message_at desc) where user_id is not null;

create table thread_messages (
  id uuid primary key default gen_random_uuid(),
  thread_id uuid not null references threads(id) on delete cascade,
  from_business boolean not null,
  author text not null,
  body text not null,
  delivery text not null default '',          -- delivered, logged, failed
  created_at timestamptz not null default now()
);
create index thread_messages_thread on thread_messages (thread_id, created_at);

create table saved_replies (
  id uuid primary key default gen_random_uuid(),
  business_id uuid not null references businesses(id) on delete cascade,
  title text not null,
  body text not null,
  sort int not null default 0
);

-- ---------- marketing ----------
create table automations (
  business_id uuid not null references businesses(id) on delete cascade,
  key text not null check (key in ('confirmation','reminder_24h','reminder_2h','review_request','rebook','win_back','birthday')),
  enabled boolean not null default true,
  message text not null,
  primary key (business_id, key)
);
create table campaigns (
  id uuid primary key default gen_random_uuid(),
  business_id uuid not null references businesses(id) on delete cascade,
  name text not null,
  audience text not null check (audience in ('all','lapsed','new','regulars','birthday')),
  channel text not null default 'email' check (channel in ('email','whatsapp','sms')),
  subject text not null default '',
  message text not null,
  status text not null default 'draft' check (status in ('draft','sending','sent')),
  recipients int not null default 0,
  delivered int not null default 0,
  logged int not null default 0,
  skipped int not null default 0,
  failed int not null default 0,
  created_by text not null,
  created_at timestamptz not null default now(),
  sent_at timestamptz
);
create index campaigns_business on campaigns (business_id, created_at desc);

-- One row per message that went to a client, for the monthly cap and for
-- counting who booked afterwards.
create table message_sends (
  id uuid primary key default gen_random_uuid(),
  business_id uuid not null references businesses(id) on delete cascade,
  client_id uuid references clients(id) on delete cascade,
  booking_id uuid references bookings(id) on delete cascade,
  campaign_id uuid references campaigns(id) on delete cascade,
  automation_key text not null default '',
  marketing boolean not null default false,
  channel text not null,
  status text not null,                       -- delivered, logged, skipped, failed
  created_at timestamptz not null default now()
);
create index message_sends_client on message_sends (client_id, created_at desc);
create index message_sends_campaign on message_sends (campaign_id);
create unique index message_sends_once on message_sends (booking_id, automation_key) where booking_id is not null and automation_key <> '';

alter table reviews
  add column pinned boolean not null default false,
  add column replied_at timestamptz;

-- The 9.25% default is Tennessee's retail rate. Nigerian prices already include VAT.
update businesses set sales_tax_bp = 0 where market = 'NG';

-- Every existing business gets the standard messages and saved replies.
insert into automations (business_id, key, message)
select b.id, v.key, v.message from businesses b cross join (values
  ('confirmation',   'Hi {first name}, you are booked at {business} for {last service} on {time}. Need to change it? {booking link}'),
  ('reminder_24h',   'Hi {first name}, a reminder of your {last service} at {business} tomorrow, {time}. Reply here if anything has changed.'),
  ('reminder_2h',    'See you soon, {first name}. Your {last service} at {business} is at {time}.'),
  ('review_request', 'Thank you for coming in, {first name}. How was your {last service} with {staff}? A short review helps a lot: {booking link}'),
  ('rebook',         'Hi {first name}, it has been a few weeks since your {last service}. Ready for the next one? {booking link}'),
  ('win_back',       'We miss you, {first name}. It has been a while since your last visit to {business}. Book when you are ready: {booking link}'),
  ('birthday',       'Happy birthday, {first name}! Treat yourself this month at {business}: {booking link}')
) as v(key, message)
on conflict do nothing;

insert into saved_replies (business_id, title, body, sort)
select b.id, v.title, v.body, v.sort from businesses b cross join (values
  ('Price list', 'Hi! Our full price list with durations is on our booking page, and you can book straight from there.', 0),
  ('Directions', 'We will send the address and parking notes with your confirmation. Message us if you get lost on the day.', 1),
  ('Deposit policy', 'A deposit holds your slot and comes off your total. It is returned in full if you cancel inside the free window.', 2),
  ('Running late', 'Thanks for letting us know. Please come as soon as you can. If you are more than 15 minutes late we may need to shorten or move the appointment.', 3),
  ('Aftercare', 'Thank you for coming in! Keep it moisturised, sleep in a bonnet, and message us if anything feels too tight.', 4)
) as v(title, body, sort);
