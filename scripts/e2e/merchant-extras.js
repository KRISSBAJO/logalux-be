// End-to-end check of the deeper salon features: rooms and chairs, pricing
// rules, breaks, packages, memberships, back-bar use, pay types, permissions,
// and moving bookings when time off is approved. Uses a throwaway business
// and deletes it. Run from logaluxe-be with the API started as:
//   MAIL_PROVIDER=log docker compose up -d api
//   node scripts/e2e/merchant-extras.js
const { execSync } = require("child_process");
const API = process.env.API || "http://localhost:18080/v1";
const stamp = Date.now().toString(36);
const EMAIL = `e2e-x-${stamp}@example.test`, PASS = "e2e-" + stamp + "-password";
let pass = 0, fail = 0;
const check = (name, ok, extra = "") => { ok ? pass++ : fail++; console.log((ok ? "ok   " : "FAIL ") + name + (ok ? "" : "  " + String(extra).slice(0, 400))); };
const call = async (method, path, body, token) => {
  const r = await fetch(API + path, { method, headers: { "Content-Type": "application/json", ...(token ? { Authorization: "Bearer " + token } : {}) }, body: body === undefined ? undefined : JSON.stringify(body) });
  const text = await r.text(); let json = {}; try { json = JSON.parse(text); } catch {}
  return { status: r.status, json, text };
};
const sql = (q) => execSync("docker exec -i logaluxe-db psql -U logaluxe -d logaluxe -At", { input: q }).toString().trim();
const DOW = ["sun", "mon", "tue", "wed", "thu", "fri", "sat"];

