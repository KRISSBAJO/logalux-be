package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"logaluxe/api/internal/geo"
)

// Looking places up. LogaLuxe asks OpenStreetMap's Nominatim service three
// kinds of question: where an address is, which places match some text, and
// which place a point is in. Its usage policy is followed here in one place:
// a real User-Agent, at most one request a second from the whole API, and
// every answer kept in geo_cache so the same question is never sent twice.
// Nothing is asked as a person types; only when they ask for a search.

const (
	nominatimURL   = "https://nominatim.openstreetmap.org"
	nominatimAgent = "LogaLuxe/1.0 (+https://logaluxe.com/help)"
	nominatimGap   = 1100 * time.Millisecond // a little over a second between requests
	nominatimQueue = 4 * time.Second         // rather than wait longer than this, answer without the lookup
	missFor        = 30 * 24 * time.Hour     // "nothing found" is believed for a month, then asked again
)

var nominatimGate struct {
	sync.Mutex
	next time.Time
}

// geoHit is one place the lookup service found.
type geoHit struct {
	Lat     float64 `json:"lat"`
	Lng     float64 `json:"lng"`
	City    string  `json:"city"`
	Region  string  `json:"region"` // as a location stores it when the country is one we serve
	County  string  `json:"county"`
	Country string  `json:"country"` // two letters, upper case
	Display string  `json:"display"`
	Kind    string  `json:"kind"`
}

// The kinds of result that are a city, a town or part of one.
var settlement = map[string]bool{"city": true, "town": true, "village": true, "hamlet": true, "municipality": true, "borough": true, "suburb": true}

type nominatimItem struct {
	Lat         string            `json:"lat"`
	Lon         string            `json:"lon"`
	Name        string            `json:"name"`
	DisplayName string            `json:"display_name"`
	AddressType string            `json:"addresstype"`
	Address     map[string]string `json:"address"`
}

func (it nominatimItem) hit() (geoHit, bool) {
	lat, err1 := strconv.ParseFloat(it.Lat, 64)
	lng, err2 := strconv.ParseFloat(it.Lon, 64)
	if err1 != nil || err2 != nil || !geo.ValidPoint(lat, lng) {
		return geoHit{}, false
	}
	a := it.Address
	h := geoHit{Lat: lat, Lng: lng, Country: strings.ToUpper(a["country_code"]), County: a["county"], Display: it.DisplayName, Kind: it.AddressType}
	// The state, by its ISO code when given ("US-TN", "NG-LA"), else by its name.
	if code := a["ISO3166-2-lvl4"]; strings.HasPrefix(code, h.Country+"-") {
		h.Region, _ = geo.Region(h.Country, strings.TrimPrefix(code, h.Country+"-"))
	}
	if h.Region == "" {
		if r, ok := geo.Region(h.Country, a["state"]); ok {
			h.Region = r
		} else {
			h.Region = a["state"]
		}
	}
	if settlement[it.AddressType] {
		h.City = it.Name
	}
	for _, k := range []string{"city", "town", "village", "municipality", "hamlet", "suburb", "city_district", "county"} {
		if h.City == "" {
			h.City = a[k]
		}
	}
	h.City = strings.TrimPrefix(h.City, "City of ")
	return h, true
}

// nominatim sends one question, after waiting its turn. It reports false when
// the service could not be asked or did not answer; that is not "nothing found".
func nominatim(ctx context.Context, path string, q url.Values, out any) bool {
	nominatimGate.Lock()
	now := time.Now()
	turn := nominatimGate.next
	if turn.Before(now) {
		turn = now
	}
	if turn.Sub(now) > nominatimQueue {
		nominatimGate.Unlock()
		return false
	}
	nominatimGate.next = turn.Add(nominatimGap)
	nominatimGate.Unlock()
	select {
	case <-ctx.Done():
		return false
	case <-time.After(turn.Sub(now)):
	}
	ctx, cancel := context.WithTimeout(ctx, 6*time.Second)
	defer cancel()
	q.Set("format", "jsonv2")
	q.Set("addressdetails", "1")
	q.Set("accept-language", "en")
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, nominatimURL+path+"?"+q.Encode(), nil)
	if err != nil {
		return false
	}
	req.Header.Set("User-Agent", nominatimAgent)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		slog.Warn("place lookup failed", "path", path, "err", err)
		return false
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		slog.Warn("place lookup refused", "path", path, "status", res.StatusCode)
		return false
	}
	return json.NewDecoder(res.Body).Decode(out) == nil
}

