-- What the customer web needs: saved businesses, a written summary of a
-- business's reviews, product reviews, and shop orders that each seller
-- fulfils and is paid for.

create table if not exists user_favourites (
  user_id uuid not null references users(id) on delete cascade,
  business_id uuid not null references businesses(id) on delete cascade,
  created_at timestamptz not null default now(),
  primary key (user_id, business_id)
);

-- Two plain sentences on what clients say, written from the published reviews and refreshed when new ones arrive.
alter table businesses add column if not exists review_summary text not null default '';
alter table businesses add column if not exists review_summary_at timestamptz;

create table if not exists product_reviews (
  id uuid primary key default gen_random_uuid(),
  product_id uuid not null references products(id) on delete cascade,
  user_id uuid references users(id) on delete set null,
  author_name text not null,
  rating int not null check (rating between 1 and 5),
  body text not null,
  verified boolean not null default false, -- the reviewer bought it on LogaLuxe
  created_at timestamptz not null default now()
);
create unique index if not exists product_reviews_one_each on product_reviews (product_id, user_id) where user_id is not null;
create index if not exists product_reviews_product on product_reviews (product_id, created_at desc);

-- One row per seller in an order: how that seller's items reach the customer, and how far along they are.
create table if not exists order_shipments (
  id uuid primary key default gen_random_uuid(),
  order_id uuid not null references orders(id) on delete cascade,
  seller_name text not null,
  business_id uuid references businesses(id) on delete set null,
  fulfilment text not null check (fulfilment in ('pickup', 'ship')),
  items_cents int not null default 0,
  shipping_cents int not null default 0,
  status text not null default 'new' check (status in ('new', 'ready', 'shipped', 'delivered', 'collected', 'cancelled')),
  tracking text not null default '',
  updated_at timestamptz not null default now(),
  unique (order_id, seller_name)
);
create index if not exists order_shipments_business on order_shipments (business_id, status);
alter table orders add column if not exists customer_email text not null default '';
alter table ledger add column if not exists order_id uuid references orders(id) on delete set null;

-- Orders placed before sellers had their own rows.
insert into order_shipments (order_id, seller_name, business_id, fulfilment, items_cents, shipping_cents, status)
select o.id, oi.seller_name, max(p.business_id::text)::uuid, o.fulfilment, sum(oi.qty * oi.unit_cents)::int, 0,
       case o.status when 'ready' then 'ready' when 'shipped' then 'shipped' when 'delivered' then case when o.fulfilment = 'pickup' then 'collected' else 'delivered' end when 'cancelled' then 'cancelled' when 'refunded' then 'cancelled' else 'new' end
from orders o join order_items oi on oi.order_id = o.id left join products p on p.id = oi.product_id
group by o.id, oi.seller_name
on conflict do nothing;

-- Product ratings now come from real reviews. Give the sample products a few, from sample client names, then count them.
do $$
declare p record; names text[] := array['Kemi A.', 'Tomi A.', 'Dami O.', 'Grace E.', 'Bola K.', 'Simi A.'];
  lines text[] := array[
    'Does what it says. I have bought it twice now.',
    'Picked it up at my appointment. Light, not greasy, and it lasts.',
    'Good, though I wish the bottle were a little bigger for the price.',
    'My stylist used it on me first, so I knew what I was getting. No complaints.',
    'Arrived quickly and well packed. Works on my daughter''s hair too.',
    'Solid product. I would buy it again.'];
  stars int[] := array[5, 5, 4, 5, 4, 5];
  n int; i int;
begin
  if exists (select 1 from product_reviews) or not exists (select 1 from sales where created_by = 'sample data') then
    return;
  end if;
  for p in select id, row_number() over (order by slug) as k from products where active loop
    n := 2 + (p.k % 3);
    for i in 1..n loop
      insert into product_reviews (product_id, author_name, rating, body, verified, created_at)
      values (p.id, names[1 + ((p.k + i) % 6)], stars[1 + ((p.k + i * 2) % 6)], lines[1 + ((p.k * 2 + i) % 6)], true, now() - ((i * 9 + p.k) || ' days')::interval);
    end loop;
  end loop;
end $$;
update products p set review_count = x.n, rating = x.avg from (select product_id, count(*) as n, round(avg(rating), 1) as avg from product_reviews group by product_id) x where x.product_id = p.id;
update products set review_count = 0, rating = 0 where not exists (select 1 from product_reviews r where r.product_id = products.id);

-- A brand with no studio on LogaLuxe has no shop to collect from.
update products set pickup = false where business_id is null and pickup;
