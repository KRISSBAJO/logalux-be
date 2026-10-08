-- The first guess of where a visitor is, from their internet address.
-- Only the address prefix is kept (three bytes of an IPv4 address, six of an
-- IPv6 one), never the whole address, and only until it expires.
create table geo_ip (
  prefix     text primary key,            -- 203.0.113.0/24
  answer     jsonb not null,              -- {country, region, city, lat, lng}, or null when nothing is known
  expires_at timestamptz not null,
  created_at timestamptz not null default now()
);
create index geo_ip_expires on geo_ip (expires_at);

-- The privacy page says what is used to place a visitor and what is kept.
-- Like the rest of that page it is a draft for a lawyer to review; staff edit it in the console.
update site_pages
set body = body || E'\n\n## Where you are (draft: review before launch)\n\n- When you first visit, we estimate your country and city from your internet address so we can show professionals and prices near you. It is a rough guess. We show it as a guess and you can change it at any time.\n- To make that estimate we may send your internet address to a location lookup service. We keep only the first part of the address, which does not identify you, with the estimate, for up to seven days.\n- We ask your device for its exact position only when you press "Near me". We round it to about a hundred metres and use it to order results by distance. If you refuse, the site carries on with the approximate place and does not ask again unless you press the button.\n- The place you choose or confirm is kept in a cookie on your device called lx_place, for up to six months, so the site opens in the right place next time. "Forget my location" in the place menu removes it.\n- Businesses never see where you were when you searched.',
    updated_at = now()
where slug = 'privacy' and body not like '%## Where you are%';
