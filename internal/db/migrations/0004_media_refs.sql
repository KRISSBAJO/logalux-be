-- Images can now belong to one thing inside a slot: a category tile, a
-- business, or a product. The hero keeps an empty ref.

alter table site_media add column ref text not null default '';
drop index if exists site_media_slot;
create index site_media_slot_ref on site_media (slot, ref, active, sort, created_at);
