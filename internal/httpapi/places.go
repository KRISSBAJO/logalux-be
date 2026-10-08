package httpapi

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"logaluxe/api/internal/geo"
)

// Places. Nothing in LogaLuxe lists cities by hand: the places it serves are
// wherever live businesses are, read from their locations. A person can also
// pick any city or state of the United States or Nigeria, whether or not a
// business is there yet.

// place is a city or a state as the API describes it.
type place struct {
	Slug        string         `json:"slug"`
	Kind        string         `json:"kind"` // city | state | country
	City        string         `json:"city"`
	Region      string         `json:"region"`
	RegionName  string         `json:"region_name"`
	Country     string         `json:"country"`
	CountryName string         `json:"country_name"`
	Label       string         `json:"label"`
	Lat         float64        `json:"lat"`
	Lng         float64        `json:"lng"`
	Businesses  int            `json:"businesses"`
	Categories  map[string]int `json:"categories,omitempty"`
	Currency    string         `json:"currency"`
	Unit        string         `json:"unit"`
	Timezone    string         `json:"timezone"`
	// Set when the place is listed in relation to a point.
	DistanceKm   *float64 `json:"distance_km,omitempty"`
	Distance     *float64 `json:"distance,omitempty"`
	DistanceText string   `json:"distance_text,omitempty"`
}

func newPlace(city, region, country string, lat, lng float64) place {
	p := place{Slug: geo.PlaceSlug(city, region, country), Kind: "city", City: city, Region: region, RegionName: geo.RegionName(country, region), Country: country,
		CountryName: geo.CountryName(country), Label: geo.Label(city, region, country), Lat: lat, Lng: lng, Currency: geo.Currency(country), Unit: geo.Unit(country)}
	if city == "" {
		p.Kind = "state"
	}
	if city == "" && region == "" {
		p.Kind = "country"
		if lat == 0 && lng == 0 {
			p.Lat, p.Lng = geo.CountryCentre(country)
		}
	}
	p.Timezone = geo.Zone(country, region, "", lat, lng, city != "")
	return p
}

func round(n float64, places int) float64 {
	m := math.Pow(10, float64(places))
	return math.Round(n*m) / m
}

// from sets how far the place is from a point, in a unit.
func (p place) from(lat, lng float64, unit string) place {
	km := geo.Km(lat, lng, p.Lat, p.Lng)
	kmR, d := round(km, 2), round(geo.FromKm(km, unit), 1)
	p.DistanceKm, p.Distance, p.DistanceText = &kmR, &d, geo.DistanceText(km, unit)
	return p
}

var placesCache struct {
	sync.Mutex
	at   time.Time
	list []place
}

// citySlugSQL is geo.Slugify written in SQL for a location row "l": "Coeur d'Alene" and "St. Louis"
// become "coeur-dalene" and "st-louis", so a city is matched however its punctuation was typed.
const citySlugSQL = `trim(both '-' from regexp_replace(regexp_replace(lower(btrim(l.city)), '[''’.]', '', 'g'), '[^a-z0-9]+', '-', 'g'))`

// Leftover test data is not a place anyone lives in. The same rule the website uses to keep it out of search engines.
const notTestData = `b.name !~* '^(e2e|scale test|test )'`

