-- Sample leads for the main sample business, so the lead screens have
-- something to show. Only where the sample history exists.
do $$
declare
  b uuid; pct numeric; lim int; total int := 0; po uuid; bk record; fee int;
begin
  select id into b from businesses where slug = 'ada' and exists (select 1 from sales where business_id = businesses.id and created_by = 'sample data');
  if b is null or exists (select 1 from leads where business_id = b) then
    return;
  end if;
  select f.new_client_pct, f.new_client_cap_cents into pct, lim from fees f join businesses bz on bz.market = f.market and bz.plan = f.plan
    where bz.id = b and f.status = 'approved' and f.effective_from <= current_date order by f.effective_from desc limit 1;
  pct := coalesce(pct, 15);

  -- The first paid visit of some clients who came through LogaLuxe search.
  for bk in
    select distinct on (x.client_id) x.id, x.client_id, x.client_name, x.total_cents, x.paid_at, (select s.id from sales s where s.booking_id = x.id limit 1) as sale_id
    from bookings x where x.business_id = b and x.source = 'search' and x.status = 'paid' and x.client_id is not null
      and not exists (select 1 from bookings y where y.client_id = x.client_id and y.business_id = b and y.starts_at < x.starts_at)
    order by x.client_id, x.starts_at limit 14
  loop
    fee := least(round(bk.total_cents * pct / 100)::int, coalesce(lim, 2147483647));
    insert into leads (business_id, client_id, booking_id, sale_id, client_name, source, status, base_pct, cap_cents, value_cents, fee_cents, charged_at, created_at)
    values (b, bk.client_id, bk.id, bk.sale_id, bk.client_name, 'search', 'charged', pct, lim, bk.total_cents, fee, bk.paid_at, bk.paid_at - interval '6 days');
    insert into ledger (business_id, kind, amount_cents, currency, method, status, in_balance, sale_id, booking_id, description, created_at)
    values (b, 'lead_fee', -fee, 'USD', 'logaluxe', 'settled', true, bk.sale_id, bk.id, 'New client from LogaLuxe · ' || bk.client_name, bk.paid_at);
    total := total + fee;
  end loop;

  -- The fees came out of money that would otherwise have been paid out, so the payout on its way is that much smaller.
  select p.id into po from payouts p where p.business_id = b and p.status = 'scheduled' and p.amount_cents > total and exists (select 1 from ledger l where l.payout_id = p.id) order by p.created_at desc limit 1;
  if po is not null and total > 0 then
    update payouts set amount_cents = amount_cents - total where id = po;
    update ledger set amount_cents = amount_cents + total where payout_id = po and kind = 'payout';
  end if;

  -- One lead whose visit is still to come, and one that was voided.
  insert into leads (business_id, client_id, booking_id, client_name, source, status, base_pct, cap_cents, value_cents)
  select b, x.client_id, x.id, x.client_name, 'search', 'pending', pct, lim, x.total_cents from bookings x
  where x.business_id = b and x.status = 'confirmed' and x.starts_at > now() and x.client_id is not null
    and not exists (select 1 from leads l where l.client_id = x.client_id and l.business_id = b) order by x.starts_at limit 1;
  insert into leads (business_id, client_id, booking_id, client_name, source, status, base_pct, cap_cents, value_cents, void_reason, created_at)
  select b, x.client_id, x.id, x.client_name, 'search', 'void', pct, lim, x.total_cents, 'The client did not come', x.starts_at - interval '4 days' from bookings x
  where x.business_id = b and x.status = 'no_show' and x.client_id is not null
    and not exists (select 1 from leads l where l.client_id = x.client_id and l.business_id = b and l.status <> 'void') order by x.starts_at desc limit 1;

  -- A month of being seen in search and opened from it.
  insert into lead_events (business_id, kind, day, n)
  select b, k, d::date, case k when 'impression' then 40 + (extract(doy from d)::int * 7) % 55 else 6 + (extract(doy from d)::int * 3) % 11 end
  from generate_series(current_date - 29, current_date, interval '1 day') d, unnest(array['impression', 'view']) k
  on conflict do nothing;
end $$;
