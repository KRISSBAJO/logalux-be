-- Stock per location, loyalty points, and promo codes a business makes itself.

-- ---------- stock per location ----------
-- products.stock stays the total. location_stock says where it sits. A change
-- to the total lands on the location named by the setting lx.location for the
-- transaction, or on the main location when none is named. Transfers move
-- stock between locations without changing the total.
create table if not exists location_stock (
  product_id uuid not null references products(id) on delete cascade,
  location_id uuid not null references locations(id) on delete cascade,
  qty int not null default 0 check (qty >= 0),
  primary key (product_id, location_id)
);
insert into location_stock (product_id, location_id, qty)
select p.id, l.id, greatest(p.stock, 0) from products p join locations l on l.business_id = p.business_id and l.is_primary
on conflict do nothing;

create or replace function sync_location_stock() returns trigger language plpgsql as $$
declare loc uuid; delta int;
begin
  if new.business_id is null or coalesce(current_setting('lx.skip_stock_sync', true), '') = '1' then
    return new;
  end if;
  delta := new.stock - case when tg_op = 'INSERT' then 0 else old.stock end;
  if delta = 0 then
    return new;
  end if;
  loc := nullif(current_setting('lx.location', true), '')::uuid;
  if loc is null or not exists (select 1 from locations where id = loc and business_id = new.business_id) then
    select id into loc from locations where business_id = new.business_id and is_primary limit 1;
  end if;
  if loc is null then
    return new;
  end if;
  insert into location_stock (product_id, location_id, qty) values (new.id, loc, greatest(delta, 0))
  on conflict (product_id, location_id) do update set qty = greatest(location_stock.qty + delta, 0);
  return new;
end $$;
drop trigger if exists products_sync_location_stock on products;
create trigger products_sync_location_stock after insert or update of stock on products for each row execute function sync_location_stock();

-- Closing a location moves what is on its shelves to the main one.
create or replace function move_stock_on_location_delete() returns trigger language plpgsql as $$
declare main uuid;
begin
  select id into main from locations where business_id = old.business_id and is_primary and id <> old.id limit 1;
  if main is not null then
    insert into location_stock (product_id, location_id, qty)
    select product_id, main, qty from location_stock where location_id = old.id
    on conflict (product_id, location_id) do update set qty = location_stock.qty + excluded.qty;
  end if;
  return old;
end $$;
drop trigger if exists locations_move_stock on locations;
create trigger locations_move_stock before delete on locations for each row execute function move_stock_on_location_delete();

alter table stock_movements add column if not exists location_id uuid references locations(id) on delete set null;
alter table stock_movements drop constraint if exists stock_movements_reason_check;
alter table stock_movements add constraint stock_movements_reason_check check (reason in ('sale', 'restock', 'adjust', 'backbar', 'return', 'count', 'transfer'));
alter table sales add column if not exists location_id uuid references locations(id) on delete set null;

-- ---------- loyalty points ----------
create table if not exists loyalty_points (
  id uuid primary key default gen_random_uuid(),
  business_id uuid not null references businesses(id) on delete cascade,
  client_id uuid not null references clients(id) on delete cascade,
  points int not null check (points <> 0),
  reason text not null check (reason in ('earn', 'redeem', 'adjust')),
  sale_id uuid references sales(id) on delete set null,
  note text not null default '',
  actor text not null default '',
  created_at timestamptz not null default now()
);
create index if not exists loyalty_points_client on loyalty_points (business_id, client_id);

-- ---------- promo codes a business makes for its own clients ----------
alter table promo_codes add column if not exists business_id uuid references businesses(id) on delete cascade;
create index if not exists promo_codes_business on promo_codes (business_id) where business_id is not null;
alter table sales drop constraint if exists sales_method_check;
alter table sales add constraint sales_method_check check (method in ('card', 'tap', 'cash', 'transfer', 'wallet', 'gift', 'link'));
