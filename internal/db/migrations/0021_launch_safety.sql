-- Launch safety: customers confirm their email address, and people who run a
-- business can add a second step to signing in.

alter table users add column if not exists email_verified_at timestamptz;

-- One row per confirmation link sent. The link carries the address it was sent
-- to, so a link for an old address cannot confirm a new one.
create table if not exists user_email_tokens (
  token_hash text primary key,
  user_id    uuid not null references users(id) on delete cascade,
  email      text not null,
  expires_at timestamptz not null,
  used_at    timestamptz,
  created_at timestamptz not null default now()
);
create index if not exists user_email_tokens_user on user_email_tokens (user_id, created_at desc);

alter table merchant_users
  add column if not exists totp_secret text,
  add column if not exists totp_enabled boolean not null default false,
  add column if not exists recovery_codes text[] not null default '{}'; -- sha256 hashes, each usable once
