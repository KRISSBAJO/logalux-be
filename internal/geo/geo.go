// Package geo holds what LogaLuxe knows about places without asking anyone:
// the states of the United States and of Nigeria, how a place is written in
// an address, the distance between two points, and the time zone of a point.
// Nothing here touches the network or the database.
package geo

import (
	"math"
	"strconv"
	"strings"
)

// A State is one state (or district, or territory) of a country LogaLuxe serves.
// Code is the postal code in the United States and the ISO 3166-2 code in Nigeria.
// Lat and Lng are a rough centre, good enough to centre a map on.
type State struct {
	Code, Name, Zone string
	Lat, Lng         float64
}

const (
	zEastern  = "America/New_York"
	zCentral  = "America/Chicago"
	zMountain = "America/Denver"
	zArizona  = "America/Phoenix"
	zPacific  = "America/Los_Angeles"
	zAlaska   = "America/Anchorage"
	zAleutian = "America/Adak"
	zHawaii   = "Pacific/Honolulu"
	zLagos    = "Africa/Lagos"
)

// USStates lists the fifty states, the District of Columbia and the inhabited
// territories. Zone is the zone most of the state keeps; Zone() handles the
// states that are split.
var USStates = []State{
	{"AL", "Alabama", zCentral, 32.81, -86.79}, {"AK", "Alaska", zAlaska, 61.37, -152.40}, {"AZ", "Arizona", zArizona, 33.73, -111.43},
	{"AR", "Arkansas", zCentral, 34.97, -92.37}, {"CA", "California", zPacific, 36.12, -119.68}, {"CO", "Colorado", zMountain, 39.06, -105.31},
	{"CT", "Connecticut", zEastern, 41.60, -72.76}, {"DE", "Delaware", zEastern, 39.32, -75.51}, {"DC", "District of Columbia", zEastern, 38.90, -77.03},
	{"FL", "Florida", zEastern, 27.77, -81.69}, {"GA", "Georgia", zEastern, 33.04, -83.64}, {"HI", "Hawaii", zHawaii, 21.09, -157.50},
	{"ID", "Idaho", zMountain, 44.24, -114.48}, {"IL", "Illinois", zCentral, 40.35, -88.99}, {"IN", "Indiana", zEastern, 39.85, -86.26},
	{"IA", "Iowa", zCentral, 42.01, -93.21}, {"KS", "Kansas", zCentral, 38.53, -96.73}, {"KY", "Kentucky", zEastern, 37.67, -84.67},
	{"LA", "Louisiana", zCentral, 31.17, -91.87}, {"ME", "Maine", zEastern, 44.69, -69.38}, {"MD", "Maryland", zEastern, 39.06, -76.80},
	{"MA", "Massachusetts", zEastern, 42.23, -71.53}, {"MI", "Michigan", zEastern, 43.33, -84.54}, {"MN", "Minnesota", zCentral, 45.69, -93.90},
	{"MS", "Mississippi", zCentral, 32.74, -89.68}, {"MO", "Missouri", zCentral, 38.46, -92.29}, {"MT", "Montana", zMountain, 46.92, -110.45},
	{"NE", "Nebraska", zCentral, 41.13, -98.27}, {"NV", "Nevada", zPacific, 38.31, -117.06}, {"NH", "New Hampshire", zEastern, 43.45, -71.56},
	{"NJ", "New Jersey", zEastern, 40.30, -74.52}, {"NM", "New Mexico", zMountain, 34.84, -106.25}, {"NY", "New York", zEastern, 42.17, -74.95},
	{"NC", "North Carolina", zEastern, 35.63, -79.81}, {"ND", "North Dakota", zCentral, 47.53, -99.78}, {"OH", "Ohio", zEastern, 40.39, -82.76},
	{"OK", "Oklahoma", zCentral, 35.57, -96.93}, {"OR", "Oregon", zPacific, 44.57, -122.07}, {"PA", "Pennsylvania", zEastern, 40.59, -77.21},
	{"RI", "Rhode Island", zEastern, 41.68, -71.51}, {"SC", "South Carolina", zEastern, 33.86, -80.95}, {"SD", "South Dakota", zCentral, 44.30, -99.44},
	{"TN", "Tennessee", zCentral, 35.75, -86.69}, {"TX", "Texas", zCentral, 31.05, -97.56}, {"UT", "Utah", zMountain, 40.15, -111.86},
	{"VT", "Vermont", zEastern, 44.05, -72.71}, {"VA", "Virginia", zEastern, 37.77, -78.17}, {"WA", "Washington", zPacific, 47.40, -121.49},
	{"WV", "West Virginia", zEastern, 38.49, -80.95}, {"WI", "Wisconsin", zCentral, 44.27, -89.62}, {"WY", "Wyoming", zMountain, 42.76, -107.30},
	{"PR", "Puerto Rico", "America/Puerto_Rico", 18.22, -66.59}, {"VI", "US Virgin Islands", "America/St_Thomas", 18.34, -64.90},
	{"GU", "Guam", "Pacific/Guam", 13.44, 144.79}, {"AS", "American Samoa", "Pacific/Pago_Pago", -14.27, -170.70}, {"MP", "Northern Mariana Islands", "Pacific/Guam", 15.19, 145.75},
}

