-- Sample data for development. Dates are relative to now() so the calendar always has today's bookings.
with b as (
  insert into businesses (slug, name, tagline, about, category, market, currency, timezone, phone, instagram, status, verification_status, plan, rating, review_count, tone, highlights, owner_name)
  values
  ('ada', 'Ada''s Braid Studio', 'Knotless, boho, and box braids in East Nashville', 'Braiding since 2019. Hair is included with every install and pre-stretched before you arrive. Gentle on edges, no tight braids, ever. Walk-ins welcome for take-downs.', 'braids', 'US', 'USD', 'America/Chicago', '+16155550144', '@adasbraids', 'live', 'verified', 'pro', 5.00, 212, '#3B1D22', '{"Hair included","Gentle on edges","Walk-ins for take-downs","Free parking","Women-owned"}', 'Ada Okafor'),
  ('barberloft', 'The Barber Loft', 'Fades, beards, and line-ups in The Gulch', 'Six chairs, walk-ins and bookings, open seven days.', 'barber', 'US', 'USD', 'America/Chicago', '+16155552210', '@thebarberloft', 'live', 'verified', 'pro', 4.90, 438, '#1F2A33', '{"Walk-ins welcome","Kids cuts","Card and Apple Pay"}', 'Marcus Hill'),
  ('nia', 'Knotless by Nia', 'Mobile braider, Antioch and south Nashville', 'I come to you. Hair included. Boho and knotless specialist.', 'braids', 'US', 'USD', 'America/Chicago', '+16155556621', '@knotlessbynia', 'live', 'verified', 'free', 4.90, 340, '#4A2A2A', '{"Comes to you","Hair included","Boho specialist"}', 'Nia Bello'),
  ('glow', 'Glow Skin Bar', 'Facials, peels, and brows in 12 South', 'Licensed aestheticians. Patch tests for every new client.', 'skin', 'US', 'USD', 'America/Chicago', '+16155559034', '@glowskinbar', 'live', 'verified', 'free', 4.90, 156, '#5A4A3A', '{"Licensed","Sensitive-skin friendly"}', 'Hannah Price'),
  ('mnm', 'MnM Spa Parlour', 'Massage and spa in Lekki Phase 1', 'Nine therapists, private rooms, open late.', 'spa', 'NG', 'NGN', 'Africa/Lagos', '+2348031234567', '@mnmspa', 'live', 'verified', 'pro', 5.00, 2041, '#4A3426', '{"Private rooms","Open late","Pay with transfer"}', 'Mary Nwosu'),
  ('freedomway', 'Freedom Way Barbers', 'Barbers on Freedom Way, Ajah', 'Three chairs. Fades, line-ups, beard work.', 'barber', 'NG', 'NGN', 'Africa/Lagos', '+2348021177000', '@freedomwaybarbers', 'live', 'verified', 'free', 4.90, 860, '#1F2A33', '{"Walk-ins","USSD and transfer"}', 'Chuks Obi'),
  ('tiwa', 'Knotless by Tiwa', 'Knotless braids in Ikoyi', 'Waist-length knotless, hair included.', 'braids', 'NG', 'NGN', 'Africa/Lagos', '+2348034567890', '@knotlessbytiwa', 'pending', 'pending', 'free', 0, 0, '#4A2A2A', '{"Hair included"}', 'Tiwa Adeleke'),
  ('glowup', 'GlowUp Skin Lagos', 'Skin clinic in Lekki', 'Facials and peels.', 'skin', 'NG', 'NGN', 'Africa/Lagos', '+2348169034000', '@glowuplagos', 'pending', 'pending', 'free', 0, 0, '#5A4A3A', '{}', 'Ngozi Eze')
  returning id, slug, currency
),
loc as (
  insert into locations (business_id, name, address, city, region, country, lat, lng, timezone, is_primary, hours, arrival_notes)
  select id,
    case slug when 'ada' then 'East Nashville' when 'barberloft' then 'The Gulch' when 'nia' then 'Antioch (mobile)' when 'glow' then '12 South' when 'mnm' then 'Lekki Phase 1' when 'freedomway' then 'Ajah' when 'tiwa' then 'Ikoyi' else 'Lekki' end,
    case slug when 'ada' then '1402 Gallatin Ave' when 'barberloft' then '1200 Division St' when 'glow' then '2400 12th Ave S' when 'mnm' then 'Admiralty Way' when 'freedomway' then '3 Freedom Way' when 'tiwa' then '12 Bourdillon Rd' else '' end,
    case when slug in ('ada','barberloft','nia','glow') then 'Nashville' else 'Lagos' end,
    case when slug in ('ada','barberloft','nia','glow') then 'TN' else 'Lagos' end,
    case when slug in ('ada','barberloft','nia','glow') then 'US' else 'NG' end,
    case when slug in ('ada','barberloft','nia','glow') then 36.17 else 6.45 end,
    case when slug in ('ada','barberloft','nia','glow') then -86.75 else 3.47 end,
    case when slug in ('ada','barberloft','nia','glow') then 'America/Chicago' else 'Africa/Lagos' end,
    true,
    case slug
      when 'ada' then '{"mon":null,"tue":["09:00","18:00"],"wed":["09:00","18:00"],"thu":["10:00","19:00"],"fri":["09:00","19:00"],"sat":["08:00","18:00"],"sun":null}'::jsonb
      when 'barberloft' then '{"mon":["09:00","19:00"],"tue":["09:00","19:00"],"wed":["09:00","19:00"],"thu":["09:00","19:00"],"fri":["09:00","20:00"],"sat":["08:00","18:00"],"sun":["10:00","16:00"]}'::jsonb
      else '{"mon":["09:00","18:00"],"tue":["09:00","18:00"],"wed":["09:00","18:00"],"thu":["09:00","18:00"],"fri":["09:00","18:00"],"sat":["09:00","17:00"],"sun":null}'::jsonb
    end,
    case slug when 'ada' then 'Free parking behind the building, ring bell 2.' else '' end
  from b
  returning id, business_id
),
st as (
  insert into staff (business_id, name, initials, role, level, tone, bookable, rating)
  select b.id, s.name, s.initials, s.role, s.level, s.tone, true, s.rating
  from b join (values
    ('ada','Ada Okafor','AO','owner','master','#7A1F2B',5.00),
    ('ada','Nia Bello','NB','staff','senior','#4A2A2A',4.90),
    ('ada','Malik Johnson','MJ','staff','senior','#1F2A33',4.90),
    ('ada','Tola Fade','TF','staff','junior','#3A3A2E',4.80),
    ('barberloft','Marcus Hill','MH','owner','master','#1F2A33',4.90),
    ('barberloft','Dre Simmons','DS','staff','senior','#2E2538',4.80),
    ('nia','Nia Bello','NB','owner','senior','#4A2A2A',4.90),
    ('glow','Hannah Price','HP','owner','master','#5A4A3A',4.90),
    ('mnm','Mary Nwosu','MN','owner','master','#4A3426',5.00),
    ('mnm','Tunde Bakare','TB','staff','senior','#2A3A33',4.90),
    ('freedomway','Chuks Obi','CO','owner','senior','#1F2A33',4.90),
    ('tiwa','Tiwa Adeleke','TA','owner','senior','#4A2A2A',0),
    ('glowup','Ngozi Eze','NE','owner','senior','#5A4A3A',0)
  ) as s(slug,name,initials,role,level,tone,rating) on s.slug=b.slug
  returning id, business_id, name
),
sv as (
  insert into services (business_id, name, category, description, duration_min, processing_min, buffer_min, price_cents, deposit_cents, online, sort)
  select b.id, s.name, s.category, s.description, s.dur, s.proc, s.buf, s.price, s.dep, true, s.sort
  from b join (values
    ('ada','Knotless braids · medium','Braids','Medium knotless braids to waist length. Pre-stretched hair included.',210,0,15,18000,4000,1),
    ('ada','Knotless braids · small','Braids','Small knotless braids, waist length. Hair included.',300,0,15,24000,6000,2),
    ('ada','Boho knotless','Braids','Knotless with curly human-hair ends. Short consult first.',240,0,15,22000,5000,3),
    ('ada','Loc retwist','Locs','Retwist and style. Wash included.',90,0,10,9500,2500,4),
    ('ada','Wash and blow-dry','Add-ons','If you can''t come with clean, stretched hair.',30,0,0,2500,0,5),
    ('ada','Take-down','Add-ons','Removal of previous braids.',45,0,0,3000,0,6),
    ('ada','Scalp treatment','Add-ons','Soothing scalp treatment with processing time.',20,20,0,2500,0,7),
    ('ada','Silk press','Hair','Wash, blow-dry, flat iron finish.',90,0,10,7500,2000,8),
    ('barberloft','Skin fade','Cuts','Skin fade with line-up.',45,0,5,3500,0,1),
    ('barberloft','Skin fade + beard','Cuts','Fade plus beard shape and line.',45,0,5,4500,0,2),
    ('barberloft','Line up','Cuts','Edge up only.',20,0,0,2000,0,3),
    ('barberloft','Kids cut','Cuts','Under 12.',30,0,5,2500,0,4),
    ('nia','Knotless braids · medium','Braids','Mobile. Hair included.',210,0,30,16000,4000,1),
    ('nia','Boho knotless','Braids','Mobile. Curly ends.',240,0,30,20000,5000,2),
    ('glow','Signature facial','Skin','Cleanse, exfoliate, mask, massage.',60,0,10,9500,0,1),
    ('glow','Chemical peel','Skin','Patch test required 48 h before.',45,0,10,12000,4000,2),
    ('glow','Brow shape','Brows','Wax or thread.',20,0,5,2500,0,3),
    ('mnm','Full body massage','Massage','60 minutes, private room.',60,0,15,3000000,1000000,1),
    ('mnm','Hot stone massage','Massage','75 minutes.',75,0,15,4000000,1000000,2),
    ('mnm','Facial','Skin','Deep cleanse facial.',45,0,10,2500000,0,3),
    ('freedomway','Skin fade','Cuts','Fade with line-up.',40,0,5,700000,0,1),
    ('freedomway','Fade + beard','Cuts','Fade and beard.',45,0,5,900000,0,2),
    ('tiwa','Knotless braids · small','Braids','Waist length, hair included.',300,0,15,4500000,1000000,1),
    ('glowup','Facial','Skin','Classic facial.',60,0,10,3500000,0,1)
  ) as s(slug,name,category,description,dur,proc,buf,price,dep,sort) on s.slug=b.slug
  returning id, business_id, name, duration_min, price_cents
),
ss as (
  -- every staff member can do every service of their business, except add-ons/retail logic kept simple
  insert into staff_services (staff_id, service_id)
  select st.id, sv.id from st join sv on sv.business_id = st.business_id
  returning staff_id
),
cl as (
  insert into clients (business_id, name, phone, notes, tags, no_show_count)
  select b.id, c.name, c.phone, c.notes, c.tags::text[], c.ns from b join (values
    ('ada','Kemi Adeyemi','+16155554471','Medium, waist length, 1B with 30 ends. Sensitive edges, lighter gel.','{"vip"}',0),
    ('ada','Tomi Alade','+16155552210','Silk press, flat iron finish, no trim. Allergic to coconut oil.','{"vip"}',0),
    ('ada','Zara Mensah','+16295558800','Mid fade, line up the beard.','{"new"}',0),
    ('ada','Grace Eze','+16155559034','Lash fill every 3 weeks.','{}',0),
    ('ada','Ifeoma Nwosu','+16155551177','One no-show in July. Full prepayment required.','{"lapsed"}',1),
    ('ada','Dami Osei','+16155556621','Silk press. Prefers mornings.','{"waitlist"}',0),
    ('ada','Bola Kalu','+16295553302','Gel manicure, nude tones.','{}',0),
    ('ada','Chidinma Okoro','+16155557045','Small knotless, bringing her own hair.','{"new"}',0)
  ) as c(slug,name,phone,notes,tags,ns) on c.slug=b.slug
  returning id, business_id, name, phone
),
today as (
  select (date_trunc('day', now() at time zone 'America/Chicago')) as d
),
bk as (
  insert into bookings (business_id, location_id, staff_id, client_id, client_name, client_phone, status, starts_at, ends_at, source, total_cents, deposit_cents, deposit_paid, notes)
  select b.id, loc.id, st.id, cl.id, cl.name, cl.phone, x.status,
    (today.d + x.start_min * interval '1 minute') at time zone 'America/Chicago',
    (today.d + (x.start_min + x.dur) * interval '1 minute') at time zone 'America/Chicago',
    x.source, x.total, x.dep, x.dep > 0, x.notes
  from b
  join loc on loc.business_id = b.id
  join today on true
  join (values
    ('ada','Ada Okafor','Zara Mensah','in_progress',540,45,'search',3500,0,'New client'),
    ('ada','Ada Okafor','Kemi Adeyemi','confirmed',600,210,'link',18000,4000,'Same as last time, slightly longer'),
    ('ada','Ada Okafor','Tomi Alade','confirmed',870,90,'rebook',7500,0,''),
    ('ada','Ada Okafor','Grace Eze','confirmed',960,60,'whatsapp',7000,0,''),
    ('ada','Ada Okafor','Bola Kalu','confirmed',1035,50,'search',4500,1500,''),
    ('ada','Nia Bello','Chidinma Okoro','confirmed',510,300,'whatsapp',24000,6000,'Waist length, own hair'),
    ('ada','Malik Johnson','Dami Osei','confirmed',600,45,'walk_in',3500,0,''),
    ('ada','Tola Fade','Ifeoma Nwosu','confirmed',690,75,'search',9500,0,'')
  ) as x(slug,staff_name,client_name,status,start_min,dur,source,total,dep,notes) on x.slug=b.slug
  join st on st.business_id=b.id and st.name=x.staff_name
  join cl on cl.business_id=b.id and cl.name=x.client_name
  returning id, business_id, client_name, total_cents
),
rv as (
  insert into reviews (business_id, author_name, service_name, rating, body, status, flag_reason, reply)
  select b.id, r.author, r.service, r.rating, r.body, r.status, r.flag, r.reply from b join (values
    ('ada','Kemi A.','Knotless braids · medium',5,'Exactly what I booked. Medium knotless to my waist, zero tension on my edges, and she finished right on time.','published','',''),
    ('ada','Tomi A.','Silk press',5,'Silk press lasted two weeks through Nashville humidity. Clean space, easy parking.','published','','Thank you Tomi! See you in three weeks.'),
    ('ada','Grace E.','Lash fill',4,'Great fill, a little wait at the start but Ada messaged me before I arrived so I knew.','published','',''),
    ('ada','Guest 4471','Knotless braids',1,'Terrible. She was rude and the braids were too tight. Do not go to this woman, she should not be allowed to touch anyone''s hair.','flagged','Personal attack on a named person (model 0.91)',''),
    ('mnm','Tunde A.','Full body massage',5,'Best spa in Lekki, very professional, the massage was heavenly. 10/10 would recommend to everyone!!','flagged','7 reviews from one device in 4 days',''),
    ('glow','Simi A.','Signature facial',2,'The facial was fine but I broke out two days later, see photo. Staff were kind though.','flagged','Photo shows a face',''),
    ('freedomway','Obi N.','Skin fade',1,'Waited 50 minutes past my booking time then he rushed the cut. Not coming back.','flagged','Business appeal: client was late',''),
    ('barberloft','Marcus T.','Skin fade + beard',5,'Clean fade every time.','published','','')
  ) as r(slug,author,service,rating,body,status,flag,reply) on r.slug=b.slug
  returning id
),
pr as (
  insert into products (business_id, seller_name, slug, name, description, how_to_use, category, price_cents, compare_cents, stock, sizes, tone, tags, rating, review_count, sold, pickup, shipping)
  select (select id from b where b.slug = p.bslug), p.seller, p.slug, p.name, p.description, p.how, p.category, p.price, p.compare, p.stock, p.sizes::jsonb, p.tone, p.tags::text[], p.rating, p.reviews, p.sold, true, true
  from (values
    ('ada','Ada''s Braid Studio','scalp-oil','Nourishing scalp oil','Light, fast-absorbing oil for braids, locs, and protective styles. Jojoba, black seed, and peppermint. No mineral oil, no silicones.','Part the hair where you can reach the scalp. Drop, don''t pour. Massage for a minute. Wait 48 hours after a fresh install.','hair',1800,null,12,'[{"label":"30 ml","price_cents":1100},{"label":"60 ml","price_cents":1800},{"label":"120 ml","price_cents":3000}]','#3B1D22','{"braids","scalp","bestseller"}',4.90,212,1040),
    ('ada','Ada''s Braid Studio','edge-mousse','Edge control mousse','Firm hold without flaking. Washes out.','Small amount on edges, brush, tie down 10 minutes.','styling',1400,null,4,'[{"label":"120 ml","price_cents":1400}]','#4A3426','{"braids","styling","bestseller"}',4.80,96,520),
    (null,'Silk & Co.','silk-bonnet','Silk bonnet · adjustable','Mulberry silk, adjustable band.','Wear nightly.','tools',2200,null,40,'[{"label":"One size","price_cents":2200}]','#2E2538','{"tools","sleep"}',4.90,1200,3100),
    (null,'Root Theory','clarifying-shampoo','Clarifying shampoo','Removes build-up before an install.','Use once before each install.','hair',2400,2800,30,'[{"label":"300 ml","price_cents":2400}]','#1F2A33','{"hair","wash"}',4.70,430,980),
    ('nia','Knotless by Nia','braiding-gel','Braiding gel · firm hold, no flake','Nia''s own gel.','Thin layer at the root before parting.','styling',1200,null,25,'[{"label":"250 ml","price_cents":1200}]','#5A4A3A','{"braids","styling"}',4.80,180,610),
    (null,'Root Theory','shine-spray','Shine and anti-humidity spray','For silk press care.','Light mist after styling.','styling',1600,null,18,'[{"label":"150 ml","price_cents":1600}]','#3A3A2E','{"silk press","styling"}',4.60,210,400),
    (null,'Root Theory','deep-repair-mask','Deep repair mask','Weekly treatment for stressed hair. Made in Nigeria.','Apply to damp hair for 15 minutes.','hair',2600,null,22,'[{"label":"200 ml","price_cents":2600}]','#4A2A2A','{"hair","made in nigeria"}',4.80,340,720),
    (null,'LogaLuxe','gift-card','LogaLuxe gift card','Any business, any amount.','Send by WhatsApp or email.','gift',2500,null,999999,'[{"label":"$25","price_cents":2500},{"label":"$50","price_cents":5000},{"label":"$100","price_cents":10000}]','#1A1513','{"gift"}',5.00,0,0)
  ) as p(bslug,seller,slug,name,description,how,category,price,compare,stock,sizes,tone,tags,rating,reviews,sold)
  returning id
),
vr as (
  insert into verification_requests (business_id, status, risk_score, id_type, id_provider, licence_status, portfolio_note, created_at)
  select b.id, v.status, v.risk, v.idt, v.prov, v.lic, v.note, now() - v.age from b join (values
    ('tiwa','pending',12,'NIN slip','Smile ID','not_required','24 photos, none found elsewhere online', interval '19 hours'),
    ('glowup','needs_info',38,'Passport','Smile ID','missing','3 of 12 photos appear on another salon''s Instagram', interval '22 hours')
  ) as v(slug,status,risk,idt,prov,lic,note,age) on v.slug=b.slug
  returning id
),
ds as (
  insert into disputes (ref, business_id, booking_id, client_name, amount_cents, currency, reason, client_statement, business_statement, status, business_deadline, created_at)
  select d.ref, b.id, (select id from bk where bk.business_id=b.id and bk.client_name=d.client limit 1), d.client, d.amt, b.currency, d.reason, d.cs, d.bs, d.status, now() + d.deadline, now() - d.age
  from b join (values
    ('DS-2041','ada','Kemi Adeyemi',14000,'Service not as described','I asked for medium and they came out small, it took 5 hours instead of 3.5.','Kemi asked for them smaller halfway through. The trim was on the booking from the start.','with_business', interval '47 hours', interval '1 hour'),
    ('DS-2039','freedomway','Obi Nwachukwu',700000,'Long wait','Waited 50 minutes. Want half back.','He was late, not me.','needs_decision', interval '-2 hours', interval '1 day'),
    ('DS-2037','glow','Simi Adeyemi',9500,'Reaction after service','Broke out badly two days after.','Offered a free follow-up and a product. Happy to refund half.','with_business', interval '9 hours', interval '2 days')
  ) as d(ref,slug,client,amt,reason,cs,bs,status,deadline,age) on d.slug=b.slug
  returning id
)
select 1;

