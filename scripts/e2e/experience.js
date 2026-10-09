// End-to-end check of the experience extras: booking for someone else, the
// questions a business asks at booking, repeat appointments, calendar sync and
// a person's own day. It makes one throwaway business and one throwaway
// customer and removes them. Run with:
//   RATE_LIMITS=off STRIPE_SECRET_KEY= PAYSTACK_SECRET_KEY= MAIL_PROVIDER=log docker compose up -d api
//   node scripts/e2e/experience.js
const { execSync } = require("child_process");
const API = process.env.API || "http://localhost:18080/v1";
const stamp = Date.now().toString(36);
const EMAIL = `e2e-xp-${stamp}@example.test`, OWNER = `e2e-xp-owner-${stamp}@example.test`, PASS = "e2e-" + stamp + "-password";
let pass = 0, fail = 0;
const check = (name, ok, extra = "") => { ok ? pass++ : fail++; console.log((ok ? "ok   " : "FAIL ") + name + (ok ? "" : "  " + String(extra).slice(0, 600))); };
const call = async (method, path, body, token) => {
  const r = await fetch(API + path, { method, headers: { "Content-Type": "application/json", ...(token ? { Authorization: "Bearer " + token } : {}) }, body: body === undefined ? undefined : JSON.stringify(body) });
  const text = await r.text(); let json = {}; try { json = JSON.parse(text); } catch {}
  return { status: r.status, json, text };
};
// SQL runs against the database the API uses: DATABASE_URL in .env when set, else the local container.
const DBURL = (() => { try { return (require("fs").readFileSync(".env", "utf8").match(/^DATABASE_URL=(.*)$/m) || [])[1]?.trim().replace(/^["']|["']$/g, "") || ""; } catch { return ""; } })();
const sql = (q) => execSync(DBURL ? `docker exec -i logaluxe-db psql "${DBURL}${DBURL.includes("?") ? "&" : "?"}sslrootcert=system" -At` : "docker exec -i logaluxe-db psql -U logaluxe -d logaluxe -At", { input: q }).toString().trim();

(async () => {
  // A throwaway business, made bookable straight in the database so the test does not depend on the setup screens.
  let r = await call("POST", "/m/signup", { name: "E2E XP Owner", email: OWNER, phone: "+16155550118", password: PASS, business: "E2E XP Studio " + stamp, category: "braids", market: "US", city: "Nashville", region: "TN", address: "1 Test St" });
  check("a business signs up", r.status === 201, r.text); const T = r.json.token;
  const m = (method, path, body) => call(method, "/m" + path, body, T);
  const me = (await m("GET", "/me")).json.merchant, biz = me.business_id, slug = me.slug;
  sql(`update businesses set status='live', verification_status='verified' where id='${biz}';
    update locations set hours = '{"mon":["08:00","20:00"],"tue":["08:00","20:00"],"wed":["08:00","20:00"],"thu":["08:00","20:00"],"fri":["08:00","20:00"],"sat":["08:00","20:00"],"sun":["08:00","20:00"]}' where business_id='${biz}';
    update staff set hours = '{"mon":["08:00","20:00"],"tue":["08:00","20:00"],"wed":["08:00","20:00"],"thu":["08:00","20:00"],"fri":["08:00","20:00"],"sat":["08:00","20:00"],"sun":["08:00","20:00"]}', bookable = true where business_id='${biz}';`);
  r = await m("POST", "/services", { name: "E2E wash", category: "Hair", duration_min: 60, price_cents: 5000, deposit_cents: 0, online: true });
  const svc = r.json.id || r.json.service?.id || sql(`select id from services where business_id='${biz}' limit 1`);
  check("it adds a service", /^[0-9a-f-]{36}$/.test(svc), r.text);
  sql(`insert into staff_services (staff_id, service_id) select st.id, '${svc}' from staff st where st.business_id='${biz}' on conflict do nothing`);
  const staff = sql(`select id from staff where business_id='${biz}' limit 1`);

  // ----- questions at booking -----
  r = await m("POST", "/intake", { label: "Hi", kind: "text" }); check("a question needs real wording", r.status === 400, r.text);
  r = await m("POST", "/intake", { label: "How long is your hair?", kind: "choice", options: ["Short"] }); check("a choice needs at least two options", r.status === 400, r.text);
  r = await m("POST", "/intake", { label: "How long is your hair?", kind: "choice", options: ["Short", "Shoulder", "Long"], required: true, sort: 1 }); const qLen = r.json.id; check("it asks a required choice", r.status === 201, r.text);
  r = await m("POST", "/intake", { label: "Any allergies we should know about?", kind: "text", sort: 2 }); const qAll = r.json.id;
  r = await m("POST", "/intake", { label: "I agree to arrive with clean, dry hair.", kind: "consent", sort: 3, service_id: svc }); const qOk = r.json.id; check("and a box the client must tick, for one service", r.status === 201, r.text);
  r = await call("GET", "/businesses/" + slug); check("the booking page gets the three questions in order", (r.json.intake || []).map((q) => q.kind).join(",") === "choice,text,consent" && r.json.intake[2].required === true, JSON.stringify(r.json.intake));

  // ----- booking for someone else, with answers -----
  r = await call("POST", "/auth/signup", { first_name: "Dami", last_name: "Parent", email: EMAIL, phone: "+16155550871", password: PASS }); const C = r.json.token;
  const slot = (await call("GET", `/businesses/${slug}/openings?services=${svc}&limit=40`)).json.slots?.pop();
  check("the business has free times", !!slot, "no openings");
  const base = { business_slug: slug, staff_id: slot.staff_id, starts_at: slot.starts_at, service_ids: [svc], client_name: "Dami Parent", client_phone: "+16155550871", source: "link", guest_name: "Tola (my daughter)" };
  r = await call("POST", "/bookings", base, C); check("a required question must be answered", r.status === 400 && /please answer: How long/.test(r.text), r.text);
  r = await call("POST", "/bookings", { ...base, answers: [{ question_id: qLen, answer: "Waist" }] }, C); check("a choice must be one of the options", r.status === 400 && /choose one of the options/.test(r.text), r.text);
  r = await call("POST", "/bookings", { ...base, answers: [{ question_id: qLen, answer: "Long" }] }, C); check("the box must be ticked", r.status === 400 && /please tick/.test(r.text), r.text);
  const answers = [{ question_id: qLen, answer: "Long" }, { question_id: qAll, answer: "None" }, { question_id: qOk, answer: "yes" }];
  r = await call("POST", "/bookings", { ...base, answers }, C); const bk = r.json.booking?.id;
  check("she books for her daughter and answers the questions", r.status === 201 && !!bk, r.text);
  r = await m("GET", "/bookings/" + bk);
  check("the business sees who is coming, who booked, and the answers", r.json.booking?.guest_name === "Tola (my daughter)" && r.json.booking.client_name === "Dami Parent" && r.json.answers?.length === 3 && r.json.answers[0].answer === "Long", r.text.slice(0, 300));
  r = await m("PUT", "/intake/" + qLen, { label: "How long is the hair now?", kind: "choice", options: ["Short", "Shoulder", "Long"], required: true, sort: 1 });
  r = await m("GET", "/bookings/" + bk); check("rewording a question later does not rewrite what was asked then", r.json.answers?.[0]?.label === "How long is your hair?", JSON.stringify(r.json.answers));
  r = await m("DELETE", "/intake/" + qAll); check("a question that has answers is switched off, not deleted", r.status === 200 && r.json.switched_off === true, r.text);
  r = await call("GET", "/businesses/" + slug); check("and is no longer asked", (r.json.intake || []).length === 2, JSON.stringify(r.json.intake));

  // ----- repeat appointments -----
  r = await call("POST", `/auth/bookings/${bk}/repeat`, { every_weeks: 0, times: 3 }, C); check("a repeat needs a sensible interval", r.status === 400, r.text);
  // block the date two repeats away, so one of the three cannot be made
  const second = new Date(new Date(slot.starts_at).getTime() + 28 * 864e5);
  sql(`insert into calendar_blocks (business_id, staff_id, starts_at, ends_at, reason, created_by) values ('${biz}', '${staff}', '${new Date(second.getTime() - 3600e3).toISOString()}', '${new Date(second.getTime() + 7200e3).toISOString()}', 'Test block', 'e2e')`);
  r = await call("POST", `/auth/bookings/${bk}/repeat`, { every_weeks: 2, times: 3 }, C);
  check("repeating every two weeks books the free dates and reports the one that is taken", r.status === 201 && r.json.made?.length === 2 && r.json.skipped?.length === 1 && !!r.json.series_id, r.text.slice(0, 500));
  const local = (iso) => new Date(iso).toLocaleTimeString("en-US", { timeZone: "America/Chicago", hour: "2-digit", minute: "2-digit" });
  check("each repeat is at the same clock time", (r.json.made || []).every((x) => local(x.starts_at) === local(slot.starts_at)), JSON.stringify(r.json.made));
  check("the repeats carry the guest and the answers, and belong to one series", sql(`select count(*) from bookings where series_id='${r.json.series_id}' and guest_name='Tola (my daughter)'`) === "3" && sql(`select count(*) from booking_answers ba join bookings b on b.id = ba.booking_id where b.series_id='${r.json.series_id}'`) >= "6", sql(`select count(*) from bookings where series_id='${r.json.series_id}'`));
  r = await call("GET", "/auth/me", undefined, C); check("her account shows the series", (r.json.bookings || []).filter((x) => x.series_id).length === 3 && r.json.bookings.every((x) => x.guest_name === "Tola (my daughter)"), r.text.slice(0, 200));

  // ----- calendar sync -----
  r = await m("GET", "/calendar-sync"); check("calendar sync starts off", r.status === 200 && !r.json.feed_url && !r.json.cal_import_set, r.text);
  r = await m("POST", "/calendar-sync/feed", {}); const feed = r.json.feed_url; check("she gets a private calendar address", /\/v1\/cal\/[0-9a-f]{32,}\.ics$/.test(feed || ""), r.text);
  const path = feed.slice(feed.indexOf("/v1") + 3);
  let f = await fetch(API + path); let ics = await f.text();
  check("the address gives her bookings as a calendar, with the guest's name", f.status === 200 && f.headers.get("content-type").startsWith("text/calendar") && (ics.match(/BEGIN:VEVENT/g) || []).length === 3 && ics.includes("Tola (my daughter) (booked by Dami Parent)") && ics.includes("@logaluxe"), ics.slice(0, 300));
  r = await m("POST", "/calendar-sync/feed", {}); f = await fetch(API + path); check("a new address stops the old one working", f.status === 404 && r.json.feed_url !== feed, f.status + "");
  f = await fetch(API + "/cal/not-a-real-token.ics"); check("a made-up address gives nothing", f.status === 404, f.status + "");
  for (const [bad, why] of [["http://example.com/cal.ics", "not https"], ["https://localhost/cal.ics", "this machine"], ["https://10.0.0.5/cal.ics", "a private network"], ["https://169.254.169.254/latest", "a cloud metadata address"]]) {
    r = await m("PUT", "/calendar-sync/import", { url: bad }); check("an import address that is " + why + " is refused", r.status === 400, r.text);
  }
  r = await m("PUT", "/calendar-sync/import", { url: "https://calendar.google.com/calendar/ical/en.usa%23holiday%40group.v.calendar.google.com/public/basic.ics" });
  if (r.status === 200 && /^Read \d+ busy/.test(r.json.cal_import_note || "")) {
    check("a real public calendar is read (US holidays: all marked free, so nothing is blocked)", r.json.cal_import_set === true && r.json.cal_import_host === "calendar.google.com" && !("cal_import_url" in r.json), r.text);
  } else console.log("     (the public test calendar could not be reached from here, so the live read is skipped: " + (r.json.cal_import_note || r.text).slice(0, 120) + ")");
  // imported busy times block bookings: put one in as the import would, on a free future time
  const free = (await call("GET", `/businesses/${slug}/openings?services=${svc}&limit=40`)).json.slots.pop();
  sql(`insert into calendar_blocks (business_id, staff_id, starts_at, ends_at, reason, created_by, external) values ('${biz}', '${staff}', '${free.starts_at}', '${new Date(new Date(free.starts_at).getTime() + 3600e3).toISOString()}', 'Busy (own calendar)', 'calendar sync', true)`);
  r = await call("POST", "/bookings", { ...base, starts_at: free.starts_at, answers: [{ question_id: qLen, answer: "Long" }, { question_id: qOk, answer: "yes" }] }, C);
  check("a time she is busy in her own calendar cannot be booked", r.status === 409, r.text);
  r = await m("PUT", "/calendar-sync/import", { url: "" }); check("turning the import off removes the busy times it made", r.status === 200 && sql(`select count(*) from calendar_blocks where staff_id='${staff}' and external`) === "0", r.text);

  // ----- a person's own day -----
  const day = new Date(slot.starts_at).toLocaleDateString("en-CA", { timeZone: "America/Chicago" });
  r = await m("GET", "/my-day?date=" + day); const mine = (r.json.bookings || [])[0] || {};
  check("her day lists the booking with the guest, the answers and how often the client has been", r.status === 200 && r.json.date === day && mine.guest_name === "Tola (my daughter)" && mine.answers?.length === 3 && mine.past_visits === 0 && r.json.staff?.id === staff, r.text.slice(0, 400));
  r = await m("GET", "/my-day?date=nonsense"); check("with no date it shows today", r.status === 200 && r.json.date === r.json.today, r.text.slice(0, 120));

  // clean up
  try {
    sql(`delete from bookings where business_id='${biz}'; delete from users where email='${EMAIL}'; delete from businesses where id='${biz}'; delete from merchant_users where email='${OWNER}';`);
    console.log("cleaned up; left behind:", sql(`select (select count(*) from users where email like 'e2e-xp-%') + (select count(*) from businesses where name like 'E2E XP Studio%')`));
  } catch (e) { console.log("clean-up problem:", e.message.slice(0, 400)); }
  console.log(`\n${pass} passed, ${fail} failed`);
  process.exit(fail ? 1 : 0);
})();
