#!/usr/bin/env bash
# Rechecks three role guards with a live ops session, then removes the rows lx-test2.sh created.
B=http://127.0.0.1:18080/v1
H='Content-Type: application/json'
code(){ curl -s -o /dev/null -w '%{http_code}' "$@"; }
TT=$(curl -s -X POST $B/admin/login -H "$H" -d '{"email":"temp@logaluxe.test","password":"temp-third-pass"}' | node -e 'let d="";process.stdin.on("data",c=>d+=c).on("end",()=>console.log(JSON.parse(d).token))')
TA="Authorization: Bearer $TT"
echo "ops session works: $(code -H "$TA" $B/admin/me) | export bookings $(code -H "$TA" $B/admin/export/bookings) | export audit $(code -H "$TA" $B/admin/export/audit) | edit a page $(code -X PUT -H "$TA" -H "$H" $B/admin/pages/terms -d '{"title":"x","body":"y","published":true}') | send a broadcast $(code -X POST -H "$TA" $B/admin/broadcasts/00000000-0000-0000-0000-000000000000/send) | reset someone's 2fa $(code -X POST -H "$TA" $B/admin/team/00000000-0000-0000-0000-000000000000/reset-2fa)"

docker exec -i logaluxe-db sh -c 'psql -v ON_ERROR_STOP=1 -U "$POSTGRES_USER" -d "$POSTGRES_DB" -q' <<'SQL'
begin;
delete from payment_events where order_id in (select id from orders where customer_name in ('Promo Tester','Again','Cheat'));
delete from gift_card_txns where gift_card_id in (select id from gift_cards where recipient_email = 'gift@logaluxe.test');
delete from orders where customer_name in ('Promo Tester','Again','Cheat');
delete from gift_cards where recipient_email = 'gift@logaluxe.test';
delete from payment_events where booking_id in (select id from bookings where client_name = 'Promo Booker');
delete from bookings where client_name = 'Promo Booker';
delete from clients where name = 'Promo Booker';
delete from promo_codes where code like 'ZZ%';
delete from products where slug = 'zz-test-oil';
delete from businesses where slug = 'zz-test-salon';
delete from support_tickets where subject like 'Zz test%';
delete from broadcasts where subject = 'Zz test notice' or body like '%a short test text%';
delete from admin_users where email = 'temp@logaluxe.test';
commit;
select 'left over' as check, (select count(*) from businesses where slug like 'zz-%') as businesses, (select count(*) from products where slug like 'zz-%') as products,
  (select count(*) from promo_codes) as promos, (select count(*) from gift_cards) as gift_cards, (select count(*) from support_tickets) as tickets, (select count(*) from broadcasts) as broadcasts,
  (select count(*) from admin_users) as admins;
SQL
