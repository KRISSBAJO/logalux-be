#!/usr/bin/env bash
# Exercises the LogaLuxe admin API end to end against the local container.
B=http://127.0.0.1:18080/v1
j(){ node -e 'let d="";process.stdin.on("data",c=>d+=c).on("end",()=>{try{const o=JSON.parse(d);console.log(eval(process.argv[1]))}catch(e){console.log("PARSE_FAIL",d.slice(0,200))}})' "$1"; }
PW=$(grep '^ADMIN_PASSWORD=' "$1/.env" | cut -d= -f2-)
H='Content-Type: application/json'
echo "1 bad login:      $(curl -s -o /dev/null -w '%{http_code}' -X POST $B/admin/login -H "$H" -d '{"email":"admin@logaluxe.test","password":"wrong-password"}')"
echo "2 no token:       $(curl -s -o /dev/null -w '%{http_code}' $B/admin/overview)"
T=$(curl -s -X POST $B/admin/login -H "$H" -d "{\"email\":\"admin@logaluxe.test\",\"password\":\"$PW\"}" | j 'o.token')
A="Authorization: Bearer $T"
echo "3 login token len: ${#T}"
echo "4 me:             $(curl -s -H "$A" $B/admin/me | j 'o.admin.email+" "+o.admin.role')"
echo "5 health:         $(curl -s -H "$A" $B/admin/health | j 'o.checks.map(c=>c.name+"="+c.value).join("; ")')"
echo "6 search ada:     $(curl -s -H "$A" "$B/admin/search?q=ada" | j 'Object.entries(o).map(([k,v])=>k+":"+v.length).join(" ")')"
BID=$(curl -s -H "$A" "$B/admin/businesses?q=ada" | j 'o.businesses[0].id')
echo "7 biz detail:     $(curl -s -H "$A" $B/admin/businesses/$BID | j 'o.business.name+" staff:"+o.staff.length+" bookings:"+o.bookings.length+" payouts:"+o.payouts.length+" clients:"+o.stats.clients')"
echo "8 note:           $(curl -s -X POST -H "$A" -H "$H" $B/admin/businesses/$BID/notes -d '{"body":"API test note"}' | j 'o.error||o.ok')"
echo "9 credit ok:      $(curl -s -X POST -H "$A" -H "$H" $B/admin/businesses/$BID/credits -d '{"amount_cents":2500,"reason":"goodwill","client_phone":"+16155554471"}' | j 'o.error||o.ok')"
echo "10 plan:          $(curl -s -X PATCH -H "$A" -H "$H" $B/admin/businesses/$BID -d '{"plan":"pro"}' | j 'o.error||o.ok')"
echo "11 bookings:      $(curl -s -H "$A" "$B/admin/bookings" | j 'o.bookings.length+" first="+o.bookings[0].client_name+" / "+o.bookings[0].services')"
BK=$(curl -s -H "$A" "$B/admin/bookings?q=Browser" | j 'o.bookings[0].id')
BK2=$(curl -s -H "$A" "$B/admin/bookings?q=Test%20Client" | j 'o.bookings[0].id')
S2=$(curl -s -H "$A" "$B/admin/bookings?q=Test%20Client" | j 'o.bookings[0].starts_at')
echo "12 resched clash: $(curl -s -X POST -H "$A" -H "$H" $B/admin/bookings/$BK/action -d "{\"action\":\"reschedule\",\"starts_at\":\"$S2\"}" | j 'o.error||o.ok')"
echo "13 resched local: $(curl -s -X POST -H "$A" -H "$H" $B/admin/bookings/$BK/action -d '{"action":"reschedule","starts_at":"2026-10-20T14:00","reason":"client asked"}' | j 'o.error||o.ok')"
echo "14 cancel:        $(curl -s -X POST -H "$A" -H "$H" $B/admin/bookings/$BK2/action -d '{"action":"cancel","reason":"test cleanup"}' | j 'o.error||o.ok')"
echo "15 clients:       $(curl -s -H "$A" "$B/admin/clients" | j 'o.clients.length+" clients, blocked list "+o.blocked.length')"
echo "16 block:         $(curl -s -X POST -H "$A" -H "$H" $B/admin/clients/block -d '{"phone":"+16150000001","blocked":true,"reason":"test"}' | j 'o.error||o.ok')"
SV=$(curl -s $B/businesses/ada | j 'o.services[0].id'); ST=$(curl -s $B/businesses/ada | j 'o.staff[0].id')
echo "17 blocked books: $(curl -s -X POST -H "$H" $B/bookings -d "{\"business_slug\":\"ada\",\"staff_id\":\"$ST\",\"starts_at\":\"2026-10-21T10:00:00-05:00\",\"service_ids\":[\"$SV\"],\"client_name\":\"Blocked\",\"client_phone\":\"+16150000001\"}" | j 'o.error||"BOOKED"')"
echo "18 unblock:       $(curl -s -X POST -H "$A" -H "$H" $B/admin/clients/block -d '{"phone":"+16150000001","blocked":false}' | j 'o.error||o.ok')"
echo "19 orders:        $(curl -s -H "$A" "$B/admin/orders" | j 'o.orders.length+" first: "+o.orders[0].items')"
OID=$(curl -s -H "$A" "$B/admin/orders" | j 'o.orders[0].id')
STK=$(curl -s $B/products/scalp-oil | j 'o.product.stock')
curl -s -X POST -H "$A" -H "$H" $B/admin/orders/$OID/status -d '{"status":"refunded","reason":"test"}' >/dev/null
echo "20 refund restock: $STK -> $(curl -s $B/products/scalp-oil | j 'o.product.stock')"
echo "21 products:      $(curl -s -H "$A" "$B/admin/products" | j 'o.products.length')"
echo "22 payouts:       $(curl -s -H "$A" "$B/admin/payouts" | j 'o.payouts.length+" payouts; "+o.totals.map(t=>t.currency+" "+t.status+" "+t.n).join(", ")+"; payments "+o.payments.length')"
PF=$(curl -s -H "$A" "$B/admin/payouts?status=failed" | j 'o.payouts[0].id')
echo "23 retry failed:  $(curl -s -X POST -H "$A" -H "$H" $B/admin/payouts/$PF/action -d '{"action":"retry"}' | j 'o.error||o.ok')"
PH=$(curl -s -H "$A" "$B/admin/payouts?status=held" | j 'o.payouts[0].id')
echo "24 retry a held:  $(curl -s -X POST -H "$A" -H "$H" $B/admin/payouts/$PH/action -d '{"action":"retry"}' | j 'o.error||o.ok')"
FEE='{"market":"NG","plan":"free","transaction_pct":2.5,"transaction_fixed_cents":0,"transaction_cap_cents":250000,"new_client_pct":15,"instant_payout_pct":0,"marketplace_pct":10,"chargeback_cents":500000,"plan_price_cents":0,"effective_from":"2026-11-01","note":"match Paystack"}'
DEC='{"market":"NG","plan":"free","effective_from":"2026-11-01","decision":"approve"}'
echo "25 propose fee:   $(curl -s -X POST -H "$A" -H "$H" $B/admin/fees -d "$FEE" | j 'o.error||o.ok')"
echo "26 self-approve:  $(curl -s -X POST -H "$A" -H "$H" $B/admin/fees/decide -d "$DEC" | j 'o.error||o.ok')"
echo "27 add ops admin: $(curl -s -X POST -H "$A" -H "$H" $B/admin/team -d '{"email":"sade@logaluxe.test","name":"Sade Adewale","role":"ops","password":"sade-dev-password"}' | j 'o.error||o.ok')"
echo "28 add support:   $(curl -s -X POST -H "$A" -H "$H" $B/admin/team -d '{"email":"joy@logaluxe.test","name":"Joy Okafor","role":"support","password":"joy-dev-password"}' | j 'o.error||o.ok')"
T2=$(curl -s -X POST $B/admin/login -H "$H" -d '{"email":"sade@logaluxe.test","password":"sade-dev-password"}' | j 'o.token'); A2="Authorization: Bearer $T2"
T3=$(curl -s -X POST $B/admin/login -H "$H" -d '{"email":"joy@logaluxe.test","password":"joy-dev-password"}' | j 'o.token'); A3="Authorization: Bearer $T3"
echo "29 ops approves fee: $(curl -s -X POST -H "$A2" -H "$H" $B/admin/fees/decide -d "$DEC" | j 'o.error||o.ok')"
echo "30 support: bookings $(curl -s -o /dev/null -w '%{http_code}' -H "$A3" $B/admin/bookings), resolve dispute $(curl -s -o /dev/null -w '%{http_code}' -X POST -H "$A3" -H "$H" $B/admin/disputes/x/resolve -d '{}'), team $(curl -s -o /dev/null -w '%{http_code}' -H "$A3" $B/admin/team)"
SID=$(curl -s -H "$A" $B/admin/team | j 'o.team.find(t=>t.email=="sade@logaluxe.test").id')
echo "31 promote sade:  $(curl -s -X PUT -H "$A" -H "$H" $B/admin/team/$SID -d '{"role":"super_admin"}' | j 'o.error||o.ok')"
echo "32 2nd approver:  $(curl -s -X POST -H "$A2" -H "$H" $B/admin/fees/decide -d "$DEC" | j 'o.error||o.ok')"
echo "33 fees rows:     $(curl -s -H "$A" $B/admin/fees | j 'o.fees.filter(f=>f.market=="NG"&&f.plan=="free").map(f=>String(f.effective_from).slice(0,10)+" "+f.transaction_pct+"% "+f.status+" by "+f.approved_by+" in_effect="+f.in_effect).join(" | ")')"
echo "34 flag create:   $(curl -s -X POST -H "$A" -H "$H" $B/admin/flags -d '{"key":"group_bookings","name":"Group bookings","description":"Bridal parties and friends","market":"","plan":""}' | j 'o.error||o.ok')"
MID=$(curl -s -H "$A" $B/admin/me | j 'o.admin.id')
echo "35 self-demote:   $(curl -s -X PUT -H "$A" -H "$H" $B/admin/team/$MID -d '{"role":"support"}' | j 'o.error||o.ok')"
echo "36 audit:         $(curl -s -H "$A" "$B/admin/audit" | j 'o.events.length+" events; actors: "+o.actors.map(a=>a.actor+"("+a.n+")").join(", ")')"
curl -s -X POST -H "$A3" $B/admin/logout >/dev/null
echo "37 after logout:  $(curl -s -o /dev/null -w '%{http_code}' -H "$A3" $B/admin/me)"