// livePlaces is every city with at least one live, listed business, busiest
// first. The centre of a place is the middle of its businesses. The answer is
// kept for a few seconds, since every page of the site asks for it.
func (s *Server) livePlaces(ctx context.Context, withTests bool) ([]place, error) {
	if !withTests {
		placesCache.Lock()
		if time.Since(placesCache.at) < 15*time.Second && placesCache.list != nil {
			list := placesCache.list
			placesCache.Unlock()
			return list, nil
		}
		placesCache.Unlock()
	}
	rs, err := s.pool.Query(ctx, `
		select l.country, l.region, lower(btrim(l.city)) as ckey, mode() within group (order by btrim(l.city)) as city, b.category,
		       count(distinct b.id)::int as n, coalesce(avg(l.lat), 0)::float8 as lat, coalesce(avg(l.lng), 0)::float8 as lng, count(l.lat)::int as placed,
		       mode() within group (order by l.timezone) as tz
		from locations l join businesses b on b.id = l.business_id
		where b.status = 'live' and coalesce((b.settings->'booking'->>'on_search')::boolean, true)
		  and btrim(l.city) <> '' and l.region <> '' and l.country in ('US','NG') and ($1 or `+notTestData+`)
		group by l.country, l.region, lower(btrim(l.city)), b.category`, withTests)
	if err != nil {
		return nil, err
	}
	defer rs.Close()
	type acc struct {
		p            place
		top, placed  int
		latSum, lngS float64
		zones        map[string]int
	}
	found := map[string]*acc{}
	var order []string
	for rs.Next() {
		var country, region, ckey, city, category, tz string
		var n, placedN int
		var lat, lng float64
		if err := rs.Scan(&country, &region, &ckey, &city, &category, &n, &lat, &lng, &placedN, &tz); err != nil {
			return nil, err
		}
		slug := geo.PlaceSlug(city, region, country)
		if slug == "" {
			continue // a state we cannot read; the start-up check tidies these
		}
		a := found[slug]
		if a == nil {
			a = &acc{p: newPlace(city, region, country, 0, 0), zones: map[string]int{}}
			a.p.Categories = map[string]int{}
			found[slug] = a
			order = append(order, slug)
		}
		a.p.Businesses += n
		a.p.Categories[category] += n
		if n > a.top {
			a.top, a.p.City = n, city
		}
		a.placed += placedN
		a.latSum += lat * float64(placedN)
		a.lngS += lng * float64(placedN)
		a.zones[tz] += n
	}
	if err := rs.Err(); err != nil {
		return nil, err
	}
	out := make([]place, 0, len(order))
	for _, slug := range order {
		a := found[slug]
		p := a.p
		if c, ok := geo.FindCity(p.City, p.Region, p.Country); ok {
			p.City = c.Name
			p.Lat, p.Lng = c.Lat, c.Lng
		}
		if a.placed > 0 {
			p.Lat, p.Lng = round(a.latSum/float64(a.placed), 4), round(a.lngS/float64(a.placed), 4)
		}
		p.Label = geo.Label(p.City, p.Region, p.Country)
		best := 0
		for z, n := range a.zones {
			if n > best || (n == best && z < p.Timezone) {
				best, p.Timezone = n, z
			}
		}
		out = append(out, p)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Businesses != out[j].Businesses {
			return out[i].Businesses > out[j].Businesses
		}
		return out[i].Label < out[j].Label
	})
	if !withTests {
		placesCache.Lock()
		placesCache.at, placesCache.list = time.Now(), out
		placesCache.Unlock()
	}
	return out, nil
}

// nearestPlaces is the few places with businesses closest to a point, nearest first.
func nearestPlaces(all []place, lat, lng float64, unit, skipSlug string, max int) []place {
	out := make([]place, 0, len(all))
	for _, p := range all {
		if p.Slug != skipSlug && (p.Lat != 0 || p.Lng != 0) {
			out = append(out, p.from(lat, lng, unit))
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return *out[i].DistanceKm < *out[j].DistanceKm })
	if len(out) > max {
		out = out[:max]
	}
	return out
}

func pointParam(latS, lngS string) (lat, lng float64, given, ok bool) {
	if strings.TrimSpace(latS) == "" && strings.TrimSpace(lngS) == "" {
		return 0, 0, false, true
	}
	lat, err1 := strconv.ParseFloat(strings.TrimSpace(latS), 64)
	lng, err2 := strconv.ParseFloat(strings.TrimSpace(lngS), 64)
	if err1 != nil || err2 != nil || !geo.ValidPoint(lat, lng) {
		return 0, 0, true, false
	}
	return lat, lng, true, true
}

