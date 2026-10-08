-- Sample history for the sample businesses, so the merchant screens have
-- something real to show: six weeks of paid visits with their sales and
-- ledger lines, weekly payouts, a payout account, stock levels and a few
-- conversations. Clients here have no email addresses on purpose, so no
-- automatic message can ever leave the building. Phone numbers use the 555
-- range, which is reserved for fiction.

do $$
declare
  biz record; st record; sv record;
  d date; slot int; visits int; cid uuid; bid uuid; sid uuid;
  starts timestamptz; ends timestamptz; tip int; fee int; method text; settled text; src text; cname text;
  pct numeric; fixed int; cap int; wk int; amount int; pid uuid; acct uuid;
  firsts text[] := array['Kemi','Grace','Tomi','Bola','Ifeoma','Dami','Chidinma','Zara','Amara','Simi','Lola','Femi','Kwame','Nia','Ruth','Halima','Tunde','Ngozi','Sade','Efe','Maya','Jasmine','Aaliyah','Imani','Destiny','Marcus','Andre','Jordan','Chloe','Naomi'];
  lasts text[] := array['Adeyemi','Eze','Alade','Kalu','Nwosu','Osei','Okoro','Mensah','Diallo','Bello','Fashola','Okafor','Asante','Johnson','Williams','Carter','Brooks','Hayes','Reed','Coleman'];
