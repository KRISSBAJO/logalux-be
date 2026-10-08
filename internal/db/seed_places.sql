-- Sample businesses beyond the first two cities, so search by distance, the
-- place pages and time zones can be seen working in development. Each has a
-- real street or neighbourhood, the position of that spot, and the time zone
-- and currency that belong to it. One of them travels to its clients.

-- On a new database the first samples are loaded after the migrations have run, so what two
-- migrations did for databases seeded earlier is done here: each sample gets its own
-- neighbourhood rather than its city's centre, and the travelling one is marked as travelling.
update locations l set lat = v.lat, lng = v.lng
from businesses b, (values
  ('ada',        36.1936, -86.7446),  -- Gallatin Ave, East Nashville
  ('barberloft', 36.1513, -86.7868),  -- Division St, The Gulch
  ('glow',       36.1247, -86.7897),  -- 12th Ave S, 12 South
  ('nia',        36.0598, -86.6722),  -- Antioch
  ('freedomway', 6.4474,  3.4723),    -- Freedom Way, Lekki
  ('mnm',        6.4478,  3.4701),    -- Admiralty Way, Lekki Phase 1
  ('glowup',     6.4391,  3.4850),    -- Lekki
  ('tiwa',       6.4541,  3.4316)     -- Bourdillon Rd, Ikoyi
) as v(slug, lat, lng)
where b.id = l.business_id and b.slug = v.slug and l.is_primary
  and ((l.lat = 36.17 and l.lng = -86.75) or (l.lat = 6.45 and l.lng = 3.47));
update locations set travels = true where name ilike '%(mobile)%' and not travels;
update locations set position_source = 'hand' where lat is not null and lng is not null and position_source = '';

