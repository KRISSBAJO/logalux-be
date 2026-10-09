-- API acceptance is not proof of delivery. Keep queued RelyKit mail separate.
alter table campaigns add column queued int not null default 0;
alter table broadcasts add column queued int not null default 0;