// GET /v1/places?country=&category=&lat=&lng=&limit=
// The places that have live businesses, busiest first, or nearest first when a point is given.
func (s *Server) listPlaces(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	all, err := s.livePlaces(r.Context(), q.Get("tests") == "1")
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	lat, lng, near, ok := pointParam(q.Get("lat"), q.Get("lng"))
	if !ok {
		writeErr(w, 400, "lat and lng must be a point on Earth")
		return
	}
	country, category := geo.CleanCountry(q.Get("country")), q.Get("category")
	unit := geo.Unit(country)
	if country == "" && near {
		unit = geo.Unit(geo.CountryOfPoint(lat, lng))
	}
	if u := q.Get("unit"); u == "mi" || u == "km" {
		unit = u
	}
	// What each country holds, whatever the filters, so a picker can offer both.
	type countryOut struct {
		Code       string `json:"code"`
		Name       string `json:"name"`
		Currency   string `json:"currency"`
		Unit       string `json:"unit"`
		Places     int    `json:"places"`
		Businesses int    `json:"businesses"`
	}
	countries := make([]countryOut, 0, len(geo.Countries))
	for _, c := range geo.Countries {
		co := countryOut{Code: c, Name: geo.CountryName(c), Currency: geo.Currency(c), Unit: geo.Unit(c)}
		for _, p := range all {
			if p.Country == c {
				co.Places++
				co.Businesses += p.Businesses
			}
		}
		countries = append(countries, co)
	}
	out := make([]place, 0, len(all))
	for _, p := range all {
		if country != "" && p.Country != country {
			continue
		}
		if category != "" {
			n := p.Categories[category]
			if n == 0 {
				continue
			}
			p.Businesses = n
		}
		if near {
			p = p.from(lat, lng, unit)
		}
		out = append(out, p)
	}
	if near {
		sort.SliceStable(out, func(i, j int) bool { return *out[i].DistanceKm < *out[j].DistanceKm })
	} else if category != "" {
		sort.SliceStable(out, func(i, j int) bool { return out[i].Businesses > out[j].Businesses })
	}
	total := len(out)
	if limit, _ := strconv.Atoi(q.Get("limit")); limit > 0 && limit < len(out) {
		out = out[:limit]
	}
	// The place to show someone we know nothing about: the first of this list.
	var first *place
	if len(out) > 0 {
		first = &out[0]
	}
	writeJSON(w, 200, M{"places": out, "total": total, "default": first, "countries": countries})
}

func matchesText(p place, f string) bool {
	if f == "" {
		return false
	}
	// A city answers to its name, with or without its state after it. A state answers to its name or its code.
	names := []string{p.City, p.City + " " + p.Region, p.City + " " + strings.TrimSuffix(p.RegionName, " State"), p.Label}
	if p.Kind == "state" {
		names = []string{p.RegionName, p.Label}
	}
	if p.Kind == "country" {
		f = strings.TrimPrefix(strings.TrimPrefix(f, "all of "), "the ")
		names = []string{p.CountryName, "all of " + p.CountryName}
		if p.Country == "US" {
			names = append(names, "usa", "us", "america")
		}
	}
	for _, s := range names {
		if g := foldText(s); g != "" && strings.HasPrefix(g, f) {
			return true
		}
	}
	return p.Kind == "state" && strings.EqualFold(p.Region, f)
}

func foldText(s string) string {
	s = strings.NewReplacer(".", "", ",", " ", "-", " ").Replace(strings.ToLower(s))
	return strings.Join(strings.Fields(s), " ")
}

// GET /v1/places/search?q=&country=&lookup=1&limit=
// (a whole country, a state or a city; country keeps the answer to one country)
// Places for a picker. Without lookup it answers from what is already known
// (places with businesses, the states, well-known cities, cities found before),
// which is safe to call as a person types. With lookup=1 it also asks the
// lookup service for any city or town of the United States or Nigeria; send
// that only when the person asks for a search.
func (s *Server) searchPlaces(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	q := r.URL.Query()
	text := strings.TrimSpace(q.Get("q"))
	if len(text) > 80 {
		text = text[:80]
	}
	f := foldText(text)
	country := geo.CleanCountry(q.Get("country"))
	limit, _ := strconv.Atoi(q.Get("limit"))
	if limit <= 0 || limit > 20 {
		limit = 8
	}
	live, err := s.livePlaces(ctx, q.Get("tests") == "1")
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	bySlug := map[string]place{}
	for _, p := range live {
		bySlug[p.Slug] = p
	}
	var out []place
	seen := map[string]bool{}
	add := func(p place) {
		if p.Slug == "" || seen[p.Slug] || (country != "" && p.Country != country) {
			return
		}
		seen[p.Slug] = true
		if l, ok := bySlug[p.Slug]; ok {
			p = l // with its businesses and its real centre
		}
		out = append(out, p)
	}
	if f == "" { // nothing typed yet: the places with businesses
		for _, p := range live {
			add(p)
		}
	} else {
		for _, p := range live {
			if matchesText(p, f) {
				add(p)
			}
		}
		// A whole country: "All of Nigeria".
		for _, c := range geo.Countries {
			if p := newPlace("", "", c, 0, 0); matchesText(p, f) {
				for _, l := range live {
					if l.Country == c {
						p.Businesses += l.Businesses
					}
				}
				add(p)
			}
		}
		for _, c := range geo.Countries {
			for _, st := range geo.States(c) {
				region, _ := geo.Region(c, st.Name)
				p := newPlace("", region, c, st.Lat, st.Lng)
				if matchesText(p, f) {
					for _, l := range live {
						if l.Country == c && l.Region == region {
							p.Businesses += l.Businesses
						}
					}
					add(p)
				}
			}
		}
		for _, c := range geo.Cities {
			if p := newPlace(c.Name, c.Region, c.Country, c.Lat, c.Lng); matchesText(p, f) {
				add(p)
			}
		}
		// Cities looked up before. The first word is enough to find them; the rest is checked here.
		first := strings.Fields(f)[0]
		if known, err := rows(ctx, s.pool, `select city, region, country, lat, lng from geo_places where lower(city) like $1 || '%' order by city limit 40`, first); err == nil {
			for _, k := range known {
				p := newPlace(k["city"].(string), k["region"].(string), k["country"].(string), k["lat"].(float64), k["lng"].(float64))
				if matchesText(p, f) {
					add(p)
				}
			}
		}
	}
	lookedUp, complete := false, true
	if q.Get("lookup") == "1" && len(f) >= 3 {
		hits, asked := s.geoPlaces(ctx, text, country)
		lookedUp, complete = true, asked
		for _, h := range hits {
			add(newPlace(h.City, h.Region, h.Country, h.Lat, h.Lng))
		}
	}
	if len(out) > limit {
		out = out[:limit]
	}
	if out == nil {
		out = []place{}
	}
	// complete is false when the lookup service could not be asked just now; what is listed is still right.
	writeJSON(w, 200, M{"places": out, "looked_up": lookedUp, "complete": complete})
}