with b as (
  insert into businesses (slug, name, tagline, about, category, market, currency, timezone, phone, instagram, status, verification_status, plan, rating, review_count, tone, highlights, owner_name)
  values
  ('peachtree', 'Peachtree Locs & Braids', 'Locs, retwists and knotless braids in Old Fourth Ward', 'Loc maintenance and braids by appointment. Hair is included with every braid install.', 'braids', 'US', 'USD', 'America/New_York', '+14045550137', '@peachtreelocs', 'live', 'verified', 'free', 4.67, 3, '#3B1D22', '{"Hair included","Street parking","Women-owned"}', 'Renee Carter'),
  ('bayoufade', 'Bayou Fade Co.', 'Fades, tapers and beard work in Upper Kirby', 'Four chairs. Bookings first, walk-ins when a chair is free.', 'barber', 'US', 'USD', 'America/Chicago', '+17135550162', '@bayoufadeco', 'live', 'verified', 'free', 4.67, 3, '#1F2A33', '{"Walk-ins when free","Kids cuts","Card and Apple Pay"}', 'Andre Guidry'),
  ('harlemglow', 'Harlem Glow Studio', 'Facials and brows on Adam Clayton Powell Jr Blvd', 'Licensed aestheticians. A patch test comes before every peel.', 'skin', 'US', 'USD', 'America/New_York', '+12125550148', '@harlemglowstudio', 'live', 'verified', 'free', 5.00, 3, '#5A4A3A', '{"Licensed","Sensitive-skin friendly"}', 'Dana Whitfield'),
  ('crenshawnails', 'Crenshaw Nail Bar', 'Gel, acrylic and pedicures in Crenshaw', 'Gel and acrylic sets, fills and pedicures. Tools are sterilised between clients.', 'nails', 'US', 'USD', 'America/Los_Angeles', '+13235550119', '@crenshawnailbar', 'live', 'verified', 'free', 4.67, 3, '#2E2538', '{"Parking on site","Open Sundays"}', 'Lena Morales'),
  ('bealebarbers', 'Beale Street Barbers', 'Cuts and hot-towel shaves on Beale Street', 'Three chairs downtown. Open late on Fridays.', 'barber', 'US', 'USD', 'America/Chicago', '+19015550173', '@bealestreetbarbers', 'live', 'verified', 'free', 4.67, 3, '#1F2A33', '{"Hot-towel shave","Open late Friday"}', 'Curtis Boyd'),
  ('wuselashes', 'Wuse Lash Lounge', 'Lash extensions and brow lamination in Wuse 2', 'Classic, hybrid and volume sets. Fills every two to three weeks.', 'lashes', 'NG', 'NGN', 'Africa/Lagos', '+2348095550126', '@wuselashlounge', 'live', 'verified', 'free', 4.67, 3, '#4A2A2A', '{"Pay with transfer","Parking in front"}', 'Hadiza Bello'),
  ('gardencitymakeup', 'Garden City Makeup', 'Bridal and event makeup across Port Harcourt', 'I come to you. Bridal, traditional and event looks, with a trial before the day.', 'makeup', 'NG', 'NGN', 'Africa/Lagos', '+2348075550184', '@gardencitymakeup', 'live', 'verified', 'free', 5.00, 3, '#4A3426', '{"Comes to you","Trial before the day"}', 'Ibinabo George')
  returning id, slug
),
loc as (
  insert into locations (business_id, name, address, city, region, country, lat, lng, timezone, is_primary, hours, arrival_notes, travels, travel_radius_km, position_source)
  select b.id, v.name, v.address, v.city, v.region, v.country, v.lat, v.lng, v.tz, true, v.hours::jsonb, v.notes, v.travels, v.radius, v.source
  from b join (values
    ('peachtree', 'Old Fourth Ward', '675 Ponce De Leon Ave NE', 'Atlanta', 'GA', 'US', 33.7726, -84.3655, 'America/New_York',
      '{"mon":null,"tue":["10:00","18:00"],"wed":["10:00","18:00"],"thu":["10:00","19:00"],"fri":["09:00","19:00"],"sat":["08:00","17:00"],"sun":null}', 'Second floor. Take the lift by the food hall.', false, null::int, 'hand'),
    ('bayoufade', 'Upper Kirby', '2800 Kirby Dr', 'Houston', 'TX', 'US', 29.7385, -95.4186, 'America/Chicago',
      '{"mon":["09:00","19:00"],"tue":["09:00","19:00"],"wed":["09:00","19:00"],"thu":["09:00","19:00"],"fri":["09:00","20:00"],"sat":["08:00","18:00"],"sun":null}', '', false, null, 'hand'),
    ('harlemglow', 'Harlem', '2271 Adam Clayton Powell Jr Blvd', 'New York', 'NY', 'US', 40.8147, -73.9443, 'America/New_York',
      '{"mon":null,"tue":["10:00","19:00"],"wed":["10:00","19:00"],"thu":["10:00","19:00"],"fri":["10:00","19:00"],"sat":["09:00","17:00"],"sun":["11:00","16:00"]}', 'Buzz 2 at the street door.', false, null, 'hand'),
    ('crenshawnails', 'Crenshaw', '3650 W Martin Luther King Jr Blvd', 'Los Angeles', 'CA', 'US', 34.0106, -118.3370, 'America/Los_Angeles',
      '{"mon":["10:00","19:00"],"tue":["10:00","19:00"],"wed":["10:00","19:00"],"thu":["10:00","19:00"],"fri":["10:00","20:00"],"sat":["09:00","19:00"],"sun":["11:00","17:00"]}', '', false, null, 'hand'),
    ('bealebarbers', 'Downtown', '310 Beale St', 'Memphis', 'TN', 'US', 35.1392, -90.0500, 'America/Chicago',
      '{"mon":null,"tue":["09:00","18:00"],"wed":["09:00","18:00"],"thu":["09:00","18:00"],"fri":["09:00","21:00"],"sat":["08:00","18:00"],"sun":null}', '', false, null, 'hand'),
    ('wuselashes', 'Wuse 2', 'Aminu Kano Crescent', 'Abuja', 'FCT', 'NG', 9.0790, 7.4690, 'Africa/Lagos',
      '{"mon":["09:00","18:00"],"tue":["09:00","18:00"],"wed":["09:00","18:00"],"thu":["09:00","18:00"],"fri":["09:00","18:00"],"sat":["10:00","17:00"],"sun":null}', '', false, null, 'hand'),
    -- She has no shop front: the position is the part of the city she works from, and she goes up to 25 km.
    ('gardencitymakeup', 'GRA Phase 2', '', 'Port Harcourt', 'Rivers', 'NG', 4.8250, 7.0000, 'Africa/Lagos',
      '{"mon":["08:00","18:00"],"tue":["08:00","18:00"],"wed":["08:00","18:00"],"thu":["08:00","18:00"],"fri":["07:00","19:00"],"sat":["06:00","19:00"],"sun":null}', '', true, 25, 'city')
  ) as v(slug, name, address, city, region, country, lat, lng, tz, hours, notes, travels, radius, source) on v.slug = b.slug
  returning id
),
st as (
  insert into staff (business_id, name, initials, role, level, tone, bookable, rating)
  select b.id, s.name, s.initials, s.role, s.level, s.tone, true, s.rating
  from b join (values
    ('peachtree', 'Renee Carter', 'RC', 'owner', 'master', '#7A1F2B', 4.67),
    ('peachtree', 'Jada Lewis', 'JL', 'staff', 'senior', '#4A2A2A', 0),
    ('bayoufade', 'Andre Guidry', 'AG', 'owner', 'master', '#1F2A33', 4.67),
    ('bayoufade', 'Luis Ortega', 'LO', 'staff', 'senior', '#2E2538', 0),
    ('harlemglow', 'Dana Whitfield', 'DW', 'owner', 'master', '#5A4A3A', 5.00),
    ('crenshawnails', 'Lena Morales', 'LM', 'owner', 'master', '#2E2538', 4.67),
    ('crenshawnails', 'Tasha Reed', 'TR', 'staff', 'senior', '#4A3426', 0),
    ('bealebarbers', 'Curtis Boyd', 'CB', 'owner', 'master', '#1F2A33', 4.67),
    ('wuselashes', 'Hadiza Bello', 'HB', 'owner', 'master', '#4A2A2A', 4.67),
    ('gardencitymakeup', 'Ibinabo George', 'IG', 'owner', 'master', '#4A3426', 5.00)
  ) as s(slug, name, initials, role, level, tone, rating) on s.slug = b.slug
  returning id, business_id
),
sv as (
  insert into services (business_id, name, category, description, duration_min, processing_min, buffer_min, price_cents, deposit_cents, online, sort)
  select b.id, s.name, s.category, s.description, s.dur, 0, s.buf, s.price, s.dep, true, s.sort
  from b join (values
    ('peachtree', 'Loc retwist', 'Locs', 'Wash, retwist and style.', 90, 10, 9000, 2000, 1),
    ('peachtree', 'Starter locs', 'Locs', 'Comb coils or two-strand twists. Short consult first.', 150, 15, 16000, 4000, 2),
    ('peachtree', 'Knotless braids · medium', 'Braids', 'Waist length. Pre-stretched hair included.', 210, 15, 19000, 4000, 3),
    ('bayoufade', 'Skin fade', 'Cuts', 'Skin fade with line-up.', 45, 5, 4000, 0, 1),
    ('bayoufade', 'Taper + beard', 'Cuts', 'Taper with beard shape and line.', 50, 5, 5000, 0, 2),
    ('bayoufade', 'Kids cut', 'Cuts', 'Under 12.', 30, 5, 2500, 0, 3),
    ('harlemglow', 'Signature facial', 'Skin', 'Cleanse, exfoliate, mask and massage.', 60, 10, 11000, 0, 1),
    ('harlemglow', 'Chemical peel', 'Skin', 'Patch test required 48 hours before.', 45, 10, 14000, 4000, 2),
    ('harlemglow', 'Brow shape', 'Brows', 'Wax or thread.', 20, 5, 2800, 0, 3),
    ('crenshawnails', 'Gel manicure', 'Nails', 'Shape, cuticle care and gel colour.', 50, 10, 4500, 0, 1),
    ('crenshawnails', 'Acrylic full set', 'Nails', 'Medium length, one colour.', 90, 10, 7500, 2000, 2),
    ('crenshawnails', 'Pedicure', 'Nails', 'Soak, scrub and polish.', 50, 10, 4000, 0, 3),
    ('bealebarbers', 'Haircut', 'Cuts', 'Clipper or scissor cut with line-up.', 40, 5, 3000, 0, 1),
    ('bealebarbers', 'Hot-towel shave', 'Shaves', 'Straight razor, hot towel.', 30, 5, 2800, 0, 2),
    ('wuselashes', 'Classic lash set', 'Lashes', 'One extension to each natural lash.', 90, 10, 2500000, 500000, 1),
    ('wuselashes', 'Volume lash set', 'Lashes', 'Handmade fans for a fuller look.', 120, 10, 3500000, 1000000, 2),
    ('wuselashes', 'Brow lamination', 'Brows', 'Lift, set and tint.', 45, 5, 1500000, 0, 3),
    ('gardencitymakeup', 'Event makeup', 'Makeup', 'At your address. Lashes included.', 75, 30, 3000000, 1000000, 1),
    ('gardencitymakeup', 'Bridal makeup', 'Makeup', 'At your address on the day. A trial is booked separately.', 120, 30, 8000000, 3000000, 2),
    ('gardencitymakeup', 'Bridal trial', 'Makeup', 'The full look, before the day.', 90, 30, 3500000, 1000000, 3)
  ) as s(slug, name, category, description, dur, buf, price, dep, sort) on s.slug = b.slug
  returning id, business_id
),
ss as (
  insert into staff_services (staff_id, service_id)
  select st.id, sv.id from st join sv on sv.business_id = st.business_id
  returning staff_id
),
rv as (
  -- Three reviews each; the rating and count on the business are exactly these.
  insert into reviews (business_id, author_name, service_name, rating, body, status)
  select b.id, r.author, r.service, r.rating, r.body, 'published'
  from b join (values
    ('peachtree', 'Monique T.', 'Loc retwist', 5, 'Neat parts and no pulling. In and out in ninety minutes.'),
    ('peachtree', 'Aisha K.', 'Knotless braids · medium', 5, 'Light on my edges and the length was what I asked for.'),
    ('peachtree', 'Dee W.', 'Starter locs', 4, 'Happy with my coils. Parking took a while on a Saturday.'),
    ('bayoufade', 'Marcus L.', 'Skin fade', 5, 'Sharp fade and he took his time with the line-up.'),
    ('bayoufade', 'Javier R.', 'Taper + beard', 5, 'Booked at lunch and was back at my desk on time.'),
    ('bayoufade', 'Tobi A.', 'Kids cut', 4, 'Patient with my son. We waited ten minutes past the time.'),
    ('harlemglow', 'Nadia P.', 'Signature facial', 5, 'She explained every step and what to use at home.'),
    ('harlemglow', 'Carla M.', 'Brow shape', 5, 'Clean shape, no redness after.'),
    ('harlemglow', 'Yemi O.', 'Chemical peel', 5, 'The patch test first made me trust the rest.'),
    ('crenshawnails', 'Brianna S.', 'Gel manicure', 5, 'Three weeks on and no chips.'),
    ('crenshawnails', 'Rosa D.', 'Acrylic full set', 4, 'Lovely shape. It ran twenty minutes over.'),
    ('crenshawnails', 'Kim H.', 'Pedicure', 5, 'Clean tools and an easy place to park.'),
    ('bealebarbers', 'Darnell J.', 'Haircut', 5, 'Same cut every time, which is what I want.'),
    ('bealebarbers', 'Sam P.', 'Hot-towel shave', 5, 'Closest shave I have had.'),
    ('bealebarbers', 'Ike N.', 'Haircut', 4, 'Good cut. Busy on a Friday evening, so book ahead.'),
    ('wuselashes', 'Amina S.', 'Classic lash set', 5, 'Natural look and they lasted the full three weeks.'),
    ('wuselashes', 'Chioma E.', 'Volume lash set', 5, 'Full without feeling heavy.'),
    ('wuselashes', 'Zainab M.', 'Brow lamination', 4, 'Brows came out well. The room was a little warm.'),
    ('gardencitymakeup', 'Boma J.', 'Bridal makeup', 5, 'She arrived early and the look held through the reception.'),
    ('gardencitymakeup', 'Tamuno P.', 'Event makeup', 5, 'Came to my house on time with everything she needed.'),
    ('gardencitymakeup', 'Ebiere A.', 'Bridal trial', 5, 'The trial meant no surprises on the day.')
  ) as r(slug, author, service, rating, body) on r.slug = b.slug
  returning id
)
select 1;
