// End-to-end check of stock per location, loyalty points, a business's own
// promo codes and monthly statements. Uses a throwaway business and deletes
// it. Run from logaluxe-be:
//   MAIL_PROVIDER=log docker compose up -d api
//   node scripts/e2e/merchant-more.js
const { execSync } = require("child_process");
const API = process.env.API || "http://localhost:18080/v1";
const stamp = Date.now().toString(36);
const EMAIL = `e2e-m-${stamp}@example.test`, PASS = "e2e-" + stamp + "-password", CODE = ("E2E" + stamp).toUpperCase().slice(0, 12);
let pass = 0, fail = 0;
const check = (name, ok, extra = "") => { ok ? pass++ : fail++; console.log((ok ? "ok   " : "FAIL ") + name + (ok ? "" : "  " + String(extra).slice(0, 400))); };
const call = async (method, path, body, token) => {
  const r = await fetch(API + path, { method, headers: { "Content-Type": "application/json", ...(token ? { Authorization: "Bearer " + token } : {}) }, body: body === undefined ? undefined : JSON.stringify(body) });
  const text = await r.text(); let json = {}; try { json = JSON.parse(text); } catch {}
  return { status: r.status, json, text };
};
// SQL runs against the database the API uses: DATABASE_URL in .env when set, else the local container.
const DBURL = (() => { try { return (require("fs").readFileSync(".env", "utf8").match(/^DATABASE_URL=(.*)$/m) || [])[1]?.trim().replace(/^["']|["']$/g, "") || ""; } catch { return ""; } })();
const sql = (q) => execSync(DBURL ? `docker exec -i logaluxe-db psql "${DBURL}${DBURL.includes("?") ? "&" : "?"}sslrootcert=system" -At` : "docker exec -i logaluxe-db psql -U logaluxe -d logaluxe -At", { input: q }).toString().trim();

(async () => {
  let r = await call("POST", "/m/signup", { name: "More Owner", email: EMAIL, phone: "+16155550411", password: PASS, business: "E2E More Studio " + stamp, category: "braids", market: "US", city: "Nashville", region: "TN", address: "1 Test St" });
  check("sign up a business", r.status === 201, r.text);
  const T = r.json.token;
  const g = (p) => call("GET", "/m" + p, undefined, T);
  const p = (path, body, method = "POST") => call(method, "/m" + path, body, T);
  const me = (await g("/me")).json.merchant;
  sql(`update businesses set status='live', verification_status='verified' where id='${me.business_id}'`);
  const owner = (await g("/staff")).json.staff[0];
  const svc = (await p("/services", { name: "More braids", category: "Braids", duration_min: 60, price_cents: 10000, staff_ids: [] })).json.id;

  // ----- stock per location -----
  r = await p("/locations", { name: "Second shop", address: "2 Test St", city: "Nashville", region: "TN" }); check("add a second location", r.status === 201, r.text);
  let inv = (await g("/inventory")).json; const main = inv.locations.find((l) => l.is_primary).id, second = inv.locations.find((l) => !l.is_primary).id;
  r = await p("/products", { name: "E2E Oil", sku: "E2E-OIL2", kind: "retail", category: "hair", price_cents: 1000, cost_cents: 400, stock: 10, reorder_at: 2, online: false }); const oil = r.json.id;
  let prod = (await g("/inventory")).json.products.find((x) => x.id === oil);
  check("a new product's stock sits at the main location", prod.stock === 10 && prod.by_location?.length === 1 && prod.by_location[0].qty === 10, JSON.stringify(prod.by_location));
  r = await p(`/products/${oil}/transfer`, { from_location_id: main, to_location_id: second, qty: 40 }); check("cannot move more than is there", r.status === 409, r.text);
  r = await p(`/products/${oil}/transfer`, { from_location_id: main, to_location_id: second, qty: 4, note: "For the weekend" }); check("move 4 to the second shop", r.status === 200, r.text);
  prod = (await g(`/inventory?location=${second}`)).json.products.find((x) => x.id === oil);
  check("the total is unchanged; the second shop has 4", prod.stock === 10 && prod.here === 4 && prod.by_location.length === 2, JSON.stringify({ s: prod.stock, h: prod.here, b: prod.by_location }));
  r = await p(`/products/${oil}/stock`, { delta: 5, reason: "restock", location_id: second }); check("a delivery to the second shop", r.json.stock === 15 && r.json.here === 9, r.text);
  r = await p(`/products/${oil}/stock`, { delta: 3, reason: "count", location_id: main }); check("a count at the main shop finds 3, not 6", r.json.stock === 12 && r.json.here === 3, r.text);
  r = await p("/checkout", { location_id: main, staff_id: owner.id, items: [{ kind: "product", product_id: oil, qty: 4 }], method: "cash" }); check("the main shop cannot sell 4 when it holds 3", r.status === 409 && /only 3/.test(r.text), r.text);
  r = await p("/checkout", { location_id: second, staff_id: owner.id, items: [{ kind: "product", product_id: oil, qty: 4 }], method: "cash" }); check("the second shop can", r.status === 201, r.text);
  prod = (await g("/inventory")).json.products.find((x) => x.id === oil);
  check("the sale came off the second shop's shelf", prod.stock === 8 && prod.by_location.find((b) => b.location_id === second).qty === 5 && prod.by_location.find((b) => b.location_id === main).qty === 3, JSON.stringify(prod.by_location));
  r = await g(`/products/${oil}/history`); check("history names the location of each change", r.json.history?.some((h) => h.reason === "transfer" && h.location === "Second shop") && r.json.history.some((h) => h.reason === "sale" && h.location === "Second shop"), r.text.slice(0, 400));
  inv = (await g("/inventory")).json; check("each location shows its units and value", inv.locations.find((l) => l.id === second).units === 5 && inv.locations.find((l) => l.id === second).value_cents === 2000, JSON.stringify(inv.locations));

  // ----- loyalty -----
  const client = (await p("/clients", { name: "Loyal Lou", phone: "+16155550421" })).json.id;
  r = await p("/checkout", { client_id: client, staff_id: owner.id, items: [{ kind: "service", service_id: svc }], method: "cash" }); check("with loyalty off, no points are earned", r.json.points_earned === 0, r.text);
  r = await p("/loyalty/settings", { enabled: true, earn_points: 1, per_cents: 100, point_value_cents: 80, min_redeem: 100 }, "PUT"); check("a scheme that gives back most of every sale is refused", r.status === 400, r.text);
  r = await p("/loyalty/settings", { enabled: true, earn_points: 1, per_cents: 100, point_value_cents: 5, min_redeem: 100 }, "PUT"); check("switch loyalty on: a point per dollar, 100 points for $5", r.status === 200, r.text);
  r = await p("/checkout", { client_id: client, staff_id: owner.id, items: [{ kind: "service", service_id: svc }], tip_cents: 2000, method: "cash" }); check("a $100 service earns 100 points; the tip earns none", r.json.points_earned === 100, r.text);
  r = await p("/checkout", { client_id: client, staff_id: owner.id, items: [{ kind: "service", service_id: svc }], redeem_points: 50, method: "cash" }); check("fewer than the minimum cannot be spent", r.status === 400, r.text);
  r = await p("/checkout", { client_id: client, staff_id: owner.id, items: [{ kind: "service", service_id: svc }], redeem_points: 500, method: "cash" }); check("more than the balance cannot be spent", r.status === 409, r.text);
  r = await p("/checkout", { staff_id: owner.id, items: [{ kind: "service", service_id: svc }], redeem_points: 100, method: "cash" }); check("points need a client", r.status === 400, r.text);
  r = await p("/checkout", { client_id: client, staff_id: owner.id, items: [{ kind: "service", service_id: svc }], redeem_points: 100, method: "cash" });
  check("spend 100 points: $5 off, and 95 new points on the $95 paid", r.status === 201 && r.json.points_discount_cents === 500 && r.json.total_cents === 9500 && r.json.points_earned === 95, r.text);
  r = await g(`/clients/${client}/plans`); check("the client's balance is 95", r.json.points === 95 && r.json.loyalty?.enabled === true, r.text.slice(0, 200));
  r = await p("/loyalty/adjust", { client_id: client, points: -500, note: "mistake" }); check("an adjustment cannot go below zero", r.status === 409, r.text);
  r = await p("/loyalty/adjust", { client_id: client, points: 25, note: "Birthday gift" }); check("add 25 points by hand", r.json.points === 120, r.text);
  r = await g("/loyalty"); check("the loyalty screen has the numbers", r.json.kpis?.members === 1 && r.json.kpis.outstanding === 120 && r.json.kpis.redeemed_30d === 100 && r.json.clients?.[0]?.points === 120, JSON.stringify(r.json.kpis));

  // ----- the business's own promo codes -----
  r = await p("/promos", { code: "no", kind: "percent", value: 10 }); check("a code that is too short is refused", r.status === 400, r.text);
  r = await p("/promos", { code: CODE, description: "Launch week", kind: "percent", value: 20, max_uses: 2 }); check("make a 20% code", r.status === 201, r.text); const promo = r.json.id;
  r = await p("/promos", { code: CODE, kind: "percent", value: 5 }); check("the same code twice is refused", r.status === 409, r.text);
  let day, slots = [];
  for (let i = 7; i < 15 && slots.length < 6; i++) { day = new Date(Date.now() + i * 864e5).toISOString().slice(0, 10); slots = (await call("GET", `/businesses/${me.slug}/availability?date=${day}&services=${svc}&staff=any`)).json.slots || []; }
  r = await call("POST", "/bookings", { business_slug: me.slug, staff_id: owner.id, starts_at: slots[0].starts_at, service_ids: [svc], client_name: "Promo Pat", client_phone: "+16155550431", source: "link", promo_code: CODE.toLowerCase() });
  check("a client uses it on the booking page: $80", r.status === 201 && r.json.booking.total_cents === 8000 && r.json.booking.discount_cents === 2000, r.text.slice(0, 300));
  r = await call("POST", "/bookings", { business_slug: "ada", staff_id: owner.id, starts_at: slots[0].starts_at, service_ids: [svc], client_name: "Other Biz", client_phone: "+16155550432", promo_code: CODE });
  check("it does not work at another business", r.status === 400, r.text);
  r = await p("/checkout", { client_id: client, staff_id: owner.id, items: [{ kind: "service", service_id: svc }], promo_code: CODE, method: "cash" }); check("the desk uses it at checkout: $20 off", r.json.promo_discount_cents === 2000 && r.json.total_cents === 8000, r.text);
  r = await p("/checkout", { client_id: client, staff_id: owner.id, items: [{ kind: "service", service_id: svc }], promo_code: CODE, method: "cash" }); check("after two uses it is used up", r.status === 400 && /used up/.test(r.text), r.text);
  r = await g("/promos"); const row = r.json.promos?.[0]; check("the list counts uses and what was given away", row?.used === 2 && row.given_cents === 4000, JSON.stringify(row));
  r = await call("DELETE", `/m/promos/${promo}`, undefined, T); check("a used code cannot be deleted", r.status === 409, r.text);
  r = await p(`/promos/${promo}`, { active: false }, "PUT"); check("but it can be switched off", r.status === 200, r.text);

  // ----- statements -----
  await p("/checkout", { staff_id: owner.id, items: [{ kind: "service", service_id: svc }], tip_cents: 500, method: "card" });
  const month = new Intl.DateTimeFormat("en-CA", { timeZone: "America/Chicago", year: "numeric", month: "2-digit" }).format(new Date()).slice(0, 7);
  r = await g("/statements"); const st = r.json.statements?.[0];
  check("this month has a statement line", st?.month === month && st.charges_cents > 0 && st.fees_cents < 0 && st.tips_cents >= 2500, JSON.stringify(st));
  r = await g(`/statements/${month}`);
  check("the statement adds up: opening + what counted toward payouts = closing", r.json.opening_cents === 0 && r.json.closing_cents === r.json.sums.net_cents + r.json.sums.payouts_cents && r.json.lines?.length > 5, JSON.stringify({ o: r.json.opening_cents, c: r.json.closing_cents, s: r.json.sums }));
  r = await g(`/statements/${month}?format=csv`); check("and downloads as a spreadsheet", r.status === 200 && r.text.split("\n").length > 5 && /date,type,description/.test(r.text), r.text.slice(0, 120));
  r = await g("/statements/nonsense"); check("a bad month is refused", r.status === 400, r.text);

  try {
    const id = me.business_id;
    sql(`delete from ledger where business_id='${id}'; delete from loyalty_points where business_id='${id}'; delete from sales where business_id='${id}'; delete from bookings where business_id='${id}';
      delete from promo_codes where business_id='${id}'; delete from businesses where id='${id}'; delete from merchant_users where email like 'e2e-m-%@example.test';`);
    console.log("cleaned up; test businesses left behind:", sql(`select count(*) from businesses where name like 'E2E More Studio%'`));
  } catch (e) { console.log("CLEANUP PROBLEM:", String(e.stderr || e).slice(0, 800)); }
  console.log(`\n${pass} passed, ${fail} failed`);
  process.exit(fail ? 1 : 0);
})();
