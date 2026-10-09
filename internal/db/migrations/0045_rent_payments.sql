alter table payments drop constraint if exists payments_purpose_check;
alter table payments add constraint payments_purpose_check check (purpose in ('deposit','order','sale','gift','tip','rent'));
alter table rent_charges add column payment_reference text references payments(reference);
alter table ledger add column rent_charge_id uuid references rent_charges(id);
create unique index ledger_rent_once on ledger(rent_charge_id,kind) where rent_charge_id is not null;
