// End-to-end check of the features an admin switches on and off: sign-in with a texted code,
// confirming a number, the message channels, and saved cards being hidden until switched on.
// Codes go to numbers in the 555 range, which are never texted: the code is read from the API log.
// It puts every switch back the way it found it and removes the accounts it made. Run with:
//   RATE_LIMITS=off STRIPE_SECRET_KEY= PAYSTACK_SECRET_KEY= MAIL_PROVIDER=log docker compose up -d api
//   node scripts/e2e/features.js
const { execSync } = require("child_process");
const fs = require("fs");
const API = process.env.API || "http://localhost:18080/v1";
const stamp = Date.now().toString(36);
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
// The last code the API "sent" to a number, read from its log (the number is never really texted).
const codeFor = async (phone) => {
  await sleep(400);
  const log = execSync("docker logs --since 60s logaluxe-api 2>&1").toString().split("\n").filter((l) => l.includes(phone) && l.includes("Your LogaLuxe code is"));
  return ((log[log.length - 1] || "").match(/code is (\d{6})/) || [])[1] || "";
};
const rnd = () => String(Math.floor(1000 + Math.random() * 9000));
const NEW = "+1615555" + rnd(), OLD = "+1615555" + rnd(), EMAIL = `e2e-f-${stamp}@example.test`, PASS = "e2e-" + stamp + "-password";

