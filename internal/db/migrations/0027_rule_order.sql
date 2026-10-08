-- The order pricing rules are applied in is the business's to choose.
alter table price_rules add column if not exists sort int not null default 0;
update price_rules pr set sort = x.n from (select id, row_number() over (partition by business_id order by created_at) as n from price_rules) x
  where x.id = pr.id and pr.sort = 0;