insert into fees (market, plan, transaction_pct, transaction_fixed_cents, transaction_cap_cents, new_client_pct, instant_payout_pct, instant_payout_min_cents, marketplace_pct, chargeback_cents, plan_price_cents, approved_by) values
 ('US','free',2.90,30,null,20,1,50,12,1500,0,'system'),
 ('US','pro',2.60,30,null,15,1,50,12,1500,4900,'system'),
 ('NG','free',2.90,0,250000,15,0,10000,10,500000,0,'system'),
 ('NG','pro',2.50,0,200000,10,0,10000,10,500000,1500000,'system');

insert into feature_flags (key, name, description, rollout_pct, market, plan, enabled) values
 ('instant_booking_default','Instant booking on by default','New businesses start with confirm-without-approval',100,null,null,true),
 ('whatsapp_two_way','Two-way WhatsApp inbox','Replies land in the business inbox',100,null,null,true),
 ('ai_reply_drafts','AI reply drafts','Suggested replies in the merchant inbox',100,null,'pro',true),
 ('ai_booking_assistant','AI booking assistant','Chat on profile pages that books a slot',10,'US',null,true),
 ('smart_gap_fill','Smart gap filling','Offers cancellations to likely clients',25,null,'pro',true),
 ('virtual_consults','Virtual consultations','Video consult before first booking',0,null,null,false),
 ('marketplace_products','Product marketplace','Cart, orders, delivery',100,null,null,true);

insert into audit_log (actor, action, target, after) values
 ('system','seed','database','{"note":"sample data loaded"}'),
 ('kriss@logaxp','flag.update','ai_booking_assistant','{"rollout_pct":10,"market":"US"}'),
 ('sade@logaxp','verification.approve','Knotless by Tiwa','{"status":"approved"}');