// resolvePlace turns a slug into a place. It knows the places with businesses,
// the states, the well-known cities and cities looked up before; with lookup
// it also asks the lookup service about a city it has not met. canonical is
// the slug the place should be reached by, when that differs from the one given
// (an old address like "nashville" is now "nashville-tn").
func (s *Server) resolvePlace(ctx context.Context, slug string, lookup bool) (p place, canonical string, ok bool) {
	slug = strings.ToLower(strings.TrimSpace(slug))
	live, _ := s.livePlaces(ctx, false)
	city, region, country, parsed := geo.ParseSlug(slug)
	if !parsed {
		// A bare city name. The place of that name with the most businesses, or failing that a well-known city.
		for _, l := range live {
			if geo.Slugify(l.City) == slug {
				return l, l.Slug, true
			}
		}
		for _, c := range geo.Cities {
			if geo.Slugify(c.Name) == slug {
				p = newPlace(c.Name, c.Region, c.Country, c.Lat, c.Lng)
				return p, p.Slug, true
			}
		}
		return place{}, "", false
	}
	if city == "" {
		st, _ := geo.FindState(country, region) // no state at all: the whole country
		p = newPlace("", region, country, st.Lat, st.Lng)
		p.Categories = map[string]int{}
		for _, l := range live {
			if l.Country == country && (region == "" || l.Region == region) {
				p.Businesses += l.Businesses
				for c, n := range l.Categories {
					p.Categories[c] += n
				}
			}
		}
		return p, "", true
	}
	for _, l := range live {
		if l.Slug == slug {
			return l, "", true
		}
	}
	var name string
	var lat, lng float64
	if err := s.pool.QueryRow(ctx, `select city, lat, lng from geo_places where slug=$1`, slug).Scan(&name, &lat, &lng); err == nil {
		return newPlace(name, region, country, lat, lng), "", true
	}
	// A city some business is in, though none is live there yet: its spelling and the middle of what is there.
	var spelled *string
	var mLat, mLng *float64
	if err := s.pool.QueryRow(ctx, `select mode() within group (order by btrim(l.city)), avg(l.lat), avg(l.lng) from locations l
		where l.country=$1 and l.region=$2 and `+citySlugSQL+` = $3`, country, region, geo.Slugify(city)).Scan(&spelled, &mLat, &mLng); err == nil && spelled != nil {
		return newPlace(*spelled, region, country, deref(mLat), deref(mLng)), "", true
	}
	for _, c := range geo.Cities {
		if c.Country == country && c.Region == region && geo.Slugify(c.Name) == geo.Slugify(city) {
			return newPlace(c.Name, c.Region, c.Country, c.Lat, c.Lng), "", true
		}
	}
	if lookup {
		hits, _ := s.geoPlaces(ctx, city+", "+geo.RegionName(country, region), country)
		for _, h := range hits {
			if geo.PlaceSlug(h.City, h.Region, h.Country) == slug {
				return newPlace(h.City, h.Region, h.Country, h.Lat, h.Lng), "", true
			}
		}
	}
	return place{}, "", false
}

