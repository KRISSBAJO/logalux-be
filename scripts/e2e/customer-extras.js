// End-to-end check of the customer extras: what a business says about itself
// (languages, reply time, delivery and returns), product details, picking up
// today or at the next visit, saved products, and referral credit. It makes two
// throwaway customers and one throwaway business and removes them. Run with:
//   RATE_LIMITS=off STRIPE_SECRET_KEY= PAYSTACK_SECRET_KEY= MAIL_PROVIDER=log docker compose up -d api
//   node scripts/e2e/customer-extras.js
const { execSync } = require("child_process");
const fs = require("fs");
const API = process.env.API || "http://localhost:18080/v1";
const stamp = Date.now().toString(36);
const A = `e2e-x-a-${stamp}@example.test`, B = `e2e-x-b-${stamp}@example.test`, OWNER = `e2e-x-owner-${stamp}@example.test`, PASS = "e2e-" + stamp + "-password";
const env = fs.readFileSync(".env", "utf8");
const ADMIN_EMAIL = (env.match(/^ADMIN_EMAIL=(.*)$/m) || [])[1]?.trim(), ADMIN_PW = (env.match(/^ADMIN_PASSWORD=(.*)$/m) || [])[1]?.trim();
let pass = 0, fail = 0;
const check = (name, ok, extra = "") => { ok ? pass++ : fail++; console.log((ok ? "ok   " : "FAIL ") + name + (ok ? "" : "  " + String(extra).slice(0, 500))); };
const call = async (method, path, body, token) => {
  const r = await fetch(API + path, { method, headers: { "Content-Type": "application/json", ...(token ? { Authorization: "Bearer " + token } : {}) }, body: body === undefined ? undefined : JSON.stringify(body) });
  const text = await r.text(); let json = {}; try { json = JSON.parse(text); } catch {}
  return { status: r.status, json, text };
};
const sql = (q) => execSync("docker exec -i logaluxe-db psql -U logaluxe -d logaluxe -At", { input: q }).toString().trim();