// geoCached reads a kept answer. found is false when the question was never
// asked, or was answered "nothing" long enough ago to ask again.
func (s *Server) geoCached(ctx context.Context, kind, key string, out any) (found bool) {
	var raw []byte
	var at time.Time
	if err := s.pool.QueryRow(ctx, `select answer, created_at from geo_cache where kind=$1 and key=$2`, kind, key).Scan(&raw, &at); err != nil {
		return false
	}
	if string(raw) == "null" || string(raw) == "[]" {
		return time.Since(at) < missFor
	}
	return json.Unmarshal(raw, out) == nil
}

func (s *Server) geoRemember(ctx context.Context, kind, key string, answer any) {
	raw, err := json.Marshal(answer)
	if err != nil {
		return
	}
	_, _ = s.pool.Exec(ctx, `insert into geo_cache (kind, key, answer) values ($1,$2,$3::jsonb)
		on conflict (kind, key) do update set answer = excluded.answer, created_at = now()`, kind, key, string(raw))
}

func cacheKey(parts ...string) string {
	var clean []string
	for _, p := range parts {
		if p = strings.Join(strings.Fields(strings.ToLower(p)), " "); p != "" {
			clean = append(clean, p)
		}
	}
	return strings.Join(clean, ", ")
}

// geoAddress finds where an address is. ok is false when it was not found or
// the service could not be asked.
func (s *Server) geoAddress(ctx context.Context, address, city, region, country string) (hit geoHit, ok bool) {
	if strings.TrimSpace(address) == "" || strings.TrimSpace(city) == "" {
		return geoHit{}, false
	}
	key := cacheKey(address, city, region, country)
	var kept *geoHit
	if s.geoCached(ctx, "address", key, &kept) {
		if kept == nil {
			return geoHit{}, false
		}
		return *kept, true
	}
	q := url.Values{"limit": {"1"}, "q": {strings.Join([]string{address, city, geo.RegionName(country, region), geo.CountryName(country)}, ", ")}}
	if geo.Supported(country) {
		q.Set("countrycodes", strings.ToLower(country))
	}
	var items []nominatimItem
	if !nominatim(ctx, "/search", q, &items) {
		return geoHit{}, false
	}
	if len(items) > 0 {
		if h, good := items[0].hit(); good {
			s.geoRemember(ctx, "address", key, h)
			return h, true
		}
	}
	s.geoRemember(ctx, "address", key, nil)
	return geoHit{}, false
}

// geoPlaces finds the cities and towns of the United States and Nigeria that
// match some text. asked is false when the service could not be asked.
func (s *Server) geoPlaces(ctx context.Context, text, country string) (hits []geoHit, asked bool) {
	codes := "us,ng"
	if geo.Supported(country) {
		codes = strings.ToLower(country)
	}
	key := cacheKey(text) + " @" + codes
	if s.geoCached(ctx, "places", key, &hits) {
		return hits, true
	}
	var items []nominatimItem
	if !nominatim(ctx, "/search", url.Values{"limit": {"8"}, "q": {text}, "countrycodes": {codes}, "featureType": {"settlement"}}, &items) {
		return nil, false
	}
	seen := map[string]bool{}
	hits = []geoHit{}
	for _, it := range items {
		h, good := it.hit()
		if !good || h.City == "" || !geo.Supported(h.Country) || !settlement[it.AddressType] {
			continue // a county or a region is not somewhere a person says they live
		}
		slug := geo.PlaceSlug(h.City, h.Region, h.Country)
		if slug == "" || seen[slug] {
			continue
		}
		seen[slug] = true
		hits = append(hits, h)
		s.rememberPlace(ctx, h)
	}
	s.geoRemember(ctx, "places", key, hits)
	return hits, true
}