// NGStates lists Nigeria's thirty-six states and the Federal Capital Territory.
// Nigeria keeps one time zone. A Nigerian location stores the state's Name
// ("Lagos", "Rivers", "FCT"), which is how people there write it.
var NGStates = []State{
	{"AB", "Abia", zLagos, 5.45, 7.52}, {"AD", "Adamawa", zLagos, 9.33, 12.40}, {"AK", "Akwa Ibom", zLagos, 5.00, 7.85}, {"AN", "Anambra", zLagos, 6.22, 7.07},
	{"BA", "Bauchi", zLagos, 10.78, 9.99}, {"BY", "Bayelsa", zLagos, 4.77, 6.07}, {"BE", "Benue", zLagos, 7.34, 8.74}, {"BO", "Borno", zLagos, 11.88, 13.15},
	{"CR", "Cross River", zLagos, 5.87, 8.60}, {"DE", "Delta", zLagos, 5.70, 5.93}, {"EB", "Ebonyi", zLagos, 6.26, 8.01}, {"ED", "Edo", zLagos, 6.63, 5.93},
	{"EK", "Ekiti", zLagos, 7.72, 5.31}, {"EN", "Enugu", zLagos, 6.54, 7.44}, {"FC", "FCT", zLagos, 8.89, 7.19}, {"GO", "Gombe", zLagos, 10.36, 11.19},
	{"IM", "Imo", zLagos, 5.57, 7.06}, {"JI", "Jigawa", zLagos, 12.23, 9.56}, {"KD", "Kaduna", zLagos, 10.38, 7.71}, {"KN", "Kano", zLagos, 11.75, 8.52},
	{"KT", "Katsina", zLagos, 12.38, 7.63}, {"KE", "Kebbi", zLagos, 11.49, 4.23}, {"KO", "Kogi", zLagos, 7.73, 6.69}, {"KW", "Kwara", zLagos, 8.97, 4.39},
	{"LA", "Lagos", zLagos, 6.52, 3.38}, {"NA", "Nasarawa", zLagos, 8.50, 8.20}, {"NI", "Niger", zLagos, 9.93, 5.60}, {"OG", "Ogun", zLagos, 7.00, 3.47},
	{"ON", "Ondo", zLagos, 6.91, 5.15}, {"OS", "Osun", zLagos, 7.56, 4.52}, {"OY", "Oyo", zLagos, 8.16, 3.61}, {"PL", "Plateau", zLagos, 9.22, 9.52},
	{"RI", "Rivers", zLagos, 4.84, 6.92}, {"SO", "Sokoto", zLagos, 13.05, 5.24}, {"TA", "Taraba", zLagos, 7.99, 10.77}, {"YO", "Yobe", zLagos, 12.29, 11.44},
	{"ZA", "Zamfara", zLagos, 12.12, 6.22},
}

// Countries LogaLuxe trades in, in the order they are offered.
var Countries = []string{"US", "NG"}

// Supported reports whether a business can trade in the country.
func Supported(country string) bool { return country == "US" || country == "NG" }

// CleanCountry reads "us", "USA", "Nigeria", "ng" and the like as "US" or "NG", or "" for anywhere else.
func CleanCountry(s string) string {
	switch strings.ToLower(strings.TrimSpace(strings.ReplaceAll(s, ".", ""))) {
	case "us", "usa", "united states", "united states of america", "america":
		return "US"
	case "ng", "nga", "nigeria":
		return "NG"
	}
	return ""
}

// CountryName is the country as a person writes it.
func CountryName(country string) string {
	switch country {
	case "US":
		return "United States"
	case "NG":
		return "Nigeria"
	}
	return country
}

// CountrySlug is the address of a whole country: "united-states", "nigeria".
func CountrySlug(country string) string {
	if !Supported(country) {
		return ""
	}
	return Slugify(CountryName(country))
}

// CountryCentre is a rough middle of a country, to centre a map on.
func CountryCentre(country string) (lat, lng float64) {
	switch country {
	case "US":
		return 39.83, -98.58
	case "NG":
		return 9.08, 8.68
	}
	return 0, 0
}

// InCountry is the country inside a sentence: "the United States", "Nigeria".
func InCountry(country string) string {
	if country == "US" {
		return "the United States"
	}
	return CountryName(country)
}

