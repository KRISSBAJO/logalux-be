#!/usr/bin/env bash
# Customer accounts: sign up, sign in, book while signed in, see it, cancel it, reset the password.
# Start the API with MAIL_PROVIDER=log first. Cleans up after itself.
B=http://127.0.0.1:18080/v1
H='Content-Type: application/json'
j(){ node -e 'let d="";process.stdin.on("data",c=>d+=c).on("end",()=>{try{const o=JSON.parse(d);console.log(eval(process.argv[1]))}catch(e){console.log("PARSE_FAIL",d.slice(0,300))}})' "$1"; }
code(){ curl -s -o /dev/null -w '%{http_code}' "$@"; }
E=zzcustomer@logaluxe.test

echo "1 bad sign-ups:   $(curl -s -X POST -H "$H" $B/auth/signup -d '{"first_name":"","email":"x@y.co","password":"longenough"}' | j 'o.error') | $(curl -s -X POST -H "$H" $B/auth/signup -d '{"first_name":"Zz","email":"nope","password":"longenough"}' | j 'o.error') | $(curl -s -X POST -H "$H" $B/auth/signup -d '{"first_name":"Zz","email":"a@b.co","password":"short"}' | j 'o.error') | $(curl -s -X POST -H "$H" $B/auth/signup -d '{"first_name":"Zz","email":"a@b.co","phone":"call me","password":"longenough"}' | j 'o.error')"
S=$(curl -s -X POST -H "$H" $B/auth/signup -d "{\"first_name\":\"Zz\",\"last_name\":\"Customer\",\"email\":\"$E\",\"phone\":\"+1 (615) 000-4444\",\"password\":\"first-password\"}")
T=$(echo "$S" | j 'o.token'); A="Authorization: Bearer $T"
echo "2 sign up:        $(echo "$S" | j 'o.error||(o.user.first_name+" "+o.user.email+" phone "+o.user.phone+" token "+o.token.length)') | again: $(curl -s -X POST -H "$H" $B/auth/signup -d "{\"first_name\":\"Zz\",\"email\":\"$E\",\"password\":\"first-password\"}" | j 'o.error')"
echo "3 me:             guest $(code $B/auth/me) | signed in $(curl -s -H "$A" $B/auth/me | j 'o.user.first_name+", bookings "+o.bookings.length+", orders "+o.orders.length') | a console token is refused: $(code -H "Authorization: Bearer $(curl -s -X POST $B/admin/login -H "$H" -d "{\"email\":\"admin@logaluxe.test\",\"password\":\"$(grep '^ADMIN_PASSWORD=' "$1/.env" | cut -d= -f2-)\"}" | j 'o.token')" $B/auth/me) | a customer token on the console: $(code -H "$A" $B/admin/me)"
echo "4 sign in:        wrong password: $(curl -s -X POST -H "$H" $B/auth/login -d "{\"email\":\"$E\",\"password\":\"nope\"}" | j 'o.error') | unknown email: $(curl -s -X POST -H "$H" $B/auth/login -d '{"email":"ghost@logaluxe.test","password":"nope"}' | j 'o.error') | right: $(curl -s -X POST -H "$H" $B/auth/login -d "{\"email\":\"ZZCustomer@LogaLuxe.test\",\"password\":\"first-password\"}" | j 'o.token?"ok":o.error')"
SV=$(curl -s $B/businesses/ada | j 'o.services[0].id'); ST=$(curl -s $B/businesses/ada | j 'o.staff[0].id')
BK=$(curl -s -X POST -H "$A" -H "$H" $B/bookings -d "{\"business_slug\":\"ada\",\"staff_id\":\"$ST\",\"starts_at\":\"2026-11-03T10:00:00-06:00\",\"service_ids\":[\"$SV\"],\"client_name\":\"Zz Customer\",\"client_phone\":\"+16150004444\"}" | j 'o.booking.id')
GK=$(curl -s -X POST -H "$H" $B/bookings -d "{\"business_slug\":\"ada\",\"staff_id\":\"$ST\",\"starts_at\":\"2026-11-04T10:00:00-06:00\",\"service_ids\":[\"$SV\"],\"client_name\":\"Zz Guest\",\"client_phone\":\"+16150004444\"}" | j 'o.booking.id')
OR=$(curl -s -X POST -H "$A" -H "$H" $B/orders -d '{"customer_name":"Zz Customer","fulfilment":"pickup","items":[{"product_slug":"scalp-oil","qty":1}]}')
echo "5 book and buy:   $(curl -s -H "$A" $B/auth/me | j '"bookings "+o.bookings.length+" ("+o.bookings[0].business+", "+o.bookings[0].services+", can cancel "+o.bookings[0].can_cancel+"), orders "+o.orders.length+" ("+o.orders[0].items+")"') | a guest booking with the same phone is not shown: $(curl -s -H "$A" $B/auth/me | j '!o.bookings.some(b=>b.id=="'$GK'")') | order hides the account id: $(echo "$OR" | j '!("user_id" in o.order)')"
echo "6 cancel:         another person booking: $(curl -s -X POST -H "$A" $B/auth/bookings/$GK/cancel | j 'o.error||o.ok') | mine: $(curl -s -X POST -H "$A" $B/auth/bookings/$BK/cancel | j 'o.error||o.ok') | twice: $(curl -s -X POST -H "$A" $B/auth/bookings/$BK/cancel | j 'o.error||o.ok') | status now $(curl -s -H "$A" $B/auth/me | j 'o.bookings[0].status')"
echo "7 profile:        $(curl -s -X PUT -H "$A" -H "$H" $B/auth/me -d '{"first_name":"Zed","last_name":"Customer","phone":"+16150005555"}' | j 'o.error||o.ok') -> $(curl -s -H "$A" "$B/auth/me?brief=1" | j 'o.user.first_name+" "+o.user.phone')"
T2=$(curl -s -X POST -H "$H" $B/auth/login -d "{\"email\":\"$E\",\"password\":\"first-password\"}" | j 'o.token')
echo "8 password:       wrong current: $(curl -s -X POST -H "$A" -H "$H" $B/auth/password -d '{"current":"nope","new":"second-password"}' | j 'o.error||o.ok') | change: $(curl -s -X POST -H "$A" -H "$H" $B/auth/password -d '{"current":"first-password","new":"second-password"}' | j 'o.error||o.ok') | this session $(code -H "$A" $B/auth/me) | other session $(code -H "Authorization: Bearer $T2" $B/auth/me)"
curl -s -X POST -H "$H" $B/auth/forgot -d '{"email":"ghost@logaluxe.test"}' >/dev/null; curl -s -X POST -H "$H" $B/auth/forgot -d "{\"email\":\"$E\"}" >/dev/null; sleep 1
RT=$(docker logs logaluxe-api 2>&1 | grep "$E" | grep -o '/reset?token=[0-9a-f]*' | tail -1 | cut -d= -f2)
echo "9 reset:          link token ${#RT} chars | $(curl -s -X POST -H "$H" $B/auth/reset -d "{\"token\":\"$RT\",\"password\":\"third-password\"}" | j 'o.error||o.ok') | old session $(code -H "$A" $B/auth/me) | reuse: $(curl -s -X POST -H "$H" $B/auth/reset -d "{\"token\":\"$RT\",\"password\":\"fourth-password\"}" | j 'o.error||o.ok') | sign in with the new one: $(curl -s -X POST -H "$H" $B/auth/login -d "{\"email\":\"$E\",\"password\":\"third-password\"}" | j 'o.token?"ok":o.error')"
T3=$(curl -s -X POST -H "$H" $B/auth/login -d "{\"email\":\"$E\",\"password\":\"third-password\"}" | j 'o.token')
echo "10 sign out:      $(curl -s -X POST -H "Authorization: Bearer $T3" $B/auth/logout | j 'o.ok') -> $(code -H "Authorization: Bearer $T3" $B/auth/me)"

docker exec -i logaluxe-db sh -c 'psql -v ON_ERROR_STOP=1 -U "$POSTGRES_USER" -d "$POSTGRES_DB" -q' <<'SQL'
begin;
delete from payment_events where booking_id in (select id from bookings where client_name in ('Zz Customer','Zz Guest'));
delete from payment_events where order_id in (select id from orders where customer_name = 'Zz Customer');
update products p set stock = p.stock + oi.qty, sold = greatest(p.sold - oi.qty, 0) from order_items oi join orders o on o.id = oi.order_id where o.customer_name = 'Zz Customer' and oi.product_id = p.id;
delete from orders where customer_name = 'Zz Customer';
delete from bookings where client_name in ('Zz Customer','Zz Guest');
delete from clients where name in ('Zz Customer','Zz Guest');
delete from users where email = 'zzcustomer@logaluxe.test';
commit;
select 'cleaned up; test customers left: ' || count(*) from users where email like 'zz%';
SQL
