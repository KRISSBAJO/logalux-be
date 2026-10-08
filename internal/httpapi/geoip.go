package httpapi

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"logaluxe/api/internal/geo"
)

// A first guess of where a visitor is, from their internet address, made
// without asking them anything. It is approximate and is always presented as
// a guess. In order:
//
//  1. What a network in front of the web app already worked out (Cloudflare,
//     Vercel or CloudFront location headers), believed only when the request
//     comes from our own web app with the shared WEB_API_KEY, the same rule
//     visitorIP uses.
//  2. A lookup of the visitor's address with the provider named by
//     GEOIP_PROVIDER, kept in geo_ip by address prefix so one visitor is one
//     lookup. The full address is never stored and never logged.
//  3. Nothing: private and local addresses (all of local development), a
//     provider that is off, or a lookup that fails or is slow. The caller then
//     falls back to the place with the most businesses.

const (
	ipHitFor  = 7 * 24 * time.Hour
	ipMissFor = 24 * time.Hour
)

// ipGuess is what an address says about where its user is.
type ipGuess struct {
	Country string  `json:"country"`
	Region  string  `json:"region"`
	City    string  `json:"city"`
	Lat     float64 `json:"lat"`
	Lng     float64 `json:"lng"`
	Point   bool    `json:"point"`
}

// webTrusted reports whether a request really comes from our own web app.
func (s *Server) webTrusted(r *http.Request) bool {
	if s.cfg.WebAPIKey != "" {
		return subtle.ConstantTimeCompare([]byte(r.Header.Get("X-Web-Key")), []byte(s.cfg.WebAPIKey)) == 1
	}
	return s.cfg.Env != "production"
}

// edgeGuess reads the location headers a content network adds. The web app passes them on unchanged.
func edgeGuess(h http.Header) (g ipGuess, ok bool) {
	first := func(names ...string) string {
		for _, n := range names {
			if v := strings.TrimSpace(h.Get(n)); v != "" {
				if u, err := url.QueryUnescape(v); err == nil { // Vercel percent-encodes city names
					return u
				}
				return v
			}
		}
		return ""
	}
	g.Country = strings.ToUpper(first("CF-IPCountry", "X-Vercel-IP-Country", "CloudFront-Viewer-Country"))
	if len(g.Country) != 2 || g.Country == "XX" || g.Country == "T1" {
		return ipGuess{}, false
	}
	g.Region = first("CF-Region-Code", "X-Vercel-IP-Country-Region", "CloudFront-Viewer-Country-Region", "CF-Region")
	g.City = first("CF-IPCity", "X-Vercel-IP-City", "CloudFront-Viewer-City")
	lat, err1 := strconv.ParseFloat(first("CF-IPLatitude", "X-Vercel-IP-Latitude", "CloudFront-Viewer-Latitude"), 64)
	lng, err2 := strconv.ParseFloat(first("CF-IPLongitude", "X-Vercel-IP-Longitude", "CloudFront-Viewer-Longitude"), 64)
	if err1 == nil && err2 == nil && geo.ValidPoint(lat, lng) {
		g.Lat, g.Lng, g.Point = lat, lng, true
	}
	return g, true
}

// publicIP reports whether an address belongs to someone on the internet: not
// this machine, a home or office network, a carrier's shared range or a
// reserved block. Only such an address is worth looking up.
func publicIP(ip net.IP) bool {
	if ip == nil || ip.IsLoopback() || ip.IsPrivate() || ip.IsUnspecified() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsMulticast() || ip.IsInterfaceLocalMulticast() {
		return false
	}
	for _, block := range []string{"100.64.0.0/10", "192.0.0.0/24", "192.0.2.0/24", "198.18.0.0/15", "198.51.100.0/24", "203.0.113.0/24", "240.0.0.0/4", "2001:db8::/32", "64:ff9b::/96"} {
		if _, n, err := net.ParseCIDR(block); err == nil && n.Contains(ip) {
			return false
		}
	}
	return true
}

// ipPrefix is the part of an address that is kept: the first three bytes of an
// IPv4 address, the first six of an IPv6 one. Neighbours share it, and it
// does not identify a person.
func ipPrefix(ip net.IP) string {
	if v4 := ip.To4(); v4 != nil {
		return v4.Mask(net.CIDRMask(24, 32)).String() + "/24"
	}
	return ip.Mask(net.CIDRMask(48, 128)).String() + "/48"
}

