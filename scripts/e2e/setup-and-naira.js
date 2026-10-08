// End-to-end check of two things: getting a new business ready (the setup
// steps and the identity papers), and the shop in naira. It makes one
// throwaway business in Lagos and one throwaway customer and removes them.
//   RATE_LIMITS=off STRIPE_SECRET_KEY= PAYSTACK_SECRET_KEY= MAIL_PROVIDER=log docker compose up -d api
//   node scripts/e2e/setup-and-naira.js
const { execSync } = require("child_process");
const fs = require("fs");
const API = process.env.API || "http://localhost:18080/v1";
const stamp = Date.now().toString(36);
const EMAIL = `e2e-ng-${stamp}@example.test`, OWNER = `e2e-ng-owner-${stamp}@example.test`, PASS = "e2e-" + stamp + "-password";
const env = fs.readFileSync(".env", "utf8");
const ADMIN_EMAIL = (env.match(/^ADMIN_EMAIL=(.*)$/m) || [])[1]?.trim(), ADMIN_PW = (env.match(/^ADMIN_PASSWORD=(.*)$/m) || [])[1]?.trim();
let pass = 0, fail = 0;
const check = (name, ok, extra = "") => { ok ? pass++ : fail++; console.log((ok ? "ok   " : "FAIL ") + name + (ok ? "" : "  " + String(extra).slice(0, 500))); };
const call = async (method, path, body, token) => {
  const r = await fetch(API + path, { method, headers: { "Content-Type": "application/json", ...(token ? { Authorization: "Bearer " + token } : {}) }, body: body === undefined ? undefined : JSON.stringify(body) });
  const text = await r.text(); let json = {}; try { json = JSON.parse(text); } catch {}
  return { status: r.status, json, text };
};
const upload = async (path, fields, bytes, type, name, token) => {
  const fd = new FormData(); for (const [k, v] of Object.entries(fields)) fd.set(k, v);
  fd.set("file", new Blob([bytes], { type }), name);
  const r = await fetch(API + path, { method: "POST", headers: { Authorization: "Bearer " + token }, body: fd });
  const text = await r.text(); let json = {}; try { json = JSON.parse(text); } catch {}
  return { status: r.status, json, text };
};
const sql = (q) => execSync("docker exec -i logaluxe-db psql -U logaluxe -d logaluxe -At", { input: q }).toString().trim();
// The smallest valid PNG: one transparent pixel.
const PNG = Buffer.from("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNkYPhfDwAChwGA60e6kgAAAABJRU5ErkJggg==", "base64");

