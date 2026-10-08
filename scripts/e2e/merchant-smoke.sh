#!/usr/bin/env bash
# Signs in as a sample owner and reads every merchant screen's data. Run from logaluxe-be.
set -u
API=${API:-http://localhost:18080/v1}
PW=$(grep '^MERCHANT_DEMO_PASSWORD=' .env | cut -d= -f2-)
TOKEN=$(curl -s -X POST "$API/m/login" -H 'Content-Type: application/json' -d "{\"email\":\"ada@logaluxe.test\",\"password\":\"$PW\"}" | node -e 'let s="";process.stdin.on("data",d=>s+=d).on("end",()=>{try{console.log(JSON.parse(s).token||"")}catch{console.log("")}})')
[ -z "$TOKEN" ] && { echo "FAIL login"; exit 1; }
echo "ok   login"
fail=0
for p in me home "calendar?date=$(date +%F)" "calendar?date=$(date +%F)&view=week" waitlist clients "clients?segment=lapsed" checkout checkout/day inbox services staff payroll inventory marketing reports "reports?range=30" storefront settings money payout-account clients/export money/export reports/export; do
  code=$(curl -s -o /tmp/lx-m.out -w '%{http_code}' "$API/m/$p" -H "Authorization: Bearer $TOKEN")
  if [ "$code" = 200 ]; then echo "ok   GET /m/$p ($(wc -c </tmp/lx-m.out) bytes)"; else echo "FAIL GET /m/$p -> $code $(head -c 300 /tmp/lx-m.out)"; fail=1; fi
done
exit $fail
