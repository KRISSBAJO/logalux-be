alter table merchant_users add column photo_id uuid references site_media(id) on delete set null;
