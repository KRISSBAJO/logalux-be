// Starts a real test-mode deposit payment, to be paid by hand on the provider's
// page with a test card. It books a far-off slot for a client named
// "Test Pay <stamp>" at a sample business and prints where to pay.
//   node scripts/e2e/pay-start.js ada        (Stripe, United States)
//   node scripts/e2e/pay-start.js mnm        (Paystack, Nigeria)
// Then: node scripts/e2e/pay-check.js <reference>
const API = process.env.API || "http://localhost:18080/v1";
const slug = process.argv[2] || "ada";
const j = async (path, init) => { const r = await fetch(API + path, init); return { status: r.status, json: await r.json().catch(() => ({})) }; };
(async () => {
  const biz = (await j(`/businesses/${slug}`)).json;
  const svc = (biz.services || []).filter((s) => s.deposit_cents > 0).sort((a, b) => a.duration_min - b.duration_min)[0];
  if (!svc) { console.log("no service with a deposit at", slug); process.exit(1); }
  let slot;
  for (let i = 20; i < 40 && !slot; i++) {
    const day = new Date(Date.now() + i * 864e5).toISOString().slice(0, 10);
    slot = ((await j(`/businesses/${slug}/availability?date=${day}&services=${svc.id}&staff=any`)).json.slots || [])[0];
  }
  if (!slot) { console.log("no free time found"); process.exit(1); }
  const stamp = Date.now().toString(36);
  const r = await j("/bookings", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ business_slug: slug, staff_id: slot.staff_id, starts_at: slot.starts_at, service_ids: [svc.id], client_name: "Test Pay " + stamp, client_phone: "", client_email: "test-pay@example.test", source: "link" }) });
  if (r.status !== 201) { console.log("booking failed:", r.status, JSON.stringify(r.json)); process.exit(1); }
  const b = r.json.booking;
  console.log(JSON.stringify({ booking: b.id, service: svc.name, starts_at: b.starts_at, deposit_cents: b.deposit_cents, deposit_paid: b.deposit_paid, payment: b.payment }, null, 1));
})();
