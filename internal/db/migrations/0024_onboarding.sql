-- Getting a new business ready: papers for identity checks.
-- The files live in private storage and are only ever read back by LogaLuxe staff.
create table if not exists verification_documents (
  id           uuid primary key default gen_random_uuid(),
  business_id  uuid not null references businesses(id) on delete cascade,
  kind         text not null check (kind in ('id', 'licence', 'address')),
  file_name    text not null default '',
  storage_key  text not null,
  content_type text not null,
  size_bytes   int not null,
  uploaded_by  text not null default '',
  created_at   timestamptz not null default now()
);
create index if not exists verification_documents_business on verification_documents (business_id, created_at desc);

alter table verification_requests add column if not exists submitted_at timestamptz;
alter table businesses add column if not exists setup_dismissed_at timestamptz;
