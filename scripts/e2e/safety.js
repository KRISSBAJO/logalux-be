// End-to-end check of the launch safety work: a customer confirms their email,
// a business owner adds two-step sign-in, and one connection cannot hammer the
// sign-in door. It makes one throwaway customer and one throwaway business and
// removes them. Run with mail logged and the limits ON (the default):
//   STRIPE_SECRET_KEY= PAYSTACK_SECRET_KEY= MAIL_PROVIDER=log docker compose up -d api
//   node scripts/e2e/safety.js
const { execSync } = require("child_process");
const crypto = require("crypto");
const fs = require("fs");
const API = process.env.API || "http://localhost:18080/v1";
const stamp = Date.now().toString(36);
const EMAIL = `e2e-safe-${stamp}@example.test`, PASS = "e2e-" + stamp + "-password";
const OWNER = `e2e-safe-owner-${stamp}@example.test`;
const env = fs.readFileSync(".env", "utf8");
const ADMIN_EMAIL = (env.match(/^ADMIN_EMAIL=(.*)$/m) || [])[1]?.trim(), ADMIN_PW = (env.match(/^ADMIN_PASSWORD=(.*)$/m) || [])[1]?.trim();
let pass = 0, fail = 0;
const check = (name, ok, extra = "") => { ok ? pass++ : fail++; console.log((ok ? "ok   " : "FAIL ") + name + (ok ? "" : "  " + String(extra).slice(0, 500))); };
// Each part of the run speaks from its own made-up address (TEST-NET-3), so one part's tries do not count against another's.
const call = async (method, path, body, token, ip = "203.0.113.10") => {
  const r = await fetch(API + path, { method, headers: { "Content-Type": "application/json", "X-Visitor-IP": ip, ...(token ? { Authorization: "Bearer " + token } : {}) }, body: body === undefined ? undefined : JSON.stringify(body) });
  const text = await r.text(); let json = {}; try { json = JSON.parse(text); } catch {}
  return { status: r.status, json, text, headers: r.headers };
};
const sql = (q) => execSync("docker exec -i logaluxe-db psql -U logaluxe -d logaluxe -At", { input: q }).toString().trim();
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
const mailLink = (to, marker) => { const m = execSync("docker logs --since 5m logaluxe-api 2>&1").toString().split("\n").filter((l) => l.includes(to) || l.includes(marker)).join("\n").match(new RegExp(marker.replace("?", "\\?") + "([0-9a-f]+)", "g")); return m ? m[m.length - 1].split("=")[1] : ""; };

// The same six-digit code an authenticator app shows (RFC 6238).
const b32 = (s) => { const A = "ABCDEFGHIJKLMNOPQRSTUVWXYZ234567"; let bits = ""; for (const c of s.replace(/=+$/, "")) bits += A.indexOf(c).toString(2).padStart(5, "0"); const out = []; for (let i = 0; i + 8 <= bits.length; i += 8) out.push(parseInt(bits.slice(i, i + 8), 2)); return Buffer.from(out); };
const totp = (secret, at = Date.now()) => { const msg = Buffer.alloc(8); msg.writeBigUInt64BE(BigInt(Math.floor(at / 30000))); const h = crypto.createHmac("sha1", b32(secret)).update(msg).digest(); const o = h[h.length - 1] & 15; return String((h.readUInt32BE(o) & 0x7fffffff) % 1000000).padStart(6, "0"); };