// Currency is the money a business in the country is paid in.
func Currency(country string) string {
	if country == "NG" {
		return "NGN"
	}
	return "USD"
}

// Unit is how distance is told in the country: miles in the United States, kilometres everywhere else.
func Unit(country string) string {
	if country == "US" {
		return "mi"
	}
	return "km"
}

// States returns the states of a country.
func States(country string) []State {
	switch country {
	case "US":
		return USStates
	case "NG":
		return NGStates
	}
	return nil
}

func fold(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = strings.NewReplacer(".", "", ",", " ", "-", " ", "_", " ").Replace(s)
	return strings.Join(strings.Fields(s), " ")
}

// FindState looks a state up by its code or by its name, however it is typed.
func FindState(country, s string) (State, bool) {
	f := fold(s)
	if f == "" {
		return State{}, false
	}
	switch country {
	case "US":
		switch f {
		case "washington dc", "dc", "d c", "washington d c":
			f = "district of columbia"
		case "virgin islands":
			f = "us virgin islands"
		}
	case "NG":
		f = strings.TrimSpace(strings.TrimSuffix(f, " state"))
		switch f {
		case "abuja", "federal capital territory", "abuja fct", "fct abuja", "abuja federal capital territory":
			f = "fct"
		}
	}
	for _, st := range States(country) {
		if strings.ToLower(st.Code) == f || fold(st.Name) == f {
			return st, true
		}
	}
	return State{}, false
}

// Region is how a state is stored on a location: the two-letter code in the
// United States (sales tax goes by it), the state's name in Nigeria.
func Region(country, s string) (string, bool) {
	st, ok := FindState(country, s)
	if !ok {
		return "", false
	}
	if country == "US" {
		return st.Code, true
	}
	return st.Name, true
}

// RegionName is the state written out: "Tennessee", "Lagos State", "Federal Capital Territory".
func RegionName(country, region string) string {
	st, ok := FindState(country, region)
	if !ok {
		return region
	}
	if country == "NG" {
		if st.Name == "FCT" {
			return "Federal Capital Territory"
		}
		return st.Name + " State"
	}
	return st.Name
}

// Label is a place as it is shown: "Nashville, TN", "Ikeja, Lagos", "Lagos"
// (a city that shares its state's name is written once), "Tennessee" for a state alone.
func Label(city, region, country string) string {
	city = strings.TrimSpace(city)
	switch {
	case city == "" && region == "":
		return CountryName(country)
	case city == "":
		return RegionName(country, region)
	case region == "" || (country == "NG" && strings.EqualFold(city, region)):
		return city
	}
	return city + ", " + region
}

