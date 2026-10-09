// End-to-end check of the Pro plan's monthly fee and the AI drafts. Uses a
// throwaway business and deletes it. The AI checks call OpenAI for real when
// OPENAI_API_KEY is set (two short requests), and are skipped when it is not.
//   node scripts/e2e/merchant-billing-ai.js
const { execSync } = require("child_process");
const API = process.env.API || "http://localhost:18080/v1";
const stamp = Date.now().toString(36);
let pass = 0, fail = 0;
const check = (name, ok, extra = "") => { ok ? pass++ : fail++; console.log((ok ? "ok   " : "FAIL ") + name + (ok ? "" : "  " + String(extra).slice(0, 500))); };
const call = async (method, path, body, token) => {
  const r = await fetch(API + path, { method, headers: { "Content-Type": "application/json", ...(token ? { Authorization: "Bearer " + token } : {}) }, body: body === undefined ? undefined : JSON.stringify(body) });
  const text = await r.text(); let json = {}; try { json = JSON.parse(text); } catch {}
  return { status: r.status, json, text };
};
// SQL runs against the database the API uses: DATABASE_URL in .env when set, else the local container.
const DBURL = (() => { try { return (require("fs").readFileSync(".env", "utf8").match(/^DATABASE_URL=(.*)$/m) || [])[1]?.trim().replace(/^["']|["']$/g, "") || ""; } catch { return ""; } })();
const sql = (q) => execSync(DBURL ? `docker exec -i logaluxe-db psql "${DBURL}${DBURL.includes("?") ? "&" : "?"}sslrootcert=system" -At` : "docker exec -i logaluxe-db psql -U logaluxe -d logaluxe -At", { input: q }).toString().trim();
const sleep = (ms) => new Promise((d) => setTimeout(d, ms));

(async () => {
  let r = await call("POST", "/m/signup", { name: "Bill Owner", email: `e2e-b-${stamp}@example.test`, phone: "+16155550611", password: "e2e-" + stamp + "-password", business: "E2E Billing Studio " + stamp, category: "braids", market: "US", city: "Nashville", region: "TN", address: "1 Test St" });
  check("sign up a business", r.status === 201, r.text);
  const T = r.json.token, g = (p) => call("GET", "/m" + p, undefined, T), p = (path, body, method = "POST") => call(method, "/m" + path, body, T);
  const me = (await g("/me")).json.merchant, id = me.business_id;
  sql(`update businesses set status='live', verification_status='verified' where id='${id}'`);

  // ----- the Pro fee -----
  r = await g("/settings"); check("a new business is on Free and owes nothing", r.json.billing?.plan === "free" && r.json.billing.pro_price_cents === 4900 && !r.json.billing.plan_paid_through, JSON.stringify(r.json.billing));
  r = await p("/plan", { plan: "pro" }); await sleep(1500);
  r = await g("/settings"); check("switching to Pro with an empty balance takes nothing yet, and notes the fee is owing", r.json.billing.plan === "pro" && !r.json.billing.plan_paid_through && !!r.json.billing.plan_due_since && r.json.billing.paid_total_cents === 0, JSON.stringify(r.json.billing));
  sql(`insert into ledger (business_id, kind, amount_cents, currency, method, status, description) values ('${id}', 'charge', 10000, 'USD', 'link', 'settled', 'Test money')`);
  await p("/plan", { plan: "pro" }); await sleep(1500);
  r = await g("/settings"); const b = r.json.billing;
  check("once the balance can cover it, $49 is taken and a month is paid for", b.paid_total_cents === 4900 && !!b.plan_paid_through && !b.plan_due_since, JSON.stringify(b));
  r = await g("/money"); check("it is a line in the ledger and the balance is $51", r.json.balances.available_cents === 5100 && r.json.transactions.some((t) => t.kind === "plan_fee" && t.amount_cents === -4900), JSON.stringify(r.json.balances));
  await p("/plan", { plan: "pro" }); await sleep(1500);
  r = await g("/settings"); check("asking again does not charge a second time", r.json.billing.paid_total_cents === 4900, JSON.stringify(r.json.billing));
  const month = new Intl.DateTimeFormat("en-CA", { timeZone: "America/Chicago", year: "numeric", month: "2-digit" }).format(new Date()).slice(0, 7);
  r = await g(`/statements/${month}`); check("the statement shows the plan fee and still adds up", r.json.sums.plan_fees_cents === -4900 && r.json.closing_cents === r.json.opening_cents + r.json.sums.net_cents + r.json.sums.payouts_cents, JSON.stringify(r.json.sums));
  sql(`update businesses set plan_paid_through = current_date - 20, plan_due_since = current_date - 20 where id='${id}'; delete from ledger where business_id='${id}' and kind='charge';`);
  // a business that has owed for longer than the grace period goes back to Free on the next run
  const other = await call("POST", "/m/signup", { name: "Trigger Owner", email: `e2e-b2-${stamp}@example.test`, phone: "+16155550612", password: "e2e-" + stamp + "-password", business: "E2E Billing Trigger " + stamp, category: "braids", market: "US", city: "Nashville", region: "TN", address: "1 Test St" });
  await call("POST", "/m/plan", { plan: "pro" }, other.json.token); await sleep(2000);
  r = await g("/settings"); check("after the grace period without funds the business is moved back to Free", r.json.billing.plan === "free", JSON.stringify(r.json.billing));

  // ----- AI drafts -----
  r = await g("/ai");
  if (!r.json.enabled) console.log("skip the AI checks: OPENAI_API_KEY is not set");
  else {
    await p("/services", { name: "Knotless braids", category: "Braids", duration_min: 210, price_cents: 18000, deposit_cents: 4000, staff_ids: [] });
    const client = (await p("/clients", { name: "Ada Tester", phone: "+16155550621" })).json.id;
    const th = (await p("/inbox", { client_id: client, channel: "whatsapp", body: "Hi Ada, thanks for getting in touch." })).json.id;
    sql(`insert into thread_messages (thread_id, from_business, author, body, delivery) values ('${th}', false, 'Ada Tester', 'How much are knotless braids and do you take a deposit?', 'delivered')`);
    r = await p("/ai/reply", { thread_id: th });
    console.log("     draft →", JSON.stringify(r.json.draft));
    check("a reply is drafted from the menu: it gives the real price and deposit", r.status === 200 && /180/.test(r.json.draft || "") && /40/.test(r.json.draft || "") && (r.json.draft || "").split(/\s+/).length < 110, r.text);
    r = await g(`/inbox/${th}`); check("drafting sends nothing: the thread still has two messages", r.json.messages?.length === 2, "");
    r = await p("/ai/campaign", { audience: "lapsed", channel: "whatsapp", goal: "Invite clients who have not been in for a while to book this month" });
    console.log("     campaign →", JSON.stringify(r.json));
    check("a campaign message is drafted with the placeholders", r.status === 200 && /\{first name\}/.test(r.json.message || "") && /\{booking link\}/.test(r.json.message || "") && !/\d+ ?%/.test(r.json.message || ""), r.text);
    r = await p("/ai/campaign", { audience: "all", channel: "sms", goal: "x" }); check("a goal that says nothing is refused", r.status === 400, r.text);
    r = await g("/ai"); check("drafts are counted against the daily allowance", r.json.used_today === 2 && r.json.limit === 100, JSON.stringify(r.json));
  }

  try {
    sql(`delete from ledger where business_id in (select id from businesses where name like 'E2E Billing %'); delete from businesses where name like 'E2E Billing %'; delete from merchant_users where email like 'e2e-b%@example.test'; delete from audit_log where actor='billing' and created_at > now() - interval '5 minutes';`);
    console.log("cleaned up; test businesses left behind:", sql(`select count(*) from businesses where name like 'E2E Billing %'`));
  } catch (e) { console.log("CLEANUP PROBLEM:", String(e.stderr || e).slice(0, 800)); }
  console.log(`\n${pass} passed, ${fail} failed`);
  process.exit(fail ? 1 : 0);
})();