begin
  for biz in select b.id, b.slug, b.name, b.currency, b.timezone, b.market, b.plan from businesses b where b.slug in ('ada','barberloft','glow','nia','mnm','freedomway') loop
    select transaction_pct, transaction_fixed_cents, transaction_cap_cents into pct, fixed, cap from fees
      where market = biz.market and plan = biz.plan and status = 'approved' and effective_from <= current_date order by effective_from desc limit 1;
    pct := coalesce(pct, 2.9); fixed := coalesce(fixed, 0);

    -- More clients, so lists and segments look like a working business.
    insert into clients (business_id, name, phone, created_at)
    select biz.id, firsts[1 + (g * 7) % 30] || ' ' || lasts[1 + (g * 3) % 20],
           case when biz.market = 'US' then '+1615555' else '+234803555' end || lpad((1000 + g)::text, 4, '0'),
           now() - (random() * 200 || ' days')::interval
    from generate_series(1, 36) g on conflict do nothing;

    -- Six weeks of visits.
    for d in select generate_series(current_date - 42, current_date - 1, interval '1 day')::date loop
      continue when extract(isodow from d) = 7;
      for st in select s.id, s.name from staff s where s.business_id = biz.id and s.bookable loop
        visits := 1 + floor(random() * 3)::int;
        for slot in 0..(visits - 1) loop
          select v.id, v.name, v.price_cents, v.duration_min into sv from services v join staff_services ss on ss.service_id = v.id and ss.staff_id = st.id
            where v.business_id = biz.id and v.duration_min <= 210 order by random() limit 1;
          continue when sv.id is null;
          select c.id, c.name into cid, cname from clients c where c.business_id = biz.id order by random() limit 1;
          starts := ((d::text || ' ' || (array['09:00','13:00','16:30'])[slot + 1])::timestamp) at time zone biz.timezone;
          ends := starts + (sv.duration_min || ' minutes')::interval;
          src := (array['web','web','web','rebook','rebook','phone','walk_in','search'])[1 + floor(random() * 8)::int];

          if random() < 0.04 then -- the odd no-show
            insert into bookings (business_id, location_id, staff_id, client_id, client_name, client_phone, status, starts_at, ends_at, source, total_cents, created_at)
            select biz.id, l.id, st.id, cid, cname, c.phone, 'no_show', starts, ends, src, sv.price_cents, starts - interval '6 days' from locations l, clients c where l.business_id = biz.id and l.is_primary and c.id = cid;
            update clients set no_show_count = no_show_count + 1 where id = cid;
            continue;
          end if;

          tip := case when random() < 0.7 then (sv.price_cents * (10 + floor(random() * 12)) / 100)::int else 0 end;
          method := (array['card','card','card','card','tap','tap','cash','transfer'])[1 + floor(random() * 8)::int];
          settled := case when ends < now() - interval '2 days' then 'settled' else 'pending' end;

          insert into bookings (business_id, location_id, staff_id, client_id, client_name, client_phone, status, starts_at, ends_at, source, total_cents, tip_cents, checked_in_at, completed_at, paid_at, created_at)
          select biz.id, l.id, st.id, cid, cname, c.phone, 'paid', starts, ends, src, sv.price_cents, tip, starts, ends, ends, starts - interval '6 days'
          from locations l, clients c where l.business_id = biz.id and l.is_primary and c.id = cid returning id into bid;
          insert into booking_items (booking_id, service_id, name, price_cents, duration_min) values (bid, sv.id, sv.name, sv.price_cents, sv.duration_min);

          insert into sales (business_id, booking_id, client_id, client_name, staff_id, subtotal_cents, tip_cents, total_cents, method, created_by, created_at)
          values (biz.id, bid, cid, cname, st.id, sv.price_cents, tip, sv.price_cents + tip, method, 'sample data', ends) returning id into sid;
          insert into sale_items (sale_id, kind, service_id, staff_id, name, qty, unit_cents) values (sid, 'service', sv.id, st.id, sv.name, 1, sv.price_cents);

          insert into ledger (business_id, kind, amount_cents, currency, method, status, in_balance, sale_id, booking_id, staff_id, description, settles_at, created_at)
          values (biz.id, 'charge', sv.price_cents, biz.currency, method, case when method = 'cash' then 'settled' else settled end, method <> 'cash', sid, bid, st.id, cname || ' · ' || sv.name, ends + interval '2 days', ends);
          if tip > 0 then
            insert into ledger (business_id, kind, amount_cents, currency, method, status, in_balance, sale_id, booking_id, staff_id, description, settles_at, created_at)
            values (biz.id, 'tip', tip, biz.currency, method, case when method = 'cash' then 'settled' else settled end, method <> 'cash', sid, bid, st.id, 'Tip · ' || cname, ends + interval '2 days', ends);
          end if;
          if method <> 'cash' then
            fee := least(round((sv.price_cents + tip) * pct / 100)::int + fixed, coalesce(cap, 2147483647));
            insert into ledger (business_id, kind, amount_cents, currency, method, status, sale_id, booking_id, staff_id, description, settles_at, created_at)
            values (biz.id, 'fee', -fee, biz.currency, method, settled, sid, bid, st.id, 'LogaLuxe fee · ' || cname, ends + interval '2 days', ends);
          end if;
        end loop;
      end loop;
    end loop;

    -- Where the money goes, and four weeks of payouts already made.
    insert into payout_accounts (business_id, provider, status, mode, bank_name, account_name, account_last4, currency)
    values (biz.id, case when biz.market = 'US' then 'stripe' else 'paystack' end, 'verified', 'simulation',
            case when biz.market = 'US' then 'Chase' else 'GTBank' end, biz.name, lpad(floor(random() * 9000 + 1000)::text, 4, '0'), biz.currency) returning id into acct;
    for wk in reverse 5..1 loop
      select coalesce(sum(amount_cents), 0) into amount from ledger where business_id = biz.id and in_balance and status = 'settled'
        and created_at >= current_date - (wk * 7 + 7) and created_at < current_date - (wk * 7);
      continue when amount < 100;
      insert into payouts (business_id, amount_cents, currency, status, provider, reference, kind, account_id, scheduled_for, paid_at, created_at)
      values (biz.id, amount, biz.currency, 'paid', case when biz.market = 'US' then 'stripe' else 'paystack' end, 'sim_sample_' || wk, 'automatic', acct,
              current_date - (wk * 7) + 2, (current_date - (wk * 7) + 2)::timestamptz, (current_date - (wk * 7) + 2)::timestamptz) returning id into pid;
      insert into ledger (business_id, kind, amount_cents, currency, method, status, payout_id, description, created_at)
      values (biz.id, 'payout', -amount, biz.currency, case when biz.market = 'US' then 'stripe' else 'paystack' end, 'settled', pid, 'Payout to bank', (current_date - (wk * 7) + 2)::timestamptz);
    end loop;

    -- Today's confirmed bookings paid a deposit; that money is held until the visit.
    insert into ledger (business_id, kind, amount_cents, currency, method, status, booking_id, description)
    select biz.id, 'deposit', bk.deposit_cents, biz.currency, 'card', 'held', bk.id, 'Deposit · ' || bk.client_name
    from bookings bk where bk.business_id = biz.id and bk.deposit_paid and bk.deposit_cents > 0 and bk.status in ('requested','confirmed')
      and not exists (select 1 from ledger l where l.booking_id = bk.id and l.kind = 'deposit');
  end loop;

  -- Stock, a supplier and some conversations for the main sample business.
  select id into bid from businesses where slug = 'ada';
  if bid is not null then
    insert into suppliers (business_id, name, contact, email, phone) values (bid, 'Beauty Supply Co.', 'Dana, account manager', '', '+16155550190') returning id into sid;
    update products set cost_cents = (price_cents * 0.55)::int, reorder_at = 6, sku = 'LX-' || upper(left(slug, 6)), supplier_id = sid where business_id = bid;
    update products set stock = 4 where business_id = bid and slug = (select slug from products where business_id = bid order by sold desc limit 1 offset 1);
    insert into products (business_id, seller_name, slug, name, category, price_cents, stock, sku, cost_cents, reorder_at, kind, supplier_id, active, pickup, shipping)
    values (bid, 'Ada''s Braid Studio', 'ada-pre-stretched-hair', 'Pre-stretched braiding hair', 'hair', 1, 38, 'BB-HAIR', 450, 20, 'backbar', sid, false, true, false),
           (bid, 'Ada''s Braid Studio', 'ada-edge-control', 'Edge control · strong hold', 'styling', 1400, 3, 'LX-EDGE', 620, 5, 'both', sid, true, true, false)
    on conflict (slug) do nothing;

    select id, name into cid, cname from clients where business_id = bid order by created_at limit 1;
    insert into threads (business_id, client_id, client_name, channel, unread_business, last_preview, last_message_at)
    values (bid, cid, cname, 'whatsapp', 1, 'Can I move my appointment an hour later on Saturday?', now() - interval '40 minutes') returning id into sid;
    insert into thread_messages (thread_id, from_business, author, body, delivery, created_at) values
      (sid, true, 'Ada Okafor', 'Hi! Just confirming your knotless braids on Saturday at 10. See you then.', 'logged', now() - interval '1 day'),
      (sid, false, cname, 'Can I move my appointment an hour later on Saturday?', 'delivered', now() - interval '40 minutes');
    select id, name into cid, cname from clients where business_id = bid order by created_at desc limit 1;
    insert into threads (business_id, client_id, client_name, channel, unread_business, last_preview, last_message_at)
    values (bid, cid, cname, 'whatsapp', 1, 'How much for small knotless, waist length?', now() - interval '3 hours') returning id into sid;
    insert into thread_messages (thread_id, from_business, author, body, delivery, created_at) values
      (sid, false, cname, 'How much for small knotless, waist length?', 'delivered', now() - interval '3 hours');

    -- A team member asking for time off next week.
    insert into time_off (business_id, staff_id, starts_on, ends_on, reason)
    select bid, s.id, current_date + 8, current_date + 10, 'Family wedding' from staff s where s.business_id = bid and s.role <> 'owner' order by s.name limit 1;
    insert into waitlist_entries (business_id, client_name, client_phone, day, time_of_day)
    select bid, c.name, c.phone, current_date + 2, 'morning' from clients c where c.business_id = bid order by random() limit 3;
  end if;
end $$;
