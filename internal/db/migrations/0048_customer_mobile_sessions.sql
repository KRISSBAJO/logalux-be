-- Mobile refresh families belong to a revocable device session. Deleting that
-- session (logout, password reset, device revocation) deletes all refresh tokens.
create table customer_refresh_tokens (
 token_hash text primary key,
 session_id uuid not null references user_sessions(id) on delete cascade,
 used_at timestamptz,
 refresh_request_hash text,
 idle_expires_at timestamptz not null,
 absolute_expires_at timestamptz not null
);
create index customer_refresh_session on customer_refresh_tokens(session_id);
alter table user_sessions add column security_verified_until timestamptz,
 add column security_verified_method text,
 add column security_attempts int not null default 0;
alter table users add column security_pin_hash text,
 add column security_pin_attempts int not null default 0,
 add column security_pin_locked_until timestamptz;
create table customer_security_codes (
 session_id uuid primary key references user_sessions(id) on delete cascade,
 code_hash text not null,
 expires_at timestamptz not null,
 sent_at timestamptz not null default now()
);
