-- The monthly fee for the Pro plan, cards saved for membership renewals, and
-- a count of AI drafts per business per day.

alter table ledger drop constraint if exists ledger_kind_check;
alter table ledger add constraint ledger_kind_check check (kind in ('charge', 'deposit', 'tip', 'refund', 'fee', 'payout', 'payout_fee', 'adjustment', 'lead_fee', 'plan_fee'));

-- The Pro fee comes out of the payout balance once a month. paid_through is
-- the last day covered; due_since is set while there has not been enough in
-- the balance to take it.
alter table businesses add column if not exists plan_paid_through date;
alter table businesses add column if not exists plan_due_since date;

-- The card a member paid with, kept as the provider's own reference (never the card number), so
-- the monthly renewal can be charged without the client being there.
alter table client_plans add column if not exists pay_provider text not null default '';
alter table client_plans add column if not exists pay_customer text not null default '';
alter table client_plans add column if not exists pay_method text not null default '';
alter table client_plans add column if not exists pay_email text not null default '';
alter table client_plans add column if not exists charge_problem text not null default '';
alter table payments add column if not exists save_card boolean not null default false;

create table if not exists ai_usage (
  business_id uuid not null references businesses(id) on delete cascade,
  day date not null default current_date,
  n int not null default 0,
  primary key (business_id, day)
);
