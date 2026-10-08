#!/usr/bin/env bash
# Adds or removes 120 temporary businesses around Nashville, to see how search
# and the map behave at scale. Usage: scale-search.sh add | remove
case "$1" in
add)
docker exec -i logaluxe-db sh -c 'psql -v ON_ERROR_STOP=1 -U "$POSTGRES_USER" -d "$POSTGRES_DB" -q' <<'SQL'
begin;
with b as (
  insert into businesses (slug, name, tagline, category, market, currency, timezone, status, verification_status, rating, review_count, tone, highlights, owner_name)
  select 'zzscale-' || n, 'Scale Test Studio ' || n, 'Temporary business for a load check',
         (array['hair','braids','barber','nails','lashes','skin','makeup','spa'])[1 + n % 8], 'US', 'USD', 'America/Chicago', 'live', 'verified',
         round((3.6 + random() * 1.4)::numeric, 1), (random() * 400)::int,
         (array['#3B1D22','#1F2A33','#4A3426','#2E2538','#5A4A3A'])[1 + n % 5], '{}', 'Scale Tester'
  from generate_series(1, 120) n returning id, slug
), l as (
  insert into locations (business_id, name, address, city, region, country, timezone, is_primary, hours, lat, lng)
  select id, 'Area ' || substr(slug, 9), '', 'Nashville', 'TN', 'US', 'America/Chicago', true,
         '{"mon":["09:00","18:00"],"tue":["09:00","18:00"],"wed":["09:00","18:00"],"thu":["09:00","18:00"],"fri":["09:00","18:00"],"sat":["09:00","16:00"],"sun":null}'::jsonb,
         36.16 + (random() - 0.5) * 0.22, -86.78 + (random() - 0.5) * 0.30 from b
), st as (
  insert into staff (business_id, name, initials, role) select id, 'Scale Tester', 'ST', 'owner' from b
)
insert into services (business_id, name, category, duration_min, price_cents) select id, 'Signature service', 'Services', 60, (20 + (random() * 180)::int) * 100 from b;
commit;
select count(*) || ' temporary businesses added' from businesses where slug like 'zzscale-%';
SQL
;;
remove)
docker exec -i logaluxe-db sh -c 'psql -v ON_ERROR_STOP=1 -U "$POSTGRES_USER" -d "$POSTGRES_DB" -At -c "delete from businesses where slug like '"'"'zzscale-%'"'"'" -c "select count(*) || '"'"' businesses left in total'"'"' from businesses"'
;;
*) echo "usage: $0 add | remove" ;;
esac
