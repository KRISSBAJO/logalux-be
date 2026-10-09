alter table bookings add column is_internal boolean not null default false;
create function customer_business_member(customer uuid, business uuid) returns boolean language sql stable as $$
 select exists(select 1 from merchant_customer_links l join merchant_members m on m.merchant_id=l.merchant_id where l.user_id=customer and m.business_id=business)
$$;
create function guard_affiliated_review() returns trigger language plpgsql as $$
declare customer uuid; business uuid; internal boolean;
begin
 if TG_TABLE_NAME='reviews' then
  select b.user_id,b.business_id,b.is_internal into customer,business,internal from bookings b where b.id=NEW.booking_id;
 else
  customer:=NEW.user_id; select p.business_id into business from products p where p.id=NEW.product_id; internal:=false;
 end if;
 if coalesce(internal,false) or customer_business_member(customer,business) then raise exception 'Owners and staff cannot review their own business' using errcode='23514'; end if;
 return NEW;
end $$;
create trigger reviews_no_self_review before insert or update on reviews for each row execute function guard_affiliated_review();
create trigger product_reviews_no_self_review before insert or update on product_reviews for each row execute function guard_affiliated_review();
