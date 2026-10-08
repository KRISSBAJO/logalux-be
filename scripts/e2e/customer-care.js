// End-to-end check of what happens after the sale: tax by seller and state, a
// quote that changes nothing, a return and its refund, a problem reported about
// a visit, a tip after the visit, and a gift card bought online. It makes one
// throwaway customer and one throwaway business and removes them. Run with:
//   RATE_LIMITS=off STRIPE_SECRET_KEY= PAYSTACK_SECRET_KEY= MAIL_PROVIDER=log docker compose up -d api
//   node scripts/e2e/customer-care.js
const { execSync } = require("child_process");
const fs = require("fs");
const API = process.env.API || "http://localhost:18080/v1";
const stamp = Date.now().toString(36);
const EMAIL = `e2e-care-${stamp}@example.test`, OWNER = `e2e-care-owner-${stamp}@example.test`, PASS = "e2e-" + stamp + "-password";
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
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

(async () => {
  let r = await call("POST", "/m/signup", { name: "E2E Care Owner", email: OWNER, phone: "+16155550116", password: PASS, business: "E2E Care Shop " + stamp, category: "braids", market: "US", city: "Nashville", region: "TN", address: "1 Test St" });
  check("a business signs up", r.status === 201, r.text); const T = r.json.token;
  const m = (method, path, body) => call(method, "/m" + path, body, T);
  const me = (await m("GET", "/me")).json.merchant, biz = me.business_id;
  r = await m("POST", "/products", { name: "E2E care oil " + stamp, kind: "retail", category: "hair", price_cents: 2000, stock: 5, online: true });
  const pid = r.json.id || r.json.product?.id; sql(`update products set active = true, pickup = true, shipping = true, shipping_cents = 500 where id='${pid}'`);
  const pslug = sql(`select slug from products where id='${pid}'`);
  sql(`update businesses set sales_tax_bp = 700 where id='${biz}'`);
  r = await call("POST", "/auth/signup", { first_name: "Cara", last_name: "Care", email: EMAIL, phone: "+16155550851", password: PASS }); const C = r.json.token;
  check("a customer signs up", r.status === 201, r.text);
  sql(`update users set email_verified_at = now() where email='${EMAIL}'`);
  const c = (method, path, body) => call(method, path, body, C);

  // ----- tax and the quote -----
  const order = { customer_name: "Cara Care", customer_phone: "+16155550851", customer_email: EMAIL, fulfilment: "pickup", items: [{ product_slug: pslug, size_label: "", qty: 1 }] };
  const before = sql(`select stock from products where id='${pid}'`);
  r = await c("POST", "/orders/quote", order);
  check("a quote uses the business's own tax rate (7%)", r.status === 200 && r.json.quote?.subtotal_cents === 2000 && r.json.quote.tax_cents === 140 && r.json.quote.total_cents === 2140, r.text);
  check("and takes nothing off the shelf", sql(`select stock from products where id='${pid}'`) === before && sql(`select count(*) from orders where customer_email='${EMAIL}'`) === "0");
  const brand = { ...order, fulfilment: "ship", items: [{ product_slug: "silk-bonnet", size_label: "", qty: 1 }], address: { line1: "1 Test St", city: "Nashville", region: "TN", postal: "37201" } };
  r = await c("POST", "/orders/quote", brand); const tn = r.json.quote || {};
  check("a brand product shipped to Tennessee is taxed at Tennessee's rate", r.status === 200 && tn.tax_cents === Math.floor((tn.subtotal_cents * 925 + 5000) / 10000) && tn.tax_cents > 0, r.text);
  r = await c("POST", "/orders/quote", { ...brand, address: { ...brand.address, city: "Portland", region: "OR", postal: "97201" } });
  check("and not taxed when shipped to a state with no rate listed", r.status === 200 && r.json.quote?.tax_cents === 0, r.text);

  // ----- a return -----
  r = await c("POST", "/orders", order); const o = r.json.order || {};
  check("she orders and collects", r.status === 201 && o.tax_cents === 140, r.text);
  let sh = ((await m("GET", "/orders?status=new")).json.orders || []).find((x) => x.order_id === o.id);
  r = await c("POST", `/auth/orders/${o.id}/returns`, { seller: me.business, reason: "damaged", note: "" });
  check("she cannot return it before she has it", r.status === 409, r.text);
  await m("POST", `/orders/${sh.id}`, { action: "ready", tracking: "" }); await m("POST", `/orders/${sh.id}`, { action: "collected", tracking: "" });
  r = await c("POST", `/auth/orders/${o.id}/returns`, { seller: me.business, reason: "damaged", note: "" });
  check("nor from a seller that states no returns policy", r.status === 409 && /not stated/.test(r.text), r.text);
  await m("PUT", "/shop-policy", { languages: [], returns_days: 14, returns_note: "", ship_days_min: null, ship_days_max: null, pickup_ready_mins: null });
  r = await call("GET", `/orders/${o.id}`); check("with a 14 day policy the order says it can be returned, and until when", r.json.order?.shipments?.[0]?.can_return === true && !!r.json.order.shipments[0].return_until, r.text.slice(0, 300));
  r = await c("POST", `/auth/orders/${o.id}/returns`, { seller: me.business, reason: "nonsense", note: "" }); check("a reason must be chosen", r.status === 400, r.text);
  r = await c("POST", `/auth/orders/${o.id}/returns`, { seller: me.business, reason: "damaged", note: "The cap was cracked." }); check("she asks to return it", r.status === 201, r.text);
  r = await c("POST", `/auth/orders/${o.id}/returns`, { seller: me.business, reason: "damaged", note: "" }); check("only once", r.status === 409, r.text);
  r = await m("GET", "/returns?status=requested"); const rt = (r.json.returns || [])[0];
  check("the business sees the request with the items", r.status === 200 && rt?.reason === "damaged" && rt.items?.[0]?.qty === 1, r.text.slice(0, 300));
  r = await m("POST", `/returns/${rt.id}`, { action: "refuse", reply: "no" }); check("a refusal needs a reason", r.status === 400, r.text);
  r = await m("POST", `/returns/${rt.id}`, { action: "approve", reply: "Sorry about that.", refund_cents: 999999, restock: false }); check("the refund cannot be more than she paid this seller", r.status === 400, r.text);
  const bal0 = Number(sql(`select coalesce(sum(amount_cents),0) from ledger where business_id='${biz}'`));
  r = await m("POST", `/returns/${rt.id}`, { action: "approve", reply: "Sorry about that.", restock: true });
  check("the business approves: with no card payment behind it, the money goes back as store credit", r.status === 200 && r.json.refund_cents === 2000 && r.json.credit_cents === 2000 && r.json.to_card_cents === 0, r.text);
  r = await m("POST", `/returns/${rt.id}`, { action: "approve", reply: "", restock: false }); check("it cannot be approved twice", r.status === 409, r.text);
  r = await c("GET", "/auth/referral"); check("she has the credit", r.json.balance_cents === 2000, r.text);
  const bal1 = Number(sql(`select coalesce(sum(amount_cents),0) from ledger where business_id='${biz}'`)), fee = Number(sql(`select coalesce(-sum(amount_cents),0) from ledger where business_id='${biz}' and kind='fee' and amount_cents < 0`));
  check("the business gives the money back and gets its marketplace fee back", bal1 === bal0 - 2000 + fee, `${bal0} -> ${bal1}, fee ${fee}`);
  check("the item is back on the shelf", sql(`select stock from products where id='${pid}'`) === before);
  r = await call("GET", `/orders/${o.id}`); check("the order shows the return was approved", r.json.order?.shipments?.[0]?.return?.status === "approved" && r.json.order.shipments[0].can_return === false, r.text.slice(0, 400));

  // ----- a problem with a visit, and a tip -----
  const bid = sql(`insert into bookings (business_id, staff_id, location_id, user_id, client_name, client_phone, status, starts_at, ends_at, total_cents, paid_at)
    select '${biz}', (select id from staff where business_id='${biz}' limit 1), (select id from locations where business_id='${biz}' limit 1), (select id from users where email='${EMAIL}'), 'Cara Care', '+16155550851', 'paid', now() - interval '2 days', now() - interval '2 days' + interval '1 hour', 8000, now() - interval '2 days' returning id`);
  check("a finished visit is on record", /^[0-9a-f-]{36}/.test(bid), bid);
  const bk = bid.split("\n")[0];
  r = await c("GET", "/auth/me"); const mine = (r.json.bookings || []).find((x) => x.id === bk) || {};
  check("her account says she can tip and can report a problem", mine.can_tip === true && mine.can_report === true && mine.tip_cents === 0, JSON.stringify(mine).slice(0, 300));
  r = await c("POST", `/auth/bookings/${bk}/tip`, { amount_cents: 10 }); check("a tip has a floor", r.status === 400, r.text);
  r = await c("POST", `/auth/bookings/${bk}/tip`, { amount_cents: 9000 }); check("and cannot be more than the visit cost", r.status === 400, r.text);
  r = await c("POST", `/auth/bookings/${bk}/tip`, { amount_cents: 1500 }); check("she tips $15", r.status === 201 && !r.json.payment, r.text);
  check("the tip is in the business's ledger", sql(`select amount_cents from ledger where booking_id='${bk}' and kind='tip'`) === "1500");
  r = await c("POST", `/auth/bookings/${bk}/problem`, { reason: "quality", statement: "short" }); check("a report needs a few sentences", r.status === 400, r.text);
  r = await c("POST", `/auth/bookings/${bk}/problem`, { reason: "quality", statement: "Two braids came loose the next day and the parting was uneven at the back." }); check("she reports a problem", r.status === 201 && /^D-/.test(r.json.ref || ""), r.text);
  r = await c("POST", `/auth/bookings/${bk}/problem`, { reason: "quality", statement: "Trying to report the same visit a second time." }); check("only once per visit", r.status === 409, r.text);
  r = await m("GET", "/problems"); const pr = (r.json.problems || [])[0];
  check("the business sees it and has a deadline", pr?.status === "with_business" && !!pr.business_deadline, r.text.slice(0, 300));
  r = await m("POST", `/problems/${pr.id}`, { statement: "We offered a free fix within the week, as our policy says, and she has not booked it." }); check("the business gives its side", r.status === 200, r.text);
  r = await m("POST", `/problems/${pr.id}`, { statement: "Trying to answer the same report a second time, which should not work." }); check("once", r.status === 409, r.text);
  r = await c("GET", "/auth/me"); const after = (r.json.bookings || []).find((x) => x.id === bk) || {};
  check("her account shows the report is with LogaLuxe to decide", after.problem?.status === "needs_decision" && after.can_report === false && after.tip_cents === 1500, JSON.stringify(after.problem));

  // ----- a gift card bought online -----
  r = await call("POST", "/gift-cards/buy", { amount_cents: 500, buyer_name: "Cara", buyer_email: "cara@example.com" }); check("a gift card is at least $10", r.status === 400, r.text);
  r = await call("POST", "/gift-cards/buy", { amount_cents: 2500, recipient_name: "Bee", recipient_email: "bee-" + stamp + "@example.com", note: "Happy birthday", buyer_name: "Cara", buyer_email: "cara-" + stamp + "@example.com" });
  check("she buys a $25 card for a friend, and the code is not shown to her", r.status === 201 && !/LX-/.test(r.text) && r.json.sent_to === "bee-" + stamp + "@example.com", r.text);
  await sleep(1200);
  const log = execSync("docker logs --since 1m logaluxe-api 2>&1").toString();
  const code = (log.match(new RegExp("bee-" + stamp + "@example.com[^\\n]*Your code: (LX-[A-Z0-9-]+)")) || [])[1] || "";
  check("the friend is emailed the code, and the buyer a receipt without it", /^LX-/.test(code) && new RegExp("to=cara-" + stamp + "@example.com[^\\n]*on its way").test(log) && !new RegExp("to=cara-" + stamp + "@example.com[^\\n]*LX-").test(log), "code " + code);
  r = await call("POST", "/checkout/check", { gift_code: code, scope: "orders", currency: "USD", subtotal_cents: 5000 });
  check("the card works at checkout for $25", r.status === 200 && JSON.stringify(r.json).includes("2500"), r.text);

  // ----- staff set tax rates -----
  if (ADMIN_EMAIL && ADMIN_PW) {
    const a = (await call("POST", "/admin/login", { email: ADMIN_EMAIL, password: ADMIN_PW })).json.token;
    if (a) {
      r = await call("PUT", "/admin/tax-rates/OR", { name: "Oregon (test)", bp: 9999 }, a); check("a rate above 25% is refused", r.status === 400, r.text);
      r = await call("PUT", "/admin/tax-rates/ZZ", { name: "Test state", bp: 500 }, a); check("staff add a state's rate", r.status === 200, r.text);
      r = await c("POST", "/orders/quote", { ...brand, address: { ...brand.address, region: "ZZ" } }); check("and orders shipped there are taxed at it", r.json.quote?.tax_cents === Math.floor((r.json.quote.subtotal_cents * 500 + 5000) / 10000), r.text);
      r = await call("DELETE", "/admin/tax-rates/ZZ", undefined, a); r = await call("GET", "/admin/tax-rates", undefined, a);
      check("and can remove it again", !(r.json.rates || []).some((x) => x.region === "ZZ") && (r.json.rates || []).some((x) => x.region === "TN"), r.text);
    }
  }

  // clean up
  try {
    sql(`delete from gift_cards where code='${code}';
      delete from disputes where business_id='${biz}';
      delete from ledger where business_id='${biz}';
      delete from payment_events where order_id in (select id from orders where customer_email='${EMAIL}');
      delete from orders where customer_email='${EMAIL}';
      delete from bookings where business_id='${biz}';
      delete from users where email='${EMAIL}';
      delete from products where business_id='${biz}';
      delete from businesses where id='${biz}';
      delete from merchant_users where email='${OWNER}';
      delete from tax_rates where region='ZZ';`);
    console.log("cleaned up; left behind:", sql(`select (select count(*) from users where email like 'e2e-care-%') + (select count(*) from businesses where name like 'E2E Care Shop%') + (select count(*) from gift_cards where issued_by like 'bought by cara-%')`));
  } catch (e) { console.log("clean-up problem:", e.message.slice(0, 400)); }
  console.log(`\n${pass} passed, ${fail} failed`);
  process.exit(fail ? 1 : 0);
})();
