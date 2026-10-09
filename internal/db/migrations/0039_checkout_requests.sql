-- A reservation and its sale/response commit together; failed attempts leave no key.
create table checkout_requests (
  business_id uuid not null references businesses(id) on delete cascade,
  request_key text not null,
  merchant_id text not null,
  payload_hash text not null,
  sale_id uuid not null references sales(id),
  response_body text not null,
  created_at timestamptz not null default now(),
  primary key (business_id, request_key)
);
