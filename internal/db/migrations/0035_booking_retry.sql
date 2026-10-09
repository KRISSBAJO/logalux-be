-- The request identity is committed with the booking, so a lost response is recoverable.
alter table bookings add column request_key_hash text;
alter table bookings add column request_body_hash text;
create unique index bookings_request_key_unique on bookings(request_key_hash) where request_key_hash is not null;