// GET /v1/places/{slug}
// One place, the nearest places that have businesses, and the slug to redirect to when the one given is an old form.
func (s *Server) getPlace(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	p, canonical, ok := s.resolvePlace(ctx, chi.URLParam(r, "slug"), true)
	if !ok {
		writeErr(w, 404, "we do not know that place")
		return
	}
	live, _ := s.livePlaces(ctx, false)
	writeJSON(w, 200, M{"place": p, "canonical": canonical, "nearest": nearestPlaces(live, p.Lat, p.Lng, p.Unit, p.Slug, 6)})
}

// GET /v1/places/reverse?lat=&lng=
// The place a point is in, for "Near me". The point is rounded to about a
// kilometre before anything is looked up or kept.
func (s *Server) reversePlace(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	lat, lng, given, ok := pointParam(r.URL.Query().Get("lat"), r.URL.Query().Get("lng"))
	if !given || !ok {
		writeErr(w, 400, "lat and lng must be a point on Earth")
		return
	}
	lat, lng = round(lat, 2), round(lng, 2)
	live, _ := s.livePlaces(ctx, false)
	country := geo.CountryOfPoint(lat, lng)
	var p place
	source := "lookup"
	// The nearest city we know of in a state, within a distance.
	known := func(region, country string, within float64) (best place, ok bool) {
		consider := func(c place) {
			if c.Country != country || (region != "" && c.Region != region) {
				return
			}
			if km := geo.Km(lat, lng, c.Lat, c.Lng); km < within {
				best, within, ok = c, km, true
			}
		}
		for _, l := range live {
			consider(l)
		}
		for _, c := range geo.Cities {
			consider(newPlace(c.Name, c.Region, c.Country, c.Lat, c.Lng))
		}
		return best, ok
	}
	if h, found := s.geoReverse(ctx, lat, lng); found {
		country = h.Country
		// Where the map can only name a county or a council area ("Eti Osa"), the city it belongs to
		// is what a person would say ("Lagos"), when one we know is close by.
		if !settlement[h.Kind] && geo.Supported(h.Country) {
			if c, ok := known(h.Region, h.Country, 30); ok {
				h.City = c.City
			}
		}
		if geo.Supported(h.Country) && geo.PlaceSlug(h.City, h.Region, h.Country) != "" {
			p = newPlace(h.City, h.Region, h.Country, lat, lng)
		} else {
			// Somewhere we do not serve, or a spot with no town to name. It still has a name and a position.
			p = place{Kind: "point", City: h.City, Region: h.Region, Country: h.Country, CountryName: geo.CountryName(h.Country), Lat: lat, Lng: lng, Unit: geo.Unit(h.Country)}
			p.Label = strings.Trim(strings.Join([]string{h.City, h.Region}, ", "), ", ")
			if p.Label == "" {
				p.Label = "Your location"
			}
		}
	} else {
		// The lookup did not answer. The nearest city we know stands in, and says so.
		source = "nearest"
		best := place{}
		for _, c := range geo.Countries {
			if b, ok := known("", c, 80); ok && (best.Slug == "" || geo.Km(lat, lng, b.Lat, b.Lng) < geo.Km(lat, lng, best.Lat, best.Lng)) {
				best = b
			}
		}
		if best.Slug != "" {
			p = newPlace(best.City, best.Region, best.Country, lat, lng)
			country = best.Country
		} else {
			source = "none"
			p = place{Kind: "point", Label: "Your location", Country: country, CountryName: geo.CountryName(country), Lat: lat, Lng: lng, Unit: geo.Unit(country)}
		}
	}
	if l, ok := findPlace(live, p.Slug); ok && p.Slug != "" {
		p.Businesses, p.Categories = l.Businesses, l.Categories
	}
	// served: a business can trade there. The product still works elsewhere, by choosing a place.
	writeJSON(w, 200, M{"place": p, "source": source, "country": country, "served": geo.Supported(country), "unit": geo.Unit(country),
		"nearest": nearestPlaces(live, lat, lng, geo.Unit(country), "", 3)})
}

func findPlace(list []place, slug string) (place, bool) {
	for _, p := range list {
		if p.Slug == slug {
			return p, true
		}
	}
	return place{}, false
}

