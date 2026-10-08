#!/usr/bin/env bash
# End-to-end test of the account, catalog, export, promo, gift card, support,
# bulk message and page routes. Run with email in log mode: nothing is sent.
# Usage: bash lx-test2.sh <path to logaluxe-be>
B=http://127.0.0.1:18080/v1
H='Content-Type: application/json'
j(){ node -e 'let d="";process.stdin.on("data",c=>d+=c).on("end",()=>{try{const o=JSON.parse(d);console.log(eval(process.argv[1]))}catch(e){console.log("PARSE_FAIL",d.slice(0,300))}})' "$1"; }
totp(){ node -e '
const c=require("crypto"),A="ABCDEFGHIJKLMNOPQRSTUVWXYZ234567";let bits="";for(const ch of process.argv[1])bits+=A.indexOf(ch).toString(2).padStart(5,"0");
const key=Buffer.from(bits.match(/.{8}/g).map(b=>parseInt(b,2)));const m=Buffer.alloc(8);m.writeBigUInt64BE(BigInt(Math.floor(Date.now()/30000)));
const h=c.createHmac("sha1",key).update(m).digest(),o=h[19]&15;console.log(String((h.readUInt32BE(o)&0x7fffffff)%1000000).padStart(6,"0"))' "$1"; }
code(){ curl -s -o /dev/null -w '%{http_code}' "$@"; }
PW=$(grep '^ADMIN_PASSWORD=' "$1/.env" | cut -d= -f2-)
T=$(curl -s -X POST $B/admin/login -H "$H" -d "{\"email\":\"admin@logaluxe.test\",\"password\":\"$PW\"}" | j 'o.token'); A="Authorization: Bearer $T"
post(){ curl -s -X POST -H "$A" -H "$H" "$B$1" -d "$2"; }
put(){ curl -s -X PUT -H "$A" -H "$H" "$B$1" -d "$2"; }

echo "== account and sign-in"
echo "1 account:          $(curl -s -H "$A" $B/admin/account | j '"two_step="+o.two_step+" mail="+o.mail_mode')"
post /admin/team '{"email":"temp@logaluxe.test","name":"Temp Tester","role":"ops","password":"temp-first-pass"}' >/dev/null
TT=$(curl -s -X POST $B/admin/login -H "$H" -d '{"email":"temp@logaluxe.test","password":"temp-first-pass"}' | j 'o.token'); TA="Authorization: Bearer $TT"
echo "2 wrong current pw: $(curl -s -X POST -H "$TA" -H "$H" $B/admin/password -d '{"current":"nope","new":"temp-second-pass"}' | j 'o.error||o.ok')"
echo "3 short new pw:     $(curl -s -X POST -H "$TA" -H "$H" $B/admin/password -d '{"current":"temp-first-pass","new":"short"}' | j 'o.error||o.ok')"
TT2=$(curl -s -X POST $B/admin/login -H "$H" -d '{"email":"temp@logaluxe.test","password":"temp-first-pass"}' | j 'o.token')
echo "4 change pw:        $(curl -s -X POST -H "$TA" -H "$H" $B/admin/password -d '{"current":"temp-first-pass","new":"temp-second-pass"}' | j 'o.error||o.ok') | this session $(code -H "$TA" $B/admin/me) | other session $(code -H "Authorization: Bearer $TT2" $B/admin/me)"
echo "5 forgot (unknown): $(curl -s -X POST -H "$H" $B/admin/forgot -d '{"email":"nobody@logaluxe.test"}' | j 'o.ok')  forgot (real): $(curl -s -X POST -H "$H" $B/admin/forgot -d '{"email":"temp@logaluxe.test"}' | j 'o.ok')"
sleep 1
RT=$(docker logs logaluxe-api 2>&1 | grep -o 'reset?token=[0-9a-f]*' | tail -1 | cut -d= -f2)
echo "6 link in the log:  token length ${#RT}"
echo "7 reset short pw:   $(curl -s -X POST -H "$H" $B/admin/reset -d "{\"token\":\"$RT\",\"password\":\"short\"}" | j 'o.error||o.ok')"
echo "8 reset:            $(curl -s -X POST -H "$H" $B/admin/reset -d "{\"token\":\"$RT\",\"password\":\"temp-third-pass\"}" | j 'o.error||o.ok') | old session now $(code -H "$TA" $B/admin/me)"
echo "9 reuse the link:   $(curl -s -X POST -H "$H" $B/admin/reset -d "{\"token\":\"$RT\",\"password\":\"temp-fourth-pass\"}" | j 'o.error||o.ok')"
TT=$(curl -s -X POST $B/admin/login -H "$H" -d '{"email":"temp@logaluxe.test","password":"temp-third-pass"}' | j 'o.token'); TA="Authorization: Bearer $TT"
SEC=$(curl -s -X POST -H "$TA" $B/admin/2fa/setup | j 'o.secret')
echo "10 2fa setup:       secret length ${#SEC}"
echo "11 enable bad code: $(curl -s -X POST -H "$TA" -H "$H" $B/admin/2fa/enable -d '{"code":"000000"}' | j 'o.error||o.ok')"
EN=$(curl -s -X POST -H "$TA" -H "$H" $B/admin/2fa/enable -d "{\"code\":\"$(totp $SEC)\"}")
RC=$(echo "$EN" | j 'o.recovery_codes[0]')
echo "12 enable:          $(echo "$EN" | j 'o.error||(o.ok+" with "+o.recovery_codes.length+" recovery codes")')"
echo "13 login, no code:  $(curl -s -X POST $B/admin/login -H "$H" -d '{"email":"temp@logaluxe.test","password":"temp-third-pass"}' | j 'o.error+" need_code="+o.need_code')"
echo "14 login, bad code: $(curl -s -X POST $B/admin/login -H "$H" -d '{"email":"temp@logaluxe.test","password":"temp-third-pass","code":"123456"}' | j 'o.error')"
echo "15 login with code: $(curl -s -X POST $B/admin/login -H "$H" -d "{\"email\":\"temp@logaluxe.test\",\"password\":\"temp-third-pass\",\"code\":\"$(totp $SEC)\"}" | j 'o.token?"ok":o.error')"
echo "16 recovery code:   first use $(curl -s -X POST $B/admin/login -H "$H" -d "{\"email\":\"temp@logaluxe.test\",\"password\":\"temp-third-pass\",\"code\":\"$RC\"}" | j 'o.token?"ok":o.error') | second use $(curl -s -X POST $B/admin/login -H "$H" -d "{\"email\":\"temp@logaluxe.test\",\"password\":\"temp-third-pass\",\"code\":\"$RC\"}" | j 'o.token?"ok":o.error')"
TID=$(curl -s -H "$A" $B/admin/team | j 'o.team.find(t=>t.email=="temp@logaluxe.test").id')
echo "17 team shows 2fa:  $(curl -s -H "$A" $B/admin/team | j 'o.team.find(t=>t.email=="temp@logaluxe.test").two_step') | super resets it: $(curl -s -X POST -H "$A" $B/admin/team/$TID/reset-2fa | j 'o.error||o.ok') | login without code: $(curl -s -X POST $B/admin/login -H "$H" -d '{"email":"temp@logaluxe.test","password":"temp-third-pass"}' | j 'o.token?"ok":o.error')"

echo "== businesses, services, team"
echo "18 bad business:    $(post /admin/businesses '{"name":"X","category":"hair","market":"US","owner_name":"A"}' | j 'o.error||o.ok')"
BZ=$(post /admin/businesses '{"name":"Zz Test Salon","category":"hair","market":"US","owner_name":"Tess Tester","tagline":"Test only","city":"Nashville","address":"1 Test St","region":"TN","phone":"+16150009999"}')
BID=$(echo "$BZ" | j 'o.id'); echo "19 create business: $(echo "$BZ" | j 'o.error||("ok slug="+o.slug)') | duplicate: $(post /admin/businesses '{"name":"Zz Test Salon","category":"hair","market":"US","owner_name":"Tess Tester"}' | j 'o.error||o.ok')"
echo "20 detail:          $(curl -s -H "$A" $B/admin/businesses/$BID | j 'o.business.status+"/"+o.business.verification_status+" "+o.business.currency+" tz="+o.business.timezone+" staff="+o.staff.length+" locations="+o.locations.length')  in verification queue: $(curl -s -H "$A" "$B/admin/verification?status=pending" | j 'o.requests.some(r=>r.name=="Zz Test Salon")')"
echo "21 edit profile:    $(put /admin/businesses/$BID/profile '{"name":"Zz Test Salon","category":"nails","owner_name":"Tess Tester","tagline":"Edited","timezone":"America/New_York","highlights":["Walk-ins"," ","Parking"]}' | j 'o.error||o.ok') -> $(curl -s -H "$A" $B/admin/businesses/$BID | j 'o.business.category+" "+o.business.timezone+" "+JSON.stringify(o.business.highlights)+" loc tz="+o.locations[0].timezone')  bad tz: $(put /admin/businesses/$BID/profile '{"name":"Zz Test Salon","category":"nails","owner_name":"T","timezone":"Mars/Base"}' | j 'o.error||o.ok')"
LID=$(curl -s -H "$A" $B/admin/businesses/$BID | j 'o.locations[0].id')
echo "22 hours:           $(put /admin/locations/$LID '{"name":"Downtown","address":"1 Test St","city":"Nashville","region":"TN","arrival_notes":"Ring the bell","hours":{"mon":["10:00","19:00"],"tue":["10:00","19:00"],"sat":["09:00","14:00"]}}' | j 'o.error||o.ok') -> $(curl -s -H "$A" $B/admin/businesses/$BID | j 'o.locations[0].name+" mon="+JSON.stringify(o.locations[0].hours.mon)+" sun="+o.locations[0].hours.sun')  backwards hours: $(put /admin/locations/$LID '{"name":"Downtown","hours":{"mon":["19:00","10:00"]}}' | j 'o.error||o.ok')"
echo "23 bad service:     $(post /admin/businesses/$BID/services '{"name":"Gel set","category":"Nails","duration_min":60,"price_cents":4000,"deposit_cents":9000}' | j 'o.error||o.ok')"
SID=$(post /admin/businesses/$BID/services '{"name":"Gel set","category":"Nails","duration_min":60,"price_cents":4000,"deposit_cents":1000}' | j 'o.id')
ST2=$(post /admin/businesses/$BID/staff '{"name":"Nia Second","role":"staff","level":"junior"}' | j 'o.id')
echo "24 service + staff: $(curl -s -H "$A" $B/admin/businesses/$BID | j '"services="+o.services.length+" staff="+o.staff.length+" who does it="+o.services[0].staff_ids.length')"
echo "25 edit service:    $(put /admin/services/$SID "{\"name\":\"Gel set deluxe\",\"category\":\"Nails\",\"duration_min\":75,\"price_cents\":5500,\"deposit_cents\":1500,\"staff_ids\":[\"$ST2\"]}" | j 'o.error||o.ok') -> $(curl -s -H "$A" $B/admin/businesses/$BID | j 'o.services[0].name+" "+o.services[0].price_cents+" staff="+o.services[0].staff_ids.length')"
echo "26 edit staff:      $(put /admin/staff/$ST2 '{"name":"Nia Second","role":"manager","level":"senior","bookable":true}' | j 'o.error||o.ok')  bad role: $(put /admin/staff/$ST2 '{"name":"Nia","role":"boss"}' | j 'o.error||o.ok')"
curl -s -X POST -H "$A" -H "$H" $B/admin/businesses/$BID/status -d '{"status":"live","reason":"test"}' >/dev/null
SLOT=$(curl -s "$B/businesses/zz-test-salon/availability?date=2026-10-12&services=$SID&staff=any" | j '(o.slots||[]).length+" slots, first "+((o.slots||[])[0]||{}).starts_at')
echo "27 live + bookable: $SLOT"
echo "28 delete service:  $(curl -s -X DELETE -H "$A" $B/admin/services/$SID | j 'o.error||("ok retired="+o.retired)')  delete staff: $(curl -s -X DELETE -H "$A" $B/admin/staff/$ST2 | j 'o.error||("ok retired="+o.retired)')"

echo "== products"
echo "29 bad product:     $(post /admin/products '{"name":"Zz Test Oil","category":"hair","price_cents":1500,"compare_cents":1000,"stock":5,"seller_name":"Zz","pickup":true,"shipping":true}' | j 'o.error||o.ok')"
PR=$(post /admin/products '{"name":"Zz Test Oil","category":"hair","price_cents":1500,"compare_cents":2000,"stock":5,"business_slug":"zz-test-salon","pickup":true,"shipping":true,"shipping_cents":499,"tags":["new"],"sizes":[{"label":"30 ml","price_cents":1500},{"label":"60 ml","price_cents":2600}],"description":"Test only"}')
PID=$(echo "$PR" | j 'o.id'); echo "30 create product:  $(echo "$PR" | j 'o.error||("ok slug="+o.slug)') | duplicate: $(post /admin/products '{"name":"Zz Test Oil","category":"hair","price_cents":1500,"stock":5,"seller_name":"Zz","pickup":true,"shipping":false}' | j 'o.error||o.ok') | in shop: $(curl -s $B/products/zz-test-oil | j 'o.product.seller_name+" "+o.product.price_cents+" sizes="+o.product.sizes.length')"
echo "31 edit product:    $(put /admin/products/$PID '{"name":"Zz Test Oil","category":"styling","price_cents":1800,"stock":9,"seller_name":"Zz Test Salon","business_slug":"zz-test-salon","pickup":true,"shipping":false}' | j 'o.error||o.ok') -> $(curl -s $B/products/zz-test-oil | j 'o.product.category+" "+o.product.price_cents+" stock="+o.product.stock+" compare="+o.product.compare_cents')"

echo "== exports"
for k in bookings orders payouts clients businesses products audit gift-cards; do
  curl -s -D "$TEMP/h.txt" -H "$A" "$B/admin/export/$k" -o "$TEMP/e.csv"; echo "32 export $k: $(head -1 "$TEMP/h.txt" | tr -d '\r' | cut -d' ' -f2) $(grep -i '^content-type' "$TEMP/h.txt" | tr -d '\r' | cut -d' ' -f2-) rows=$(($(wc -l < "$TEMP/e.csv")-1)) cols=$(head -1 "$TEMP/e.csv" | tr ',' '\n' | wc -l)"
done
echo "33 filter + guard:  confirmed only rows=$(($(curl -s -H "$A" "$B/admin/export/bookings?status=confirmed" | wc -l)-1)) | unknown $(code -H "$A" $B/admin/export/nope) | audit as ops $(code -H "$TA" $B/admin/export/audit) | support role $(code -H "Authorization: Bearer $(curl -s -X POST $B/admin/login -H "$H" -d '{"email":"joy@logaluxe.test","password":"joy-dev-password"}' | j 'o.token')" $B/admin/export/bookings)"

echo "== promo codes and gift cards"
echo "34 bad promo:       $(post /admin/promos '{"code":"ZZ","kind":"percent","value":10}' | j 'o.error||o.ok') | 150 percent: $(post /admin/promos '{"code":"ZZBIG","kind":"percent","value":150}' | j 'o.error||o.ok')"
P1=$(post /admin/promos '{"code":"zztest10","kind":"percent","value":10,"applies_to":"orders","max_uses":1,"description":"test"}' | j 'o.id')
P2=$(post /admin/promos '{"code":"ZZBOOK5","kind":"fixed","value":500,"currency":"USD","applies_to":"bookings"}' | j 'o.id')
echo "35 create promos:   ${#P1} ${#P2} | duplicate: $(post /admin/promos '{"code":"ZZTEST10","kind":"percent","value":10}' | j 'o.error||o.ok')"
echo "36 cart check:      $(curl -s -X POST -H "$H" $B/checkout/check -d '{"scope":"orders","subtotal_cents":3600,"promo_code":"zztest10","gift_code":"LX-NOPE-NOPE-NOPE"}' | j '"discount="+o.discount_cents+" | gift: "+o.gift_error') | wrong place: $(curl -s -X POST -H "$H" $B/checkout/check -d '{"scope":"orders","subtotal_cents":3600,"promo_code":"ZZBOOK5"}' | j 'o.promo_error')"
GC=$(post /admin/gift-cards '{"amount_cents":2000,"currency":"USD","recipient_name":"Gift Tester","recipient_email":"gift@logaluxe.test","note":"Enjoy","send_email":true}')
GID=$(echo "$GC" | j 'o.id'); GCODE=$(echo "$GC" | j 'o.code')
echo "37 issue gift card: $(echo "$GC" | j 'o.error||("ok email="+o.email+" code shape "+o.code.replace(/[A-Z0-9]/g,"X"))') | no amount: $(post /admin/gift-cards '{"amount_cents":0}' | j 'o.error||o.ok') | bad email: $(post /admin/gift-cards '{"amount_cents":500,"recipient_email":"nope"}' | j 'o.error||o.ok')"
echo "38 old credit trick: $(curl -s -X POST -H "$H" $B/orders -d '{"customer_name":"Cheat","fulfilment":"pickup","credit_cents":999999,"items":[{"product_slug":"zz-test-oil","qty":1}]}' | j 'o.error||("PLACED total "+o.order.total_cents)')"
OR=$(curl -s -X POST -H "$H" $B/orders -d "{\"customer_name\":\"Promo Tester\",\"customer_phone\":\"+16150007777\",\"fulfilment\":\"pickup\",\"promo_code\":\"zztest10\",\"gift_code\":\"$GCODE\",\"items\":[{\"product_slug\":\"zz-test-oil\",\"qty\":2}]}")
OID=$(echo "$OR" | j 'o.order.id')
echo "39 order:           $(echo "$OR" | j 'o.error||("subtotal "+o.order.subtotal_cents+" discount "+o.order.discount_cents+" tax "+o.order.tax_cents+" gift "+o.order.gift_cents+" total "+o.order.total_cents+" | stored gift code: "+o.order.gift_code)')"
echo "40 after the order: card balance $(curl -s -H "$A" "$B/admin/gift-cards?q=gift@logaluxe.test" | j 'o.cards[0].balance_cents') | promo used $(curl -s -H "$A" $B/admin/promos | j 'o.promos.find(p=>p.code=="ZZTEST10").used') | reuse promo: $(curl -s -X POST -H "$H" $B/orders -d '{"customer_name":"Again","fulfilment":"pickup","promo_code":"ZZTEST10","items":[{"product_slug":"zz-test-oil","qty":1}]}' | j 'o.error||"PLACED"')"
curl -s -X POST -H "$A" -H "$H" $B/admin/orders/$OID/status -d '{"status":"refunded","reason":"test"}' >/dev/null
echo "41 refund:          card balance back to $(curl -s -H "$A" "$B/admin/gift-cards?q=gift@logaluxe.test" | j 'o.cards[0].balance_cents+" after "+o.txns.filter(t=>t.code==o.cards[0].code).length+" ledger lines"')"
echo "42 void card:       no reason: $(post /admin/gift-cards/$GID/void '{}' | j 'o.error||o.ok') | with reason: $(post /admin/gift-cards/$GID/void '{"reason":"test over"}' | j 'o.error||o.ok') | use it now: $(curl -s -X POST -H "$H" $B/checkout/check -d "{\"subtotal_cents\":1000,\"gift_code\":\"$GCODE\"}" | j 'o.gift_error')"
SV=$(curl -s $B/businesses/ada | j 'o.services[0].id'); STF=$(curl -s $B/businesses/ada | j 'o.staff[0].id')
BK=$(curl -s -X POST -H "$H" $B/bookings -d "{\"business_slug\":\"ada\",\"staff_id\":\"$STF\",\"starts_at\":\"2026-10-27T10:00:00-05:00\",\"service_ids\":[\"$SV\"],\"client_name\":\"Promo Booker\",\"client_phone\":\"+16150008888\",\"promo_code\":\"zzbook5\"}")
echo "43 booking promo:   $(echo "$BK" | j 'o.error||("total "+o.booking.total_cents+" discount "+o.booking.discount_cents+" code "+o.booking.promo_code)')"
echo "44 promo off/delete: off $(put /admin/promos/$P2 '{"active":false}' | j 'o.error||o.ok') | delete a used one: $(curl -s -X DELETE -H "$A" $B/admin/promos/$P2 | j 'o.error||o.ok')"

echo "== support, bulk messages, pages"
echo "45 bad ticket:      $(curl -s -X POST -H "$H" $B/support -d '{"name":"A","subject":"Hi","message":"short"}' | j 'o.error||o.ref')"
REF=$(curl -s -X POST -H "$H" $B/support -d '{"name":"Zz Support Tester","email":"client@logaluxe.test","role":"client","business_slug":"ada","subject":"Zz test: charged twice","message":"I think my deposit was taken twice for the same booking."}' | j 'o.ref')
TKT=$(curl -s -H "$A" "$B/admin/support?q=$REF" | j 'o.tickets[0].id')
echo "46 ticket:          $REF | in inbox: $(curl -s -H "$A" "$B/admin/support?status=open" | j '"open="+o.counts.open+" business="+o.tickets.find(t=>t.subject.startsWith("Zz test")).business') | overview badge: $(curl -s -H "$A" $B/admin/overview | j 'o.kpis.open_tickets')"
echo "47 internal note:   $(post /admin/support/$TKT/reply '{"body":"Checking the ledger.","internal":true}' | j 'o.error||("ok email=["+o.email+"]")') | reply: $(post /admin/support/$TKT/reply '{"body":"Thanks for flagging this. One of the two holds will drop off within a day."}' | j 'o.error||("ok email="+o.email)')"
echo "48 thread:          $(curl -s -H "$A" $B/admin/support/$TKT | j 'o.ticket.status+", assigned to "+o.ticket.assigned_to+", "+o.messages.length+" messages: "+o.messages.map(m=>(m.internal?"note":m.from_staff?"staff":"client")).join(",")')"
echo "49 assign + close:  bad person: $(put /admin/support/$TKT '{"assigned_to":"ghost@logaluxe.test"}' | j 'o.error||o.ok') | urgent+close: $(put /admin/support/$TKT '{"priority":"urgent","status":"closed"}' | j 'o.error||o.ok')"
echo "50 bad broadcast:   $(post /admin/broadcasts '{"audience":"businesses","channel":"email","body":"Hello there everyone"}' | j 'o.error||o.ok')"
BC=$(post /admin/broadcasts '{"audience":"businesses","market":"US","channel":"email","subject":"Zz test notice","body":"Hello {{name}}, this is a test notice."}')
BCID=$(echo "$BC" | j 'o.id'); echo "51 draft:           $(echo "$BC" | j 'o.error||(o.recipients+" businesses, "+o.reachable+" with an email")') | send as ops: $(code -X POST -H "$TA" $B/admin/broadcasts/$BCID/send)"
echo "52 send:            $(curl -s -X POST -H "$A" $B/admin/broadcasts/$BCID/send | j 'o.error||("accepted, mode="+o.mode)') | send twice: $(curl -s -X POST -H "$A" $B/admin/broadcasts/$BCID/send | j 'o.error||o.ok')"
sleep 2
echo "53 result:          $(curl -s -H "$A" $B/admin/broadcasts | j '(b=>b.status+" recipients="+b.recipients+" delivered="+b.delivered+" logged="+b.logged+" skipped="+b.skipped+" failed="+b.failed)(o.broadcasts.find(b=>b.subject=="Zz test notice"))')"
BC2=$(post /admin/broadcasts '{"audience":"clients","channel":"sms","body":"Hi {{name}}, a short test text."}' | j 'o.id')
echo "54 delete a draft:  $(curl -s -X DELETE -H "$A" $B/admin/broadcasts/$BC2 | j 'o.error||o.ok') | delete a sent one: $(curl -s -X DELETE -H "$A" $B/admin/broadcasts/$BCID | j 'o.error||o.ok')"
ORIG=$(curl -s -H "$A" $B/admin/pages | node -e 'let d="";process.stdin.on("data",c=>d+=c).on("end",()=>{const p=JSON.parse(d).pages.find(p=>p.slug=="accessibility");console.log(JSON.stringify({title:p.title,body:p.body,published:true}))})')
echo "55 pages:           public terms: $(curl -s $B/pages/terms | j 'o.page.title') | list: $(curl -s -H "$A" $B/admin/pages | j 'o.pages.map(p=>p.slug).join(",")') | edit as ops: $(code -X PUT -H "$TA" -H "$H" $B/admin/pages/accessibility -d '{"title":"x","body":"y","published":true}')"
echo "56 unpublish:       $(put /admin/pages/accessibility '{"title":"Accessibility","body":"Hidden for a test","published":false}' | j 'o.error||o.ok') -> public $(code $B/pages/accessibility) | restore: $(put /admin/pages/accessibility "$ORIG" | j 'o.error||o.ok') -> public $(code $B/pages/accessibility)"
echo "57 health:          $(curl -s -H "$A" $B/admin/health | j 'o.checks.map(c=>c.name+"="+c.value).join("; ")')"
