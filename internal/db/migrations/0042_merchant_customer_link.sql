create table merchant_customer_links (
 merchant_id uuid primary key references merchant_users(id) on delete cascade,
 user_id uuid not null unique references users(id) on delete cascade,
 created_at timestamptz not null default now()
);
