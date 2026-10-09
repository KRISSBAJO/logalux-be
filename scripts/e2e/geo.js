// End-to-end check of places: the list of places that have businesses, the
// place picker, turning a point into a place, search by distance with its
// widening radius and honest notices, search by place and by map area,
// travelling businesses, the country being browsed, the first guess from an
// internet address, gift cards for either country, and a business's country,
// pin and time zone at sign-up and when its address changes. It signs up throwaway businesses in
// Boise, Knoxville and Kano and removes them. It takes no payment, so it runs
// with or without the payment keys:
//   RATE_LIMITS=off MAIL_PROVIDER=log docker compose up -d api
//   node scripts/e2e/geo.js
// A few steps ask the place lookup service (OpenStreetMap Nominatim) at most
// once each; when it cannot be reached they accept the answer the API gives
// without it, and say so.
const { execSync } = require("child_process");
const DBURL = (() => { try { return (require("fs").readFileSync(".env", "utf8").match(/^DATABASE_URL=(.*)$/m) || [])[1]?.trim().replace(/^["']|["']$/g, "") || ""; } catch { return ""; } })();
const API = process.env.API || "http://localhost:18080/v1";
const DB = process.env.DB || "logaluxe";
const stamp = Date.now().toString(36);
const PASS = "e2e-" + stamp + "-password";
const mail = (tag) => `e2e-geo-${tag}-${stamp}@example.test`;
let pass = 0, fail = 0;
const check = (name, ok, extra = "") => { ok ? pass++ : fail++; console.log((ok ? "ok   " : "FAIL ") + name + (ok ? "" : "  " + String(extra).slice(0, 600))); };
const call = async (method, path, body, token) => {
  const r = await fetch(API + path, { method, headers: { "Content-Type": "application/json", ...(token ? { Authorization: "Bearer " + token } : {}) }, body: body === undefined ? undefined : JSON.stringify(body) });
  const text = await r.text(); let json = {}; try { json = JSON.parse(text); } catch {}
  return { status: r.status, json, text };
};
const get = (path) => call("GET", path);
const sql = (q) => execSync(DBURL ? `docker exec -i logaluxe-db psql "${DBURL}${DBURL.includes("?") ? "&" : "?"}sslrootcert=system" -At` : `docker exec -i logaluxe-db psql -U logaluxe -d ${DB} -At`, { input: q }).toString().trim();
/** Kilometres between two points, worked out here so the API's own sum is checked against another. */
const km = (a, b, c, d) => { const r = (x) => (x * Math.PI) / 180; const h = Math.sin(r(c - a) / 2) ** 2 + Math.cos(r(a)) * Math.cos(r(c)) * Math.sin(r(d - b) / 2) ** 2; return 6371 * 2 * Math.asin(Math.sqrt(h)); };
const rising = (list) => list.every((x, i) => i === 0 || list[i - 1] <= x);

(async () => {
  const samples = sql("select count(*) from businesses where slug in ('ada','mnm','bealebarbers','gardencitymakeup','harlemglow','crenshawnails','peachtree','bayoufade','wuselashes') and status='live'") === "9";
  if (!samples) console.log("note: the sample businesses are not all here and live, so the checks that need them are skipped");
  let r;

  // ----- places come from data -----
  r = await get("/places");
  const places = r.json.places || [];
  check("the places that have businesses are listed, busiest first, each with a centre", r.status === 200 && places.length >= 2 && places.every((p) => p.slug && p.label && p.businesses > 0 && Math.abs(p.lat) > 0 && Math.abs(p.lng) > 0 && p.timezone && (p.unit === "mi" || p.unit === "km"))
    && places.every((p, i) => i === 0 || places[i - 1].businesses >= p.businesses), r.text.slice(0, 300));
  check("each place counts its businesses by category, and they add up", places.every((p) => Object.values(p.categories || {}).reduce((a, b) => a + b, 0) === p.businesses), JSON.stringify(places.map((p) => [p.slug, p.businesses, p.categories])));
  check("the answer says which place to show a stranger, and what each country holds", r.json.default?.slug === places[0].slug && r.json.countries?.length === 2 && r.json.countries.find((c) => c.code === "US").unit === "mi" && r.json.countries.find((c) => c.code === "NG").currency === "NGN", JSON.stringify(r.json.countries));
  const counted = Number(sql("select count(distinct b.id) from businesses b join locations l on l.business_id=b.id where b.status='live' and coalesce((b.settings->'booking'->>'on_search')::boolean, true) and l.is_primary and b.name !~* '^(e2e|scale test|test )'"));
  check("the counts are the live businesses, no more", places.reduce((a, p) => a + p.businesses, 0) >= counted && r.json.countries.reduce((a, c) => a + c.businesses, 0) === places.reduce((a, p) => a + p.businesses, 0), `${counted} live`);
  if (samples) {
    const by = Object.fromEntries(places.map((p) => [p.slug, p]));
    check("Nashville and Lagos are two places among the rest, not special ones", by["nashville-tn"]?.businesses >= 4 && by["lagos-lagos"]?.businesses >= 2 && places.length >= 9, JSON.stringify(places.map((p) => p.slug)));
    check("a place's time zone, money and unit come from where it is", by["new-york-ny"]?.timezone === "America/New_York" && by["los-angeles-ca"]?.timezone === "America/Los_Angeles" && by["houston-tx"]?.timezone === "America/Chicago" && by["atlanta-ga"]?.timezone === "America/New_York"
      && by["abuja-fct"]?.timezone === "Africa/Lagos" && by["abuja-fct"].currency === "NGN" && by["abuja-fct"].unit === "km" && by["memphis-tn"]?.unit === "mi" && by["port-harcourt-rivers"]?.label === "Port Harcourt, Rivers", JSON.stringify(by["abuja-fct"]));
    r = await get("/places?country=ng"); check("one country's places", r.json.places.length >= 3 && r.json.places.every((p) => p.country === "NG") && r.json.default.country === "NG", r.text.slice(0, 200));
    r = await get("/places?category=barber"); check("the places that have one kind of business", r.json.places.length >= 3 && r.json.places.every((p) => p.categories.barber > 0 && p.businesses === p.categories.barber), r.text.slice(0, 200));
    r = await get("/places?lat=35.96&lng=-83.92&limit=3"); const near = r.json.places;
    check("places nearest a point, with the distance in miles for a point in the United States", near.length === 3 && rising(near.map((p) => p.distance_km)) && near[0].slug === "atlanta-ga" && /^\d+ mi$/.test(near[0].distance_text) && Math.abs(near[0].distance_km - km(35.96, -83.92, near[0].lat, near[0].lng)) < 0.5, JSON.stringify(near.map((p) => [p.slug, p.distance_text])));
  }
  r = await get("/places?lat=200&lng=0"); check("a point that is not on Earth is refused", r.status === 400, r.text);

  // ----- the picker -----
  r = await get("/places/search?q=");
  check("with nothing typed the picker offers the places that have businesses", r.json.places?.length > 0 && r.json.places.every((p) => p.businesses > 0) && r.json.looked_up === false, r.text.slice(0, 200));
  r = await get("/places/search?q=ten"); check("a state is found by its name", r.json.places?.[0]?.slug === "tennessee" && r.json.places[0].kind === "state" && r.json.places[0].label === "Tennessee", r.text.slice(0, 300));
  r = await get("/places/search?q=fct"); check("the Federal Capital Territory is found by its short name", r.json.places?.some((p) => p.slug === "fct" && p.kind === "state" && p.country === "NG"), r.text.slice(0, 300));
  r = await get("/places/search?q=port%20h"); check("a well-known city is found as it is typed, with no lookup", r.json.places?.[0]?.slug === "port-harcourt-rivers" && r.json.looked_up === false, r.text.slice(0, 300));
  r = await get("/places/search?q=Chicago,%20IL"); check("a city can be typed with its state", r.json.places?.[0]?.slug === "chicago-il" && r.json.places[0].unit === "mi" && r.json.places[0].timezone === "America/Chicago", r.text.slice(0, 300));
  r = await get("/places/search?q=la&country=ng"); check("the picker can be kept to one country", r.json.places?.length > 0 && r.json.places.every((p) => p.country === "NG") && r.json.places.some((p) => p.slug === "lagos-lagos"), r.text.slice(0, 300));
  r = await get("/places/search?q=zzzzqqq"); check("nothing matching gives an empty list, not an error", r.status === 200 && r.json.places.length === 0, r.text);
  // A small town only the lookup service knows. Asked once; the second time the answer comes from the cache.
  const town = "Tullahoma, TN";
  r = await get("/places/search?lookup=1&q=" + encodeURIComponent(town));
  if (r.json.complete === false) console.log("note: the lookup service could not be asked just now; the lookup checks are skipped");
  else {
    check("a town we had not met is found by the lookup service", r.json.looked_up === true && r.json.places?.[0]?.slug === "tullahoma-tn" && Math.abs(r.json.places[0].lat - 35.36) < 0.1 && r.json.places[0].timezone === "America/Chicago", r.text.slice(0, 300));
    const cached = sql("select count(*) from geo_cache");
    const t0 = Date.now(); r = await get("/places/search?lookup=1&q=" + encodeURIComponent(town.toUpperCase() + "  "));
    check("the same question is not asked twice: the second answer comes from the cache", r.json.places?.[0]?.slug === "tullahoma-tn" && sql("select count(*) from geo_cache") === cached && Date.now() - t0 < 900, `${Date.now() - t0} ms`);
    r = await get("/places/search?q=tulla"); check("and the town is offered as it is typed from then on", r.json.places?.some((p) => p.slug === "tullahoma-tn") && r.json.looked_up === false, r.text.slice(0, 200));
    r = await get("/places/tullahoma-tn"); check("and has a page address of its own", r.json.place?.label === "Tullahoma, TN" && r.json.place.businesses === 0 && Array.isArray(r.json.nearest), r.text.slice(0, 200));
  }

  // ----- slugs -----
  r = await get("/places/tennessee"); check("a state is a place too", r.json.place?.kind === "state" && r.json.place.region === "TN" && r.json.place.unit === "mi", r.text.slice(0, 200));
  r = await get("/places/lagos-state"); check("so is a Nigerian state", r.json.place?.kind === "state" && r.json.place.region === "Lagos" && r.json.place.label === "Lagos State" && r.json.place.currency === "NGN", r.text.slice(0, 200));
  r = await get("/places/boise-id"); check("a city with no business yet still resolves, and says it has none", r.json.place?.label === "Boise, ID" && r.json.place.timezone === "America/Denver" && r.json.canonical === "", r.text.slice(0, 200));
  r = await get("/places/no-such-thing"); check("an unknown place is not found", r.status === 404, r.text);
  if (samples) {
    r = await get("/places/nashville"); check("the old address /nashville points at /nashville-tn", r.json.canonical === "nashville-tn" && r.json.place.slug === "nashville-tn", r.text.slice(0, 200));
    r = await get("/places/lagos"); check("and /lagos at /lagos-lagos", r.json.canonical === "lagos-lagos", r.text.slice(0, 200));
    r = await get("/places/nashville-tn"); check("a place lists the nearest other places that have businesses", r.json.canonical === "" && r.json.nearest?.[0]?.slug === "memphis-tn" && rising(r.json.nearest.map((p) => p.distance_km)) && !r.json.nearest.some((p) => p.slug === "nashville-tn"), JSON.stringify(r.json.nearest?.map((p) => [p.slug, p.distance_text])));
  }

  // ----- a point becomes a place -----
  r = await get("/places/reverse?lat=36.0331&lng=-86.7828");
  check("a point in the United States becomes a place, served, in miles", r.status === 200 && r.json.served === true && r.json.unit === "mi" && r.json.country === "US" && r.json.place?.region === "TN" && r.json.place.lat === 36.03 && r.json.place.lng === -86.78, r.text.slice(0, 300));
  if (r.json.source === "lookup") check("the lookup names the town the point is in", r.json.place.slug === "brentwood-tn" && r.json.place.label === "Brentwood, TN", JSON.stringify(r.json.place));
  else console.log("note: the lookup service could not be asked; the point was named after the nearest city we know:", r.json.place?.label);
  if (samples) check("and comes with the nearest places that have businesses", r.json.nearest?.[0]?.slug === "nashville-tn" && r.json.nearest[0].distance_km < 20, JSON.stringify(r.json.nearest?.map((p) => [p.slug, p.distance_text])));
  r = await get("/places/reverse?lat=6.4281&lng=3.4219");
  check("a point in Nigeria becomes a place, served, in kilometres", r.json.served === true && r.json.unit === "km" && r.json.place?.region === "Lagos" && r.json.place.currency === "NGN", r.text.slice(0, 300));
  r = await get("/places/reverse?lat=51.5072&lng=-0.1276");
  check("a point elsewhere says plainly it is not served, and still names the nearest places", r.status === 200 && r.json.served === false && r.json.place?.slug === "" && Array.isArray(r.json.nearest) && (!samples || r.json.nearest.length === 3), r.text.slice(0, 300));
  r = await get("/places/reverse?lat=abc&lng=1"); check("a bad point is refused", r.status === 400, r.text);

  // ----- search by distance -----
  if (samples) {
    r = await get("/businesses?lat=36.1627&lng=-86.7816&quiet=1"); let b = r.json.businesses, g = r.json.geo;
    check("near downtown Nashville: nearest first, each with its distance", r.status === 200 && b.length >= 4 && rising(b.map((x) => x.distance_km)) && b.every((x) => x.city === "Nashville" && x.distance_unit === "mi" && /^\d+(\.\d)? mi$/.test(x.distance_text) && x.distance_km <= 25 * 1.609344), JSON.stringify(b.map((x) => [x.slug, x.distance_text])));
    check("the distance is the real one", b.every((x) => Math.abs(x.distance_km - km(36.1627, -86.7816, x.lat, x.lng)) < 0.02 && Math.abs(x.distance - x.distance_km / 1.609344) < 0.06), JSON.stringify(b.map((x) => [x.distance_km, x.distance])));
    check("the answer says what was searched: 25 miles, not widened", g.mode === "near" && g.unit === "mi" && g.radius_asked === 25 && g.radius_used === 25 && g.widened === false && g.within_asked === r.json.total && g.notice === "" && g.nearest?.slug === "nashville-tn", JSON.stringify(g));
    check("the map pins carry the same distances", r.json.pins.length === r.json.total && rising(r.json.pins.map((p) => p.distance_km)) && r.json.pins.every((p) => p.pin === "exact" || p.pin === "area"), JSON.stringify(r.json.pins.slice(0, 2)));
    r = await get("/businesses?lat=6.4474&lng=3.4723&quiet=1"); b = r.json.businesses;
    check("near Lekki: kilometres", b.length >= 2 && b.every((x) => x.distance_unit === "km" && / km$/.test(x.distance_text) && x.currency === "NGN") && r.json.geo.unit === "km" && r.json.geo.radius_asked === 25, JSON.stringify(b.map((x) => [x.slug, x.distance_text])));
    r = await get("/businesses?lat=36.1627&lng=-86.7816&sort=top&quiet=1");
    check("with a point the list can still be ordered by rating; distances stay", r.json.businesses.length >= 4 && r.json.businesses.every((x) => x.distance_km > 0) && r.json.geo.nearest?.distance_km === Math.min(...r.json.businesses.map((x) => x.distance_km)), JSON.stringify(r.json.businesses.map((x) => [x.slug, x.rating, x.distance_text])));
    r = await get("/businesses?lat=36.1627&lng=-86.7816&category=barber&q=fade&quiet=1");
    check("distance works together with the other filters", r.json.businesses.length === 1 && r.json.businesses[0].slug === "barberloft", JSON.stringify(r.json.businesses.map((x) => x.slug)));

    // ----- little nearby -----
    r = await get("/businesses?lat=35.6145&lng=-88.8139&quiet=1"); g = r.json.geo; // Jackson, Tennessee
    check("nothing within 25 miles: the radius widens and the answer says so", g.widened === true && g.within_asked === 0 && g.radius_asked === 25 && g.radius_used > 25 && r.json.businesses.length > 0 && r.json.businesses[0].slug === "bealebarbers", JSON.stringify(g).slice(0, 300));
    check("in a plain sentence, in miles", /^Nothing within 25 miles\. The nearest (is|are) in Memphis, TN, \d+ miles away\.$/.test(g.notice) && g.nearest.label === "Memphis, TN" && Math.abs(g.nearest.distance - 77) < 3, g.notice);
    check("and every result is inside the radius it reports", r.json.businesses.every((x) => x.distance <= g.radius_used + 0.1) && g.nearest_places?.[0]?.slug === "memphis-tn", JSON.stringify(r.json.businesses.map((x) => x.distance)));
    r = await get("/businesses?lat=35.6145&lng=-88.8139&widen=0&quiet=1");
    check("asked not to widen, it says nothing is nearby and names the nearest places", r.json.total === 0 && r.json.geo.widened === false && r.json.geo.notice === "Nothing within 25 miles." && r.json.geo.nearest_places.length === 3 && r.json.geo.nearest_places[0].distance_text.endsWith(" mi"), JSON.stringify(r.json.geo).slice(0, 300));
    r = await get("/businesses?lat=36.1627&lng=-86.7816&radius=2&quiet=1"); g = r.json.geo;
    check("only a few nearby: it looks a little further and says how far", g.widened === true && g.within_asked >= 1 && g.within_asked < 3 && g.radius_used === 50 && new RegExp(`^Only ${g.within_asked} within 2 miles, so this list goes out to 50 miles\\.$`).test(g.notice) && r.json.total >= 4, g.notice);
    r = await get("/businesses?lat=36.1627&lng=-86.7816&radius=60&unit=km&quiet=1");
    check("the radius and unit can be chosen", r.json.geo.unit === "km" && r.json.geo.radius_asked === 60 && r.json.businesses.every((x) => x.distance_unit === "km" && x.distance <= 60), JSON.stringify(r.json.geo).slice(0, 200));
    r = await get("/businesses?lat=51.5072&lng=-0.1276&quiet=1"); g = r.json.geo; // London
    check("far from everything: no radius, the nearest wherever they are, in kilometres", g.widened === true && g.radius_used === null && g.unit === "km" && r.json.businesses.length > 0 && rising(r.json.businesses.map((x) => x.distance_km)) && /^Nothing within 25 kilometres\. The nearest are in .+, [\d,]+ kilometres away\.$/.test(g.notice), g.notice);
    r = await get("/businesses?lat=36.1627&lng=-86.7816&category=makeup&quiet=1");
    check("a kind nobody nearby offers is found further away, honestly", r.json.geo.within_asked === 0 && (r.json.total === 0 || r.json.geo.widened === true), JSON.stringify(r.json.geo).slice(0, 300));

    // ----- by place, and by what the map shows -----
    r = await get("/businesses?place=memphis-tn&quiet=1");
    check("a place by its slug: only businesses in that city", r.json.geo.mode === "place" && r.json.geo.place.slug === "memphis-tn" && r.json.total >= 1 && r.json.businesses.every((x) => x.city === "Memphis" && x.region === "TN" && x.country === "US"), JSON.stringify(r.json.businesses.map((x) => [x.slug, x.city])));
    r = await get("/businesses?city=port%20harcourt&region=Rivers%20State&country=NG&quiet=1");
    check("a place by city, state and country, however the state is written", r.json.geo.place?.slug === "port-harcourt-rivers" && r.json.businesses.some((x) => x.slug === "gardencitymakeup") && r.json.geo.unit === "km", r.text.slice(0, 200));
    r = await get("/businesses?place=tennessee&lat=36.1627&lng=-86.7816&sort=distance&quiet=1");
    check("a whole state, nearest first from a point", r.json.businesses.length >= 5 && r.json.businesses.every((x) => x.region === "TN") && rising(r.json.businesses.map((x) => x.distance_km)) && r.json.businesses.at(-1).city === "Memphis", JSON.stringify(r.json.businesses.map((x) => [x.slug, x.distance_text])));
    r = await get("/businesses?place=knoxville-tn&quiet=1");
    check("a place with nobody in it says so and names the nearest places that have someone", r.json.total === 0 && r.json.geo.place.label === "Knoxville, TN" && r.json.geo.nearest_places.length === 3 && rising(r.json.geo.nearest_places.map((p) => p.distance_km)), JSON.stringify(r.json.geo.nearest_places.map((p) => [p.slug, p.distance_text])));
    r = await get("/businesses?bbox=36.0,-87.0,36.3,-86.5&quiet=1");
    check("search this area: only what the map shows", r.json.geo.mode === "bbox" && r.json.total >= 4 && r.json.businesses.every((x) => x.lat > 36.0 && x.lat < 36.3 && x.lng > -87.0 && x.lng < -86.5) && r.json.pins.length === r.json.total, JSON.stringify(r.json.businesses.map((x) => [x.slug, x.lat, x.lng])));
    r = await get("/businesses?bbox=40.0,-75.0,41.0,-73.0&quiet=1");
    check("another area shows another city", r.json.businesses.some((x) => x.slug === "harlemglow") && r.json.businesses.every((x) => x.city === "New York"), JSON.stringify(r.json.businesses.map((x) => x.slug)));
    r = await get("/businesses?market=NG&where=lekki&quiet=1"); check("the old market and where filters still work", r.json.geo.mode === "all" && r.json.businesses.length >= 1 && r.json.businesses.every((x) => x.market === "NG" && x.distance_km === null), JSON.stringify(r.json.businesses.map((x) => x.slug)));

    // ----- a business that travels to its clients -----
    r = await get("/businesses?lat=4.8156&lng=7.0498&quiet=1"); let mk = r.json.businesses.find((x) => x.slug === "gardencitymakeup");
    check("a travelling business is offered where it goes, and marked as travelling with an area pin", mk && mk.travels === true && mk.travel_radius_km === 25 && mk.pin === "area" && mk.distance_km < 25 && r.json.pins.find((p) => p.slug === "gardencitymakeup")?.pin === "area", JSON.stringify(mk || r.json.businesses.map((x) => x.slug)));
    r = await get("/businesses?lat=5.1066&lng=7.3667&radius=100&quiet=1"); // Aba, about 50 km away
    check("and is not offered beyond the distance it says it travels", !r.json.businesses.some((x) => x.slug === "gardencitymakeup") && r.json.total === 0 && r.json.geo.radius_used === null === false || !r.json.businesses.some((x) => x.slug === "gardencitymakeup"), JSON.stringify(r.json.businesses.map((x) => [x.slug, x.distance_text])));
    r = await get("/businesses/nia"); check("a business page says whether each location travels", r.json.locations?.[0]?.travels === true && r.json.locations[0].pin === "area", JSON.stringify(r.json.locations?.[0]).slice(0, 300));
    r = await get("/businesses/ada"); check("and a shop front has an exact pin", r.json.locations?.[0]?.travels === false && r.json.locations[0].pin === "exact", JSON.stringify(r.json.locations?.[0]).slice(0, 300));
  }
  r = await get("/businesses?lat=95&lng=0"); check("a bad point is refused", r.status === 400, r.text);
  r = await get("/businesses?lat=36.1"); check("half a point is refused", r.status === 400, r.text);
  r = await get("/businesses?place=no-such-zz"); check("an unknown place is refused", r.status === 404, r.text);
  r = await get("/businesses?bbox=1,2,3"); check("a bad map area is refused", r.status === 400, r.text);
  r = await get("/businesses?city=Nashville"); check("a city with no state or country is refused", r.status === 400, r.text);
  r = await get("/businesses?region=Narnia&country=US"); check("an unknown state is refused", r.status === 400, r.text);

  // ----- the country being browsed -----
  r = await get("/places/nigeria"); check("a whole country is a place", r.json.place?.kind === "country" && r.json.place.slug === "nigeria" && r.json.place.label === "Nigeria" && r.json.place.currency === "NGN" && r.json.place.unit === "km", r.text.slice(0, 200));
  r = await get("/places/search?q=all%20of%20nig"); check("the picker finds \"All of Nigeria\"", r.json.places?.[0]?.kind === "country" && r.json.places[0].country === "NG", r.text.slice(0, 200));
  r = await get("/places/search?q=usa"); check("and the United States by its short names", r.json.places?.[0]?.slug === "united-states", r.text.slice(0, 200));
  r = await get("/businesses?scope=XX"); check("a scope that is not a country we serve is refused", r.status === 400, r.text);
  if (samples) {
    r = await get("/businesses?scope=US&quiet=1"); check("scope=US lists no Nigerian business", r.json.total >= 9 && r.json.businesses.every((x) => x.market === "US" && x.currency === "USD") && r.json.geo.scope === "US", JSON.stringify(r.json.businesses.map((x) => [x.slug, x.market])));
    r = await get("/businesses?scope=NG&quiet=1"); check("scope=NG lists no business in the United States", r.json.total >= 4 && r.json.businesses.every((x) => x.market === "NG" && x.currency === "NGN"), JSON.stringify(r.json.businesses.map((x) => [x.slug, x.market])));
    r = await get("/businesses?place=nigeria&lat=6.4474&lng=3.4723&sort=distance&quiet=1");
    check("all of Nigeria, nearest first from a point", r.json.geo.mode === "place" && r.json.geo.place.kind === "country" && r.json.businesses.every((x) => x.country === "NG") && rising(r.json.businesses.map((x) => x.distance_km)) && r.json.businesses.some((x) => x.city === "Abuja"), JSON.stringify(r.json.businesses.map((x) => [x.slug, x.distance_text])));
    r = await get("/businesses?country=us&quiet=1"); check("a country alone is a search", r.json.geo.place?.slug === "united-states" && r.json.total >= 9 && r.json.businesses.every((x) => x.country === "US"), r.text.slice(0, 200));
    r = await get("/businesses?lat=51.5072&lng=-0.1276&scope=US&quiet=1");
    check("someone far away browsing the United States is shown only the United States, nearest first", r.json.businesses.length > 0 && r.json.businesses.every((x) => x.market === "US") && r.json.businesses[0].city === "New York" && r.json.geo.nearest_places.every((p) => p.country === "US"), JSON.stringify(r.json.businesses.map((x) => [x.slug, x.distance_text])));
    r = await get("/businesses?lat=6.4474&lng=3.4723&scope=US&widen=0&quiet=1");
    check("someone in Lagos browsing the United States is not shown the barber down the road", r.json.total === 0 && r.json.geo.nearest_places.every((p) => p.country === "US"), JSON.stringify(r.json.businesses.map((x) => x.slug)));
  }

  // ----- a first guess from the internet address -----
  r = await get("/locate");
  check("a local address is not looked up: the guess is the busiest place, and says it is a default", r.status === 200 && r.json.source === "default" && r.json.approximate === false && r.json.place?.slug === r.json.default?.slug && r.json.place?.slug === places[0].slug, r.text.slice(0, 300));
  const lookedUp = () => sql("select count(*) from geo_ip");
  sql("delete from geo_ip where prefix in ('8.8.8.0/24','102.89.23.0/24','203.0.113.0/24')");
  r = await get("/locate?geo_ip=8.8.8.8");
  if (r.json.source === "default") console.log("note: the address lookup is off, was not reachable, or this API is not in development mode; the address checks are skipped");
  else {
    check("in development an address can be named, and is placed in its country as a guess", r.json.source === "ip" && r.json.approximate === true && r.json.country === "US" && r.json.served === true && r.json.place?.country === "US" && (!samples || r.json.nearest.every((p) => p.country === "US")), r.text.slice(0, 400));
    check("only the first part of the address is kept, with an expiry", sql("select prefix || '|' || (expires_at > now() + interval '6 days') from geo_ip where prefix='8.8.8.0/24'") === "8.8.8.0/24|true" && sql("select count(*) from geo_ip where prefix like '8.8.8.8%'") === "0");
    const n = lookedUp(); const t0 = Date.now(); r = await get("/locate?geo_ip=8.8.8.77");
    check("a neighbour of that address is answered from what is kept", r.json.source === "ip" && r.json.country === "US" && lookedUp() === n && Date.now() - t0 < 2000, `${Date.now() - t0} ms`); // a hosted database answers slower than a local one
    r = await get("/locate?geo_ip=102.89.23.4");
    check("an address in Nigeria is placed in Nigeria", r.json.country === "NG" && r.json.served === true && r.json.place?.country === "NG" && r.json.place.currency === "NGN" && (!samples || r.json.nearest.every((p) => p.country === "NG")), r.text.slice(0, 400));
  }
  r = await get("/locate?geo_ip=203.0.113.9"); check("a reserved address is never looked up", r.json.source === "default" && sql("select count(*) from geo_ip where prefix='203.0.113.0/24'") === "0", r.text.slice(0, 200));
  r = await get("/locate?geo_ip=not-an-address"); check("nonsense for an address falls back quietly", r.status === 200 && r.json.source === "default", r.text.slice(0, 200));
  const edge = await fetch(API + "/locate", { headers: { "X-Vercel-IP-Country": "NG", "X-Vercel-IP-Country-Region": "LA", "X-Vercel-IP-City": "Ikeja", "X-Vercel-IP-Latitude": "6.60", "X-Vercel-IP-Longitude": "3.35", "X-Web-Key": "wrong-key" } }).then((x) => x.json());
  const keyed = sql("select 1") === "1" && /^WEB_API_KEY=.+/m.test(require("fs").readFileSync(".env", "utf8"));
  if (keyed) check("location headers are not believed without the web app's key", edge.source === "default", JSON.stringify(edge).slice(0, 200));
  else check("with no web key set, outside production, a network's location headers are read", edge.source === "edge" && edge.place?.slug === "ikeja-lagos" && edge.country === "NG" && edge.approximate === true, JSON.stringify(edge).slice(0, 300));

  // ----- a gift card for someone in either country -----
  r = await get("/gift-cards/options"); const opt = Object.fromEntries((r.json.options || []).map((o) => [o.country, o]));
  check("gift cards are offered for both countries, each in its own money", opt.US?.currency === "USD" && opt.US.provider === "stripe" && opt.NG?.currency === "NGN" && opt.NG.provider === "paystack" && opt.NG.amounts_cents.every((a) => a >= opt.NG.min_cents && a <= opt.NG.max_cents) && opt.US.amounts_cents.every((a) => a >= opt.US.min_cents && a <= opt.US.max_cents), r.text.slice(0, 400));
  const gift = (more) => call("POST", "/gift-cards/buy", { recipient_name: "Ngozi", recipient_email: mail("to"), buyer_name: "Geo Buyer", buyer_email: mail("gift"), note: "", ...more });
  r = await gift({ country: "NG", amount_cents: 2500 }); check("a naira card cannot be 25 naira", r.status === 400 && /Nigeria/.test(r.text) && /5,000/.test(r.text), r.text);
  r = await gift({ country: "US", amount_cents: 2500000 }); check("a dollar card cannot be $25,000", r.status === 400 && /\$500/.test(r.text), r.text);
  r = await gift({ country: "FR", amount_cents: 2500 }); check("a card for a country we do not serve is refused", r.status === 400, r.text);
  r = await gift({ country: "NG", amount_cents: 2500000 });
  check("a card for someone in Nigeria is in naira and paid through Paystack", r.status === 201 && r.json.currency === "NGN" && r.json.country === "NG" && (!r.json.payment || (r.json.payment.provider === "paystack" && r.json.payment.currency === "NGN" && /^https:\/\//.test(r.json.payment.url))), r.text.slice(0, 300));
  if (!r.json.payment) check("with payments simulated the naira card is issued at once, in naira", sql(`select currency || '|' || initial_cents from gift_cards where recipient_email='${mail("to")}' and currency='NGN'`) === "NGN|2500000", sql(`select currency, initial_cents from gift_cards where recipient_email='${mail("to")}'`));
  else check("nothing is issued until Paystack says it was paid", sql(`select count(*) from gift_cards where recipient_email='${mail("to")}'`) === "0" && sql(`select provider || '|' || currency || '|' || amount_cents from payments where reference='${r.json.payment.reference}'`) === "paystack|NGN|2500000", sql(`select provider, currency, amount_cents from payments where reference='${r.json.payment.reference}'`));
  r = await gift({ amount_cents: 2500 });
  check("with no country given a card is in dollars, as before", r.status === 201 && r.json.currency === "USD" && (!r.json.payment || r.json.payment.provider === "stripe"), r.text.slice(0, 300));

  // ----- where an address lands, before anything is saved -----
  r = await call("POST", "/places/locate", { address: "", city: "chattanooga", region: "Tennessee", country: "United States" });
  check("a city alone gives its centre, its state in stored form, and the time zone of that spot (Chattanooga keeps Eastern time)", r.status === 200 && r.json.location?.region === "TN" && r.json.location.city === "Chattanooga" && r.json.location.timezone === "America/New_York" && r.json.position === "approximate" && r.json.timezone_name === "Eastern time" && r.json.place?.slug === "chattanooga-tn", r.text.slice(0, 400));
  r = await call("POST", "/places/locate", { address: "", city: "Abuja", region: "", country: "NG" });
  check("a well-known city says which state it is in", r.json.location?.region === "FCT" && r.json.location.timezone === "Africa/Lagos" && r.json.currency === "NGN", r.text.slice(0, 300));
  r = await call("POST", "/places/locate", { address: "1 Main St", city: "Springfield", region: "", country: "US" }); check("a city that could be in several states needs its state", r.status === 400 && /state/.test(r.text), r.text);
  r = await call("POST", "/places/locate", { address: "1 Main St", city: "Nashville", region: "TN", country: "US", lat: 6.5, lng: 3.4 }); check("a pin in the other country is refused", r.status === 400 && /Nigeria/.test(r.text), r.text);
  r = await call("POST", "/places/locate", { address: "", city: "Pensacola", region: "FL", country: "US", lat: 30.4213, lng: -87.2169 });
  check("a pin placed by hand is kept, and decides the time zone (Pensacola keeps Central time)", r.json.location?.lat === 30.4213 && r.json.position === "set by hand" && r.json.location.timezone === "America/Chicago", r.text.slice(0, 300));
  r = await get("/places/states"); check("the states of both countries are listed for a form", r.json.states?.US?.length === 56 && r.json.states.NG.length === 37 && r.json.states.US.some((s) => s.value === "TN" && s.name === "Tennessee") && r.json.states.NG.some((s) => s.value === "FCT" && s.name === "Federal Capital Territory"), r.text.slice(0, 200));

  // ----- a business anywhere: country, pin and time zone at sign-up -----
  const signup = (tag, more) => call("POST", "/m/signup", { name: "E2E Geo Owner", email: mail(tag), password: PASS, business: `E2E Geo ${tag} ${stamp}`, category: "barber", ...more });
  r = await signup("boise", { phone: "(208) 555-0142", country: "US", city: "boise", region: "Idaho", address: "150 N Capitol Blvd" });
  check("sign up in Boise: Mountain time, dollars, the state stored as ID", r.status === 201 && r.json.timezone === "America/Denver" && r.json.currency === "USD" && r.json.country === "US" && r.json.location?.region === "ID" && r.json.location.city === "Boise" && r.json.place?.slug === "boise-id", r.text.slice(0, 500));
  const T = r.json.token, boise = r.json.location || {};
  check("the answer says where the pin landed, so a wrong one can be seen", (r.json.position === "found" || r.json.position === "approximate") && km(43.62, -116.2, boise.lat, boise.lng) < 20 && r.json.travels === false, `${r.json.position} ${boise.lat},${boise.lng} ${boise.matched}`);
  if (r.json.position !== "found") console.log("note: the lookup service did not find the street address; the pin is the city centre");
  const m = (method, path, body) => call(method, "/m" + path, body, T);
  r = await m("GET", "/settings"); const biz = r.json.business, loc = r.json.locations?.[0];
  check("the business keeps that zone, and its phone is in international form", biz?.timezone === "America/Denver" && biz.market === "US" && biz.phone === "+12085550142" && loc?.timezone === "America/Denver" && loc.country === "US" && loc.travels === false, JSON.stringify([biz?.timezone, biz?.phone, loc?.timezone]));
  const cacheRows = sql("select count(*) from geo_cache");
  r = await call("POST", "/places/locate", { address: "150 N Capitol Blvd", city: "Boise", region: "ID", country: "US" });
  check("asking where the same address is again uses the kept answer", r.status === 200 && sql("select count(*) from geo_cache") === cacheRows && Math.abs(r.json.location.lat - boise.lat) < 1e-9, r.text.slice(0, 200));

  r = await signup("knox", { phone: "+18655550142", market: "US", city: "Knoxville", region: "TN", address: "" });
  check("sign up in Knoxville (older 'market' field): Eastern time, not Central", r.status === 201 && r.json.timezone === "America/New_York" && r.json.travels === true, r.text.slice(0, 300));
  r = await signup("kano", { phone: "0803 555 0142", country: "NG", city: "Kano", region: "Kano State", address: "", travels: true, travel_radius_km: 15 });
  check("sign up in Kano: naira, West Africa time, the state stored by name", r.status === 201 && r.json.currency === "NGN" && r.json.timezone === "Africa/Lagos" && r.json.location?.region === "Kano" && r.json.place?.slug === "kano-kano" && r.json.travels === true, r.text.slice(0, 300));
  check("a Nigerian number typed the local way gains its country code; the travel distance is kept", sql(`select b.phone || '|' || l.travel_radius_km || '|' || l.travels from businesses b join locations l on l.business_id=b.id where b.email='${mail("kano")}'`) === "+2348035550142|15|true", sql(`select b.phone from businesses b where b.email='${mail("kano")}'`));
  r = await signup("gb", { phone: "+442075550142", country: "GB", city: "London", region: "" }); check("a country we do not serve is refused", r.status === 400 && /United States or Nigeria/.test(r.text), r.text);
  r = await signup("st", { phone: "+16155550142", country: "US", city: "Smallville", region: "Lagos" }); check("a state that is not in the country is refused", r.status === 400 && /state/.test(r.text), r.text);
  r = await signup("nc", { phone: "+16155550142", country: "US", city: "", region: "TN" }); check("a city is required", r.status === 400 && /city/.test(r.text), r.text);
  r = await signup("tr", { phone: "+16155550142", country: "US", city: "Nashville", region: "TN", travels: true, travel_radius_km: 9000 }); check("a travel distance must be sensible", r.status === 400 && /travel/.test(r.text), r.text);

  // ----- it shows up where it is -----
  sql(`update businesses set status='live', verification_status='verified' where email='${mail("boise")}'`);
  r = await get("/places?tests=1"); const pb = r.json.places?.find((p) => p.slug === "boise-id");
  check("once live, its city is a place with one business", pb?.businesses === 1 && pb.categories.barber === 1 && pb.timezone === "America/Denver" && km(pb.lat, pb.lng, boise.lat, boise.lng) < 0.1, JSON.stringify(pb));
  r = await get("/places"); check("test data is not counted among the places customers see", !r.json.places.some((p) => p.slug === "boise-id"), JSON.stringify(r.json.places.map((p) => p.slug)));
  r = await get("/businesses?place=boise-id&quiet=1"); check("it is found by its place", r.json.businesses.length === 1 && r.json.businesses[0].region === "ID" && r.json.businesses[0].timezone === "America/Denver", r.text.slice(0, 200));
  r = await get("/businesses?lat=43.5407&lng=-116.5635&quiet=1"); // Nampa, about 20 miles west
  check("and from the next town, with its distance", r.json.businesses.length === 1 && r.json.businesses[0].distance > 10 && r.json.businesses[0].distance < 25 && r.json.geo.widened === false, JSON.stringify(r.json.businesses.map((x) => [x.slug, x.distance_text])));

  // ----- when the address changes -----
  const hours = { mon: ["09:00", "18:00"], sat: ["09:00", "14:00"] };
  r = await m("PUT", `/locations/${loc.id}`, { name: "Main", address: "710 E Mullan Ave", city: "Coeur d'Alene", region: "ID", arrival_notes: "", hours });
  check("moving to Coeur d'Alene looks the address up again", r.status === 200 && ["found", "approximate"].includes(r.json.position) && r.json.location?.timezone === "America/Los_Angeles" && km(47.68, -116.78, r.json.location.lat, r.json.location.lng) < 20, r.text.slice(0, 400));
  r = await m("GET", "/settings");
  check("and the business moves to Pacific time with it", r.json.business.timezone === "America/Los_Angeles" && r.json.locations[0].timezone === "America/Los_Angeles" && r.json.locations[0].city === "Coeur d'Alene", JSON.stringify([r.json.business.timezone, r.json.locations[0].city]));
  r = await get("/businesses?place=coeur-dalene-id&quiet=1"); check("its place page address follows (coeur-dalene-id)", r.json.businesses.length === 1 && r.json.geo.place?.label === "Coeur d'Alene, ID", r.text.slice(0, 200));
  r = await m("PUT", `/locations/${loc.id}`, { name: "Main", address: "710 E Mullan Ave", city: "Coeur d'Alene", region: "ID", arrival_notes: "", hours, lat: 47.6735, lng: -116.7812 });
  check("a pin dragged by hand is kept as placed", r.json.position === "set by hand" && r.json.location.lat === 47.6735 && r.json.location.position_source === "hand", r.text.slice(0, 300));
  r = await m("PUT", `/locations/${loc.id}`, { name: "Main shop", address: "710 E Mullan Ave", city: "Coeur d'Alene", region: "ID", arrival_notes: "Park behind.", hours, lat: 47.6735, lng: -116.7812 });
  check("saving other details leaves the pin where it was", r.json.position === "kept" && r.json.location.lat === 47.6735 && sql(`select name || '|' || position_source from locations where id='${loc.id}'`) === "Main shop|hand", r.text.slice(0, 300));
  r = await m("PUT", `/locations/${loc.id}`, { name: "Main shop", address: "710 E Mullan Ave", city: "Coeur d'Alene", region: "ID", country: "NG", arrival_notes: "", hours }); check("a location cannot move to the other country", r.status === 400 && /one country/.test(r.text), r.text);
  r = await m("PUT", `/locations/${loc.id}`, { name: "Main shop", address: "710 E Mullan Ave", city: "Coeur d'Alene", region: "ID", arrival_notes: "", hours, travels: true, travel_radius_km: 30 });
  check("a location can be marked as travelling, with how far", r.status === 200 && sql(`select travels || '|' || travel_radius_km from locations where id='${loc.id}'`) === "true|30", r.text);
  r = await m("POST", "/locations", { name: "Second shop", address: "", city: "Boise", region: "ID" });
  check("a second location in another zone keeps its own zone", r.status === 201 && r.json.location?.timezone === "America/Denver", r.text.slice(0, 300));
  r = await m("POST", `/locations/${r.json.id}/action`, { action: "primary" });
  r = await m("GET", "/settings"); check("and making it the main one moves the business to its zone", r.json.business.timezone === "America/Denver", r.json.business.timezone);

  // ----- sales tax goes by state, however the state is written -----
  const brand = sql("select slug from products where business_id is null and shipping and stock > 0 and slug <> 'gift-card' order by slug limit 1");
  const rate = sql("select bp from tax_rates where region='TN'");
  if (brand && rate) {
    const order = { customer_name: "Geo Check", customer_phone: "+16155550871", customer_email: mail("tax"), fulfilment: "ship", items: [{ product_slug: brand, size_label: "", qty: 1 }], address: { line1: "1 Test St", city: "Nashville", region: "TN", postal: "37201" } };
    const a = (await call("POST", "/orders/quote", order)).json.quote || {};
    const z = await call("POST", "/orders/quote", { ...order, address: { ...order.address, region: "Tennessee" } });
    check("an order to 'Tennessee' is taxed as an order to 'TN'", a.tax_cents > 0 && z.json.quote?.tax_cents === a.tax_cents, `${a.tax_cents} and ${z.text.slice(0, 200)}`);
  } else console.log("note: no brand product or no Tennessee tax rate here, so the tax check is skipped");

  try {
    sql(`delete from businesses where email like 'e2e-geo-%-${stamp}@example.test'; delete from merchant_users where email like 'e2e-geo-%-${stamp}@example.test';
      delete from gift_cards where recipient_email = '${mail("to")}'; delete from payments where purpose = 'gift' and payload->>'buyer_email' = '${mail("gift")}';`);
    console.log("cleaned up; test businesses left behind:", sql(`select count(*) from businesses where name like 'E2E Geo %'`));
  } catch (e) { console.log("CLEANUP PROBLEM:", String(e.stderr || e).slice(0, 800)); }
  console.log(`\n${pass} passed, ${fail} failed`);
  process.exit(fail ? 1 : 0);
})();