// geoReverse finds the city or town a point is in. The point is rounded to two
// decimal places (about a kilometre), which is as exact as a city needs and
// lets neighbours share one answer.
func (s *Server) geoReverse(ctx context.Context, lat, lng float64) (hit geoHit, ok bool) {
	lat, lng = math.Round(lat*100)/100, math.Round(lng*100)/100
	key := fmt.Sprintf("%.2f,%.2f", lat, lng)
	var kept *geoHit
	if s.geoCached(ctx, "reverse", key, &kept) {
		if kept == nil {
			return geoHit{}, false
		}
		return *kept, true
	}
	var item nominatimItem
	if !nominatim(ctx, "/reverse", url.Values{"lat": {fmt.Sprintf("%.2f", lat)}, "lon": {fmt.Sprintf("%.2f", lng)}, "zoom": {"10"}}, &item) {
		return geoHit{}, false
	}
	h, good := item.hit()
	if !good || h.Country == "" {
		s.geoRemember(ctx, "reverse", key, nil) // open sea, or nowhere the map names
		return geoHit{}, false
	}
	h.Lat, h.Lng = lat, lng // the point asked about, not the centre of whatever was found
	s.geoRemember(ctx, "reverse", key, h)
	return h, true
}

// rememberPlace keeps a city the lookup service found, so the picker offers it straight away next time.
func (s *Server) rememberPlace(ctx context.Context, h geoHit) {
	slug := geo.PlaceSlug(h.City, h.Region, h.Country)
	if slug == "" {
		return
	}
	_, _ = s.pool.Exec(ctx, `insert into geo_places (slug, city, region, country, county, lat, lng) values ($1,$2,$3,$4,$5,$6,$7) on conflict (slug) do nothing`,
		slug, h.City, h.Region, h.Country, h.County, h.Lat, h.Lng)
}

// cityCentre is the centre of a city we already know: one of the well-known
// ones, or one looked up before. It never asks the lookup service.
func (s *Server) cityCentre(ctx context.Context, city, region, country string) (lat, lng float64, county string, ok bool) {
	if slug := geo.PlaceSlug(city, region, country); slug != "" {
		if err := s.pool.QueryRow(ctx, `select lat, lng, county from geo_places where slug=$1`, slug).Scan(&lat, &lng, &county); err == nil {
			return lat, lng, county, true
		}
	}
	if c, found := geo.FindCity(city, region, country); found {
		return c.Lat, c.Lng, "", true
	}
	return 0, 0, "", false
}

// ---------- placing a location ----------

// placed is a location's address made regular, with its position and time zone.
type placed struct {
	Address  string   `json:"address"`
	City     string   `json:"city"`
	Region   string   `json:"region"`
	Country  string   `json:"country"`
	County   string   `json:"county"`
	Lat      *float64 `json:"lat"`
	Lng      *float64 `json:"lng"`
	Timezone string   `json:"timezone"`
	// How the position was reached: "address" (found from the street address), "city" (only the city
	// was found; the pin is its centre), "hand" (placed by a person), "" (no position).
	Source string `json:"position_source"`
	// What the map matched, to show beside the pin so a wrong match can be seen.
	Matched string `json:"matched"`
}