(async () => {
  if (!ADMIN_EMAIL || !ADMIN_PW) { console.log("ADMIN_EMAIL and ADMIN_PASSWORD are needed in .env"); process.exit(1); }
  const admin = (await call("POST", "/admin/login", { email: ADMIN_EMAIL, password: ADMIN_PW })).json.token;
  let r = await call("GET", "/admin/features", undefined, admin);
  check("the console lists the five features", r.status === 200 && r.json.features.length === 5 && r.json.features.every((f) => typeof f.on === "boolean" && Array.isArray(f.missing)), r.text);
  const before = Object.fromEntries(r.json.features.map((f) => [f.key, f.on]));
  const login = r.json.features.find((f) => f.key === "sms_login");
  const set = (key, on) => call("PUT", `/admin/features/${key}`, { on }, admin);
  try {
    // ----- off: nobody is offered anything -----
    await set("sms_login", false); await set("saved_cards", false);
    r = await call("GET", "/features"); check("the public list says sign-in by code is off", r.status === 200 && r.json.features.sms_login === false, r.text);
    r = await call("POST", "/auth/code/send", { phone: NEW }); check("a code cannot be asked for while it is off", r.status === 404, r.text);
    r = await call("PUT", "/admin/features/nonsense", { on: true }, admin); check("an unknown feature is refused", r.status === 400, r.text);
    r = await call("PUT", "/admin/features/sms_login", { on: true }); check("only a signed-in admin can switch a feature", r.status === 401 || r.status === 403, r.text);

    // ----- on -----
    r = await set("sms_login", true); check("an admin switches sign-in by code on", r.status === 200 && r.json.on === true, r.text);
    if (!login.ready) { console.log("     (the text provider keys are not set, so the rest of sign-in by code is skipped)"); }
    else {
      r = await call("GET", "/features"); check("the public list now says it is on", r.json.features.sms_login === true, r.text);
      r = await call("POST", "/auth/code/send", { phone: "6155550100" }); check("a number without a country code is refused", r.status === 400, r.text);
      r = await call("POST", "/auth/code/send", { phone: NEW }); check("a code is made for a new number, and not really texted", r.status === 200 && r.json.sent === "logged", r.text);
      let code = await codeFor(NEW); check("the code is six digits", /^\d{6}$/.test(code), code);
      r = await call("POST", "/auth/code/verify", { phone: NEW, code: code === "000000" ? "111111" : "000000" }); check("a wrong code is refused", r.status === 401, r.text);
      r = await call("POST", "/auth/code/verify", { phone: NEW, code }); check("a new number is asked for a name", r.status === 409 && r.json.need === "name", r.text);
      r = await call("POST", "/auth/code/verify", { phone: NEW, code, first_name: "E2E", last_name: "Coder" }); check("with a name the account is made and signed in", r.status === 200 && r.json.token && r.json.new === true && r.json.user.phone_verified === true, r.text);
      const T1 = r.json.token;
      r = await call("POST", "/auth/code/verify", { phone: NEW, code, first_name: "E2E" }); check("the code cannot be used twice", r.status === 401, r.text);
      r = await call("GET", "/auth/me?brief=1", undefined, T1); check("the new account is signed in with a confirmed number", r.status === 200 && r.json.user.phone === NEW && r.json.user.phone_verified === true, r.text);
      r = await call("POST", "/auth/code/send", { phone: NEW }); code = await codeFor(NEW);
      r = await call("POST", "/auth/code/verify", { phone: NEW, code }); check("the same number signs in again without a name", r.status === 200 && r.json.new === false && r.json.user.first_name === "E2E", r.text);

      // An account made with a phone alone adds an email and a password, so it can always sign in.
      const EMAIL2 = `e2e-f2-${stamp}@example.test`, PASS2 = "e2e-" + stamp + "-second";
      r = await call("POST", "/auth/password", { new: PASS2 }, T1); check("a password needs an email first", r.status === 400, r.text);
      r = await call("PUT", "/auth/me", { first_name: "E2E", last_name: "Coder", phone: NEW, email: EMAIL2 }, T1); check("the account adds an email", r.status === 200, r.text);
      r = await call("GET", "/auth/me?brief=1", undefined, T1); check("the email is on the account, unconfirmed, with no password yet", r.json.user.email === EMAIL2 && r.json.user.email_verified === false && r.json.user.has_password === false, r.text);
      r = await call("PUT", "/auth/me", { first_name: "E2E", last_name: "Coder", phone: NEW, email: "other-" + EMAIL2 }, T1);
      r = await call("GET", "/auth/me?brief=1", undefined, T1); check("the email cannot be swapped the same way", r.json.user.email === EMAIL2, r.text);
      r = await call("POST", "/auth/password", { new: PASS2 }, T1); check("a first password is set without a current one", r.status === 200, r.text);
      r = await call("POST", "/auth/password", { new: PASS2 + "x" }, T1); check("after that the current password is asked for", r.status === 403, r.text);
      r = await call("POST", "/auth/login", { email: EMAIL2, password: PASS2 }); check("the account now signs in with email and password too", r.status === 200 && r.json.user.has_password === true && r.json.user.phone === NEW, r.text);

      // An account made with a password, whose number was never confirmed.
      r = await call("POST", "/auth/signup", { first_name: "E2E", last_name: "Typed", email: EMAIL, phone: OLD, password: PASS }); const T2 = r.json.token;
      check("a password account is made with a number it has not confirmed", r.status === 201 && r.json.user.phone === OLD, r.text);
      r = await call("POST", "/auth/code/send", { phone: OLD }); code = await codeFor(OLD);
      r = await call("POST", "/auth/code/verify", { phone: OLD, code }); check("a code alone does not open an account that never confirmed the number", r.status === 409 && r.json.need === "password", r.text);
      r = await call("POST", "/auth/phone/send", {}, T2); check("the signed-in person asks for a code to confirm the number", r.status === 200, r.text);
      code = await codeFor(OLD);
      r = await call("POST", "/auth/phone/verify", { code: "12" }, T2); check("a wrong confirming code is refused", r.status === 401, r.text);
      r = await call("POST", "/auth/phone/verify", { code }, T2); check("the right one confirms the number", r.status === 200, r.text);
      await sql(`delete from login_codes where phone='${OLD}'`); // the test asked for several codes in a row
      r = await call("POST", "/auth/code/send", { phone: OLD }); code = await codeFor(OLD);
      r = await call("POST", "/auth/code/verify", { phone: OLD, code }); check("now the number signs that account in", r.status === 200 && r.json.user.email === EMAIL, r.text);
      r = await call("PUT", "/auth/me", { first_name: "E2E", last_name: "Typed", phone: "+16155550199" }, T2);
      r = await call("GET", "/auth/me?brief=1", undefined, T2); check("changing the number takes the confirmation away", r.json.user.phone_verified === false, r.text);
      await sql(`delete from login_codes where phone='${NEW}'`);
      for (let i = 0; i < 3; i++) await call("POST", "/auth/code/send", { phone: NEW });
      r = await call("POST", "/auth/code/send", { phone: NEW }); check("a fourth code in ten minutes is refused", r.status === 429, r.text);

      r = await call("PUT", "/auth/channel", { channel: "pigeon" }, T2); check("an unknown channel is refused", r.status === 400, r.text);
      r = await call("PUT", "/auth/channel", { channel: "sms" }, T2);
      r = await call("GET", "/auth/me?brief=1", undefined, T2); check("the person chooses how to hear about bookings", r.json.user.preferred_channel === "sms", r.text);

      // ----- saved cards stay hidden until switched on -----
      r = await call("GET", "/auth/cards", undefined, T2); check("no cards are offered while saved cards are off", r.status === 200 && r.json.enabled === false && r.json.cards.length === 0, r.text);
      r = await call("DELETE", "/auth/cards/pm_not_mine", undefined, T2); check("a card that is not theirs cannot be removed", r.status === 404, r.text);
    }
    r = await set("sms_login", false);
    r = await call("POST", "/auth/code/send", { phone: NEW }); check("switched off again, codes stop at once", r.status === 404, r.text);
    r = sql(`select count(*) from audit_log where action='feature.set' and target='sms_login' and created_at > now() - interval '2 minutes'`); check("each switch is written to the audit log", Number(r) >= 2, r);
  } finally {
    for (const [key, on] of Object.entries(before)) await set(key, on);
    sql(`delete from login_codes where phone in ('${NEW}','${OLD}'); delete from users where phone in ('${NEW}','${OLD}','+16155550199') and (first_name='E2E'); delete from users where email='${EMAIL}';`);
  }
  console.log(`\n${pass} passed, ${fail} failed`);
  process.exit(fail ? 1 : 0);
})();
