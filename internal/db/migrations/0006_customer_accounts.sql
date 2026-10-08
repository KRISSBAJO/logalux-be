-- Customer accounts: sign up with an email and password, then see your own
-- bookings and orders. Booking as a guest still works.

-- A phone number typed at sign-up is not proven, so it cannot be unique:
-- otherwise anyone could claim someone else's number.
alter table users drop constraint if exists users_phone_key;
alter table users
  add column password_hash text,
  add column failed_logins int not null default 0,
  add column locked_until timestamptz,
  add column last_login_at timestamptz;
create unique index users_email_account on users (lower(email)) where password_hash is not null;

-- Only a hash of each token is stored.
create table user_sessions (
  token_hash text primary key,
  user_id uuid not null references users(id) on delete cascade,
  expires_at timestamptz not null,
  created_at timestamptz not null default now()
);
create index user_sessions_user on user_sessions (user_id);

create table user_password_resets (
  token_hash text primary key,
  user_id uuid not null references users(id) on delete cascade,
  expires_at timestamptz not null,
  used_at timestamptz,
  created_at timestamptz not null default now()
);

-- A booking made while signed in belongs to that account. Orders already have user_id.
alter table bookings add column user_id uuid references users(id) on delete set null;
create index bookings_user on bookings (user_id, starts_at desc) where user_id is not null;
create index orders_user on orders (user_id, created_at desc) where user_id is not null;
