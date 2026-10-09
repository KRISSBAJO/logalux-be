create table push_devices (
 id uuid primary key default gen_random_uuid(),
 owner_kind text not null check(owner_kind in ('customer','merchant')),
 owner_id uuid not null,
 session_hash text not null,
 token text not null,
 platform text not null check(platform in ('ios','android')),
 updated_at timestamptz not null default now(),
 unique(owner_kind,token)
);
create table push_events (
 id uuid primary key default gen_random_uuid(),
 event_key text not null unique,
 owner_kind text not null,
 owner_id uuid not null,
 title text not null,
 body text not null,
 path text not null,
 created_at timestamptz not null default now()
);
create table push_deliveries (
 event_id uuid not null references push_events(id) on delete cascade,
 device_id uuid not null references push_devices(id) on delete cascade,
 state text not null default 'pending' check(state in ('pending','ticket','sent','failed')),
 ticket_id text,
 attempts int not null default 0,
 next_at timestamptz not null default now(),
 error text,
 primary key(event_id,device_id)
);
create index push_delivery_due on push_deliveries(next_at) where state in ('pending','ticket');
create function queue_mobile_booking_push() returns trigger language plpgsql as $$
begin
 if TG_OP='UPDATE' then
  if old.status=new.status then return new; end if;
 end if;
 if new.user_id is not null then
  insert into push_events(event_key,owner_kind,owner_id,title,body,path)
  values('booking:'||new.id||':'||gen_random_uuid(),'customer',new.user_id,'Appointment update','Open LogaLuxe to see the latest details.','/client/bookings');
 end if;
 if TG_OP='INSERT' then
  insert into push_events(event_key,owner_kind,owner_id,title,body,path)
  select 'new-booking:'||new.id||':'||mm.merchant_id,'merchant',mm.merchant_id,'New appointment','Open your calendar to see the booking.','/business/calendar'
  from merchant_members mm where mm.business_id=new.business_id and (mm.role in ('owner','manager') or mm.staff_id=new.staff_id)
  on conflict(event_key) do nothing;
 end if;
 return new;
end $$;
create trigger mobile_booking_push after insert or update of status on bookings for each row execute function queue_mobile_booking_push();
create function queue_mobile_message_push() returns trigger language plpgsql as $$
declare t threads;
begin
 select * into t from threads where id=new.thread_id;
 if t.channel <> 'in_app' then return new; end if;
 if new.from_business and t.user_id is not null then
  insert into push_events(event_key,owner_kind,owner_id,title,body,path)
  values('message:'||new.id,'customer',t.user_id,'New message','You have a new message in LogaLuxe.','/client/inbox');
 elsif not new.from_business then
  insert into push_events(event_key,owner_kind,owner_id,title,body,path)
  select 'message:'||new.id||':'||mm.merchant_id,'merchant',mm.merchant_id,'New client message','Open your inbox to read it.','/m/inbox'
  from merchant_members mm where mm.business_id=t.business_id and (mm.role in ('owner','manager') or mm.staff_id=t.assigned_staff_id)
  on conflict(event_key) do nothing;
 end if;
 return new;
end $$;
create trigger mobile_message_push after insert on thread_messages for each row execute function queue_mobile_message_push();
