// Checks real payments against the providers' TEST modes. It needs
// STRIPE_SECRET_KEY and PAYSTACK_SECRET_KEY to be test keys (sk_test_…) and
// refuses to run with live keys. It uses throwaway businesses and deletes
// them. Paying a link by hand is done in the browser; this script covers
// everything up to the payment page, and payouts.
//   node scripts/e2e/payments-live.js
const { execSync } = require("child_process");
const fs = require("fs");
const env = fs.readFileSync(".env", "utf8");
const keyOf = (k) => ((env.match(new RegExp("^" + k + "=(.*)$", "m")) || [])[1] || "").replace(/["\r]/g, "").trim();
for (const k of ["STRIPE_SECRET_KEY", "PAYSTACK_SECRET_KEY"]) {
  if (!keyOf(k).startsWith("sk_test_")) { console.log(`${k} is not a test key (sk_test_…). This script only runs against test mode.`); process.exit(2); }
}
const API = process.env.API || "http://localhost:18080/v1";
const stamp = Date.now().toString(36);
let pass = 0, fail = 0;
const check = (name, ok, extra = "") => { ok ? pass++ : fail++; console.log((ok ? "ok   " : "FAIL ") + name + (ok ? "" : "  " + String(extra).slice(0, 400))); };
const call = async (method, path, body, token) => {
  const r = await fetch(API + path, { method, headers: { "Content-Type": "application/json", ...(token ? { Authorization: "Bearer " + token } : {}) }, body: body === undefined ? undefined : JSON.stringify(body) });
  const text = await r.text(); let json = {}; try { json = JSON.parse(text); } catch {}
  return { status: r.status, json, text };
};
const sql = (q) => execSync("docker exec -i logaluxe-db psql -U logaluxe -d logaluxe -At", { input: q }).toString().trim();
const sleep = (ms) => new Promise((d) => setTimeout(d, ms));

async function business(market, city) {
  const email = `e2e-p-${market.toLowerCase()}-${stamp}@example.test`;
  const r = await call("POST", "/m/signup", { name: "Pay Owner", email, phone: market === "NG" ? "+2348035550411" : "+16155550511", password: "e2e-" + stamp + "-password", business: `E2E Pay ${market} ${stamp}`, category: "braids", market, city, region: "", address: "1 Test St" });
  const T = r.json.token, me = (await call("GET", "/m/me", undefined, T)).json.merchant;
  sql(`update businesses set status='live', verification_status='verified' where id='${me.business_id}'`);
  return { T, me, g: (p) => call("GET", "/m" + p, undefined, T), p: (path, body, method = "POST") => call(method, "/m" + path, body, T) };
}

(async () => {
  let r = await call("GET", "/../health");
  // ----- United States: Stripe -----
  const us = await business("US", "Nashville");
  r = await us.g("/checkout"); check("US: payments are live", r.json.payments_mode === "live", r.text.slice(0, 120));
  r = await us.p("/checkout", { items: [{ kind: "custom", name: "Card machine sale", unit_cents: 5000 }], method: "card" });
  r = await us.g("/money"); check("US: a sale on the business's own card machine stays out of the payout balance", r.json.balances.pending_cents === 0 && r.json.balances.available_cents === 0 && r.json.transactions.some((t) => t.kind === "charge" && t.in_balance === false), JSON.stringify(r.json.balances));
  r = await us.p("/checkout/link", { items: [], method: "card" }); check("US: a pay link for an empty sale is refused", r.status === 400, r.text);
  r = await us.p("/checkout/link", { client_name: "Link Lou", items: [{ kind: "custom", name: "Silk press", unit_cents: 7500 }], tip_cents: 500, email: "lou@example.com" });
  check("US: a pay link opens a Stripe page for the full amount", r.status === 201 && r.json.amount_cents === 8000 && /^https:\/\/checkout\.stripe\.com\//.test(r.json.url), r.text.slice(0, 300));
  const ref = r.json.reference;
  r = await us.g(`/payments/${ref}`); check("US: the till sees it as waiting", r.json.payment?.status === "pending", r.text.slice(0, 200));
  r = await us.g("/checkout"); check("US: no sale is recorded until the client pays", !r.json.sales.some((s) => s.client_name === "Link Lou"), "");
  r = await call("GET", `/m/payments/${ref}`, undefined, undefined); check("the payment status needs a sign-in", r.status === 401, r.text);
  r = await call("POST", "/payments/lx000000000000000000000000/confirm"); check("an unknown reference is not found", r.status === 404, r.text);
  r = await call("POST", "/webhooks/stripe", { type: "checkout.session.completed", data: { object: { client_reference_id: ref } } }); check("a webhook without Stripe's signature is refused", r.status === 400, r.text);
  r = await call("POST", "/webhooks/paystack", { event: "charge.success", data: { reference: ref } }); check("a webhook without Paystack's signature is refused", r.status === 400, r.text);
  r = await us.g(`/payments/${ref}`); check("so a forged webhook changes nothing", r.json.payment?.status === "pending", "");
  sql(`insert into ledger (business_id, kind, amount_cents, currency, method, status, description) values ('${us.me.business_id}', 'charge', 20000, 'USD', 'link', 'settled', 'Test money to pay out')`);
  r = await us.p("/payouts", { instant: false }); check("US: no payout before a Stripe account is connected", r.status >= 400, r.text);
  r = await us.p("/payout-account/stripe", {}); check("US: connecting starts Stripe's own onboarding pages", r.status === 200 && /^https:\/\/connect\.stripe\.com\//.test(r.json.url || ""), r.text.slice(0, 200));

  // ----- Nigeria: Paystack -----
  const ng = await business("NG", "Lagos");
  r = await ng.g("/payout-account"); check("NG: the live bank list comes from Paystack", r.json.paystack_live === true && (r.json.banks || []).length > 30, `${(r.json.banks || []).length} banks`);
  r = await ng.p("/payout-account/bank", { bank_code: "057", bank_name: "Zenith Bank", account_number: "0000000000" });
  check("NG: Paystack looks up who owns the account", r.status === 200 && !!r.json.account_name, r.text.slice(0, 300));
  const owner = r.json.account_name;
  r = await ng.p("/payout-account/bank", { bank_code: "057", bank_name: "Zenith Bank", account_number: "0000000000", account_name: owner, confirm: true });
  check("NG: confirming saves a real payout account", r.status === 201 || r.status === 200, r.text.slice(0, 300));
  sql(`insert into ledger (business_id, kind, amount_cents, currency, method, status, description) values ('${ng.me.business_id}', 'charge', 5000000, 'NGN', 'link', 'settled', 'Test money to pay out')`);
  r = await ng.p("/payouts", { instant: false }); check("NG: pay out ₦50,000", r.status === 201, r.text);
  await sleep(6000);
  r = await ng.g("/money"); const po = r.json.payouts?.[0];
  console.log("     payout →", JSON.stringify({ status: po?.status, reference: po?.reference, failure_reason: po?.failure_reason }), "balance →", JSON.stringify(r.json.balances));
  check("NG: Paystack answered: the payout is paid or on its way, or it failed and the money came back", (["paid", "sending"].includes(po?.status) && r.json.balances.available_cents === 0) || (po?.status === "failed" && r.json.balances.available_cents === 5000000 && !!po.failure_reason), JSON.stringify(po));
  r = await ng.p("/checkout/link", { client_name: "Link Lola", items: [{ kind: "custom", name: "Gel manicure", unit_cents: 1500000 }] });
  check("NG: a pay link opens a Paystack page", r.status === 201 && /^https:\/\/checkout\.paystack\.com\//.test(r.json.url), r.text.slice(0, 300));

  try {
    for (const b of [us, ng]) {
      const id = b.me.business_id;
      sql(`delete from payments where business_id='${id}'; delete from ledger where business_id='${id}'; delete from payouts where business_id='${id}'; delete from sales where business_id='${id}'; delete from businesses where id='${id}';`);
    }
    sql(`delete from merchant_users where email like 'e2e-p-%@example.test'`);
    console.log("cleaned up; test businesses left behind:", sql(`select count(*) from businesses where name like 'E2E Pay %'`));
  } catch (e) { console.log("CLEANUP PROBLEM:", String(e.stderr || e).slice(0, 800)); }
  console.log(`\n${pass} passed, ${fail} failed`);
  process.exit(fail ? 1 : 0);
})();