// placeLocation checks and completes where a location is. The country must be
// one LogaLuxe serves and the state one of its states. A position given by
// hand is kept; otherwise the address is looked up, and failing that the
// city. why is a message for the person when the place cannot be accepted.
func (s *Server) placeLocation(ctx context.Context, address, city, region, country string, lat, lng *float64) (p placed, why string) {
	p.Address, p.City = strings.TrimSpace(address), strings.Join(strings.Fields(city), " ")
	p.Country = geo.CleanCountry(country)
	if p.Country == "" {
		return p, "choose the United States or Nigeria"
	}
	if p.City == "" || len(p.City) > 80 {
		return p, "enter the city or town"
	}
	if len(p.Address) > 200 {
		return p, "keep the street address under 200 characters"
	}
	if r, ok := geo.Region(p.Country, region); ok {
		p.Region = r
	} else if c, found := geo.FindCity(p.City, "", p.Country); found && strings.TrimSpace(region) == "" {
		p.Region = c.Region // a well-known city says which state it is in
	} else if strings.TrimSpace(region) == "" {
		return p, "choose the state"
	} else {
		return p, "we do not know a state called " + strings.TrimSpace(region) + " in " + geo.InCountry(p.Country)
	}
	if (lat == nil) != (lng == nil) {
		return p, "give both latitude and longitude, or neither"
	}
	switch {
	case lat != nil:
		if !geo.ValidPoint(*lat, *lng) {
			return p, "that map position is not on Earth; latitude is -90 to 90 and longitude -180 to 180"
		}
		if other := geo.CountryOfPoint(*lat, *lng); other != "" && other != p.Country {
			return p, "that pin is in " + geo.InCountry(other) + ", but the address is in " + geo.InCountry(p.Country) + "; move the pin or correct the address"
		}
		p.Lat, p.Lng, p.Source = lat, lng, "hand"
		// The county settles the time zone where a state is split. Ask for it only there.
		if p.Country == "US" && splitState[p.Region] {
			if h, ok := s.geoReverse(ctx, *lat, *lng); ok && h.Country == p.Country && h.Region == p.Region {
				p.County = h.County
			}
		}
	default:
		if h, ok := s.geoAddress(ctx, p.Address, p.City, p.Region, p.Country); ok && h.Country == p.Country && (h.Region == "" || h.Region == p.Region) {
			la, ln := h.Lat, h.Lng
			p.Lat, p.Lng, p.County, p.Source, p.Matched = &la, &ln, h.County, "address", h.Display
			break
		}
		if la, ln, county, ok := s.cityCentre(ctx, p.City, p.Region, p.Country); ok {
			p.Lat, p.Lng, p.County, p.Source, p.Matched = &la, &ln, county, "city", geo.Label(p.City, p.Region, p.Country)
			break
		}
		// A city we have not met: ask for it once; the answer is remembered.
		if hits, _ := s.geoPlaces(ctx, p.City+", "+geo.RegionName(p.Country, p.Region), p.Country); len(hits) > 0 {
			for _, h := range hits {
				if h.Region == p.Region {
					la, ln := h.Lat, h.Lng
					p.Lat, p.Lng, p.County, p.Source, p.Matched = &la, &ln, h.County, "city", geo.Label(h.City, h.Region, h.Country)
					if strings.EqualFold(h.City, p.City) {
						p.City = h.City // the map's spelling and capitals
					}
					break
				}
			}
		}
	}
	if c, found := geo.FindCity(p.City, p.Region, p.Country); found {
		p.City = c.Name
	}
	if p.Lat != nil {
		p.Timezone = geo.Zone(p.Country, p.Region, p.County, *p.Lat, *p.Lng, true)
	} else {
		p.Timezone = geo.Zone(p.Country, p.Region, "", 0, 0, false)
	}
	return p, ""
}

// The states two time zones share, where a county is worth asking for.
var splitState = map[string]bool{"FL": true, "IN": true, "KY": true, "TN": true, "MI": true, "KS": true, "TX": true, "OR": true, "ID": true, "ND": true, "SD": true, "NE": true}

// positionWord is how a save reports what happened to the pin, in the words the screens already use.
func positionWord(source string, kept bool) string {
	switch {
	case kept:
		return "kept"
	case source == "address":
		return "found"
	case source == "city":
		return "approximate"
	case source == "hand":
		return "set by hand"
	}
	return "not found"
}

// syncBusinessZone sets a business's time zone from its main location. Bookings
// and opening hours are read in the business's zone, so it follows the main address.
func (s *Server) syncBusinessZone(ctx context.Context, businessID string) {
	_, _ = s.pool.Exec(ctx, `update businesses b set timezone = l.timezone from locations l
		where l.business_id = b.id and l.is_primary and b.id = $1 and l.timezone <> '' and b.timezone <> l.timezone`, businessID)
}

// oldLocation is a location as it stands before a save.
type oldLocation struct {
	BusinessID, Address, City, Region, Country, County, Timezone, Source string
	Lat, Lng                                                             *float64
	Primary, Travels                                                     bool
}

func (s *Server) loadLocation(ctx context.Context, id, businessID string) (o oldLocation, err error) {
	err = s.pool.QueryRow(ctx, `select business_id::text, address, city, region, country, county, timezone, position_source, lat, lng, is_primary, travels
		from locations where id::text=$1 and ($2='' or business_id::text=$2)`, id, businessID).
		Scan(&o.BusinessID, &o.Address, &o.City, &o.Region, &o.Country, &o.County, &o.Timezone, &o.Source, &o.Lat, &o.Lng, &o.Primary, &o.Travels)
	return o, err
}

