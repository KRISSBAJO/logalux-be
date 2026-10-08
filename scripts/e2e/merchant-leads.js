// End-to-end check of the lead system: a first-time client who finds a
// business on LogaLuxe opens a lead, it is charged when the visit is paid
// for, capped, voided on cancellation, and can be promoted, disputed and
// refunded. Uses a throwaway business and deletes it. Run from logaluxe-be:
//   MAIL_PROVIDER=log docker compose up -d api
//   node scripts/e2e/merchant-leads.js
const { execSync } = require("child_process");
const fs = require("fs");
const API = process.env.API || "http://localhost:18080/v1";
const stamp = Date.now().toString(36);
const EMAIL = `e2e-l-${stamp}@example.test`, PASS = "e2e-" + stamp + "-password", NAME = "E2E Leads Studio " + stamp;
const ADMIN = (fs.readFileSync(".env", "utf8").match(/^ADMIN_TOKEN=(.*)$/m) || [])[1]?.trim();
let pass = 0, fail = 0;
const check = (name, ok, extra = "") => { ok ? pass++ : fail++; console.log((ok ? "ok   " : "FAIL ") + name + (ok ? "" : "  " + String(extra).slice(0, 400))); };
const call = async (method, path, body, token) => {
  const r = await fetch(API + path, { method, headers: { "Content-Type": "application/json", ...(token ? { Authorization: "Bearer " + token } : {}) }, body: body === undefined ? undefined : JSON.stringify(body) });
  const text = await r.text(); let json = {}; try { json = JSON.parse(text); } catch {}
  return { status: r.status, json, text };
};
const sql = (q) => execSync("docker exec -i logaluxe-db psql -U logaluxe -d logaluxe -At", { input: q }).toString().trim();

