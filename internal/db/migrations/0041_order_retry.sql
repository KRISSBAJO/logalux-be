-- The request identity is committed with the order, so a lost response is recoverable without a second order.
alter table orders add column if not exists request_key_hash text;
alter table orders add column if not exists request_body_hash text;
create unique index if not exists orders_request_key_unique on orders(request_key_hash) where request_key_hash is not null;
