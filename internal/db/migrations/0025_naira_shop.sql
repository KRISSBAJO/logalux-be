-- The shop in naira as well as dollars. A product is priced in its seller's currency
-- (brands sell in dollars); an order is in one currency and is paid through that market's provider.
alter table orders add column if not exists currency text not null default 'USD';

-- A Lagos sample business gets a few things to sell, so the naira shop has something in it.
insert into products (business_id, seller_name, slug, name, description, how_to_use, category, price_cents, stock, tone, tags, kind, pickup, shipping, shipping_cents, active, sizes)
select b.id, b.name, x.slug, x.name, x.description, x.how, x.category, x.price, x.stock, x.tone, x.tags, 'retail', true, true, 250000, true, '[]'::jsonb
from businesses b,
  (values ('mnm-body-butter', 'Shea body butter', 'Whipped raw shea with a light citrus scent. Made in small batches in Lagos.', 'Warm a little between your palms and press into damp skin after a bath.', 'skin', 850000, 18, '#4A3426', array['skin','handmade']),
          ('mnm-black-soap', 'African black soap bar', 'Traditional black soap with cocoa pod ash and palm kernel oil. No added colour.', 'Lather in your hands, not straight on the skin. Rinse well and follow with a moisturiser.', 'skin', 450000, 30, '#1F2A33', array['skin','handmade']),
          ('mnm-massage-oil', 'Warming massage oil', 'The ginger and clove blend used in the parlour for the hot stone massage.', 'For the body only. Warm in your hands first. Keep away from the eyes.', 'skin', 1200000, 12, '#3B1D22', array['skin'])
  ) as x(slug, name, description, how, category, price, stock, tone, tags)
where b.slug = 'mnm' and exists (select 1 from sales where created_by = 'sample data')
on conflict (slug) do nothing;
update businesses set returns_days = 7, returns_note = 'Unopened items only.', ship_days_min = 1, ship_days_max = 3, pickup_ready_mins = 30
  where slug = 'mnm' and returns_days is null and exists (select 1 from sales where created_by = 'sample data');
