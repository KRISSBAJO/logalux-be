-- Account security, promo codes, gift cards, the support inbox, bulk messages
-- and editable legal pages.

-- ---------- admin account security ----------
alter table admin_users
  add column totp_secret text,
  add column totp_enabled boolean not null default false,
  add column recovery_codes text[] not null default '{}', -- sha256 hashes, each usable once
  add column password_changed_at timestamptz;

-- Only a hash of the reset token is stored. A link works once, for 30 minutes.
create table admin_password_resets (
  token_hash text primary key,
  admin_id uuid not null references admin_users(id) on delete cascade,
  expires_at timestamptz not null,
  used_at timestamptz,
  created_at timestamptz not null default now()
);

-- ---------- promo codes ----------
create table promo_codes (
  id uuid primary key default gen_random_uuid(),
  code text not null unique check (code = upper(code)),
  description text not null default '',
  kind text not null check (kind in ('percent','fixed')),
  value int not null check (value > 0),          -- a percentage, or minor units
  currency text not null default 'USD',          -- only matters for a fixed amount
  applies_to text not null default 'both' check (applies_to in ('orders','bookings','both')),
  min_cents int not null default 0,
  max_uses int,
  used int not null default 0,
  starts_at timestamptz,
  ends_at timestamptz,
  active boolean not null default true,
  created_by text not null,
  created_at timestamptz not null default now(),
  check (kind <> 'percent' or value <= 100)
);

-- ---------- gift cards ----------
create table gift_cards (
  id uuid primary key default gen_random_uuid(),
  code text not null unique,
  initial_cents int not null check (initial_cents > 0),
  balance_cents int not null check (balance_cents >= 0),
  currency text not null default 'USD',
  recipient_name text not null default '',
  recipient_email text not null default '',
  note text not null default '',
  status text not null default 'active' check (status in ('active','void')),
  expires_on date,
  issued_by text not null,
  created_at timestamptz not null default now()
);
-- Every change to a balance, so a card can always be explained.
create table gift_card_txns (
  id uuid primary key default gen_random_uuid(),
  gift_card_id uuid not null references gift_cards(id) on delete cascade,
  order_id uuid references orders(id) on delete set null,
  amount_cents int not null,                     -- negative when spent
  note text not null default '',
  actor text not null default '',
  created_at timestamptz not null default now()
);
create index gift_card_txns_card on gift_card_txns (gift_card_id, created_at);

alter table orders
  add column promo_code text not null default '',
  add column discount_cents int not null default 0,
  add column gift_code text not null default '',
  add column gift_cents int not null default 0;
alter table bookings
  add column promo_code text not null default '',
  add column discount_cents int not null default 0;

-- ---------- support inbox ----------
create sequence support_ref_seq start 1001;
create table support_tickets (
  id uuid primary key default gen_random_uuid(),
  ref text not null unique default ('SP-' || nextval('support_ref_seq')),
  name text not null,
  email text not null default '',
  phone text not null default '',
  role text not null default 'client' check (role in ('client','business','other')),
  business_id uuid references businesses(id) on delete set null,
  subject text not null,
  status text not null default 'open' check (status in ('open','waiting','closed')),
  priority text not null default 'normal' check (priority in ('normal','urgent')),
  assigned_to text not null default '',
  created_at timestamptz not null default now(),
  updated_at timestamptz not null default now()
);
create index support_tickets_status on support_tickets (status, updated_at desc);
create table support_messages (
  id uuid primary key default gen_random_uuid(),
  ticket_id uuid not null references support_tickets(id) on delete cascade,
  author text not null,
  from_staff boolean not null default false,
  internal boolean not null default false,       -- a staff note the customer never sees
  delivery text not null default '',             -- sent, logged, failed, or empty
  body text not null,
  created_at timestamptz not null default now()
);
create index support_messages_ticket on support_messages (ticket_id, created_at);

-- ---------- bulk messages ----------
create table broadcasts (
  id uuid primary key default gen_random_uuid(),
  audience text not null check (audience in ('businesses','clients')),
  market text not null default '',
  plan text not null default '',
  channel text not null check (channel in ('email','whatsapp','sms')),
  subject text not null default '',
  body text not null,
  status text not null default 'draft' check (status in ('draft','sending','sent')),
  recipients int not null default 0,
  delivered int not null default 0,
  logged int not null default 0,
  skipped int not null default 0,
  failed int not null default 0,
  created_by text not null,
  sent_by text not null default '',
  created_at timestamptz not null default now(),
  sent_at timestamptz
);

-- ---------- legal and help pages ----------
create table site_pages (
  slug text primary key,
  title text not null,
  body text not null default '',
  published boolean not null default true,
  updated_by text not null default 'system',
  updated_at timestamptz not null default now()
);
insert into site_pages (slug, title, body) values
('terms', 'Terms of service', E'## Draft: have a lawyer review this page before launch\n\nThese terms explain how LogaLuxe works for clients and for the professionals who list on it.\n\n## Booking\n\nA booking is an agreement between you and the business. LogaLuxe shows real openings and confirms your slot. The business provides the service.\n\n## Payments and deposits\n\nPrices are shown before you confirm. A deposit holds your slot and comes off the total. Each business sets its own cancellation policy, which you see before you book.\n\n## Reviews\n\nYou can review a visit you completed and paid for. We remove reviews that attack a person, are off topic, or were not written by the client.\n\n## Contact\n\nQuestions about these terms go to the help page.'),
('privacy', 'Privacy policy', E'## Draft: have a lawyer review this page before launch\n\nWe collect what we need to run your bookings and nothing more.\n\n## What we collect\n\n- Your name and phone number, to hold a booking and send reminders\n- Your booking and order history\n- Messages you send to support\n\n## What we do not do\n\n- We do not store card numbers. Payment partners handle them.\n- We do not sell your details.\n\n## Your choices\n\nYou can ask for a copy of your data or ask us to delete it from the help page.'),
('cancellation', 'Cancellation policy', E'## Draft: have a lawyer review this page before launch\n\nEach business sets its own cancellation window. You see it before you confirm.\n\n## If you cancel\n\n- Inside the free window: your deposit comes back in full.\n- After the window: the business may keep the deposit.\n\n## If the business cancels\n\nYou get your deposit back in full, every time.\n\n## No-shows\n\nA no-show can cost the deposit. Repeated no-shows can lead to a block on booking.'),
('accessibility', 'Accessibility', E'## Draft: review before launch\n\nWe want everyone to be able to book. The site is built to work with a keyboard and with screen readers, and to respect a reduced-motion setting.\n\nIf something does not work for you, tell us on the help page and we will fix it.');
