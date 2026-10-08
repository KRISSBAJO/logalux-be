-- Early sample bookings were stored without their service lines, so the
-- calendar and checkout had no name to show. Give each one the service on that
-- business's menu that best matches its price and length.
insert into booking_items (booking_id, service_id, name, price_cents, duration_min)
select bk.id, sv.id, sv.name, bk.total_cents, greatest(5, (extract(epoch from (bk.ends_at - bk.starts_at)) / 60)::int)
from bookings bk
join lateral (
  select s.id, s.name from services s where s.business_id = bk.business_id
  order by abs(s.price_cents - bk.total_cents), abs(s.duration_min - extract(epoch from (bk.ends_at - bk.starts_at)) / 60) limit 1
) sv on true
where not exists (select 1 from booking_items i where i.booking_id = bk.id);
