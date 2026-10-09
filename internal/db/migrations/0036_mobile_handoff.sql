-- Short-lived, single-use exchange codes. Long-lived native tokens never enter a URL.
create table customer_web_handoffs (
 code_hash text primary key,
 user_id uuid not null references users(id) on delete cascade,
 source_session_hash text not null references user_sessions(token_hash) on delete cascade, return_path text not null,
 expires_at timestamptz not null
);
create index customer_web_handoffs_expiry on customer_web_handoffs(expires_at);