// Slugify makes the part of an address a name becomes: "St. Louis" is "st-louis".
func Slugify(s string) string {
	var b strings.Builder
	dash := true // no leading dash
	for _, r := range strings.ToLower(strings.TrimSpace(s)) {
		switch {
		case (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9'):
			b.WriteRune(r)
			dash = false
		case r == '\'' || r == '’' || r == '.':
			// "O'Fallon" is "ofallon", "St." is "st"
		default:
			if !dash {
				b.WriteByte('-')
				dash = true
			}
		}
	}
	return strings.TrimRight(b.String(), "-")
}

// PlaceSlug is the address of a place's page.
//
//	a city in the United States   nashville-tn, new-york-ny
//	a city in Nigeria             lagos-lagos, port-harcourt-rivers, abuja-fct
//	a state of the United States  tennessee
//	a state of Nigeria            lagos-state, fct
//	a whole country               united-states, nigeria
//
// A United States city always ends in its two-letter state and a Nigerian one
// in its state's name, which is never two letters, so two cities with one name
// in different states, or in the two countries, never share a slug.
func PlaceSlug(city, region, country string) string {
	if strings.TrimSpace(city) == "" && strings.TrimSpace(region) == "" {
		return CountrySlug(country)
	}
	st, ok := FindState(country, region)
	if !ok {
		return ""
	}
	state := Slugify(st.Name)
	if country == "US" {
		if strings.TrimSpace(city) == "" {
			return state
		}
		return Slugify(city) + "-" + strings.ToLower(st.Code)
	}
	if strings.TrimSpace(city) == "" {
		if st.Name == "FCT" {
			return "fct"
		}
		return state + "-state"
	}
	return Slugify(city) + "-" + state
}

// ParseSlug reads a slug back into a place. The city comes back as the words
// of the slug ("port harcourt"); its proper spelling comes from the data.
func ParseSlug(slug string) (city, region, country string, ok bool) {
	slug = strings.ToLower(strings.TrimSpace(slug))
	if slug == "" || Slugify(slug) != slug {
		return "", "", "", false
	}
	for _, c := range Countries {
		if CountrySlug(c) == slug {
			return "", "", c, true
		}
	}
	for _, st := range USStates {
		if Slugify(st.Name) == slug {
			return "", st.Code, "US", true
		}
	}
	for _, st := range NGStates {
		if (st.Name == "FCT" && slug == "fct") || (st.Name != "FCT" && Slugify(st.Name)+"-state" == slug) {
			return "", st.Name, "NG", true
		}
	}
	if i := strings.LastIndexByte(slug, '-'); i > 0 && len(slug)-i-1 == 2 {
		for _, st := range USStates {
			if strings.ToLower(st.Code) == slug[i+1:] {
				return strings.ReplaceAll(slug[:i], "-", " "), st.Code, "US", true
			}
		}
	}
	// The longest state name that ends the slug: "uyo-akwa-ibom" is Uyo in Akwa Ibom.
	best := State{}
	for _, st := range NGStates {
		if s := "-" + Slugify(st.Name); strings.HasSuffix(slug, s) && len(slug) > len(s) && len(st.Name) > len(best.Name) {
			best = st
		}
	}
	if best.Name != "" {
		return strings.ReplaceAll(strings.TrimSuffix(slug, "-"+Slugify(best.Name)), "-", " "), best.Name, "NG", true
	}
	return "", "", "", false
}

// ---------- distance ----------

const earthKm = 6371.0
const kmPerMile = 1.609344

// Km is the distance between two points on the globe, in kilometres (haversine).
func Km(lat1, lng1, lat2, lng2 float64) float64 {
	rad := func(d float64) float64 { return d * math.Pi / 180 }
	dLat, dLng := rad(lat2-lat1), rad(lng2-lng1)
	h := math.Pow(math.Sin(dLat/2), 2) + math.Cos(rad(lat1))*math.Cos(rad(lat2))*math.Pow(math.Sin(dLng/2), 2)
	return earthKm * 2 * math.Asin(math.Min(1, math.Sqrt(h)))
}

// ToKm turns a distance in a unit into kilometres; FromKm does the reverse.
func ToKm(n float64, unit string) float64 {
	if unit == "mi" {
		return n * kmPerMile
	}
	return n
}

func FromKm(km float64, unit string) float64 {
	if unit == "mi" {
		return km / kmPerMile
	}
	return km
}

// DistanceText is a distance as a person reads it: "2.3 mi", "14 km", "1,240 km".
// One decimal under ten, whole numbers above.
func DistanceText(km float64, unit string) string {
	n := FromKm(km, unit)
	if n < 9.95 {
		return strconv.FormatFloat(math.Round(n*10)/10, 'f', 1, 64) + " " + unit
	}
	return commas(int(math.Round(n))) + " " + unit
}

// UnitWord is the unit written out for a sentence: "miles", "kilometres".
func UnitWord(unit string) string {
	if unit == "mi" {
		return "miles"
	}
	return "kilometres"
}

func commas(n int) string {
	s := strconv.Itoa(n)
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	return s
}

// Box is the rectangle that holds every point within km of a point. A search
// narrows to it first, so the exact distance is worked out for few rows.
// Whole is true when the rectangle would cross the poles or the date line;
// then there is nothing to narrow by.
func Box(lat, lng, km float64) (south, west, north, east float64, whole bool) {
	dLat := km / 111.045
	cos := math.Cos(lat * math.Pi / 180)
	if cos < 0.01 || lat+dLat > 89 || lat-dLat < -89 {
		return -90, -180, 90, 180, true
	}
	dLng := km / (111.045 * cos)
	if lng-dLng < -180 || lng+dLng > 180 {
		return -90, -180, 90, 180, true
	}
	return lat - dLat, lng - dLng, lat + dLat, lng + dLng, false
}

// ValidPoint reports whether a latitude and longitude are on Earth.
func ValidPoint(lat, lng float64) bool {
	return !math.IsNaN(lat) && !math.IsNaN(lng) && lat >= -90 && lat <= 90 && lng >= -180 && lng <= 180
}

// CountryOfPoint guesses the country of a point from rough rectangles. It is
// used to choose miles or kilometres and a first guess, never to decide money.
// Points just over a border (southern Ontario, northern Mexico, Benin, western
// Cameroon) are taken for the neighbour.
func CountryOfPoint(lat, lng float64) string {
	in := func(s, w, n, e float64) bool { return lat >= s && lat <= n && lng >= w && lng <= e }
	switch {
	case in(4.2, 2.6, 13.95, 14.7):
		return "NG"
	case in(24.4, -125.0, 49.4, -66.9), in(51.0, -180, 71.6, -129.9), in(18.8, -160.6, 22.4, -154.7), in(17.6, -67.4, 18.6, -64.5):
		return "US"
	}
	return ""
}