(async () => {
  let r = await call("POST", "/m/signup", { name: "Leads Owner", email: EMAIL, phone: "+16155550311", password: PASS, business: NAME, category: "braids", market: "US", city: "Nashville", region: "TN", address: "1 Test St" });
  check("sign up a business", r.status === 201, r.text);
  const T = r.json.token;
  const g = (p) => call("GET", "/m" + p, undefined, T);
  const p = (path, body, method = "POST") => call(method, "/m" + path, body, T);
  const me = (await g("/me")).json.merchant;
  sql(`update businesses set status='live', verification_status='verified' where id='${me.business_id}'`);
  const owner = (await g("/staff")).json.staff[0];
  const svc = (await p("/services", { name: "Lead braids", category: "Braids", duration_min: 60, price_cents: 10000, staff_ids: [] })).json.id;
  const big = (await p("/services", { name: "Big install", category: "Braids", duration_min: 60, price_cents: 50000, staff_ids: [] })).json.id;

  // free times a week out
  let day, slots = [];
  for (let i = 7; i < 15 && slots.length < 12; i++) { day = new Date(Date.now() + i * 864e5).toISOString().slice(0, 10); slots = (await call("GET", `/businesses/${me.slug}/availability?date=${day}&services=${svc}&staff=any`)).json.slots || []; }
  check("the public page offers free times", slots.length >= 12, JSON.stringify(slots).slice(0, 200));
  const book = (i, phone, source, service = svc, name = "Lead Client " + i) => call("POST", "/bookings", { business_slug: me.slug, staff_id: owner.id, starts_at: slots[i * 2].starts_at, service_ids: [service], client_name: name, client_phone: phone, source });

  r = await g("/leads");
  check("the rate for a free-plan US business is 20%, capped at $60", r.json.rate?.base_pct === 20 && r.json.rate.cap_cents === 6000 && r.json.leads?.length === 0, r.text.slice(0, 300));

  // 1. found on LogaLuxe, first time: a lead
  r = await book(0, "+16155550321", "search"); check("a first-time client books from search", r.status === 201, r.text); const bk1 = r.json.booking.id;
  r = await g("/leads"); let lead = r.json.leads?.[0];
  check("that opens a pending lead at 20%", r.json.leads.length === 1 && lead.status === "pending" && lead.base_pct === 20 && lead.fee_cents === 0, JSON.stringify(lead));
  // 2. the business's own link: never a lead
  r = await book(1, "+16155550322", "link"); const bk2 = r.json.booking?.id;
  // 3. the same client again from search: still one lead
  r = await book(2, "+16155550321", "search");
  r = await g("/leads"); check("the business's own link and a returning client open no lead", r.json.leads.length === 1, JSON.stringify(r.json.leads.map((l) => l.client_name)));

  // paying for the visit charges the lead
  r = await p("/checkout", { booking_id: bk1, items: [], tip_cents: 1000, method: "card" });
  check("checking out the first visit charges 20% of the services, not the tip", r.status === 201 && r.json.lead_fee_cents === 2000, r.text);
  r = await p("/checkout", { booking_id: bk2, items: [], method: "card" });
  check("a client from the business's own link costs nothing", r.json.lead_fee_cents === 0, r.text);
  r = await g("/leads"); lead = r.json.leads[0];
  check("the lead is charged and can be disputed", lead.status === "charged" && lead.fee_cents === 2000 && lead.can_dispute === true && r.json.kpis.month_fee_cents === 2000, JSON.stringify(lead));
  r = await g("/money"); const line = r.json.transactions?.find((t) => t.kind === "lead_fee");
  check("the fee is a line in the ledger", line && line.amount_cents === -2000, JSON.stringify(r.json.transactions?.map((t) => [t.kind, t.amount_cents])));

  // the cap
  r = await book(3, "+16155550323", "search", big); const bk3 = r.json.booking?.id;
  r = await p("/checkout", { booking_id: bk3, items: [], method: "cash" });
  check("a $500 first visit is capped at $60, even when paid in cash", r.json.lead_fee_cents === 6000, r.text);

  // a cancelled booking is not a lead
  r = await book(4, "+16155550324", "search"); const bk4 = r.json.booking?.id;
  r = await p(`/bookings/${bk4}/action`, { action: "cancel", reason: "test" });
  r = await g("/leads?status=void"); check("cancelling voids the lead: nothing is charged", r.json.leads.length === 1 && /cancelled/.test(r.json.leads[0].void_reason), r.text.slice(0, 300));

  // promotion: bid, be listed as promoted, pay more, stop at the budget
  r = await call("GET", `/businesses?q=${encodeURIComponent(NAME)}`); check("not promoted before bidding", r.json.businesses?.[0]?.promoted === false, r.text.slice(0, 200));
  r = await p("/leads/settings", { boost_pct: 5.25, monthly_budget_cents: 0 }, "PUT"); check("a bid that is not a half step is refused", r.status === 400, r.text);
  r = await p("/leads/settings", { boost_pct: 5, monthly_budget_cents: 9000, paused: false }, "PUT"); check("bid 5 extra points with a $90 monthly budget", r.status === 200 && r.json.running === true, r.text);
  r = await call("GET", `/businesses?q=${encodeURIComponent(NAME)}`); check("search marks it as promoted", r.json.businesses?.[0]?.promoted === true, r.text.slice(0, 200));
  r = await call("GET", `/businesses?q=${encodeURIComponent(NAME)}&sort=price`); check("but not when the visitor sorts by price", r.json.businesses?.[0]?.promoted === false, "");
  r = await book(5, "+16155550325", "search"); const bk5 = r.json.booking?.id;
  r = await p("/checkout", { booking_id: bk5, items: [], method: "card" });
  check("a promoted lead pays 25%", r.json.lead_fee_cents === 2500, r.text);
  r = await g("/leads"); check("the budget is spent ($80 + $25 of $90), so promotion stops by itself", r.json.boost?.running === false && r.json.boost.spent_cents === 10500, JSON.stringify(r.json.boost));
  r = await call("GET", `/businesses?q=${encodeURIComponent(NAME)}`); check("and search stops marking it", r.json.businesses?.[0]?.promoted === false, "");

  // the funnel
  await call("GET", `/businesses/${me.slug}?src=search`); await new Promise((d) => setTimeout(d, 600));
  r = await g("/leads"); check("search impressions and page views are counted", r.json.kpis.impressions_30d >= 3 && r.json.kpis.views_30d >= 1 && r.json.kpis.promoted_impressions_30d >= 1, JSON.stringify(r.json.kpis));
  check("the business sees where it ranks among its peers", r.json.rank?.peers >= 1 && r.json.rank.position >= 1, JSON.stringify(r.json.rank));

  // dispute and the console's decision
  const charged = r.json.leads.find((l) => l.fee_cents === 2000);
  r = await p(`/leads/${charged.id}/dispute`, { reason: "x" }); check("a dispute needs a reason", r.status === 400, r.text);
  r = await p(`/leads/${charged.id}/dispute`, { reason: "She has been my client for two years, she just used the app this time." }); check("dispute a charge", r.status === 200, r.text);
  if (ADMIN) {
    r = await call("GET", "/admin/leads?status=disputed", undefined, ADMIN); check("the console lists the dispute", r.json.leads?.some((l) => l.id === charged.id), r.text.slice(0, 200));
    r = await call("POST", `/admin/leads/${charged.id}/resolve`, { outcome: "refund", note: "Existing client confirmed by booking history." }, ADMIN); check("the console refunds it", r.json.status === "refunded", r.text);
    r = await g("/money"); check("the fee comes back as a ledger line", r.json.transactions?.some((t) => t.kind === "adjustment" && t.amount_cents === 2000), "");
    r = await call("PUT", "/admin/leads/rates", { market: "US", category: "braids", delta_pct: 5 }, ADMIN); check("the console raises the share for braids by 5 points", r.status === 200, r.text);
    r = await g("/leads"); check("the business's rate follows: 25%", r.json.rate.base_pct === 25, JSON.stringify(r.json.rate));
    r = await call("PUT", "/admin/leads/rates", { market: "US", category: "braids", delta_pct: 0 }, ADMIN); check("and puts it back", r.status === 200, r.text);
  } else console.log("skip the console checks: ADMIN_TOKEN is not set in .env");

  try {
    const id = me.business_id;
    sql(`delete from ledger where business_id='${id}'; delete from leads where business_id='${id}'; delete from sales where business_id='${id}'; delete from bookings where business_id='${id}';
      delete from businesses where id='${id}'; delete from merchant_users where email like 'e2e-l-%@example.test'; delete from audit_log where action like 'lead.%' and target like '%' and created_at > now() - interval '5 minutes';`);
    console.log("cleaned up; test businesses left behind:", sql(`select count(*) from businesses where name like 'E2E Leads Studio%'`));
  } catch (e) { console.log("CLEANUP PROBLEM:", String(e.stderr || e).slice(0, 800)); }
  console.log(`\n${pass} passed, ${fail} failed`);
  process.exit(fail ? 1 : 0);
})();
