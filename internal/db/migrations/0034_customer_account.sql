alter table users add column deleted_at timestamptz;
alter table user_sessions add column device_name text not null default '';
alter table user_sessions add column last_seen_at timestamptz not null default now();
alter table user_sessions add column id uuid not null default gen_random_uuid();
create unique index user_sessions_public_id on user_sessions(id);
create index bookings_customer_history on bookings(user_id, starts_at desc, id);