(async () => {
  // ----- a business states its languages and its shop policy -----
  let r = await call("POST", "/m/signup", { name: "E2E X Owner", email: OWNER, phone: "+16155550115", password: PASS, business: "E2E Extras Shop " + stamp, category: "braids", market: "US", city: "Nashville", region: "TN", address: "1 Test St" });
  check("a business signs up", r.status === 201, r.text); const T = r.json.token;
  const m = (method, path, body) => call(method, "/m" + path, body, T);
  const me = (await m("GET", "/me")).json.merchant, slug = me.slug;
  r = await m("GET", "/shop-policy"); check("its shop policy starts empty", r.status === 200 && r.json.returns_days === null && r.json.languages.length === 0 && r.json.language_choices.length > 5, r.text);
  r = await m("PUT", "/shop-policy", { languages: ["English", "Yoruba", "Klingon"], returns_days: 14, returns_note: "Unopened only.", ship_days_min: 4, ship_days_max: 2, pickup_ready_mins: 30 });
  check("delivery days must run shortest first", r.status === 400, r.text);
  r = await m("PUT", "/shop-policy", { languages: ["English", "Yoruba", "Klingon"], returns_days: 14, returns_note: "Unopened only.", ship_days_min: 2, ship_days_max: 4, pickup_ready_mins: 30 });
  check("the policy is saved", r.status === 200, r.text);
  r = await call("GET", "/businesses/" + slug);
  check("the business page shows it, and drops a language we do not list", r.json.extras?.returns_days === 14 && JSON.stringify(r.json.extras.languages) === '["English","Yoruba"]' && r.json.extras.ship_days_max === 4, JSON.stringify(r.json.extras));
  check("no reply time is claimed before there are replies to measure", r.json.extras && !("reply_minutes" in r.json.extras), JSON.stringify(r.json.extras));

  // ----- a product with ingredients, sold by that business -----
  r = await m("POST", "/products", { name: "E2E extras oil " + stamp, kind: "retail", category: "hair", price_cents: 1500, stock: 5, online: true });
  check("the business adds a product", r.status === 201 || r.status === 200, r.text);
  const pid = r.json.id || r.json.product?.id, pslug = r.json.slug || r.json.product?.slug || sql(`select slug from products where id='${pid}'`);
  r = await m("PUT", `/products/${pid}/details`, { ingredients: "Jojoba oil, peppermint oil.", how_to_use: "Two drops on the scalp." });
  check("it writes the ingredients", r.status === 200, r.text);
  sql(`update products set active = true where id='${pid}'`);
  r = await call("GET", "/products/" + pslug);
  check("the product page shows ingredients, delivery time and returns", r.json.extras?.ingredients === "Jojoba oil, peppermint oil." && r.json.extras.delivery?.days_min === 2 && r.json.extras.returns?.days === 14, JSON.stringify(r.json.extras));
  // open all day today, so pick-up today is true whatever the clock says (unless it is the last half hour of the day)
  sql(`update locations set hours = '{"mon":["00:00","23:59"],"tue":["00:00","23:59"],"wed":["00:00","23:59"],"thu":["00:00","23:59"],"fri":["00:00","23:59"],"sat":["00:00","23:59"],"sun":["00:00","23:59"]}' where business_id='${me.business_id}'`);
  r = await call("GET", "/products/" + pslug);
  const late = new Date().toLocaleString("en-US", { timeZone: "America/Chicago", hour: "2-digit", minute: "2-digit", hour12: false }) >= "23:29";
  check("it can be picked up today while the business is open", late || (r.json.extras?.pickup_today?.until === "23:59" && /^\d\d:\d\d$/.test(r.json.extras.pickup_today.ready_at)), JSON.stringify(r.json.extras));
  sql(`update locations set hours = '{"mon":null,"tue":null,"wed":null,"thu":null,"fri":null,"sat":null,"sun":null}' where business_id='${me.business_id}'`);
  r = await call("GET", "/products/" + pslug); check("and not on a day it is closed", !r.json.extras?.pickup_today, JSON.stringify(r.json.extras));

  // ----- saved products -----
  r = await call("POST", "/auth/signup", { first_name: "Ada", last_name: "Friend", email: A, phone: "+16155550821", password: PASS }); const CA = r.json.token;
  check("a customer signs up", r.status === 201, r.text);
  sql(`update users set email_verified_at = now() where email='${A}'`);
  r = await call("PUT", "/auth/favourite-products/" + pslug, {}, CA); check("she saves the product", r.status === 200 && r.json.saved === true, r.text);
  r = await call("GET", "/auth/favourite-products", undefined, CA); check("it is in her saved products", r.json.products?.length === 1 && r.json.products[0].slug === pslug, r.text);
  r = await call("GET", "/products/" + pslug, undefined, CA); check("the product page knows she saved it", r.json.extras?.saved === true, JSON.stringify(r.json.extras));
  r = await call("DELETE", "/auth/favourite-products/" + pslug, undefined, CA); r = await call("GET", "/auth/favourite-products", undefined, CA); check("and she can remove it", r.json.products?.length === 0, r.text);

  // ----- referral credit -----
  r = await call("GET", "/auth/referral", undefined, CA); const wasOn = r.json.on, before = r.json.credit_cents;
  let admin = "";
  if (ADMIN_EMAIL && ADMIN_PW) admin = (await call("POST", "/admin/login", { email: ADMIN_EMAIL, password: ADMIN_PW })).json.token || "";
  if (!admin) { console.log("     (no admin sign-in available, so the referral checks are skipped)"); }
  else {
    r = await call("PUT", "/admin/settings/referral", { credit_cents: 0 }, admin);
    r = await call("GET", "/auth/referral", undefined, CA); check("with the programme off, no code is handed out", r.json.on === false && !r.json.code, r.text);
    r = await call("PUT", "/admin/settings/referral", { credit_cents: 999999 }, admin); check("the credit cannot be set above $100", r.status === 400, r.text);
    r = await call("PUT", "/admin/settings/referral", { credit_cents: 2500 }, admin); check("staff switch it on at $25", r.status === 200, r.text);
    r = await call("GET", "/auth/referral", undefined, CA); const code = r.json.code;
    check("she now has a code and a link", r.json.on === true && /^[A-Z2-9]{7}$/.test(code || "") && String(r.json.link).endsWith("/signup?ref=" + code) && r.json.balance_cents === 0, r.text);
    r = await call("POST", "/auth/signup", { first_name: "Bola", last_name: "Friend", email: B, phone: "+16155550822", password: PASS, ref: code }); const CB = r.json.token;
    check("a friend joins with her code", r.status === 201, r.text);
    r = await call("GET", "/auth/referral", undefined, CA); check("she sees one friend joined, none paid yet", r.json.friends_joined === 1 && r.json.friends_paid === 0 && r.json.balance_cents === 0, r.text);
    // the friend confirms their email and has a first order delivered; the worker then pays both
    sql(`update users set email_verified_at = now() where email='${B}'`);
    sql(`update locations set hours = '{"mon":["00:00","23:59"],"tue":["00:00","23:59"],"wed":["00:00","23:59"],"thu":["00:00","23:59"],"fri":["00:00","23:59"],"sat":["00:00","23:59"],"sun":["00:00","23:59"]}' where business_id='${me.business_id}'`);
    r = await call("POST", "/orders", { customer_name: "Bola Friend", customer_phone: "+16155550822", customer_email: B, fulfilment: "pickup", items: [{ product_slug: pslug, size_label: "", qty: 1 }] }, CB);
    check("the friend places a first order", r.status === 201, r.text); const o1 = r.json.order;
    r = await m("GET", "/orders?status=new"); const sh = (r.json.orders || []).find((x) => x.order_id === o1.id);
    await m("POST", `/orders/${sh.id}`, { action: "ready", tracking: "" }); r = await m("POST", `/orders/${sh.id}`, { action: "collected", tracking: "" });
    check("the business hands it over", r.status === 200, r.text);
    r = await call("GET", "/admin/settings/referral", undefined, admin); // opening the staff page brings credits up to date, as the worker does every ten minutes
    check("both are credited, and staff see the totals", r.json.stats?.paid >= 1 && r.json.stats.given_cents >= 5000, r.text);
    r = await call("GET", "/admin/settings/referral", undefined, admin);
    r = await call("GET", "/auth/referral", undefined, CA); check("she has $25 of credit", r.json.balance_cents === 2500 && r.json.history?.length === 1, r.text);
    r = await call("GET", "/auth/wallet", undefined, CA); check("it shows in her wallet", r.json.credit_cents === 2500, r.text);
    // she spends it: a $15 item collected, tax on top, all covered by credit, so nothing is charged
    r = await call("POST", "/orders", { customer_name: "Ada Friend", customer_phone: "+16155550821", customer_email: A, fulfilment: "pickup", items: [{ product_slug: pslug, size_label: "", qty: 1 }] }, CA);
    const o2 = r.json.order || {};
    check("her next order is paid by credit, with nothing left to charge", r.status === 201 && o2.credit_cents === 1639 && o2.total_cents === 0 && o2.status === "paid" && !o2.payment, r.text);
    r = await call("GET", "/auth/referral", undefined, CA); check("and the rest of her credit is kept", r.json.balance_cents === 2500 - 1639, r.text);
    r = await call("PUT", "/admin/settings/referral", { credit_cents: wasOn ? before : 0 }, admin); check("the programme is put back as it was", r.status === 200, r.text);
  }

  // clean up
  try {
    sql(`delete from ledger where order_id in (select id from orders where user_id in (select id from users where email in ('${A}','${B}')));
      delete from payment_events where order_id in (select id from orders where user_id in (select id from users where email in ('${A}','${B}')));
      delete from orders where user_id in (select id from users where email in ('${A}','${B}'));
      delete from users where email in ('${A}','${B}');
      delete from ledger where business_id='${me.business_id}';
      delete from products where business_id='${me.business_id}';
      delete from businesses where id='${me.business_id}';
      delete from merchant_users where email='${OWNER}';`);
    console.log("cleaned up; left behind:", sql(`select (select count(*) from users where email like 'e2e-x-%') + (select count(*) from businesses where name like 'E2E Extras Shop%')`));
  } catch (e) { console.log("clean-up problem:", e.message.slice(0, 400)); }
  console.log(`\n${pass} passed, ${fail} failed`);
  process.exit(fail ? 1 : 0);
})();
