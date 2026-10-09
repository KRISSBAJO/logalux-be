alter table campaigns drop constraint campaigns_status_check;
alter table campaigns add constraint campaigns_status_check check (status in ('draft','waiting','sending','sent','stalled'));
alter table campaigns add column uncertain int not null default 0;
alter table campaigns add column pending int not null default 0;
alter table campaigns add column stalled_reason text not null default '';
-- Old processes had no durable snapshot or attempt marker. Never replay them.
update campaigns set status='stalled', stalled_reason='Legacy campaign interrupted; delivery outcomes cannot be reconstructed safely.' where status='sending';

create table campaign_jobs (
  id uuid primary key default gen_random_uuid(),
  campaign_id uuid not null references campaigns(id) on delete cascade,
  client_id uuid not null, -- retain snapshot even after client deletion
  email text not null,
  phone text not null,
  subject text not null,
  body text not null,
  business_name text not null,
  status text not null default 'pending' check (status in ('pending','sending','delivered','queued','logged','skipped','failed','uncertain')),
  lease_until timestamptz,
  completed_at timestamptz,
  unique(campaign_id,client_id)
);
create index campaign_jobs_pending on campaign_jobs(id) where status='pending';
create index campaign_jobs_leases on campaign_jobs(lease_until) where status='sending';