(async () => {
  // ----- a customer confirms their email -----
  let r = await call("POST", "/auth/signup", { first_name: "Vera", last_name: "Safe", email: EMAIL, phone: "+16155550812", password: PASS });
  check("a customer signs up and starts unconfirmed", r.status === 201 && r.json.user?.email_verified === false, r.text); const C = r.json.token;
  r = await call("POST", "/auth/products/scalp-oil/review", { rating: 5, body: "Trying to review before confirming my email." }, C);
  check("an unconfirmed customer cannot post a review", r.status === 403 && r.json.need_verify === true, r.text);
  r = await call("POST", "/auth/verify/send", {}, C); check("asking for a second email straight away is refused", r.status === 429, r.text);
  await sleep(800);
  const link = mailLink(EMAIL, "/verify?token=");
  check("the sign-up email carries a confirmation link", link.length >= 32, "no link in the API log; is MAIL_PROVIDER=log?");
  r = await call("POST", "/auth/verify", { token: "0".repeat(64) }); check("a made-up link is refused", r.status === 400, r.text);
  r = await call("POST", "/auth/verify", { token: link }); check("the real link confirms the email", r.status === 200 && r.json.email === EMAIL, r.text);
  r = await call("GET", "/auth/me?brief=1", undefined, C); check("the account now shows as confirmed", r.json.user?.email_verified === true, r.text);
  r = await call("POST", "/auth/verify", { token: link }); check("opening the link again is harmless", r.status === 200 && r.json.already === true, r.text);
  r = await call("POST", "/auth/products/scalp-oil/review", { rating: 5, body: "Now confirmed, but I have not bought it." }, C);
  check("once confirmed, the usual review rules apply", r.status !== 403 || r.json.need_verify !== true, r.text);

  // ----- a business owner adds two-step sign-in -----
  r = await call("POST", "/m/signup", { name: "E2E Safe Owner", email: OWNER, phone: "+16155550113", password: PASS, business: "E2E Safe Studio " + stamp, category: "braids", market: "US", city: "Nashville", region: "TN", address: "1 Test St" }, undefined, "203.0.113.11");
  check("a business signs up", r.status === 201 && r.json.token, r.text); const T = r.json.token;
  const m = (method, path, body, tok = T) => call(method, "/m" + path, body, tok, "203.0.113.11");
  r = await m("GET", "/security"); check("two-step sign-in starts off", r.status === 200 && r.json.two_step === false, r.text);
  r = await m("POST", "/2fa/setup", {}); const secret = r.json.secret;
  check("setup gives a key for the authenticator app", r.status === 200 && /^[A-Z2-7]{32}$/.test(secret || "") && String(r.json.uri).startsWith("otpauth://totp/"), r.text);
  r = await m("POST", "/2fa/enable", { code: "000000" }); check("a wrong code does not turn it on", r.status === 400, r.text);
  r = await m("POST", "/2fa/enable", { code: totp(secret) }); const codes = r.json.recovery_codes || [];
  check("the right code turns it on and gives eight recovery codes", r.status === 200 && codes.length === 8, r.text);
  r = await m("GET", "/me"); check("the session that set it up stays signed in", r.status === 200, r.text);
  const login = (body) => call("POST", "/m/login", { email: OWNER, password: PASS, ...body }, undefined, "203.0.113.11");
  r = await login({}); check("the password alone no longer signs in", r.status === 401 && r.json.need_code === true && !r.json.token, r.text);
  r = await login({ code: "123456" }); check("a wrong code is refused", r.status === 401 && r.json.need_code === true, r.text);
  r = await login({ code: totp(secret) }); check("password and code sign in", r.status === 200 && !!r.json.token, r.text);
  r = await login({ code: codes[0] }); check("a recovery code signs in", r.status === 200 && !!r.json.token, r.text);
  r = await login({ code: codes[0] }); check("the same recovery code does not work twice", r.status === 401, r.text);
  r = await m("GET", "/security"); check("seven recovery codes are left", r.json.recovery_left === 7, r.text);
  r = await m("POST", "/2fa/disable", { password: "not-the-password" }); check("turning it off needs the password", r.status === 403, r.text);
  if (ADMIN_EMAIL && ADMIN_PW) {
    const a = await call("POST", "/admin/login", { email: ADMIN_EMAIL, password: ADMIN_PW }, undefined, "203.0.113.12");
    if (a.json.token) {
      r = await call("POST", "/admin/merchants/reset-2fa", { email: OWNER }, a.json.token, "203.0.113.12"); check("a super admin can reset it for someone who lost their phone", r.status === 200, r.text);
      r = await m("GET", "/me"); check("which signs that person out everywhere", r.status === 401, r.text);
      r = await login({}); check("and the password alone signs in again", r.status === 200 && !!r.json.token, r.text);
    } else console.log("     (admin sign-in needs a code here, so the reset check is skipped)");
  }

  // ----- one connection cannot hammer the sign-in door -----
  const burst = "203.0.113.50"; let last;
  for (let i = 0; i < 21; i++) last = await call("POST", "/auth/login", { email: `nobody-${i}@example.test`, password: "wrong-password" }, undefined, burst);
  check("the 21st sign-in try in ten minutes from one address is turned away", last.status === 429 && Number(last.headers.get("retry-after")) > 0, last.status + " " + last.text);
  r = await call("POST", "/auth/login", { email: EMAIL, password: PASS }, undefined, "203.0.113.51"); check("someone at another address is not affected", r.status === 200, r.text);
  for (let i = 0; i < 9; i++) last = await call("POST", "/auth/login", { email: EMAIL, password: "wrong-" + i }, undefined, "203.0.113." + (60 + i));
  check("eight wrong passwords lock the account itself, whatever address they come from", last.status === 429, last.status + " " + last.text);

  // clean up
  try {
    sql(`delete from product_reviews where user_id in (select id from users where email='${EMAIL}');
      delete from users where email='${EMAIL}';
      delete from businesses where name = 'E2E Safe Studio ${stamp}';
      delete from merchant_users where email='${OWNER}';`);
    console.log("cleaned up; left behind:", sql(`select (select count(*) from users where email like 'e2e-safe-%') + (select count(*) from businesses where name like 'E2E Safe Studio%')`));
  } catch (e) { console.log("clean-up problem:", e.message.slice(0, 300)); }
  console.log(`\n${pass} passed, ${fail} failed`);
  process.exit(fail ? 1 : 0);
})();
