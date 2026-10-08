-- Images the LogaXP team uploads for the public site, such as the landing hero.
-- The bytes live in S3; this table says which image fills which slot.

create table site_media (
  id uuid primary key default gen_random_uuid(),
  slot text not null,
  storage_key text not null unique,
  content_type text not null,
  size_bytes int not null,
  alt text not null default '',
  active boolean not null default true,
  sort int not null default 0,
  uploaded_by text not null,
  created_at timestamptz not null default now()
);
create index site_media_slot on site_media (slot, active, sort, created_at);
