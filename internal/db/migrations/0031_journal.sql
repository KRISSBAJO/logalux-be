-- The Journal: articles about beauty, shown on the home screens and at /journal.
-- A published list filters on published_at <= now(), so a scheduled article goes
-- live by itself. The search column is kept by Postgres from the title, the
-- one-line summary and the body.

create table articles (
  id uuid primary key default gen_random_uuid(),
  slug text not null unique check (slug ~ '^[a-z0-9][a-z0-9-]{0,119}$'),
  title text not null,
  dek text not null default '',
  body_md text not null default '',
  cover_media_id uuid null references site_media(id) on delete set null,
  cover_alt text not null default '',
  category text not null check (category in ('hair','braids','barber','nails','lashes','skin','makeup','spa','business','guide')),
  tags text[] not null default '{}',
  author_name text not null default 'LogaLuxe editorial',
  author_role text not null default '',
  author_media_id uuid null references site_media(id) on delete set null,
  country text not null default '' check (country in ('','US','NG')),
  status text not null default 'draft' check (status in ('draft','scheduled','published','archived')),
  published_at timestamptz null,
  featured boolean not null default false,
  sort int not null default 0,
  reading_minutes int not null default 1,
  view_count int not null default 0,
  related_category text null,
  cta_text text not null default '',
  seo_title text not null default '',
  seo_description text not null default '',
  created_by text not null default '',
  created_at timestamptz not null default now(),
  updated_at timestamptz not null default now(),
  search tsvector generated always as (
    setweight(to_tsvector('english', coalesce(title, '')), 'A') ||
    setweight(to_tsvector('english', coalesce(dek, '')), 'B') ||
    setweight(to_tsvector('english', coalesce(body_md, '')), 'C')
  ) stored
);
create index articles_live on articles (status, published_at desc) where status in ('published','scheduled');
create index articles_search on articles using gin (search);
create index articles_tags on articles using gin (tags);

-- A cover can be one of the photos already uploaded for the landing page: two
-- rows then point at the same file in the bucket, so the key is no longer unique.
-- The delete route only removes the file when no other row still uses it.
alter table site_media drop constraint if exists site_media_storage_key_key;
create index if not exists site_media_storage_key on site_media (storage_key);
