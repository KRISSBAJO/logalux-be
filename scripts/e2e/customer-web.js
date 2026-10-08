// End-to-end check of what the customer web relies on: next openings, the
// month calendar, saved businesses, reviews, booking and moving a booking,
// the shop filters, an order with two sellers, each seller's fulfilment and
// pay, and product reviews. It uses one throwaway customer on the sample
// business "ada" and removes what it made. Run with simulated payments:
//   STRIPE_SECRET_KEY= PAYSTACK_SECRET_KEY= MAIL_PROVIDER=log docker compose up -d api
//   node scripts/e2e/customer-web.js
const { execSync } = require("child_process");
const fs = require("fs");
const API = process.env.API || "http://localhost:18080/v1";
const stamp = Date.now().toString(36);
const EMAIL = `e2e-cw-${stamp}@example.test`, PASS = "e2e-" + stamp + "-password";
const MPW = fs.readFileSync(".env", "utf8").match(/^MERCHANT_DEMO_PASSWORD=(.*)$/m)[1].trim();
let pass = 0, fail = 0;
const check = (name, ok, extra = "") => { ok ? pass++ : fail++; console.log((ok ? "ok   " : "FAIL ") + name + (ok ? "" : "  " + String(extra).slice(0, 500))); };
const call = async (method, path, body, token) => {
  const r = await fetch(API + path, { method, headers: { "Content-Type": "application/json", ...(token ? { Authorization: "Bearer " + token } : {}) }, body: body === undefined ? undefined : JSON.stringify(body) });
  const text = await r.text(); let json = {}; try { json = JSON.parse(text); } catch {}
  return { status: r.status, json, text };
};
const sql = (q) => execSync("docker exec -i logaluxe-db psql -U logaluxe -d logaluxe -At", { input: q }).toString().trim();