// POST /v1/places/locate   {address, city, region, country, lat?, lng?}
// Where an address lands on the map and which time zone it is in, before anything is saved,
// so a sign-up or location form can show the pin and let a wrong one be corrected.
func (s *Server) locatePlace(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Address string   `json:"address"`
		City    string   `json:"city"`
		Region  string   `json:"region"`
		Country string   `json:"country"`
		Lat     *float64 `json:"lat"`
		Lng     *float64 `json:"lng"`
	}
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, "invalid json")
		return
	}
	p, why := s.placeLocation(r.Context(), req.Address, req.City, req.Region, req.Country, req.Lat, req.Lng)
	if why != "" {
		writeErr(w, 400, why)
		return
	}
	writeJSON(w, 200, M{"location": p, "position": positionWord(p.Source, false), "timezone_name": geo.ZoneLabel(p.Timezone),
		"place": newPlace(p.City, p.Region, p.Country, deref(p.Lat), deref(p.Lng)), "currency": geo.Currency(p.Country)})
}

func deref(f *float64) float64 {
	if f == nil {
		return 0
	}
	return *f
}

// GET /v1/places/states?country=US
// The states of a country, for a form's list.
func (s *Server) listStates(w http.ResponseWriter, r *http.Request) {
	type stateOut struct {
		Value string `json:"value"` // what a location stores
		Code  string `json:"code"`
		Name  string `json:"name"`
	}
	out := M{}
	for _, c := range geo.Countries {
		if want := geo.CleanCountry(r.URL.Query().Get("country")); want != "" && want != c {
			continue
		}
		list := []stateOut{}
		for _, st := range geo.States(c) {
			region, _ := geo.Region(c, st.Name)
			list = append(list, stateOut{Value: region, Code: st.Code, Name: geo.RegionName(c, region)})
		}
		sort.SliceStable(list, func(i, j int) bool { return list[i].Name < list[j].Name })
		out[c] = list
	}
	writeJSON(w, 200, M{"states": out})
}

// BackfillGeo puts existing locations in order once: the country and state in
// their stored form, and the time zone that belongs to where each one is. It
// asks no outside service. A business's own zone follows its main location.
func BackfillGeo(ctx context.Context, pool *pgxpool.Pool) error {
	const task = "zones-v1"
	var done bool
	if err := pool.QueryRow(ctx, `select exists(select 1 from geo_tasks where name=$1)`, task).Scan(&done); err != nil || done {
		return err
	}
	list, err := rows(ctx, pool, `select l.id::text as id, l.city, l.region, l.country, l.county, l.lat, l.lng, l.timezone, b.market
		from locations l join businesses b on b.id = l.business_id`)
	if err != nil {
		return err
	}
	changed := 0
	for _, l := range list {
		country := geo.CleanCountry(fmt.Sprint(l["country"]))
		if country == "" {
			country = geo.CleanCountry(fmt.Sprint(l["market"]))
		}
		city, was := fmt.Sprint(l["city"]), fmt.Sprint(l["region"])
		region, ok := geo.Region(country, was)
		if !ok {
			if c, found := geo.FindCity(city, "", country); found && strings.TrimSpace(was) == "" {
				region, ok = c.Region, true
			} else {
				region = was // left as it was typed; it cannot be read as a state
			}
		}
		lat, hasLat := l["lat"].(float64)
		lng, hasLng := l["lng"].(float64)
		zone := geo.Zone(country, region, fmt.Sprint(l["county"]), lat, lng, hasLat && hasLng)
		if zone == "" {
			zone = fmt.Sprint(l["timezone"])
		}
		if country == fmt.Sprint(l["country"]) && region == was && zone == fmt.Sprint(l["timezone"]) {
			continue
		}
		if _, err := pool.Exec(ctx, `update locations set country=$2, region=$3, timezone=$4 where id=$1`, l["id"], country, region, zone); err != nil {
			return err
		}
		changed++
	}
	tag, err := pool.Exec(ctx, `update businesses b set timezone = l.timezone from locations l where l.business_id = b.id and l.is_primary and l.timezone <> '' and b.timezone <> l.timezone`)
	if err != nil {
		return err
	}
	note := fmt.Sprintf("%d of %d locations corrected, %d business time zones changed", changed, len(list), tag.RowsAffected())
	slog.Info("places put in order", "note", note)
	_, err = pool.Exec(ctx, `insert into geo_tasks (name, note) values ($1,$2) on conflict do nothing`, task, note)
	return err
}
