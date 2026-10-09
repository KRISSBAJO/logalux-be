// End-to-end check of the merchant API. Signs up a throwaway business, runs
// the working day through it, then deletes it. Run from logaluxe-be with the
// API started as: MAIL_PROVIDER=log docker compose up -d api
//   node scripts/e2e/merchant.js
const { execSync } = require("child_process");
const API = process.env.API || "http://localhost:18080/v1";
const stamp = Date.now().toString(36);
const EMAIL = `e2e-${stamp}@example.test`;
const PASS = "e2e-" + stamp + "-password";
let pass = 0, fail = 0;
const check = (name, ok, extra = "") => { ok ? pass++ : fail++; console.log((ok ? "ok   " : "FAIL ") + name + (ok ? "" : "  " + extra)); };
const call = async (method, path, body, token) => {
  const r = await fetch(API + path, { method, headers: { "Content-Type": "application/json", ...(token ? { Authorization: "Bearer " + token } : {}) }, body: body === undefined ? undefined : JSON.stringify(body) });
  const text = await r.text();
  let json = {}; try { json = JSON.parse(text); } catch {}
  return { status: r.status, json, text };
};
// SQL runs against the database the API uses: DATABASE_URL in .env when set, else the local container.
const DBURL = (() => { try { return (require("fs").readFileSync(".env", "utf8").match(/^DATABASE_URL=(.*)$/m) || [])[1]?.trim().replace(/^["']|["']$/g, "") || ""; } catch { return ""; } })();
const sql = (q) => execSync(DBURL ? `docker exec -i logaluxe-db psql "${DBURL}${DBURL.includes("?") ? "&" : "?"}sslrootcert=system" -At` : "docker exec -i logaluxe-db psql -U logaluxe -d logaluxe -At", { input: q }).toString().trim();

(async () => {
  let r = await call("POST", "/m/signup", { name: "E2E Owner", email: EMAIL, phone: "+16155550111", password: PASS, business: "E2E Test Studio " + stamp, category: "braids", market: "US", city: "Nashville", region: "TN", address: "1 Test St" });
  check("sign up a business", r.status === 201 && r.json.token, r.text);
  const T = r.json.token;
  const g = (p) => call("GET", "/m" + p, undefined, T);
  const p = (path, body, method = "POST") => call(method, "/m" + path, body, T);

  r = await g("/me"); const me = r.json.merchant;
  check("me shows the owner", me?.role === "owner" && me.market === "US", r.text);
  r = await call("POST", "/m/login", { email: EMAIL, password: "wrong-password-here" });
  check("wrong password is refused", r.status === 401 || r.status === 403, r.text);
  r = await call("GET", "/m/me");
  check("no token is refused", r.status === 401, r.text);

  // menu and team
  r = await g("/staff"); const owner = r.json.staff?.[0];
  check("signup made the owner bookable", !!owner?.id, r.text);
  r = await p("/staff", { name: "Tess Helper", role: "staff", level: "junior", commission_pct: 35, email: "", phone: "" });
  check("add a team member", r.status === 201, r.text); const helper = r.json.id;
  r = await p("/services", { name: "E2E Braids", category: "Braids", duration_min: 60, buffer_min: 0, price_cents: 10000, deposit_cents: 2000, staff_ids: [] });
  check("add a service", r.status === 201, r.text); const svc = r.json.id;
  r = await p("/services", { name: "", category: "Braids", duration_min: 60, price_cents: 100 });
  check("a service without a name is refused", r.status === 400, r.text);
  r = await p(`/services/${svc}`, { name: "E2E Braids", category: "Braids", duration_min: 60, buffer_min: 0, price_cents: 12000, deposit_cents: 2000, staff_ids: [owner.id, helper], staff_prices: { [helper]: 9000 } }, "PUT");
  check("edit a service and give one person their own price", r.status === 200, r.text);
  r = await p(`/services/${svc}/action`, { action: "duplicate" });
  check("duplicate a service", r.status === 201 || r.status === 200, r.text);
  r = await g("/services"); check("menu lists both", r.json.services?.length === 2, r.text);
  const copy = (r.json.services || []).find((s) => s.id !== svc);
  r = await p(`/services/${copy?.id}/action`, { action: "delete" });
  check("delete the copy", r.status === 200, r.text);

  // calendar: find a slot in the next week and book it
  let day, slot;
  for (let i = 1; i < 8 && !slot; i++) {
    day = new Date(Date.now() + i * 864e5).toISOString().slice(0, 10);
    r = await g(`/availability?date=${day}&services=${svc}&staff=${owner.id}`);
    slot = r.json.slots?.[0];
  }
  check("availability returns open slots", !!slot, r.text);
  console.log("     slot →", JSON.stringify(slot));
  const startsAt = slot.starts_at || slot.start || slot.time;
  r = await p("/bookings", { client_name: "Casey Client", client_phone: "+16155550122", staff_id: owner.id, starts_at: startsAt, service_ids: [svc], notes: "first visit", source: "phone" });
  check("take a booking by phone", r.status === 201, r.text); const bk = r.json.id;
  r = await p("/bookings", { client_name: "Dana Double", client_phone: "+16155550123", staff_id: owner.id, starts_at: startsAt, service_ids: [svc] });
  check("the same slot cannot be booked twice", r.status === 409, r.text);
  r = await g(`/calendar?date=${day}`);
  check("the booking is on the calendar with its service", r.json.bookings?.some((b) => b.id === bk && b.services === "E2E Braids"), r.text.slice(0, 300));
  r = await g(`/bookings/${bk}`); check("booking detail", r.json.items?.length === 1 && r.json.booking?.client_name === "Casey Client", r.text);
  const clientId = r.json.booking?.client_id;
  check("the booking made a client", !!clientId);

  // blocked time stops bookings
  const s0 = new Date(startsAt), blockStart = new Date(s0.getTime() + 2 * 36e5), blockEnd = new Date(s0.getTime() + 3 * 36e5);
  r = await p("/blocks", { staff_id: owner.id, starts_at: blockStart.toISOString(), ends_at: blockEnd.toISOString(), reason: "Lunch" });
  check("block out time", r.status === 201, r.text); const block = r.json.id;
  r = await g(`/availability?date=${day}&services=${svc}&staff=${owner.id}`);
  check("blocked time is not offered", !(r.json.slots || []).some((s) => { const t = new Date(s.starts_at || s.start).getTime(); return t >= blockStart.getTime() && t < blockEnd.getTime(); }), "");
  r = await call("DELETE", `/m/blocks/${block}`, undefined, T); check("remove the block", r.status === 200, r.text);

  // the visit
  r = await p(`/bookings/${bk}/action`, { action: "reschedule", starts_at: new Date(s0.getTime() + 36e5).toISOString(), staff_id: owner.id });
  check("move the booking an hour", r.status === 200, r.text);
  for (const a of ["check_in", "start", "complete"]) { r = await p(`/bookings/${bk}/action`, { action: a }); check("booking: " + a, r.status === 200, r.text); }
  r = await p(`/bookings/${bk}/action`, { action: "cancel", reason: "x" });
  check("a finished booking cannot be cancelled", r.status === 409, r.text);

  // stock
  r = await p("/suppliers", { name: "E2E Supply", contact: "Sam", email: "", phone: "" });
  check("add a supplier", r.status === 201, r.text); const sup = r.json.id;
  r = await p("/products", { name: "E2E Oil", sku: "E2E-OIL", kind: "retail", category: "hair", description: "", price_cents: 1500, cost_cents: 600, stock: 3, reorder_at: 5, supplier_id: sup, online: false });
  check("add a product", r.status === 201, r.text); const prod = r.json.id;
  r = await p(`/products/${prod}/stock`, { delta: 2, reason: "restock", note: "delivery" });
  check("restock", r.status === 200, r.text);
  r = await p("/purchase-orders", { suggest: true });
  check("suggest a purchase order for low stock", r.status === 201 || r.status === 200, r.text);
  console.log("     purchase order →", JSON.stringify(r.json).slice(0, 200));

  // checkout
  r = await g("/checkout");
  check("the finished visit waits at checkout", r.json.queue?.some((b) => b.id === bk), r.text.slice(0, 200));
  r = await p("/checkout", { booking_id: bk, items: [{ kind: "service", service_id: svc, staff_id: owner.id, qty: 1 }, { kind: "product", product_id: prod, qty: 99 }], tip_cents: 0, discount_cents: 0, method: "card" });
  check("cannot sell more than is in stock", r.status === 409, r.text);
  r = await p("/checkout", { booking_id: bk, items: [{ kind: "service", service_id: svc, staff_id: owner.id, qty: 1 }, { kind: "product", product_id: prod, qty: 2 }], tip_cents: 2000, discount_cents: 500, method: "card" });
  check("take payment", r.status === 201, r.text); const sale = r.json.id || r.json.sale_id;
  console.log("     sale →", JSON.stringify(r.json));
  r = await p("/checkout", { booking_id: bk, items: [{ kind: "service", service_id: svc, qty: 1 }], method: "card" });
  check("a booking cannot be paid twice", r.status === 409, r.text);
  r = await g("/inventory"); const oil = r.json.products?.find((x) => x.id === prod);
  check("the sale took stock off the shelf (3 + 2 - 2 = 3)", oil?.stock === 3, JSON.stringify(oil));
  r = await g(`/products/${prod}/history`);
  check("stock history is kept", JSON.stringify(r.json).split("delta").length > 3, r.text.slice(0, 300));
  r = await g("/checkout/day"); check("end of day counts the sale", r.json.totals?.sales === 1, r.text);
  r = await g("/money"); const bal = r.json.balances;
  check("card money is pending, not yet available", bal?.pending_cents > 0 && bal.available_cents === 0, JSON.stringify(bal));
  console.log("     balances →", JSON.stringify(bal), "month →", JSON.stringify(r.json.month));
  r = await p("/payouts", { instant: false });
  check("no payout before a payout account exists", r.status >= 400, r.text);
  r = await p("/payout-account/stripe", { bank_name: "Test Bank", last4: "4242", account_name: "E2E Test Studio" });
  check("add a payout account (simulated)", r.status === 201, r.text);
  r = await p("/payouts", { instant: false });
  check("nothing has settled yet, so no payout", r.status >= 400 && /nothing to pay out/.test(r.text), r.text);
  sql(`update ledger set status='settled' where business_id='${me.business_id}' and status='pending'`);
  r = await p("/payouts", { instant: true });
  check("instant payout once money has settled", r.status === 201, r.text);
  r = await g("/money");
  check("the balance is empty after the payout", r.json.balances?.available_cents === 0 && r.json.payouts?.[0]?.status === "paid", JSON.stringify(r.json.balances) + JSON.stringify(r.json.payouts?.[0]));
  console.log("     payout →", JSON.stringify(r.json.payouts?.[0]));
  r = await p(`/sales/${sale}/refund`, { amount_cents: 1500, reason: "wrong product", restock: false }); // a part refund cannot restock by itself
  check("refund part of a sale", r.status === 200 || r.status === 201, r.text);
  r = await p(`/sales/${sale}/refund`, { amount_cents: 99999999, reason: "too much" });
  check("cannot refund more than was paid", r.status === 400, r.text);
  r = await p("/payout-schedule", { schedule: "weekly" }, "PUT"); check("change the payout schedule", r.status === 200, r.text);

  // clients
  r = await p("/clients", { name: "Wendy Walkin", phone: "+16155550133", email: "", notes: "likes tea", tags: ["vip"] });
  check("add a client", r.status === 201, r.text); const c2 = r.json.id;
  r = await p("/clients", { name: "Wendy Again", phone: "+16155550133" });
  check("the same phone number twice is refused", r.status === 409, r.text);
  r = await p(`/clients/${c2}`, { name: "Wendy Walkin", phone: "+16155550133", notes: "prefers mornings", tags: ["vip", "tender-headed"], marketing_opt_in: false }, "PUT");
  check("edit a client", r.status === 200, r.text);
  r = await p("/clients/import", { rows: [{ name: "Imp One", phone: "+16155550141" }, { name: "Imp Two", phone: "+16155550142" }, { name: "Wendy Dup", phone: "+16155550133" }, { name: "", phone: "" }] });
  check("import clients, skipping duplicates and blanks", r.status === 200 && r.json.added === 2, r.text);
  r = await g(`/clients/${clientId}`); check("client page shows the paid visit", r.json.visits?.length === 1 && r.json.client?.visits === 1, r.text.slice(0, 300));
  r = await g("/clients?q=wendy"); check("search clients", r.json.total === 1, r.text.slice(0, 200));

  // inbox
  r = await p("/inbox", { client_id: c2, channel: "whatsapp", body: "Hi Wendy, we have a slot on Friday." });
  check("start a conversation", r.status === 201, r.text); const th = r.json.id;
  r = await p(`/inbox/${th}/reply`, { body: "Shall I hold it for you?" }); check("reply in a conversation", r.status === 201 || r.status === 200, r.text);
  console.log("     reply →", JSON.stringify(r.json));
  r = await g(`/inbox/${th}`); check("the thread has both messages", r.json.messages?.length === 2, r.text.slice(0, 200));
  r = await p(`/inbox/${th}`, { status: "closed" }, "PUT"); check("close a conversation", r.status === 200, r.text);

  // marketing
  r = await p("/automations/reminder_24h", { enabled: false }, "PUT");
  check("switch an automation off", r.status === 200, r.text);
  r = await p("/campaigns", { name: "E2E offer", audience: "all", channel: "whatsapp", subject: "", message: "Hi {first name}, 10% off this week." });
  check("draft a campaign", r.status === 201, r.text); const camp = r.json.id;
  r = await p(`/campaigns/${camp}/test`, {}); check("send a test to myself", r.status === 200, r.text);
  r = await p(`/campaigns/${camp}/send`, {}); check("send the campaign", r.status === 202, r.text);
  console.log("     campaign →", JSON.stringify(r.json));
  // Sending is a job the worker works through; wait for it, up to 15 seconds.
  let sent = null;
  for (let i = 0; i < 30 && !(sent && sent.status === "sent"); i++) { await new Promise((d) => setTimeout(d, 500)); r = await g("/marketing"); sent = r.json.campaigns?.find((c) => c.id === camp); }
  check("the campaign is marked sent", sent && sent.status === "sent", JSON.stringify(sent));
  console.log("     campaign row →", JSON.stringify(sent));

  // team
  r = await p("/time-off", { staff_id: helper, starts_on: day, ends_on: day, reason: "Dentist" });
  check("ask for time off", r.status === 201, r.text); const off = r.json.id;
  check("an owner's time off is approved as it is made", r.json.status === "approved", r.text);
  r = await g(`/availability?date=${day}&services=${svc}&staff=${helper}`);
  check("someone on approved leave has no slots", r.json.slots?.length === 0, r.text.slice(0, 200));
  r = await g("/payroll"); const pay = r.json.payroll?.find((x) => x.id === owner.id);
  check("payroll shows commission and tips", pay && pay.tips_cents === 2000 && pay.sales === 1, JSON.stringify(pay));
  r = await p(`/staff/${helper}/invite`, { email: `e2e-staff-${stamp}@example.test`, role: "staff" });
  check("invite a team member to sign in", r.status === 201 || r.status === 200, r.text);
  const log = execSync("docker logs --tail 80 logaluxe-api 2>&1").toString();
  const tok = ((log.match(/business\/reset\?token=([a-f0-9]+)/g) || []).pop() || "").split("=")[1];
  check("the invite email carries a set-password link", !!tok, "no link in the API log");
  r = await call("POST", "/m/reset", { token: tok, password: PASS + "-staff" }); check("the team member sets a password", r.status === 200, r.text);
  r = await call("POST", "/m/login", { email: `e2e-staff-${stamp}@example.test`, password: PASS + "-staff" });
  check("the team member signs in", r.status === 200 && r.json.token, r.text); const ST = r.json.token;
  r = await call("GET", "/m/calendar?date=" + day, undefined, ST); check("staff can see the calendar", r.status === 200, r.text.slice(0, 100));
  r = await call("GET", "/m/money", undefined, ST); check("staff cannot see money", r.status === 403, r.text);
  r = await call("GET", "/m/reports", undefined, ST); check("staff cannot see reports", r.status === 403, r.text);
  r = await call("POST", "/m/services", { name: "Sneaky", category: "x", duration_min: 30, price_cents: 1 }, ST); check("staff cannot change the menu", r.status === 403, r.text);
  r = await call("DELETE", `/m/staff/${helper}/invite`, undefined, T); check("the owner takes the sign-in away", r.status === 200, r.text);
  r = await call("GET", "/m/me", undefined, ST); check("and the old session stops working", r.status === 401 || r.status === 403, r.text);

  // storefront and settings
  r = await p("/storefront", { name: "E2E Test Studio " + stamp, slug: "e2e-" + stamp, tagline: "Braids done right", about: "A test.", category: "braids", highlights: ["Walk-ins welcome"], instagram: "@e2e", tiktok: "", website: "", tone: "#3B1D22", display: { show_phone: true, notice: "Closed on the 25th" } }, "PUT");
  check("edit the storefront and its address", r.status === 200, r.text);
  r = await p("/settings/rules", { booking: { lead_hours: 4, max_days: 30, instant: false }, policy: { cancel_hours: 48, late_cancel_fee: "deposit" } }, "PUT");
  check("change the booking rules", r.status === 200 && r.json.rules?.booking?.lead_hours === 4, r.text.slice(0, 200));
  r = await p("/settings/rules", { booking: { lead_hours: 9999 } }, "PUT"); check("a silly rule is refused", r.status === 400, r.text);
  r = await p("/settings/profile", { name: "E2E Test Studio " + stamp, category: "braids", phone: "+16155550111", email: "", about: "A test.", timezone: "America/Chicago", sales_tax_pct: 9.25 }, "PUT");
  check("edit the business profile", r.status === 200, r.text);
  r = await p("/locations", { name: "Second chair", address: "2 Test St", city: "Nashville", region: "TN" }); check("add a second location", r.status === 201, r.text);
  r = await p("/plan", { plan: "pro" }); check("change plan", r.status === 200, r.text);
  r = await p("/listing", { paused: true }); check("pause the listing", r.status === 200 || r.status === 409, r.text);
  console.log("     listing →", r.text.slice(0, 160));

  r = await g("/home"); check("home loads", r.status === 200 && !!r.json.today, r.text.slice(0, 200));
  r = await g("/reports?range=7d"); check("reports count the sale", r.json.now?.sales === 1, JSON.stringify(r.json.now));
  r = await g("/reports/export?range=7d"); check("reports export is a spreadsheet", r.status === 200 && r.text.split("\n").length >= 2, r.text.slice(0, 100));
  r = await p("/password", { current: PASS, new: PASS + "-2" }); check("change my password", r.status === 200, r.text);
  r = await p("/logout", {}); check("sign out", r.status === 200, r.text);
  r = await g("/me"); check("the session is gone", r.status === 401, r.text);

  // clean up: the throwaway business and its people
  try {
    const id = me.business_id;
    sql(`delete from ledger where business_id='${id}';
      delete from payouts where business_id='${id}';
      delete from sales where business_id='${id}';
      delete from bookings where business_id='${id}';
      delete from businesses where id='${id}';
      delete from merchant_users where email like 'e2e-%@example.test';`);
    console.log("cleaned up; test businesses left behind:", sql(`select count(*) from businesses where name like 'E2E Test Studio%'`));
  } catch (e) { console.log("CLEANUP PROBLEM:", String(e.stderr || e).slice(0, 800)); }
  console.log(`\n${pass} passed, ${fail} failed`);
  process.exit(fail ? 1 : 0);
})();