// replaceLocation decides what a save does to where a location is. A pin the
// person moved is kept as they put it. A changed address is looked up again.
// Otherwise the position stays as it was (kept is true).
func (s *Server) replaceLocation(ctx context.Context, old oldLocation, req *locationReq) (p placed, kept bool, why string) {
	if c := strings.TrimSpace(req.Country); c != "" && geo.CleanCountry(c) != old.Country {
		return p, false, "a business trades in one country, and this one is in " + geo.InCountry(old.Country)
	}
	if (req.Lat == nil) != (req.Lng == nil) {
		return p, false, "give both latitude and longitude, or neither"
	}
	address, city := strings.TrimSpace(req.Address), strings.Join(strings.Fields(req.City), " ")
	region, known := geo.Region(old.Country, req.Region)
	moved := req.Lat != nil && (old.Lat == nil || old.Lng == nil || math.Abs(*req.Lat-*old.Lat) > 1e-6 || math.Abs(*req.Lng-*old.Lng) > 1e-6)
	changed := address != old.Address || !strings.EqualFold(city, old.City) || (known && region != old.Region) || (!known && strings.TrimSpace(req.Region) != "")
	switch {
	case moved:
		p, why = s.placeLocation(ctx, address, city, req.Region, old.Country, req.Lat, req.Lng)
	case changed || old.Lat == nil || old.Timezone == "":
		p, why = s.placeLocation(ctx, address, city, req.Region, old.Country, nil, nil)
	default:
		return placed{Address: old.Address, City: old.City, Region: old.Region, Country: old.Country, County: old.County, Lat: old.Lat, Lng: old.Lng, Timezone: old.Timezone, Source: old.Source}, true, ""
	}
	return p, false, why
}

// travelRadius reads how far a travelling business goes: nil leaves it as it is, 0 clears it.
func travelRadius(km *int) (set bool, value *int, why string) {
	if km == nil {
		return false, nil, ""
	}
	if *km == 0 {
		return true, nil, ""
	}
	if *km < 1 || *km > 500 {
		return false, nil, "how far you travel is a distance between 1 and 500 kilometres"
	}
	return true, km, ""
}

// localPhone reads a number typed the way people in the country write it:
// ten digits in the United States, a leading 0 in Nigeria. A number that
// already carries its country code is left alone.
func localPhone(country, phone string) string {
	p, ok := cleanPhone(phone)
	if !ok || p == "" || strings.HasPrefix(p, "+") {
		return phone
	}
	switch {
	case country == "US" && len(p) == 10:
		return "+1" + p
	case country == "US" && len(p) == 11 && p[0] == '1':
		return "+" + p
	case country == "NG" && len(p) == 11 && p[0] == '0':
		return "+234" + p[1:]
	case country == "NG" && len(p) == 13 && strings.HasPrefix(p, "234"):
		return "+" + p
	}
	return phone
}

// saveLocation writes a location's details and, when its address or pin
// changed, its new position and time zone. It returns where the location now
// is and what happened to the pin: kept, found, approximate, set by hand, or
// not found. hours is the stored JSON.
func (s *Server) saveLocation(ctx context.Context, id string, old oldLocation, req *locationReq, hours string) (p placed, position, why string) {
	setRadius, radius, why := travelRadius(req.TravelRadiusKm)
	if why != "" {
		return p, "", why
	}
	if strings.TrimSpace(req.City) == "" && old.City != "" && strings.TrimSpace(req.Address) == old.Address {
		req.City = old.City // a form that does not send the city leaves it alone
	}
	if strings.TrimSpace(req.Region) == "" && strings.EqualFold(strings.TrimSpace(req.City), old.City) {
		req.Region = old.Region
	}
	p, kept, why := s.replaceLocation(ctx, old, req)
	if why != "" {
		return p, "", why
	}
	position = positionWord(p.Source, kept)
	source := p.Source
	if p.Lat == nil { // nothing found: the old pin stays, and the save says so
		source = old.Source
		p.Lat, p.Lng = old.Lat, old.Lng
	}
	travels := old.Travels
	if req.Travels != nil {
		travels = *req.Travels
	}
	if _, err := s.pool.Exec(ctx, `update locations set name=$2, address=$3, city=$4, region=$5, arrival_notes=$6, hours=$7::jsonb, lat=$8, lng=$9,
		county=$10, timezone=coalesce(nullif($11,''), timezone), position_source=$12, travels=$13,
		travel_radius_km = case when not $13 then null when $14 then $15::int else travel_radius_km end where id::text=$1`,
		id, strings.TrimSpace(req.Name), p.Address, p.City, p.Region, req.ArrivalNotes, hours, p.Lat, p.Lng, p.County, p.Timezone, source, travels, setRadius, radius); err != nil {
		return p, "", err.Error()
	}
	if old.Primary && !kept {
		s.syncBusinessZone(ctx, old.BusinessID)
	}
	return p, position, ""
}