(async () => {
  let r = await call("POST", "/m/signup", { name: "Extras Owner", email: EMAIL, phone: "+16155550211", password: PASS, business: "E2E Extras Studio " + stamp, category: "braids", market: "US", city: "Nashville", region: "TN", address: "1 Test St" });
  check("sign up a business", r.status === 201, r.text);
  const T = r.json.token;
  const g = (p) => call("GET", "/m" + p, undefined, T);
  const p = (path, body, method = "POST") => call(method, "/m" + path, body, T);
  const me = (await g("/me")).json.merchant;
  const owner = (await g("/staff")).json.staff[0];

  // menu and team
  r = await p("/services/import", { rows: [{ name: "Braids", category: "Braids", duration_min: 60, price_cents: 10000, deposit_cents: 0 }, { name: "Wash", category: "Add-ons", duration_min: 30, price_cents: 2500 }, { name: "", category: "x", duration_min: 30, price_cents: 1 }, { name: "Braids", category: "Braids", duration_min: 60, price_cents: 1 }] });
  check("import a price list, skipping a blank and a duplicate", r.json.added === 2 && r.json.skipped?.length === 2, r.text);
  let svcs = (await g("/services")).json.services;
  const braids = svcs.find((s) => s.name === "Braids").id, wash = svcs.find((s) => s.name === "Wash").id;
  r = await p("/staff", { name: "Bea Second", role: "staff", level: "junior", commission_pct: 30, pay_type: "hourly", hourly_cents: 1800 });
  check("add a team member paid by the hour", r.status === 201, r.text); const bea = r.json.id;

  // a day a week or more out, so nothing is in the way
  let day, slots;
  for (let i = 7; i < 15 && !slots?.length; i++) { day = new Date(Date.now() + i * 864e5).toISOString().slice(0, 10); slots = (await g(`/availability?date=${day}&services=${braids}&staff=${owner.id}`)).json.slots; }
  check("free times come back with a price", slots?.length > 0 && slots[0].price_cents === 10000, JSON.stringify(slots?.[0]));
  const dow = DOW[new Date(day + "T12:00:00Z").getUTCDay()];
  const at = slots.find((s) => s.time === "11:00") || slots[2];

  // pricing rules
  r = await p("/price-rules", { name: "Busy day", service_id: braids, days: [dow], adjust_kind: "amount", adjust_value: 1500 });
  check("add a rule: this weekday costs more", r.status === 200, r.text); const rule1 = r.json.id;
  r = await p("/price-rules", { name: "Junior rate", days: [], level: "junior", adjust_kind: "percent", adjust_value: -10 });
  check("add a rule: juniors cost 10% less", r.status === 200, r.text);
  r = await p("/price-rules", { name: "Bad", adjust_kind: "percent", adjust_value: -500 });
  check("a silly rule is refused", r.status === 400, r.text);
  r = await g(`/price-check?service=${braids}&staff=${owner.id}&at=${day}T11:00`);
  check("the owner's price that day is $115", r.json.price_cents === 11500 && r.json.rules?.[0] === "Busy day", r.text);
  r = await g(`/price-check?service=${braids}&staff=${bea}&at=${day}T11:00`);
  check("the junior's price is 10% less: $103.50", r.json.price_cents === 10350 && r.json.rules?.length === 2, r.text);
  r = await g(`/availability?date=${day}&services=${braids}&staff=${owner.id}`);
  check("free times show the ruled price", r.json.slots?.[0]?.price_cents === 11500, JSON.stringify(r.json.slots?.[0]));

  // rooms and chairs
  r = await p("/resources", { name: "Braiding chair", qty: 1, service_ids: [braids] });
  check("add a braiding chair (there is one)", r.status === 200, r.text); const chair = r.json.id;
  r = await p("/bookings", { client_name: "Cora Chair", client_phone: "+16155550221", staff_id: owner.id, starts_at: at.starts_at, service_ids: [braids], source: "phone" });
  check("book the owner in the chair", r.status === 201, r.text); const bk1 = r.json.id;
  r = await g(`/bookings/${bk1}`);
  check("the booking carries the ruled price", r.json.booking?.total_cents === 11500 && r.json.items?.[0]?.price_cents === 11500, r.text.slice(0, 300));
  const cora = r.json.booking.client_id;
  r = await g(`/availability?date=${day}&services=${braids}&staff=${bea}`);
  check("the second person is free then, but the chair is not, so the time is not offered", !r.json.slots.some((s) => s.starts_at === at.starts_at), "");
  r = await p("/bookings", { client_name: "Dee Double", client_phone: "+16155550222", staff_id: bea, starts_at: at.starts_at, service_ids: [braids] });
  check("and booking it anyway is refused", r.status === 409 && /chair/.test(r.text), r.text);
  r = await p(`/resources/${chair}`, { name: "Braiding chair", qty: 2, service_ids: [braids] }, "PUT");
  r = await g(`/availability?date=${day}&services=${braids}&staff=${bea}`);
  check("with a second chair the time opens up", r.json.slots.some((s) => s.starts_at === at.starts_at), "");

  // breaks
  r = await p(`/staff/${bea}`, { name: "Bea Second", role: "staff", level: "junior", commission_pct: 30, retail_commission_pct: 0, breaks: { [dow]: [["13:00", "14:00"]] } }, "PUT");
  check("give her a lunch break", r.status === 200, r.text);
  r = await g(`/availability?date=${day}&services=${wash}&staff=${bea}`);
  check("nothing is offered during the break", r.json.slots.length > 0 && !r.json.slots.some((s) => s.time === "13:00" || s.time === "13:30"), JSON.stringify(r.json.slots.map((s) => s.time)));
  r = await g("/staff"); const beaRow = r.json.staff.find((s) => s.id === bea);
  check("pay type and breaks are kept when other fields are saved", beaRow.pay_type === "hourly" && beaRow.hourly_cents === 1800 && beaRow.breaks?.[dow]?.length === 1, JSON.stringify(beaRow).slice(0, 300));

  // time off: approve and move the booking to the other person
  sql(`insert into time_off (business_id, staff_id, starts_on, ends_on, reason, status) values ('${me.business_id}', '${owner.id}', '${day}', '${day}', 'Test', 'requested')`);
  const off = (await g("/staff")).json.time_off.find((t) => t.status === "requested");
  check("the request shows one booking affected", off?.bookings_affected === 1, JSON.stringify(off));
  r = await p(`/time-off/${off.id}`, { decision: "approve", reassign: true });
  check("approve and reassign moves it", r.json.moved === 1 && r.json.left === 0, r.text);
  r = await g(`/bookings/${bk1}`); check("the booking is now with the second person", r.json.booking?.staff_id === bea, r.json.booking?.staff);

  // packages
  r = await p("/packages", { name: "Three washes", description: "Buy three", price_cents: 6000, valid_days: 90, items: [{ service_id: wash, qty: 3 }] });
  check("create a package", r.status === 200, r.text); const pkg = r.json.id;
  r = await p("/packages", { name: "Empty", price_cents: 100, items: [] });
  check("a package with nothing in it is refused", r.status === 400, r.text);
  r = await p("/checkout", { items: [{ kind: "package", package_id: pkg }], method: "card" });
  check("a package cannot be sold to nobody", r.status === 400, r.text);
  r = await p("/checkout", { client_id: cora, staff_id: bea, items: [{ kind: "package", package_id: pkg }], method: "card" });
  check("sell the package", r.status === 201 && r.json.total_cents === 6000, r.text);
  r = await g(`/clients/${cora}/plans`);
  check("the client holds three wash credits", r.json.plans?.[0]?.credits?.[0]?.left === 3 && r.json.plans[0].status === "active", r.text.slice(0, 400));
  r = await p("/checkout", { client_id: cora, staff_id: bea, items: [{ kind: "service", service_id: wash, redeem: true }, { kind: "service", service_id: wash, redeem: true }], method: "cash" });
  check("use two credits: nothing to pay", r.status === 201 && r.json.total_cents === 0, r.text);
  r = await g(`/clients/${cora}/plans`); check("one credit is left", r.json.plans?.[0]?.credits?.[0]?.left === 1, r.text.slice(0, 300));
  r = await p("/checkout", { client_id: cora, staff_id: bea, items: [{ kind: "service", service_id: wash, redeem: true }], method: "cash" });
  r = await p("/checkout", { client_id: cora, staff_id: bea, items: [{ kind: "service", service_id: wash, redeem: true }], method: "cash" });
  check("a fourth use is refused", r.status === 409 && /no credit left/.test(r.text), r.text);
  r = await g(`/clients/${cora}/plans`); check("the used-up package is marked used", r.json.plans?.[0]?.status === "used", r.text.slice(0, 200));

  // memberships
  r = await p("/memberships", { name: "Glow club", price_cents: 4000, service_discount_pct: 10, retail_discount_pct: 5, items: [{ service_id: wash, qty: 1 }] });
  check("create a membership", r.status === 200, r.text); const mem = r.json.id;
  r = await p("/checkout", { client_id: cora, items: [{ kind: "membership", membership_id: mem }], method: "card" });
  check("the client joins", r.status === 201 && r.json.total_cents === 4000, r.text);
  r = await p("/checkout", { client_id: cora, items: [{ kind: "membership", membership_id: mem }], method: "card" });
  check("joining twice is refused", r.status === 409, r.text);
  r = await g(`/clients/${cora}/plans`);
  check("the client is a member with this month's free wash", r.json.member?.service_discount_pct === 10 && r.json.plans.some((x) => x.kind === "membership" && x.credits?.[0]?.left === 1), r.text.slice(0, 400));
  r = await p("/checkout", { client_id: cora, staff_id: bea, items: [{ kind: "service", service_id: wash, unit_cents: 2000 }], method: "card" });
  check("a member's discount comes off by itself (10% of $20)", r.status === 201 && r.json.member_discount_cents === 200 && r.json.total_cents === 1800, r.text);
  const plan = (await g(`/clients/${cora}/plans`)).json.plans.find((x) => x.kind === "membership");
  sql(`update client_plans set renews_on = current_date - 1 where id='${plan.id}'; update client_credits set used = total where plan_id='${plan.id}';`);
  r = await p(`/client-plans/${plan.id}`, { action: "cancel" }); check("cancel a membership", r.status === 200, r.text);
  r = await p("/checkout", { client_id: cora, staff_id: bea, items: [{ kind: "service", service_id: wash, unit_cents: 2000 }], method: "card" });
  check("after cancelling, no discount", r.json.member_discount_cents === 0 && r.json.total_cents === 2000, r.text);
  r = await g("/menu"); check("the menu lists the package, membership, rules and chair", r.json.packages?.length === 1 && r.json.memberships?.length === 1 && r.json.price_rules?.length === 2 && r.json.resources?.[0]?.qty === 2 && r.json.holders?.length >= 2, r.text.slice(0, 300));

  // back-bar: a service uses half a unit
  r = await p("/products", { name: "E2E Gel", sku: "E2E-GEL", kind: "backbar", category: "styling", price_cents: 0, cost_cents: 400, stock: 5, reorder_at: 1, par_level: 10, online: false });
  check("add a back-bar product with a full-shelf level", r.status === 201, r.text); const gel = r.json.id;
  r = await p(`/products/${gel}/services`, { items: [{ service_id: wash, qty: 0.5 }] }, "PUT");
  check("say a wash uses half a unit", r.status === 200, r.text);
  await p("/checkout", { staff_id: bea, items: [{ kind: "service", service_id: wash }], method: "cash" });
  let prod = (await g("/inventory")).json.products.find((x) => x.id === gel);
  check("one wash: still 5 on the shelf, half a unit open", prod.stock === 5 && prod.backbar_open === 0.5 && prod.par_level === 10 && prod.used_in?.[0]?.qty === 0.5, JSON.stringify(prod).slice(0, 400));
  await p("/checkout", { staff_id: bea, items: [{ kind: "service", service_id: wash }], method: "cash" });
  prod = (await g("/inventory")).json.products.find((x) => x.id === gel);
  check("a second wash takes one off the shelf", prod.stock === 4 && prod.backbar_open === 0, JSON.stringify({ s: prod.stock, o: prod.backbar_open }));
  r = await g(`/products/${gel}/history`); check("and the stock history says why", /backbar/.test(r.text), r.text.slice(0, 300));

  // pay
  r = await g("/payroll"); const pay = r.json.payroll.find((x) => x.id === bea), own = r.json.payroll.find((x) => x.id === owner.id);
  check("payroll: commission counts redeemed services at their value, not the package sale", pay && pay.plan_cents === 6000 && pay.service_cents === 2250 * 3 + 2000 + 2000 + 2250 * 2, JSON.stringify(pay)); // a wash by a junior is $22.50 after the 10% rule
  check("payroll: the owner takes no commission; hourly staff have a wage line", own.service_commission_cents === 0 && pay.pay_type === "hourly" && pay.wage_cents >= 0 && pay.earned_cents >= pay.service_commission_cents, JSON.stringify({ own, wage: pay.wage_cents }));
  r = await p("/staff", { name: "Lash Haus", role: "staff", level: "senior", pay_type: "renter", rent_cents: 18000, rent_period: "weekly", rent_days: ["mon", "tue"], trading_name: "Lash Haus" });
  check("add a chair renter", r.status === 201, r.text); const renter = r.json.id;
  r = await g("/staff"); const rr = r.json.staff.find((s) => s.id === renter);
  check("a renter is not bookable through the business", rr.bookable === false && rr.rent_cents === 18000 && rr.rent_days.length === 2, JSON.stringify(rr).slice(0, 300));
  sql(`insert into rent_charges (business_id, staff_id, period_start, period_end, amount_cents) values ('${me.business_id}', '${renter}', date_trunc('week', now())::date, date_trunc('week', now())::date + 6, 18000)`);
  const charge = (await g("/staff")).json.rent?.[0];
  r = await p(`/rent/${charge?.id}`, { action: "paid", method: "transfer" }); check("mark the week's rent as paid", r.status === 200, r.text);
  r = await g("/payroll"); check("the renter is listed apart from payroll", r.json.renters?.[0]?.paid_cents === 18000 && !r.json.payroll.some((x) => x.id === renter), JSON.stringify(r.json.renters));

  // permissions
  r = await p(`/staff/${bea}/invite`, { email: `e2e-x-staff-${stamp}@example.test`, role: "staff" });
  const tok = ((execSync("docker logs --tail 60 logaluxe-api 2>&1").toString().match(/business\/reset\?token=([a-f0-9]+)/g) || []).pop() || "").split("=")[1];
  await call("POST", "/m/reset", { token: tok, password: PASS + "-s" });
  let ST = (await call("POST", "/m/login", { email: `e2e-x-staff-${stamp}@example.test`, password: PASS + "-s" })).json.token;
  check("the team member signs in", !!ST);
  r = await call("GET", "/m/reports", undefined, ST); check("by default a team member cannot see reports", r.status === 403, r.text);
  r = await call("POST", "/m/checkout", { items: [{ kind: "custom", name: "Tip jar", unit_cents: 100 }], method: "cash" }, ST); check("but can take a payment", r.status === 201, r.text);
  r = await p(`/staff/${bea}`, { name: "Bea Second", role: "staff", level: "junior", commission_pct: 30, retail_commission_pct: 0, permissions: { see_reports: true, take_payments: false, see_all_calendars: false } }, "PUT");
  check("the owner changes her permissions", r.status === 200, r.text);
  r = await call("GET", "/m/me", undefined, ST); check("her sign-in knows", r.json.merchant?.permissions?.see_reports === true && r.json.merchant.permissions.take_payments === false, JSON.stringify(r.json.merchant?.permissions));
  r = await call("GET", "/m/reports", undefined, ST); check("now she can see reports", r.status === 200, r.text.slice(0, 100));
  r = await call("POST", "/m/checkout", { items: [{ kind: "custom", name: "Tip jar", unit_cents: 100 }], method: "cash" }, ST); check("and cannot take a payment", r.status === 403, r.text);
  r = await call("GET", `/m/calendar?date=${day}`, undefined, ST); check("and sees only her own column", r.json.staff?.length === 1 && r.json.staff[0].id === bea && r.json.bookings.every((b) => b.staff_id === bea), JSON.stringify(r.json.staff?.map((s) => s.name)));
  r = await call("POST", "/m/packages", { name: "Sneaky", price_cents: 1, items: [{ service_id: wash, qty: 1 }] }, ST); check("and cannot change the menu", r.status === 403, r.text);

  // tidy rules
  r = await call("DELETE", `/m/price-rules/${rule1}`, undefined, T); check("delete a pricing rule", r.status === 200, r.text);
  r = await call("DELETE", `/m/packages/${pkg}`, undefined, T); check("a package someone bought is switched off, not deleted", r.json.archived === true, r.text);

  try {
    const id = me.business_id;
    sql(`delete from ledger where business_id='${id}'; delete from payouts where business_id='${id}'; delete from client_plans where business_id='${id}'; delete from sales where business_id='${id}';
      delete from bookings where business_id='${id}'; delete from businesses where id='${id}'; delete from merchant_users where email like 'e2e-x-%@example.test';`);
    console.log("cleaned up; test businesses left behind:", sql(`select count(*) from businesses where name like 'E2E Extras Studio%'`));
  } catch (e) { console.log("CLEANUP PROBLEM:", String(e.stderr || e).slice(0, 800)); }
  console.log(`\n${pass} passed, ${fail} failed`);
  process.exit(fail ? 1 : 0);
})();
