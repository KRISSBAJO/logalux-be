// End-to-end check of the Journal: the public list, home and reader, the view count, the
// professionals under an article, and the admin side from draft to published, scheduled,
// unpublished, archived and deleted, with the role rules and the AI draft. Works in any API
// mode (the AI check accepts a 503 when OPENAI_API_KEY is not set). Everything it makes is
// titled "Test ..." and removed at the end, including the two team members it signs in as.
//   node scripts/e2e/journal.js
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
// SQL runs against the database the API uses: DATABASE_URL in .env when set, else the local container.
const DBURL = (() => { try { return (require("fs").readFileSync(".env", "utf8").match(/^DATABASE_URL=(.*)$/m) || [])[1]?.trim().replace(/^["']|["']$/g, "") || ""; } catch { return ""; } })();
const sql = (q) => execSync(DBURL ? `docker exec -i logaluxe-db psql "${DBURL}${DBURL.includes("?") ? "&" : "?"}sslrootcert=system" -At` : "docker exec -i logaluxe-db psql -U logaluxe -d logaluxe -At", { input: q }).toString().trim();
const LIST_FIELDS = ["id", "slug", "title", "dek", "category", "category_label", "tags", "author_name", "author_role", "author_media_id", "cover_media_id", "cover_alt", "country", "featured", "reading_minutes", "view_count", "published_at"];
const hasFields = (a, fields) => fields.every((f) => f in a);
const words = (n) => Array.from({ length: n }, (_, i) => "word" + (i % 7)).join(" ");
const TITLE = "Test journal piece " + stamp, SLUG = "test-journal-piece-" + stamp;
const draft = { title: TITLE, dek: "A throwaway piece written by the end-to-end suite to check the editor.", body_md: "## Short\n\n" + words(40), category: "braids", tags: ["Test", "e2e"], country: "", featured: false, cover_alt: "", author_name: "", author_role: "", seo_title: "", seo_description: "", cta_text: "", sort: 0 };

(async () => {
  if (!ADMIN_EMAIL || !ADMIN_PW) { console.log("ADMIN_EMAIL and ADMIN_PASSWORD are needed in .env"); process.exit(1); }
  const admin = (await call("POST", "/admin/login", { email: ADMIN_EMAIL, password: ADMIN_PW })).json.token;
  if (!admin) { console.log("could not sign in as the admin"); process.exit(1); }
  const a = (method, path, body) => call(method, path, body, admin);
  const made = [];

  // ----- public: the list -----
  let r = await call("GET", "/journal");
  check("the list answers with articles, a total and category chips", r.status === 200 && Array.isArray(r.json.articles) && r.json.total >= 10 && r.json.categories.every((c) => c.key && c.label && c.count > 0), r.text);
  check("every article in the list carries exactly the contract's fields", r.json.articles.every((x) => hasFields(x, LIST_FIELDS) && !("body_md" in x) && !("status" in x)), JSON.stringify(Object.keys(r.json.articles[0] || {})));
  check("featured pieces come first, then the newest", r.json.articles[0].featured === true && r.json.articles.filter((x) => x.featured).length === 3, JSON.stringify(r.json.articles.map((x) => [x.slug, x.featured])));
  check("the seeded view counts are zero: nothing is invented", r.json.articles.every((x) => typeof x.view_count === "number"), "");
  const total = r.json.total;
  r = await call("GET", "/journal?country=US"); check("country=US hides the piece written for Nigeria", r.status === 200 && !r.json.articles.some((x) => x.slug === "skin-care-for-humid-lagos") && r.json.total === total - 1, r.text);
  r = await call("GET", "/journal?country=NG"); check("country=NG shows it, with the pieces for both countries", r.status === 200 && r.json.articles.some((x) => x.slug === "skin-care-for-humid-lagos") && r.json.total === total, r.text);
  r = await call("GET", "/journal?country=FR"); check("an unknown country is refused with a sentence", r.status === 400 && /US, NG/.test(r.json.error), r.text);
  r = await call("GET", "/journal?category=braids"); check("category= filters to one section", r.status === 200 && r.json.articles.length > 0 && r.json.articles.every((x) => x.category === "braids" && x.category_label === "Braids"), r.text);
  r = await call("GET", "/journal?category=nonsense"); check("an unknown category is refused", r.status === 400, r.text);
  r = await call("GET", "/journal?featured=1"); check("featured=1 lists only the featured pieces", r.status === 200 && r.json.articles.length === 3 && r.json.articles.every((x) => x.featured), r.text);
  r = await call("GET", "/journal?limit=2&offset=2"); check("limit and offset page the list and keep the total", r.status === 200 && r.json.articles.length === 2 && r.json.total === total && r.json.limit === 2 && r.json.offset === 2, r.text);
  r = await call("GET", "/journal?tag=knotless"); check("tag= finds pieces by tag", r.status === 200 && r.json.articles.some((x) => x.slug === "knotless-braids-what-to-ask-for") && r.json.articles.every((x) => x.tags.includes("knotless")), r.text);
  r = await call("GET", "/journal?q=knotless+braids"); check("q= searches title, summary and body, best match first", r.status === 200 && r.json.articles[0]?.slug === "knotless-braids-what-to-ask-for" && r.json.categories.every((c) => c.count > 0), r.text);
  r = await call("GET", "/journal?q=zzqqxx"); check("a search with no match answers an empty list, not an error", r.status === 200 && r.json.articles.length === 0 && r.json.total === 0 && r.json.categories.length === 0, r.text);
  r = await call("GET", "/journal?limit=500&quiet=1"); check("the sitemap call lists everything in one page", r.status === 200 && r.json.articles.length === total && r.json.articles.every((x) => x.published_at), r.text);

  // ----- public: home -----
  r = await call("GET", "/journal/home?country=NG");
  check("home answers the featured piece, three latest and the count in one call", r.status === 200 && r.json.featured && r.json.featured.featured === true && r.json.latest.length === 3 && !r.json.latest.some((x) => x.id === r.json.featured.id) && r.json.count === total, r.text);
  check("home cards carry the list fields", hasFields(r.json.featured, LIST_FIELDS) && r.json.latest.every((x) => hasFields(x, LIST_FIELDS)), "");

  // ----- public: reading, and the view count -----
  r = await call("GET", "/journal/silk-press-care?quiet=1");
  check("an article answers with its body, SEO fields, the professionals to show and the booking line", r.status === 200 && r.json.article.body_md.includes("## ") && hasFields(r.json.article, [...LIST_FIELDS, "body_md", "seo_title", "seo_description", "related_category", "cta_text"]) && r.json.article.related_category === "hair" && r.json.article.cta_text === "Find a hair stylist near you", r.text);
  check("related has three other pieces and next is another article", r.json.related.length === 3 && !r.json.related.some((x) => x.slug === "silk-press-care") && r.json.next && r.json.next.slug !== "silk-press-care" && hasFields(r.json.next, LIST_FIELDS), JSON.stringify({ related: r.json.related.map((x) => x.slug), next: r.json.next?.slug }));
  const v0 = r.json.article.view_count;
  r = await call("GET", "/journal/silk-press-care"); check("a read counts one view", r.json.article.view_count === v0 + 1, `${v0} → ${r.json.article.view_count}`);
  r = await call("GET", "/journal/silk-press-care?quiet=1"); check("quiet=1 does not count", r.json.article.view_count === v0 + 1, `${r.json.article.view_count}`);
  r = await call("GET", "/journal/silk-press-care?quiet=1&images=url"); check("images=url rewrites nothing here (the seed has no inline pictures) and still answers", r.status === 200 && !r.json.article.body_md.includes("media:"), r.text);
  r = await call("GET", "/journal/deposits-and-cancellations-on-logaluxe?quiet=1"); check("a guide has no professionals category and a general booking line", r.status === 200 && r.json.article.related_category === null && r.json.article.cta_text === "Find a professional near you", r.text);
  r = await call("GET", "/journal/no-such-piece"); check("an unknown slug is 404", r.status === 404, r.text);

  // ----- public: the professionals under an article -----
  r = await call("GET", "/journal/knotless-braids-what-to-ask-for/professionals?place=nashville-tn");
  check("professionals answers in the shape of GET /v1/businesses, four at most, in the article's category", r.status === 200 && Array.isArray(r.json.businesses) && "total" in r.json && "pins" in r.json && "geo" in r.json && r.json.businesses.length > 0 && r.json.businesses.length <= 4 && r.json.businesses.every((b) => b.category === "braids" && "slug" in b && "rating" in b && "services" in b), r.text.slice(0, 300));
  check("it is filled to four and says where they are from", r.json.geo.fill && typeof r.json.geo.fill_notice === "string" && r.json.businesses.every((b) => b.tier), JSON.stringify(r.json.geo.fill));
  r = await call("GET", "/journal/knotless-braids-what-to-ask-for/professionals?lat=36.16&lng=-86.78&scope=US");
  check("with a point, nearest first", r.status === 200 && r.json.geo.mode === "near" && r.json.businesses[0].distance_km !== null, r.text.slice(0, 300));
  r = await call("GET", "/journal/deposits-and-cancellations-on-logaluxe/professionals?scope=NG");
  check("a guide with no category still answers professionals for the country", r.status === 200 && r.json.businesses.length > 0 && r.json.businesses.every((b) => b.country === "NG" || b.market === "NG"), r.text.slice(0, 300));
  r = await call("GET", "/journal/no-such-piece/professionals"); check("professionals for an unknown slug is 404", r.status === 404, r.text);

  // ----- admin: the list and the media slot -----
  r = await call("GET", "/admin/journal"); check("the admin list needs a sign-in", r.status === 401, r.text);
  r = await a("GET", "/admin/journal"); check("the admin list has every field", r.status === 200 && r.json.articles.length >= 10 && r.json.articles.every((x) => hasFields(x, ["id", "slug", "title", "dek", "body_md", "status", "published_at", "created_by", "updated_at", "seo_title", "related_category"])), r.text.slice(0, 300));
  r = await a("GET", "/admin/journal?status=published&q=silk"); check("status= and q= narrow it", r.status === 200 && r.json.articles.length >= 1 && r.json.articles.every((x) => x.status === "published") && r.json.articles.some((x) => x.slug === "silk-press-care"), r.text.slice(0, 300));
  r = await a("GET", "/admin/journal?status=nonsense"); check("an unknown status is refused", r.status === 400, r.text);
  r = await a("GET", "/admin/media"); check("the article media slot exists, six pictures per article, ref by slug", r.status === 200 && r.json.slots.some((s) => s.key === "article" && s.max === 6 && s.ref === "article"), JSON.stringify(r.json.slots));
  const heroes = Number(sql("select count(*) from site_media where slot='hero' and active"));
  r = await call("GET", "/site/media?slot=article&ref=knotless-braids-what-to-ask-for");
  if (heroes > 0) check("the seeded article has a cover picture", r.status === 200 && r.json.media.length >= 1 && r.json.media[0].alt.length > 10, r.text);
  else console.log("     (no hero photos uploaded, so the seeded covers are empty here)");

  // ----- admin: validation -----
  r = await a("POST", "/admin/journal", { ...draft, title: "Test" }); check("a short title is refused with a sentence", r.status === 400 && /8 to 120/.test(r.json.error), r.text);
  r = await a("POST", "/admin/journal", { ...draft, dek: "Too short" }); check("a short summary is refused", r.status === 400 && /20 to 200/.test(r.json.error), r.text);
  r = await a("POST", "/admin/journal", { ...draft, slug: "Bad Slug!" }); check("a slug with spaces or capitals is refused", r.status === 400 && /slug/.test(r.json.error), r.text);
  r = await a("POST", "/admin/journal", { ...draft, category: "shoes" }); check("an unknown category is refused", r.status === 400 && /category/.test(r.json.error), r.text);
  r = await a("POST", "/admin/journal", { ...draft, country: "GH" }); check("an unknown country is refused", r.status === 400 && /country/.test(r.json.error), r.text);
  r = await a("POST", "/admin/journal", { ...draft, cover_media_id: "00000000-0000-0000-0000-000000000000" }); check("a cover that does not exist is refused", r.status === 400 && /picture/.test(r.json.error), r.text);
  r = await a("POST", "/admin/journal", { ...draft, body_md: "![x](media:00000000-0000-0000-0000-000000000000)" }); check("a body picture that does not exist is refused", r.status === 400 && /picture in the body/.test(r.json.error), r.text);
  r = await a("POST", "/admin/journal", { ...draft, related_category: "guide" }); check("related_category must be a service category", r.status === 400 && /related_category/.test(r.json.error), r.text);
  r = await a("POST", "/admin/journal", { ...draft, nonsense: 1 }); check("an unknown field is refused", r.status === 400, r.text);

  // ----- admin: create, preview, publish -----
  r = await a("POST", "/admin/journal", draft);
  check("a draft is created with its slug made from the title", r.status === 201 && r.json.ok && r.json.id && r.json.slug === SLUG, r.text);
  const id = r.json.id; made.push(id);
  r = await a("GET", `/admin/journal/${id}`); check("the editor can load it, as a draft, with reading time and tidy tags", r.status === 200 && r.json.article.status === "draft" && r.json.article.reading_minutes === 1 && r.json.article.tags.join() === "test,e2e" && r.json.article.author_name === "LogaLuxe editorial", r.text);
  r = await a("POST", "/admin/journal", draft); check("the same slug twice is a 409", r.status === 409, r.text);
  r = await call("GET", `/journal/${SLUG}`); check("the public cannot read a draft", r.status === 404, r.text);
  r = await call("GET", `/journal`); check("a draft is not in the public list", !r.json.articles.some((x) => x.id === id), "");
  r = await a("GET", `/journal/${SLUG}`); check("a staff token previews a draft, without counting a view", r.status === 200 && r.json.article.status === "draft" && r.json.article.view_count === 0 && r.json.next, r.text.slice(0, 300));
  r = await a("POST", `/admin/journal/${id}/publish`, {}); check("a short draft cannot be published", r.status === 400 && /300 words/.test(r.json.error) && /it has 4\d/.test(r.json.error), r.text);
  const hero = sql("select id from site_media where slot='hero' and active order by sort, created_at limit 1");
  const long = { ...draft, slug: SLUG, body_md: "## One\n\n" + words(200) + (hero ? `\n\n![A picture](/v1/media/${hero})\n\n` : "\n\n") + "## Two\n\n" + words(140) + "\n\n[Find a braider](/search?category=braids)", tags: ["test"], featured: false, seo_title: "Test piece", seo_description: "A test piece." };
  r = await a("PUT", `/admin/journal/${id}`, long);
  check("the body is replaced and the reading time recomputed", r.status === 200 && r.json.reading_minutes === 2, r.text);
  if (hero) { r = await a("GET", `/admin/journal/${id}`); check("a pasted picture address is stored as media:<id>", r.json.article.body_md.includes(`](media:${hero})`), r.json.article.body_md.slice(0, 400)); }
  r = await a("POST", `/admin/journal/${id}/publish`, {}); check("a super admin publishes it now", r.status === 200 && r.json.status === "published" && r.json.published_at, r.text);
  r = await call("GET", `/journal/${SLUG}`); check("the public can read it, and the view counts", r.status === 200 && r.json.article.view_count === 1 && r.json.article.related_category === "braids", r.text.slice(0, 200));
  r = await call("GET", `/journal?category=braids`); check("it is in the public list, after the featured pieces", r.json.articles.some((x) => x.id === id) && r.json.articles[0].featured === true, "");
  r = await a("PUT", `/admin/journal/${id}`, { ...long, slug: SLUG + "-renamed" }); check("the slug cannot change once published", r.status === 400 && /unpublish/.test(r.json.error), r.text);
  r = await a("DELETE", `/admin/journal/${id}`); check("a published piece cannot be deleted", r.status === 409, r.text);

  // ----- admin: schedule, unpublish, archive, delete -----
  const tomorrow = new Date(Date.now() + 86400000).toISOString();
  r = await a("POST", `/admin/journal/${id}/publish`, { at: tomorrow }); check("a future time schedules it", r.status === 200 && r.json.status === "scheduled", r.text);
  r = await call("GET", `/journal/${SLUG}`); check("a scheduled piece is not public yet", r.status === 404, r.text);
  r = await call("GET", `/journal/home`); check("nor on the home screen", !r.json.latest.some((x) => x.id === id) && r.json.featured.id !== id, "");
  r = await a("GET", `/admin/journal?status=scheduled`); check("the admin list shows it as scheduled", r.json.articles.some((x) => x.id === id), "");
  r = await a("POST", `/admin/journal/${id}/publish`, { at: "yesterday" }); check("a time that is not a time is refused", r.status === 400, r.text);
  sql(`update articles set published_at = now() - interval '1 minute' where id='${id}'`); // the scheduled time arrives
  r = await call("GET", `/journal/${SLUG}?quiet=1`); check("when the time comes it is live by itself, with no worker", r.status === 200 && r.json.article.status === "published", r.text.slice(0, 200));
  r = await a("POST", `/admin/journal/${id}/unpublish`); check("unpublish takes it down", r.status === 200 && r.json.status === "draft", r.text);
  r = await call("GET", `/journal/${SLUG}`); check("and it is 404 again", r.status === 404, r.text);
  r = await a("POST", `/admin/journal/${id}/publish`, {}); r = await a("POST", `/admin/journal/${id}/archive`); check("archive keeps it but hides it", r.status === 200 && r.json.status === "archived", r.text);
  r = await call("GET", `/journal/${SLUG}`); check("an archived piece is not public", r.status === 404, r.text);
  r = await a("DELETE", `/admin/journal/${id}`); check("an archived piece cannot be deleted either", r.status === 409, r.text);
  r = await a("POST", `/admin/journal/${id}/unpublish`); r = await a("DELETE", `/admin/journal/${id}`); check("back to a draft, it can be deleted", r.status === 200, r.text);
  r = await a("GET", `/admin/journal/${id}`); check("and it is gone", r.status === 404, r.text);
  r = await a("GET", "/admin/audit?action=journal.publish"); check("publishing was audited", r.status === 200 && JSON.stringify(r.json).includes(id), r.text.slice(0, 200));

  // ----- roles -----
  const PW = "e2e-" + stamp + "-password";
  const sup = (await a("POST", "/admin/team", { email: `e2e-j-support-${stamp}@logaluxe.test`, name: "Test Support " + stamp, role: "support", password: PW })).json.id;
  const ops = (await a("POST", "/admin/team", { email: `e2e-j-ops-${stamp}@logaluxe.test`, name: "Test Ops " + stamp, role: "ops", password: PW })).json.id;
  const supT = (await call("POST", "/admin/login", { email: `e2e-j-support-${stamp}@logaluxe.test`, password: PW })).json.token;
  const opsT = (await call("POST", "/admin/login", { email: `e2e-j-ops-${stamp}@logaluxe.test`, password: PW })).json.token;
  r = await call("GET", "/admin/journal", undefined, supT); check("support reads the admin list", r.status === 200, r.text);
  r = await call("POST", "/admin/journal", draft, supT); check("support cannot write", r.status === 403, r.text);
  r = await call("POST", "/admin/journal", { ...draft, title: "Test ops piece " + stamp }, opsT); check("ops can write", r.status === 201, r.text);
  const opsID = r.json.id; made.push(opsID);
  r = await call("POST", `/admin/journal/${opsID}/publish`, {}, opsT); check("ops cannot publish", r.status === 403 && /super admin/.test(r.json.error), r.text);
  r = await call("POST", `/admin/journal/${opsID}/archive`, undefined, opsT); check("ops can archive", r.status === 200, r.text);
  r = await call("POST", `/admin/journal/${opsID}/unpublish`, undefined, opsT); r = await call("DELETE", `/admin/journal/${opsID}`, undefined, opsT); check("ops can delete a draft", r.status === 200, r.text);
  r = await call("GET", `/journal/silk-press-care?quiet=1`, undefined, supT); check("a staff token on a live piece reads it like anyone", r.status === 200, r.text.slice(0, 100));

  // ----- the AI draft -----
  r = await a("POST", "/admin/journal/draft", { topic: "x", category: "braids", country: "", notes: "" }); check("a topic that says nothing is refused", r.status === 400, r.text);
  r = await a("POST", "/admin/journal/draft", { topic: "Test: how to prepare for a first braid appointment", category: "braids", country: "US", notes: "Keep it short." });
  if (r.status === 503) check("without OPENAI_API_KEY the draft answers 503 with a sentence", /not set up|AI service/.test(r.json.error), r.text);
  else {
    check("the AI writes a draft that fills the editor: title, summary, body and tags", r.status === 200 && r.json.title && r.json.dek && /^#{1,3} /m.test(r.json.body_md) && Array.isArray(r.json.tags), r.text.slice(0, 300));
    console.log("     draft title →", JSON.stringify(r.json.title), "words:", (r.json.body_md || "").split(/\s+/).length);
    r = await a("GET", "/admin/journal?q=" + encodeURIComponent("Test: how to prepare")); check("nothing was saved: a draft is only text for a person to read", !r.json.articles.some((x) => /first braid appointment/i.test(x.title)), "");
  }

  try {
    const left = sql(`select count(*) from articles where title like 'Test %'`);
    sql(`delete from articles where title like 'Test %'; delete from admin_sessions where admin_id in (select id from admin_users where email like 'e2e-j-%@logaluxe.test'); delete from admin_users where email like 'e2e-j-%@logaluxe.test';`);
    console.log("cleaned up; test articles that were still there:", left, "· team members left behind:", sql(`select count(*) from admin_users where email like 'e2e-j-%@logaluxe.test'`));
  } catch (e) { console.log("CLEANUP PROBLEM:", String(e.stderr || e).slice(0, 800)); }
  console.log(`\n${pass} passed, ${fail} failed`);
  process.exit(fail ? 1 : 0);
})();
