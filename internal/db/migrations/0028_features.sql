-- Features an admin can switch on and off: sign-in by texted code, texts and WhatsApp to clients,
-- Apple Pay and Google Pay on the payment page, and saved cards.
insert into platform_settings (key, value) values ('features', '{}') on conflict do nothing;

alter table users
  add column if not exists phone_verified_at timestamptz,   -- set when the person typed a code we sent to this number
  add column if not exists stripe_customer_id text;         -- where Stripe keeps this person's saved cards

-- Codes sent to a phone: to sign in, or to confirm the number on an account.
create table if not exists login_codes (
  id         uuid primary key default gen_random_uuid(),
  phone      text not null,
  purpose    text not null check (purpose in ('login','verify')),
  code_hash  text not null,
  attempts   int not null default 0,
  expires_at timestamptz not null,
  created_at timestamptz not null default now()
);
create index if not exists login_codes_phone on login_codes (phone, created_at desc);

-- Cards a customer chose to keep, for the provider that hands us a reusable token (Paystack).
-- Stripe keeps its own list against the customer, so nothing of a Stripe card is stored here.
create table if not exists user_cards (
  id         uuid primary key default gen_random_uuid(),
  user_id    uuid not null references users(id) on delete cascade,
  provider   text not null,
  token      text not null,          -- the provider's reusable authorisation, never a card number
  email      text not null,          -- the address the provider ties the authorisation to
  brand      text not null default '',
  last4      text not null default '',
  exp_month  text not null default '',
  exp_year   text not null default '',
  created_at timestamptz not null default now(),
  unique (user_id, provider, token)
);

alter table payments add column if not exists user_id uuid references users(id) on delete set null;

-- A business that is deleted takes its products with it. Before, they stayed in the shop with no owner.
alter table products drop constraint if exists products_business_id_fkey;
alter table products add constraint products_business_id_fkey foreign key (business_id) references businesses(id) on delete cascade;
