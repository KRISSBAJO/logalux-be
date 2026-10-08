-- Sample data for the deeper salon features, on the main sample business
-- only, and only where the sample history exists. A real installation has no
-- business with this handle and sample sales, so nothing happens there.
do $$
declare
  b uuid; chair uuid; station uuid; pkg uuid; mem uuid; plan uuid; c1 uuid; c2 uuid; renter uuid; wash uuid; scalp uuid; knot uuid;
begin
  select id into b from businesses where slug = 'ada' and exists (select 1 from sales where business_id = businesses.id and created_by = 'sample data');
  if b is null or exists (select 1 from resources where business_id = b) then
    return;
  end if;
  select id into wash from services where business_id = b and name ilike 'Wash and blow%' limit 1;
  select id into scalp from services where business_id = b and name ilike 'Scalp treatment%' limit 1;
  select id into knot from services where business_id = b and name ilike 'Knotless braids%medium%' limit 1;

  -- Three braiding chairs and one wash station.
  insert into resources (business_id, name, qty) values (b, 'Braiding chair', 3) returning id into chair;
  insert into resources (business_id, name, qty) values (b, 'Wash station', 1) returning id into station;
  insert into service_resources (service_id, resource_id) select id, chair from services where business_id = b and category in ('Braids', 'Locs');
  insert into service_resources (service_id, resource_id) select id, station from services where business_id = b and (name ilike 'Wash%' or name ilike 'Silk press%' or name ilike 'Scalp%');

  -- Saturdays cost a little more for the busiest service; juniors cost a little less.
  if knot is not null then
    insert into price_rules (business_id, name, service_id, days, adjust_kind, adjust_value) values (b, 'Saturday peak', knot, '{sat}', 'amount', 1500);
  end if;
  insert into price_rules (business_id, name, level, adjust_kind, adjust_value) values (b, 'Junior stylist', 'junior', 'percent', -10);
  insert into price_rules (business_id, name, days, from_time, to_time, adjust_kind, adjust_value, active) values (b, 'Quiet Tuesday mornings', '{tue}', '09:00', '12:00', 'percent', -15, false);

  -- A package and a membership, each with one client already holding it.
  if wash is not null then
    insert into packages (business_id, name, description, price_cents, valid_days) values (b, 'Wash day trio', 'Three wash and blow-dry visits. Use them within six months.', 6500, 180) returning id into pkg;
    insert into package_items (package_id, service_id, qty) values (pkg, wash, 3);
    select id into c1 from clients where business_id = b order by created_at limit 1 offset 2;
    insert into client_plans (business_id, client_id, kind, package_id, name, price_cents, started_at, expires_at) values (b, c1, 'package', pkg, 'Wash day trio', 6500, now() - interval '20 days', now() + interval '160 days') returning id into plan;
    insert into client_credits (plan_id, service_id, service_name, total, used, expires_at) values (plan, wash, 'Wash and blow-dry', 3, 1, now() + interval '160 days');
  end if;
  insert into memberships (business_id, name, description, price_cents, service_discount_pct, retail_discount_pct) values (b, 'Braid club', '10% off every service, 5% off products, and a scalp treatment each month.', 3500, 10, 5) returning id into mem;
  if scalp is not null then
    insert into membership_items (membership_id, service_id, qty) values (mem, scalp, 1);
  end if;
  select id into c2 from clients where business_id = b order by created_at limit 1 offset 4;
  insert into client_plans (business_id, client_id, kind, membership_id, name, price_cents, started_at, renews_on) values (b, c2, 'membership', mem, 'Braid club', 3500, now() - interval '12 days', (current_date + 18)) returning id into plan;
  if scalp is not null then
    insert into client_credits (plan_id, service_id, service_name, total, used, expires_at) values (plan, scalp, 'Scalp treatment', 1, 0, (current_date + 18)::timestamptz);
  end if;

  -- How people are paid, a lunch break, and a rented chair.
  update staff set pay_type = 'hourly', hourly_cents = 1800 where business_id = b and name = 'Malik Johnson';
  update staff set pay_type = 'hourly', hourly_cents = 1500, breaks = '{"wed":[["13:00","13:30"]],"thu":[["13:00","13:30"]],"fri":[["13:00","13:30"]],"sat":[["12:30","13:00"]]}' where business_id = b and name = 'Tola Fade';
  update staff set breaks = '{"tue":[["13:00","13:30"]],"wed":[["13:00","13:30"]]}' where business_id = b and role = 'owner';
  insert into staff (business_id, name, initials, role, level, tone, bookable, pay_type, rent_cents, rent_period, rent_days, trading_name)
  values (b, 'Chair 3 · Lash Haus', 'C3', 'staff', 'senior', '#9A8E85', false, 'renter', 18000, 'weekly', '{mon,tue,wed,thu}', 'Lash Haus') returning id into renter;
  insert into rent_charges (business_id, staff_id, period_start, period_end, amount_cents, status, method, paid_at) values
    (b, renter, date_trunc('week', now())::date - 14, date_trunc('week', now())::date - 8, 18000, 'paid', 'transfer', now() - interval '13 days'),
    (b, renter, date_trunc('week', now())::date - 7, date_trunc('week', now())::date - 1, 18000, 'paid', 'transfer', now() - interval '6 days'),
    (b, renter, date_trunc('week', now())::date, date_trunc('week', now())::date + 6, 18000, 'due', '', null);

  -- What the services use up, and what a full shelf looks like.
  insert into service_products (service_id, product_id, qty)
  select sv.id, p.id, 2 from services sv, products p where sv.business_id = b and sv.name ilike 'Knotless%' and p.business_id = b and p.slug = 'ada-pre-stretched-hair';
  insert into service_products (service_id, product_id, qty)
  select sv.id, p.id, 0.1 from services sv, products p where sv.business_id = b and sv.category = 'Braids' and p.business_id = b and p.slug = 'ada-edge-control';
  update products set par_level = greatest(reorder_at * 4, stock, 12) where business_id = b;
end $$;