(async () => {
  // ----- getting ready -----
  let r = await call("POST", "/m/signup", { name: "E2E Lagos Owner", email: OWNER, phone: "+2348035550117", password: PASS, business: "E2E Lagos Shop " + stamp, category: "spa", market: "NG", city: "Lagos", region: "Lagos", address: "1 Test Close, Lekki" });
  check("a Lagos business signs up", r.status === 201, r.text); const T = r.json.token;
  const m = (method, path, body) => call(method, "/m" + path, body, T);
  const me = (await m("GET", "/me")).json.merchant, biz = me.business_id;
  r = await m("GET", "/onboarding"); const steps0 = Object.fromEntries((r.json.steps || []).map((s) => [s.key, s.done]));
  check("its setup list has seven steps and is not live yet", r.status === 200 && r.json.total === 7 && r.json.live === false && steps0.verify === false && steps0.services === false && steps0.photos === false, r.text.slice(0, 400));
  check("the steps already true at sign-up are ticked, the rest are not invented", typeof steps0.address === "boolean" && steps0.payout === false && r.json.done === Object.values(steps0).filter(Boolean).length, JSON.stringify(steps0));
  r = await m("POST", "/verification/submit", { id_type: "Passport", note: "" }); check("papers cannot be sent before an ID is uploaded", r.status === 409, r.text);
  r = await upload("/m/verification/documents", { kind: "id" }, Buffer.from("this is not an image"), "text/plain", "id.txt", T); check("a file that is not a photo or a PDF is refused, whatever it is called", r.status === 415, r.text);
  r = await upload("/m/verification/documents", { kind: "selfie" }, PNG, "image/png", "id.png", T); check("the kind of document must be one we know", r.status === 400, r.text);
  r = await upload("/m/verification/documents", { kind: "id" }, PNG, "image/png", "my passport.png", T);
  const stored = r.status === 201; const docId = r.json.id;
  if (r.status === 503) console.log("     (document storage is not configured here, so the upload checks are skipped)");
  else check("a photo of the ID is uploaded", stored, r.text);
  if (stored) {
    r = await m("POST", "/verification/submit", { id_type: "A note from my mum", note: "" }); check("the kind of ID must be one we know", r.status === 400, r.text);
    r = await m("POST", "/verification/submit", { id_type: "Passport", note: "Renewed last year." }); check("the papers are sent for checking", r.status === 200, r.text);
    r = await m("GET", "/onboarding"); check("the setup list now ticks that step and shows the document", (r.json.steps || []).find((s) => s.key === "verify")?.done === true && r.json.verification?.documents?.length === 1 && !!r.json.verification.submitted_at, r.text.slice(0, 300));
    r = await call("DELETE", "/m/verification/documents/" + docId, undefined, T); check("a document that is being checked cannot be removed", r.status === 409, r.text);
    r = await call("GET", "/admin/verification-documents/" + docId); check("nobody outside LogaLuxe staff can read the document", r.status === 401 || r.status === 403, r.status + " " + r.text.slice(0, 80));
    r = await call("GET", "/admin/verification-documents/" + docId, undefined, T); check("not even the business's own sign-in, through the staff door", r.status === 401 || r.status === 403, r.status + "");
    if (ADMIN_EMAIL && ADMIN_PW) {
      const a = (await call("POST", "/admin/login", { email: ADMIN_EMAIL, password: ADMIN_PW })).json.token;
      if (a) {
        r = await call("GET", "/admin/verification?status=pending", undefined, a); const vr = (r.json.requests || []).find((x) => x.business_id === biz);
        check("staff see the request with the kind of ID", vr?.id_type === "Passport", r.text.slice(0, 200));
        r = await call("GET", `/admin/verification/${vr.id}/documents`, undefined, a); check("and its documents", r.json.documents?.length === 1 && r.json.documents[0].kind === "id", r.text);
        const f = await fetch(API + "/admin/verification-documents/" + docId, { headers: { Authorization: "Bearer " + a } }); const got = Buffer.from(await f.arrayBuffer());
        check("staff can open the file, exactly as it was uploaded", f.status === 200 && got.equals(PNG) && f.headers.get("cache-control").includes("no-store"), f.status + " " + got.length);
        check("and the look is written to the audit log", sql(`select count(*) from audit_log where action='verification.document_viewed' and created_at > now() - interval '1 minute'`) !== "0");
        r = await call("POST", `/admin/verification/${vr.id}/decide`, { decision: "approve", note: "ID matches." }, a); check("staff approve", r.status === 200, r.text);
        r = await m("GET", "/onboarding"); check("the business is now verified and live", r.json.live === true && r.json.verification?.status === "verified", r.text.slice(0, 200));
      }
    }
  }
  r = await m("POST", "/onboarding/dismiss", {}); r = await m("GET", "/onboarding"); check("the owner can put the setup list away", r.json.dismissed === true, r.text.slice(0, 120));

  // ----- the shop in naira -----
  sql(`update businesses set status='live', verification_status='verified', sales_tax_bp=750 where id='${biz}'`);
  r = await m("POST", "/products", { name: "E2E naira balm " + stamp, kind: "retail", category: "skin", price_cents: 500000, stock: 4, online: true });
  const pid = r.json.id || r.json.product?.id; sql(`update products set active = true, pickup = true, shipping = true, shipping_cents = 150000 where id='${pid}'`);
  const pslug = sql(`select slug from products where id='${pid}'`);
  r = await call("GET", "/products?per=60"); check("the dollar shop does not show naira products", r.json.currency === "USD" && !(r.json.products || []).some((p) => p.slug === pslug) && r.json.products.every((p) => p.currency === "USD"), r.text.slice(0, 200));
  r = await call("GET", "/products?per=60&currency=NGN"); check("the naira shop shows them, and only them", r.json.currency === "NGN" && r.json.products.some((p) => p.slug === pslug) && r.json.products.every((p) => p.currency === "NGN"), r.text.slice(0, 200));
  check("its filters count naira products only", (r.json.sellers || []).every((s) => s.seller_name !== "LogaLuxe") && (r.json.sellers || []).some((s) => s.business_slug === me.slug), JSON.stringify(r.json.sellers).slice(0, 200));
  r = await call("GET", "/products/" + pslug); check("a product says its currency", r.json.product?.currency === "NGN", r.text.slice(0, 200));
  r = await call("POST", "/auth/signup", { first_name: "Ngozi", last_name: "Naira", email: EMAIL, phone: "+2348035550861", password: PASS }); const C = r.json.token;
  const order = { customer_name: "Ngozi Naira", customer_phone: "+2348035550861", customer_email: EMAIL, fulfilment: "pickup", items: [{ product_slug: pslug, size_label: "", qty: 1 }] };
  r = await call("POST", "/orders/quote", order, C);
  check("a naira order is quoted in naira with the business's 7.5% tax", r.json.quote?.currency === "NGN" && r.json.quote.tax_cents === 37500 && r.json.quote.total_cents === 537500, r.text);
  r = await call("POST", "/orders/quote", { ...order, items: [...order.items, { product_slug: "scalp-oil", size_label: "60 ml", qty: 1 }] }, C);
  check("dollar and naira items cannot share an order", r.status === 400 && /one order for each/.test(r.text), r.text);
  r = await call("POST", "/orders/quote", { ...order, gift_code: "LX-AAAA-BBBB-CCCC" }, C); check("a dollar gift card cannot pay for a naira order", r.status === 400 && /US dollars/.test(r.text), r.text);
  sql(`insert into user_credits (user_id, amount_cents, reason) select id, 2500, 'Test credit' from users where email='${EMAIL}'`);
  r = await call("POST", "/orders/quote", order, C); check("nor is dollar store credit spent on it", r.json.quote?.credit_cents === 0 && r.json.quote.total_cents === 537500, r.text);
  r = await call("POST", "/orders", order, C); const o = r.json.order || {};
  check("she places the order in naira", r.status === 201 && o.currency === "NGN" && o.total_cents === 537500 && o.tax_cents === 37500, r.text.slice(0, 300));
  check("the business is credited in naira, less the marketplace fee", sql(`select string_agg(distinct currency, ',') from ledger where order_id='${o.id}'`) === "NGN" && Number(sql(`select sum(amount_cents) from ledger where order_id='${o.id}'`)) > 0);
  r = await call("GET", "/auth/me", undefined, C); check("her account lists the order with its currency", (r.json.orders || []).find((x) => x.id === o.id)?.currency === "NGN", r.text.slice(0, 200));

  // clean up
  try {
    const keys = sql(`select storage_key from verification_documents where business_id='${biz}'`);
    sql(`delete from ledger where business_id='${biz}';
      delete from payment_events where order_id in (select id from orders where customer_email='${EMAIL}');
      delete from orders where customer_email='${EMAIL}';
      delete from users where email='${EMAIL}';
      delete from products where business_id='${biz}';
      delete from businesses where id='${biz}';
      delete from merchant_users where email='${OWNER}';`);
    console.log("cleaned up; left behind:", sql(`select (select count(*) from users where email like 'e2e-ng-%') + (select count(*) from businesses where name like 'E2E Lagos Shop%')`), keys ? "(one test file stays in storage: " + keys + ")" : "");
  } catch (e) { console.log("clean-up problem:", e.message.slice(0, 400)); }
  console.log(`\n${pass} passed, ${fail} failed`);
  process.exit(fail ? 1 : 0);
})();
