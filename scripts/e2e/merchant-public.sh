#!/usr/bin/env bash
# Checks the places where clients meet the merchant web: the public booking
# page, a client's messages to a business, and the business's reply.
# Run from logaluxe-be with the API and the web app (port 3100) running.
# It creates one throwaway client account and removes everything it made.
set -u
API=${API:-http://localhost:18080/v1}
WEB=${WEB:-http://localhost:3100}
STAMP=$(date +%s)
EMAIL="e2e-client-$STAMP@example.test"
CPW="e2e-client-$STAMP-pw"
MPW=$(grep '^MERCHANT_DEMO_PASSWORD=' .env | cut -d= -f2-)
pass=0; fail=0
ok() { if [ "$2" = "1" ]; then pass=$((pass+1)); echo "ok   $1"; else fail=$((fail+1)); echo "FAIL $1  ${3:-}"; fi; }
field() { sed -E "s/.*\"$1\":\"([^\"]+)\".*/\1/"; }
LX_DB=$(grep -E "^DATABASE_URL=" .env | cut -d= -f2- | tr -d '\047\042\015'); [ -n "$LX_DB" ] && LX_DB="$LX_DB&sslrootcert=system"
sql() { if [ -n "$LX_DB" ]; then docker exec -i logaluxe-db psql "$LX_DB" -At; else docker exec -i logaluxe-db psql -U logaluxe -d logaluxe -At; fi; }

code=$(curl -s -o /tmp/lx-b.html -w '%{http_code}' "$WEB/b/ada")
ok "the public booking page loads" "$([ "$code" = 200 ] && echo 1)" "$code"
ok "it shows the cancellation rule from the business settings" "$(grep -c 'Free until' /tmp/lx-b.html | sed 's/^[1-9].*/1/')"
ok "it hides the phone number by default" "$(grep -c 'tel:' /tmp/lx-b.html | sed 's/^0$/1/')"
code=$(curl -s -o /dev/null -w '%{http_code}' "$WEB/business/signin"); ok "business sign in page loads" "$([ "$code" = 200 ] && echo 1)" "$code"
code=$(curl -s -o /dev/null -w '%{http_code}' "$WEB/business/signup"); ok "business sign up page loads" "$([ "$code" = 200 ] && echo 1)" "$code"
code=$(curl -s -o /dev/null -w '%{http_code}' "$WEB/business"); ok "the merchant web sends a signed-out visitor to sign in" "$([ "$code" = 307 ] && echo 1)" "$code"

CT=$(curl -s -X POST "$API/auth/signup" -H 'Content-Type: application/json' -d "{\"first_name\":\"Evie\",\"last_name\":\"Tester\",\"email\":\"$EMAIL\",\"phone\":\"\",\"password\":\"$CPW\"}" | field token)
ok "a client signs up" "$([ ${#CT} -gt 20 ] && echo 1)"
out=$(curl -s -X POST "$API/auth/threads" -H "Authorization: Bearer $CT" -H 'Content-Type: application/json' -d '{"business_slug":"ada","body":"Do you have anything free on Saturday morning?"}')
TH=$(echo "$out" | field id)
ok "the client writes to a business" "$([ ${#TH} -gt 20 ] && echo 1)" "$out"

MT=$(curl -s -X POST "$API/m/login" -H 'Content-Type: application/json' -d "{\"email\":\"ada@logaluxe.test\",\"password\":\"$MPW\"}" | field token)
ok "the message reaches the business inbox" "$(curl -s "$API/m/inbox" -H "Authorization: Bearer $MT" | grep -c "$TH")"
out=$(curl -s -X POST "$API/m/inbox/$TH/reply" -H "Authorization: Bearer $MT" -H 'Content-Type: application/json' -d '{"body":"Yes, 9:30 with Nia. Shall I hold it?"}')
ok "the business replies, and an in-app reply is really delivered" "$(echo "$out" | grep -c '"delivery":"delivered"')" "$out"
ok "the client sees the reply" "$(curl -s "$API/auth/threads/$TH" -H "Authorization: Bearer $CT" | grep -c 'Shall I hold it')"

code=$(curl -s -o /tmp/lx-acc.html -w '%{http_code}' -H "Cookie: lx_user=$CT" "$WEB/account?thread=$TH")
ok "the account page shows the conversation" "$([ "$code" = 200 ] && grep -q 'Shall I hold it' /tmp/lx-acc.html && echo 1)" "$code"
code=$(curl -s -o /tmp/lx-acc.html -w '%{http_code}' -H "Cookie: lx_user=$CT" "$WEB/account?to=mnm")
ok "the account page offers a new message to another business" "$([ "$code" = 200 ] && grep -q 'Message to' /tmp/lx-acc.html && echo 1)" "$code"

# clean up
# A message makes the client known to the business, so that card goes first; then the account itself.
echo "delete from threads where id='$TH'; delete from clients where user_id in (select id from users where email='$EMAIL'); delete from users where email='$EMAIL';" | sql >/dev/null
left=$(echo "select count(*) from users where email like 'e2e-client-%@example.test'" | sql)
ok "the throwaway client is removed" "$([ "$left" = 0 ] && echo 1)" "$left left"
echo; echo "$pass passed, $fail failed"; [ "$fail" = 0 ]