// lookupIP asks the provider about an address. asked is false when no provider is set up or it did not answer.
func (s *Server) lookupIP(ctx context.Context, ip net.IP) (g ipGuess, asked bool) {
	provider := strings.ToLower(strings.TrimSpace(s.cfg.GeoIPProvider))
	if provider == "" {
		provider = "geojs"
	}
	var endpoint string
	switch provider {
	case "off", "none":
		return ipGuess{}, false
	case "geojs": // needs no key
		endpoint = "https://get.geojs.io/v1/ip/geo/" + ip.String() + ".json"
	case "ipinfo":
		if s.cfg.GeoIPKey == "" {
			return ipGuess{}, false
		}
		endpoint = "https://ipinfo.io/" + ip.String() + "?token=" + url.QueryEscape(s.cfg.GeoIPKey)
	case "ipapi":
		endpoint = "https://ipapi.co/" + ip.String() + "/json/"
		if s.cfg.GeoIPKey != "" {
			endpoint += "?key=" + url.QueryEscape(s.cfg.GeoIPKey)
		}
	default:
		return ipGuess{}, false
	}
	timeout := time.Duration(s.cfg.GeoIPTimeoutMS) * time.Millisecond
	if timeout <= 0 {
		timeout = 1200 * time.Millisecond
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return ipGuess{}, false
	}
	req.Header.Set("User-Agent", nominatimAgent)
	req.Header.Set("Accept", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return ipGuess{}, false // slow or unreachable: the page carries on without it
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return ipGuess{}, false
	}
	var raw map[string]any
	if err := json.NewDecoder(http.MaxBytesReader(nil, res.Body, 1<<16)).Decode(&raw); err != nil {
		return ipGuess{}, false
	}
	str := func(keys ...string) string {
		for _, k := range keys {
			if v, ok := raw[k].(string); ok && strings.TrimSpace(v) != "" {
				return strings.TrimSpace(v)
			}
		}
		return ""
	}
	num := func(k string) (float64, bool) {
		switch v := raw[k].(type) {
		case float64:
			return v, true
		case string:
			f, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
			return f, err == nil
		}
		return 0, false
	}
	g.Country = strings.ToUpper(str("country_code", "country"))
	if len(g.Country) != 2 {
		g.Country = ""
	}
	g.Region, g.City = str("region_code", "region"), str("city")
	lat, ok1 := num("latitude")
	lng, ok2 := num("longitude")
	if loc := strings.Split(str("loc"), ","); len(loc) == 2 { // ipinfo writes "36.16,-86.78"
		lat, ok1 = num2(loc[0])
		lng, ok2 = num2(loc[1])
	}
	if ok1 && ok2 && geo.ValidPoint(lat, lng) && (lat != 0 || lng != 0) {
		g.Lat, g.Lng, g.Point = round(lat, 2), round(lng, 2), true
	}
	return g, true
}

func num2(s string) (float64, bool) {
	f, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	return f, err == nil
}

// guessWhere is the first guess for a request and how it was reached:
// "edge" (a network in front of us said), "ip" (looked up, or remembered from a lookup), or "" (no guess).
func (s *Server) guessWhere(r *http.Request) (g ipGuess, source string) {
	ctx := r.Context()
	if s.webTrusted(r) {
		if e, ok := edgeGuess(r.Header); ok {
			return e, "edge"
		}
	}
	addr := s.visitorIP(r)
	// In development only, an address can be named, so the whole flow can be tried from a laptop.
	if s.cfg.Env == "development" {
		if v := strings.TrimSpace(firstNonEmpty(r.URL.Query().Get("geo_ip"), r.Header.Get("X-Geo-IP"))); v != "" {
			addr = v
		}
	}
	ip := net.ParseIP(addr)
	if !publicIP(ip) {
		return ipGuess{}, ""
	}
	prefix := ipPrefix(ip)
	var raw []byte
	if err := s.pool.QueryRow(ctx, `select answer from geo_ip where prefix=$1 and expires_at > now()`, prefix).Scan(&raw); err == nil {
		if json.Unmarshal(raw, &g) == nil && g.Country != "" {
			return g, "ip"
		}
		return ipGuess{}, "" // looked up before, nothing known
	}
	g, asked := s.lookupIP(ctx, ip)
	if !asked {
		return ipGuess{}, ""
	}
	keep, body := ipMissFor, []byte("null")
	if g.Country != "" {
		keep = ipHitFor
		body, _ = json.Marshal(g)
	}
	_, _ = s.pool.Exec(ctx, `insert into geo_ip (prefix, answer, expires_at) values ($1,$2::jsonb,$3)
		on conflict (prefix) do update set answer = excluded.answer, expires_at = excluded.expires_at`, prefix, string(body), time.Now().Add(keep))
	if g.Country == "" {
		return ipGuess{}, ""
	}
	return g, "ip"
}

// GET /v1/locate
// The first guess of where the visitor is, made from their internet address
// without asking them. It is approximate: show it as a guess the person can
// change. With no guess (a local address, or the lookup did not answer) the
// place is the one with the most businesses and source says "default".
func (s *Server) locate(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	live, _ := s.livePlaces(ctx, false)
	g, source := s.guessWhere(r)
	var busiest *place
	if len(live) > 0 {
		busiest = &live[0]
	}
	out := M{"source": "default", "approximate": true, "country": "", "country_name": "", "served": false, "place": busiest, "default": busiest, "nearest": []place{}}
	if source != "" {
		out["source"], out["country"], out["country_name"], out["served"] = source, g.Country, geo.CountryName(g.Country), geo.Supported(g.Country)
	}
	if source != "" && geo.Supported(g.Country) {
		region, _ := geo.Region(g.Country, g.Region)
		p := newPlace("", "", g.Country, 0, 0) // the country alone, when no more is known
		if region != "" && g.City != "" {
			p = newPlace(g.City, region, g.Country, g.Lat, g.Lng)
			if !g.Point {
				if la, ln, _, ok := s.cityCentre(ctx, g.City, region, g.Country); ok {
					p.Lat, p.Lng = la, ln
				}
			}
		} else if region != "" {
			st, _ := geo.FindState(g.Country, region)
			p = newPlace("", region, g.Country, st.Lat, st.Lng)
		}
		if g.Point {
			p.Lat, p.Lng = g.Lat, g.Lng
		}
		if l, ok := findPlace(live, p.Slug); ok {
			p.Businesses, p.Categories = l.Businesses, l.Categories
		}
		out["place"] = p
		// The nearest places that have businesses, in the person's own country.
		var own []place
		for _, l := range live {
			if l.Country == g.Country {
				own = append(own, l)
			}
		}
		out["nearest"] = nearestPlaces(own, p.Lat, p.Lng, p.Unit, "", 3)
	} else {
		// No guess, or somewhere we do not trade: the busiest place stands in, and the answer says so.
		out["approximate"] = false
	}
	w.Header().Set("Cache-Control", "private, no-store")
	writeJSON(w, 200, out)
}
