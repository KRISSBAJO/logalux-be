-- Places come from data. A business can be anywhere in the United States or
-- Nigeria; search goes by distance; a location's time zone comes from where it is.

-- A business that goes to its clients has no shop front. Its position is the
-- area it works from, and it may say how far it travels.
alter table locations add column travels boolean not null default false;
alter table locations add column travel_radius_km integer check (travel_radius_km is null or travel_radius_km between 1 and 500);
-- The county, when the address was found on the map. It settles the time zone in the states two zones share.
alter table locations add column county text not null default '';
-- How the position was reached: 'address' (found from the street address), 'city' (only the city was found,
-- so the pin is the city's centre), 'hand' (someone placed it), or '' (none yet).
alter table locations add column position_source text not null default '' check (position_source in ('', 'address', 'city', 'hand'));

-- Until now a travelling business was only marked in its location's name.
update locations l set travels = true
from businesses b
where b.id = l.business_id and (l.name ilike '%(mobile)%' or (btrim(l.address) = '' and b.tagline ilike '%mobile%'));
-- Existing positions were placed by hand (the samples) or found from an address.
update locations set position_source = 'hand' where lat is not null and lng is not null;

create index if not exists locations_business on locations (business_id);
create index if not exists locations_place on locations (country, region, lower(city));

-- Places a person has looked for and the lookup service found, so the picker can offer them again at once.
create table geo_places (
  slug       text primary key,            -- nashville-tn, port-harcourt-rivers
  city       text not null,
  region     text not null,               -- TN in the United States, the state's name in Nigeria
  country    text not null,
  county     text not null default '',
  lat        double precision not null,
  lng        double precision not null,
  created_at timestamptz not null default now()
);
create index geo_places_city on geo_places (lower(city) text_pattern_ops);

-- Every answer from the lookup service, so the same question is never asked twice.
-- kind: 'address' (an address to a position), 'places' (text to places), 'reverse' (a position to a place).
create table geo_cache (
  kind       text not null,
  key        text not null,
  answer     jsonb not null,              -- null when the service found nothing
  created_at timestamptz not null default now(),
  primary key (kind, key)
);

-- Work on existing rows that needs the API's own code and runs once at start-up.
create table geo_tasks (
  name    text primary key,
  done_at timestamptz not null default now(),
  note    text not null default ''
);