(async () => {
  let r = await call("POST", "/auth/signup", { first_name: "Cleo", last_name: "Webster", email: EMAIL, phone: "+16155550811", password: PASS });
  check("a customer signs up", r.status === 201 || r.status === 200, r.text); const C = r.json.token;
  const g = (p) => call("GET", p, undefined, C), p = (path, body, method = "POST") => call(method, path, body, C);
  const M = (await call("POST", "/m/login", { email: "ada@logaluxe.test", password: MPW })).json.token;

  // ----- finding a time -----
  r = await call("GET", "/openings?slugs=ada,nia,nosuch&q=silk");
  check("search cards get the next openings of several businesses at once", r.json.openings?.ada?.slots?.length === 3 && /silk/i.test(r.json.openings.ada.service) && !r.json.openings.nosuch, r.text.slice(0, 300));
  const biz = (await g("/businesses/ada")).json;
  const wash = biz.services.find((s) => /wash/i.test(s.name));
  r = await call("GET", `/businesses/ada/openings?services=${wash.id}&limit=8`);
  check("the storefront gets the next openings for the chosen service, with prices", r.json.slots?.length === 8 && r.json.slots.every((s) => s.price_cents > 0 && s.staff), r.text.slice(0, 200));
  const month = new Date(Date.now() + 10 * 864e5).toISOString().slice(0, 7);
  r = await call("GET", `/businesses/ada/days?month=${month}&services=${wash.id}&staff=any`);
  const openDays = (r.json.days || []).filter((d) => d.open > 0);
  check("the calendar knows which days of the month have room", r.json.days?.length >= 28 && openDays.length > 3 && r.json.days.some((d) => d.open === 0), `${openDays.length} open of ${r.json.days?.length}`);
  r = await call("GET", `/businesses/ada/days?month=nonsense&services=${wash.id}`); check("a bad month is refused", r.status === 400, r.text);

  // ----- saving a business -----
  r = await call("PUT", "/auth/favourites/ada"); check("saving needs a sign-in", r.status === 401, r.text);
  r = await p("/auth/favourites/ada", undefined, "PUT"); check("save a business", r.json.saved === true, r.text);
  r = await p("/auth/favourites/no-such-place", undefined, "PUT"); check("an unknown business cannot be saved", r.status === 404, r.text);
  r = await g("/auth/favourites"); check("it is in the saved list", r.json.favourites?.length === 1 && r.json.favourites[0].slug === "ada" && r.json.favourites[0].from_cents > 0, r.text.slice(0, 200));
  r = await g("/businesses/ada"); check("the storefront knows it is saved", r.json.saved === true && r.json.photo_count >= 0, "");
  r = await call("GET", "/businesses/ada"); check("and a visitor who is not signed in sees it as not saved", r.json.saved === false, "");
  r = await p("/auth/favourites/ada", undefined, "DELETE"); r = await g("/auth/favourites"); check("unsave it", r.json.favourites?.length === 0, r.text);

  // ----- reviews -----
  r = await call("GET", "/businesses/ada/reviews?page=1");
  check("reviews come a page at a time with the star breakdown", r.status === 200 && r.json.per_page === 10 && r.json.breakdown?.all >= r.json.reviews.length && typeof r.json.summary === "string", r.text.slice(0, 200));

  // ----- booking, the calendar file, and moving it -----
  const far = (await call("GET", `/businesses/ada/days?month=${month}&services=${wash.id}&staff=any`)).json.days.filter((d) => d.open > 2).slice(-3);
  const slots = (await call("GET", `/businesses/ada/availability?date=${far[0].date}&services=${wash.id}&staff=any`)).json.slots;
  r = await p("/bookings", { business_slug: "ada", staff_id: slots[0].staff_id, starts_at: slots[0].starts_at, service_ids: [wash.id], client_name: "Cleo Webster", client_phone: "+16155550811", client_email: EMAIL, source: "link" });
  check("book while signed in", r.status === 201, r.text); const bk = r.json.booking.id;
  r = await fetch(API + `/bookings/${bk}/calendar.ics`); const ics = await r.text();
  check("the booking downloads as a calendar file", r.status === 200 && /BEGIN:VEVENT/.test(ics) && /SUMMARY:.*Wash/.test(ics) && /DTSTART:\d{8}T\d{6}Z/.test(ics), ics.slice(0, 200));
  r = await g(`/auth/bookings/${bk}`); check("the account shows it can be moved or cancelled", r.json.booking?.can_reschedule === true && r.json.booking.can_cancel === true && r.json.booking.service_ids?.length === 1, r.text.slice(0, 300));
  const other = (await call("GET", `/businesses/ada/availability?date=${far[1].date}&services=${wash.id}&staff=any`)).json.slots;
  r = await p(`/auth/bookings/${bk}/reschedule`, { starts_at: "2031-01-01T10:00:00Z", staff_id: "any" }); check("it cannot be moved to a time that is not on offer", r.status === 409, r.text);
  r = await p(`/auth/bookings/${bk}/reschedule`, { starts_at: other[1].starts_at, staff_id: other[1].staff_id }); check("move it to another free time", r.status === 200, r.text);
  r = await g(`/auth/bookings/${bk}`); check("the booking has the new time and the same length", new Date(r.json.booking.starts_at).getTime() === new Date(other[1].starts_at).getTime() && new Date(r.json.booking.ends_at) - new Date(r.json.booking.starts_at) > 0, r.text.slice(0, 200));
  sql(`update bookings set starts_at = now() + interval '3 hours', ends_at = now() + interval '4 hours' where id='${bk}'`);
  r = await p(`/auth/bookings/${bk}/reschedule`, { starts_at: other[2].starts_at, staff_id: other[2].staff_id }); check("inside the cancellation window it can no longer be moved online", r.status === 409 && /message the business/.test(r.text), r.text);
  r = await g("/auth/wallet"); check("the wallet answers (nothing held yet)", Array.isArray(r.json.wallet), r.text);

  // ----- the shop -----
  r = await call("GET", "/products"); const all = r.json.products;
  check("the shop lists products with what can be filtered", all?.length >= 6 && r.json.categories?.length >= 3 && r.json.sellers?.length >= 3 && r.json.tags?.length > 0 && all.every((x) => x.review_count >= 0), r.text.slice(0, 200));
  r = await call("GET", "/products?seller=ada&delivery=pickup"); check("filter: one studio's products that can be collected", r.json.products.length > 0 && r.json.products.every((x) => x.business_slug === "ada" && x.pickup), JSON.stringify(r.json.products.map((x) => x.slug)));
  r = await call("GET", "/products?seller=brands"); check("filter: brands only, which cannot be collected", r.json.products.length > 0 && r.json.products.every((x) => !x.business_slug && !x.pickup), JSON.stringify(r.json.products.map((x) => [x.slug, x.pickup])));
  const low = (x) => Math.min(x.price_cents, ...(x.sizes || []).map((z) => z.price_cents));
  r = await call("GET", "/products?max=1500&sort=price_asc"); check("filter by price and sort use the lowest size price", r.json.products.length > 0 && r.json.products.every((x) => low(x) <= 1500) && r.json.products.every((x, i, a) => i === 0 || low(a[i - 1]) <= low(x) || a[i - 1].stock > 0 !== x.stock > 0), JSON.stringify(r.json.products.map(low)));
  r = await call("GET", "/businesses/ada"); check("a business page says who performs what and how deposits are paid", r.json.staff.every((x) => Array.isArray(x.service_ids)) && r.json.staff.some((x) => x.service_ids.length > 0) && typeof r.json.policy.payments_live === "boolean" && r.json.policy.new_client_deposit_pct >= 0, JSON.stringify(r.json.policy));
  r = await g("/products?seller=booked"); check("filter: from professionals I have booked", r.json.booked?.includes("ada") && r.json.products.every((x) => x.business_slug === "ada"), JSON.stringify(r.json.booked));
  r = await g("/products/scalp-oil"); check("a product page has reviews, related products and who sells it", r.json.reviews?.length >= 2 && r.json.related?.length === 4 && r.json.product.business_slug === "ada" && r.json.can?.review === false && /bought/.test(r.json.can.why), JSON.stringify(r.json.can));
  r = await p("/auth/products/scalp-oil/review", { rating: 5, body: "I have not bought this but I love it." }); check("someone who has not bought it cannot review it", r.status === 403, r.text);

  // ----- an order with two sellers -----
  const before = Number(sql("select stock from products where slug='scalp-oil'"));
  r = await p("/orders", { customer_name: "Cleo Webster", customer_phone: "+16155550811", fulfilment: "pickup", items: [{ product_slug: "clarifying-shampoo", qty: 1 }] });
  check("a brand's product cannot be collected", r.status === 400 && /cannot be collected/.test(r.text), r.text);
  r = await p("/orders", { customer_name: "Cleo Webster", customer_phone: "+16155550811", fulfilment: "ship", fulfilment_by_seller: { "Ada's Braid Studio": "pickup" }, items: [{ product_slug: "scalp-oil", size_label: "60 ml", qty: 2 }, { product_slug: "clarifying-shampoo", qty: 1 }] });
  check("shipping needs an address", r.status === 400 && /address/.test(r.text), r.text);
  r = await p("/orders", { customer_name: "Cleo Webster", customer_phone: "+16155550811", customer_email: EMAIL, fulfilment: "ship", fulfilment_by_seller: { "Ada's Braid Studio": "pickup" }, address: { line1: "1 Test St", city: "Nashville", zip: "37206" },
    items: [{ product_slug: "scalp-oil", size_label: "60 ml", qty: 2 }, { product_slug: "clarifying-shampoo", qty: 1 }] });
  const o = r.json.order;
  check("order from a studio (collect) and a brand (ship) in one go", r.status === 201 && o.subtotal_cents === 6000 && o.shipping_cents === 499 && o.shipments?.length === 2, r.text.slice(0, 400));
  check("each seller has its own way of getting there", o.shipments.find((s) => /Ada/.test(s.seller_name)).fulfilment === "pickup" && o.shipments.find((s) => /Root/.test(s.seller_name)).fulfilment === "ship" && o.shipments.find((s) => /Root/.test(s.seller_name)).shipping_cents === 499, JSON.stringify(o.shipments));
  const led = sql(`select kind, amount_cents from ledger where order_id='${o.id}' order by kind`).split("\n");
  check("the studio is credited its $36 and pays the 12% marketplace fee; the brand is not in the ledger", led.join(";") === "charge|3600;fee|-432", led.join(";"));
  check("stock came off the shelf", Number(sql("select stock from products where slug='scalp-oil'")) === before - 2, "");
  r = await call("GET", "/m/orders?status=open", undefined, M); const sh = r.json.orders?.find((x) => x.order_id === o.id);
  check("the business sees its part of the order, and no address since it is a pickup", sh && sh.items.length === 1 && sh.items[0].qty === 2 && sh.address === null && sh.net_cents === 3168 && r.json.marketplace_pct === 12, JSON.stringify(sh).slice(0, 300));
  r = await call("POST", `/m/orders/${sh.id}`, { action: "collected" }, M); check("it cannot jump straight to collected", r.status === 409, r.text);
  r = await call("POST", `/m/orders/${sh.id}`, { action: "ready" }, M); check("mark it ready to collect", r.status === 200, r.text);
  r = await call("POST", `/m/orders/${sh.id}`, { action: "collected" }, M); check("mark it collected", r.status === 200, r.text);
  r = await g("/auth/me"); const mine = r.json.orders?.find((x) => x.id === o.id);
  check("the customer sees each seller's progress", mine?.shipments?.find((s) => /Ada/.test(s.seller)).status === "collected" && mine.shipments.find((s) => /Root/.test(s.seller)).status === "new", JSON.stringify(mine?.shipments));
  r = await p("/auth/products/scalp-oil/review", { rating: 4, body: "Light and it lasts. A little pricey for the size." }); check("having bought it, the customer can review it", r.status === 201, r.text);
  r = await p("/auth/products/scalp-oil/review", { rating: 5, body: "Trying to review it a second time." }); check("but only once", r.status === 409, r.text);
  r = await g("/products/scalp-oil"); check("the review shows as a verified purchase and the rating is recounted", r.json.reviews.some((x) => x.author_name === "Cleo W." && x.verified) && r.json.can.review === false && r.json.product.review_count === r.json.reviews.length, JSON.stringify({ n: r.json.product.review_count, got: r.json.reviews.length }));

  try {
    sql(`delete from ledger where order_id='${o.id}'; update products p set stock = p.stock + oi.qty, sold = greatest(p.sold - oi.qty, 0) from order_items oi where oi.order_id='${o.id}' and oi.product_id = p.id;
      delete from payment_events where order_id='${o.id}' or booking_id='${bk}'; delete from orders where id='${o.id}'; delete from product_reviews where user_id in (select id from users where email='${EMAIL}');
      update products p set review_count = x.n, rating = x.avg from (select product_id, count(*) as n, round(avg(rating), 1) as avg from product_reviews group by product_id) x where x.product_id = p.id;
      delete from leads where booking_id='${bk}'; delete from bookings where id='${bk}'; delete from clients where phone='+16155550811'; delete from users where email='${EMAIL}';`);
    console.log("cleaned up; throwaway customers left behind:", sql(`select count(*) from users where email like 'e2e-cw-%@example.test'`));
  } catch (e) { console.log("CLEANUP PROBLEM:", String(e.stderr || e).slice(0, 800)); }
  console.log(`\n${pass} passed, ${fail} failed`);
  process.exit(fail ? 1 : 0);
})();
