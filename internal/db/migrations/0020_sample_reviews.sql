-- The sample businesses showed a review count that no reviews stood behind.
-- Give each one reviews tied to its sample paid visits, then make every
-- one of those businesses show the true count and average of its published reviews.
do $$
declare
  bk record; n int; pick int;
  good text[] := array[
    'Exactly what I booked. Started on time and she checked in with me the whole way through.',
    'Neat work and no pulling at my edges. I have already booked the next one.',
    'Clean space, easy to find, and the price was the price. No surprises at the end.',
    'She listened to what I wanted and told me honestly what would suit me. Very happy with it.',
    'Quick, careful and friendly. It has held up well two weeks on.',
    'My third visit. Consistent every time, which is why I keep coming back.',
    'Booked the night before and still got a morning slot. Lovely result.',
    'Gentle hands and she explained how to look after it at home.'];
  fine text[] := array[
    'Good result. We started about fifteen minutes late, but she told me before I arrived.',
    'Happy with how it turned out. Parking was a bit tight on a Saturday.',
    'Nice work overall. It took longer than the time shown, so plan for that.',
    'Solid. I would have liked a little more time on the finish, but I will be back.'];
begin
  if not exists (select 1 from sales where created_by = 'sample data') then
    return;
  end if;
  for bk in
    select x.id, x.business_id, x.client_name, x.completed_at, x.rn,
           coalesce((select bi.name from booking_items bi where bi.booking_id = x.id limit 1), 'Visit') as service
    from (select b.*, row_number() over (partition by b.business_id order by b.starts_at desc) as rn
          from bookings b join businesses z on z.id = b.business_id
          where b.status = 'paid' and z.slug in ('ada','barberloft','glow','nia','mnm','freedomway')
            and exists (select 1 from sales s where s.booking_id = b.id and s.created_by = 'sample data')
            and not exists (select 1 from reviews r where r.booking_id = b.id)) x
    where x.rn % 5 = 0 and x.rn <= 60
  loop
    n := (bk.rn / 5);
    pick := 1 + (n * 3 % 8);
    if n % 4 = 0 then
      insert into reviews (business_id, booking_id, author_name, service_name, rating, body, status, created_at)
      values (bk.business_id, bk.id, split_part(bk.client_name, ' ', 1) || ' ' || left(split_part(bk.client_name, ' ', 2), 1) || '.', bk.service, 4, fine[1 + (n / 4 % 4)], 'published', coalesce(bk.completed_at, now()) + interval '1 day');
    else
      insert into reviews (business_id, booking_id, author_name, service_name, rating, body, status, created_at)
      values (bk.business_id, bk.id, split_part(bk.client_name, ' ', 1) || ' ' || left(split_part(bk.client_name, ' ', 2), 1) || '.', bk.service, 5, good[pick], 'published', coalesce(bk.completed_at, now()) + interval '1 day');
    end if;
  end loop;
end $$;

update businesses b set
  review_count = (select count(*) from reviews r where r.business_id = b.id and r.status = 'published'),
  rating = coalesce((select round(avg(r.rating), 2) from reviews r where r.business_id = b.id and r.status = 'published'), 0)
where b.slug in ('ada','barberloft','glow','nia','mnm','freedomway');
