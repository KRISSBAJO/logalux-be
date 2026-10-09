#!/usr/bin/env bash
# Opens every merchant screen, tab and view as three sample owners and checks
# each one answers 200 with no error on the page. Needs the API and the web
# app (port 3100) running. Run from logaluxe-be. It changes nothing.
set -u
API=${API:-http://localhost:18080/v1}
WEB=${WEB:-http://localhost:3100}
COOKIE=${MERCHANT_TEST_COOKIE:-lx_merchant}
PW=${MERCHANT_TEST_PASSWORD:-$(grep '^MERCHANT_DEMO_PASSWORD=' .env | cut -d= -f2-)}
MONTH=$(date +%Y-%m)
PAGES="
/business
/business/calendar /business/calendar?view=day /business/calendar?view=week /business/calendar?view=month /business/calendar?panel=waitlist /business/calendar?new=1 /business/calendar?q=a
/business/clients /business/clients?segment=regulars /business/clients?sort=spent&page=2
/business/checkout /business/checkout?sale=new /business/checkout?sales_page=2&sales_per_page=25
/business/money /business/money?tab=tx /business/money?tab=tx&transactions_page=2&transactions_per_page=25 /business/money?tab=payouts /business/money?tab=statements /business/money?tab=online /business/money/statement/$MONTH /business/money/payout-account
/business/inbox /business/inbox?filter=closed /business/inbox?new=1 /business/inbox?tab=problems
/business/marketing /business/marketing?tab=camp /business/marketing?tab=leads /business/marketing?tab=loyalty /business/marketing?tab=promos
/business/reports /business/reports?range=7d /business/reports?range=90d
/business/services /business/services?view=packages /business/services?view=memberships /business/services?view=rules /business/services?view=resources /business/services?view=archived
/business/storefront /business/storefront?tab=photos
/business/inventory /business/inventory?filter=low /business/inventory?filter=online /business/inventory?tab=orders /business/inventory?tab=orders&view=returns
/business/staff /business/staff?tab=timeoff /business/staff?tab=pay /business/staff?tab=rent
/business/settings /business/settings?tab=booking /business/settings?tab=policies /business/settings?tab=notifications /business/settings?tab=team /business/settings?tab=integrations /business/settings?tab=plan /business/settings?tab=account
"
fail=0; n=0
for who in ada mnm tiwa; do
  TOKEN=$(curl -s -X POST "$API/m/login" -H 'Content-Type: application/json' -d "{\"email\":\"$who@logaluxe.test\",\"password\":\"$PW\"}" | sed -E 's/.*"token":"([^"]+)".*/\1/')
  [ ${#TOKEN} -lt 20 ] && { echo "FAIL sign in as $who"; fail=1; continue; }
  for p in $PAGES; do
    n=$((n+1))
    code=$(curl -s --max-time 60 -o /tmp/lx-page.html -w '%{http_code}' -H "Cookie: $COOKIE=$TOKEN" "$WEB$p")
    bad=$(grep -c -E 'Application error|NEXT_REDIRECT|digest:|>NaN<|\$NaN|>undefined<|\[object Object\]' /tmp/lx-page.html)
    err=$(grep -o 'class="flash flash-err"[^<]*<' /tmp/lx-page.html | head -1)
    if [ "$code" != 200 ] || [ "$bad" != 0 ] || [ -n "$err" ]; then echo "FAIL $who $p -> $code, $bad bad marker(s) $err"; fail=1; fi
  done
done
echo "$n pages opened"
[ $fail = 0 ] && echo "all pages are fine"
exit $fail
