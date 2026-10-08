package geo

import (
	"math"
	"testing"
	"time"
)

func TestKm(t *testing.T) {
	cases := []struct {
		name                   string
		lat1, lng1, lat2, lng2 float64
		want, within           float64
	}{
		{"the same point", 36.16, -86.78, 36.16, -86.78, 0, 0.001},
		{"Nashville to Memphis", 36.1627, -86.7816, 35.1495, -90.0490, 315, 5},
		{"Lagos to Abuja", 6.5244, 3.3792, 9.0579, 7.4951, 535, 8},
		{"New York to Los Angeles", 40.7128, -74.0060, 34.0522, -118.2437, 3936, 15},
		{"one degree of latitude", 0, 0, 1, 0, 111.19, 0.1},
		{"across the date line", 0, 179.5, 0, -179.5, 111.19, 0.1},
	}
	for _, c := range cases {
		if got := Km(c.lat1, c.lng1, c.lat2, c.lng2); math.Abs(got-c.want) > c.within {
			t.Errorf("%s: got %.2f km, want %.2f within %.2f", c.name, got, c.want, c.within)
		}
	}
	if a, b := Km(36, -86, 6, 3), Km(6, 3, 36, -86); math.Abs(a-b) > 1e-9 {
		t.Errorf("distance should be the same both ways: %f and %f", a, b)
	}
}

func TestBoxHoldsTheCircle(t *testing.T) {
	for _, p := range [][2]float64{{36.16, -86.78}, {6.52, 3.38}, {61.2, -149.9}, {25.76, -80.19}} {
		for _, km := range []float64{5, 40, 400} {
			s, w, n, e, whole := Box(p[0], p[1], km)
			if whole {
				t.Errorf("box at %v for %v km should not be the whole globe", p, km)
			}
			// The four points due north, south, east and west at the radius must be inside (with a hair of slack).
			if Km(p[0], p[1], n, p[1]) < km-0.5 || Km(p[0], p[1], s, p[1]) < km-0.5 || Km(p[0], p[1], p[0], e) < km-0.5 || Km(p[0], p[1], p[0], w) < km-0.5 {
				t.Errorf("box at %v for %v km is too small: %v %v %v %v", p, km, s, w, n, e)
			}
		}
	}
	if _, _, _, _, whole := Box(89.5, 0, 200); !whole {
		t.Error("a box over the pole should be the whole globe")
	}
	if _, _, _, _, whole := Box(0, 179.9, 200); !whole {
		t.Error("a box over the date line should be the whole globe")
	}
}

func TestDistanceText(t *testing.T) {
	cases := []struct {
		km   float64
		unit string
		want string
	}{
		{3.7, "mi", "2.3 mi"}, {3.7, "km", "3.7 km"}, {0.04, "km", "0.0 km"}, {16.0, "mi", "9.9 mi"}, {16.1, "mi", "10 mi"},
		{338, "mi", "210 mi"}, {1995.4, "km", "1,995 km"}, {14.2, "km", "14 km"},
	}
	for _, c := range cases {
		if got := DistanceText(c.km, c.unit); got != c.want {
			t.Errorf("DistanceText(%v, %s) = %q, want %q", c.km, c.unit, got, c.want)
		}
	}
	if math.Abs(FromKm(ToKm(25, "mi"), "mi")-25) > 1e-9 || math.Abs(ToKm(25, "mi")-40.2336) > 1e-6 || ToKm(25, "km") != 25 {
		t.Error("miles and kilometres do not convert cleanly")
	}
}

func TestZone(t *testing.T) {
	cases := []struct {
		name, country, region, county string
		lat, lng                      float64
		point                         bool
		want                          string
	}{
		{"Nashville", "US", "TN", "Davidson County", 36.16, -86.78, true, "America/Chicago"},
		{"Nashville from the state alone", "US", "Tennessee", "", 0, 0, false, "America/Chicago"},
		{"Knoxville by county", "US", "TN", "Knox County", 0, 0, false, "America/New_York"},
		{"Knoxville by point", "US", "TN", "", 35.96, -83.92, true, "America/New_York"},
		{"Chattanooga by point", "US", "TN", "", 35.05, -85.31, true, "America/New_York"},
		{"Crossville by point", "US", "TN", "", 35.95, -85.03, true, "America/Chicago"},
		{"Crossville by county", "US", "TN", "Cumberland", 35.95, -85.03, true, "America/Chicago"},
		{"Memphis", "US", "tn", "Shelby County", 35.15, -90.05, true, "America/Chicago"},
		{"New York", "US", "NY", "", 40.71, -74.01, true, "America/New_York"},
		{"Los Angeles", "US", "CA", "Los Angeles County", 34.05, -118.24, true, "America/Los_Angeles"},
		{"Atlanta", "US", "GA", "Fulton County", 33.75, -84.39, true, "America/New_York"},
		{"Houston", "US", "TX", "Harris County", 29.76, -95.37, true, "America/Chicago"},
		{"El Paso by county", "US", "TX", "El Paso County", 31.76, -106.49, true, "America/Denver"},
		{"El Paso by point", "US", "TX", "", 31.76, -106.49, true, "America/Denver"},
		{"Miami", "US", "FL", "Miami-Dade County", 25.76, -80.19, true, "America/New_York"},
		{"Pensacola by county", "US", "FL", "Escambia County", 30.42, -87.22, true, "America/Chicago"},
		{"Pensacola by point", "US", "FL", "", 30.42, -87.22, true, "America/Chicago"},
		{"Tallahassee by point", "US", "FL", "", 30.44, -84.28, true, "America/New_York"},
		{"Louisville by point", "US", "KY", "", 38.25, -85.76, true, "America/New_York"},
		{"Bowling Green by point", "US", "KY", "", 36.99, -86.44, true, "America/Chicago"},
		{"Bowling Green by county", "US", "KY", "Warren County", 0, 0, false, "America/Chicago"},
		{"Indianapolis", "US", "IN", "Marion County", 39.77, -86.16, true, "America/New_York"},
		{"Evansville by point", "US", "IN", "", 37.97, -87.57, true, "America/Chicago"},
		{"Gary by county", "US", "IN", "Lake County", 41.59, -87.35, true, "America/Chicago"},
		{"Detroit", "US", "MI", "", 42.33, -83.05, true, "America/New_York"},
		{"Ironwood by county", "US", "MI", "Gogebic County", 46.45, -90.17, true, "America/Chicago"},
		{"Phoenix", "US", "AZ", "", 33.45, -112.07, true, "America/Phoenix"},
		{"Denver", "US", "CO", "", 39.74, -104.99, true, "America/Denver"},
		{"Boise", "US", "ID", "Ada County", 43.62, -116.20, true, "America/Denver"},
		{"Coeur d'Alene by point", "US", "ID", "", 47.68, -116.78, true, "America/Los_Angeles"},
		{"Coeur d'Alene by county", "US", "ID", "Kootenai County", 0, 0, false, "America/Los_Angeles"},
		{"Portland", "US", "OR", "Multnomah County", 45.52, -122.68, true, "America/Los_Angeles"},
		{"Ontario, Oregon", "US", "OR", "Malheur County", 44.03, -116.96, true, "America/Denver"},
		{"Rapid City by point", "US", "SD", "", 44.08, -103.23, true, "America/Denver"},
		{"Sioux Falls", "US", "SD", "Minnehaha County", 43.55, -96.73, true, "America/Chicago"},
		{"Bismarck", "US", "ND", "Burleigh County", 46.81, -100.78, true, "America/Chicago"},
		{"Dickinson", "US", "ND", "Stark County", 46.88, -102.79, true, "America/Denver"},
		{"Scottsbluff by point", "US", "NE", "", 41.87, -103.67, true, "America/Denver"},
		{"Omaha", "US", "NE", "Douglas County", 41.26, -95.93, true, "America/Chicago"},
		{"Goodland, Kansas", "US", "KS", "Sherman County", 39.35, -101.71, true, "America/Denver"},
		{"Wichita", "US", "KS", "", 37.69, -97.34, true, "America/Chicago"},
		{"Anchorage", "US", "AK", "", 61.22, -149.90, true, "America/Anchorage"},
		{"Adak", "US", "AK", "", 51.88, -176.66, true, "America/Adak"},
		{"Honolulu", "US", "HI", "", 21.31, -157.86, true, "Pacific/Honolulu"},
		{"Washington", "US", "District of Columbia", "", 38.91, -77.04, true, "America/New_York"},
		{"San Juan", "US", "PR", "", 18.47, -66.11, true, "America/Puerto_Rico"},
		{"Lagos", "NG", "Lagos", "", 6.52, 3.38, true, "Africa/Lagos"},
		{"Abuja with no state", "NG", "", "", 0, 0, false, "Africa/Lagos"},
		{"Kano", "NG", "Kano State", "", 12.0, 8.59, true, "Africa/Lagos"},
		{"an unknown state", "US", "Narnia", "", 0, 0, false, ""},
		{"a country we do not serve", "GB", "England", "", 51.5, -0.12, true, ""},
	}
	for _, c := range cases {
		if got := Zone(c.country, c.region, c.county, c.lat, c.lng, c.point); got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}

// Every zone a state can be given must be one the machine can load.
func TestZonesLoad(t *testing.T) {
	seen := map[string]bool{zAleutian: true}
	for _, st := range append(append([]State{}, USStates...), NGStates...) {
		seen[st.Zone] = true
	}
	for z := range seen {
		if _, err := time.LoadLocation(z); err != nil {
			t.Errorf("zone %q does not load: %v", z, err)
		}
		if ZoneLabel(z) == z {
			t.Errorf("zone %q has no plain name", z)
		}
	}
}

func TestRegion(t *testing.T) {
	cases := []struct{ country, in, want string }{
		{"US", "TN", "TN"}, {"US", "tn", "TN"}, {"US", "Tennessee", "TN"}, {"US", " new york ", "NY"}, {"US", "N.Y.", "NY"},
		{"US", "Washington DC", "DC"}, {"US", "District of Columbia", "DC"}, {"US", "Puerto Rico", "PR"},
		{"NG", "Lagos", "Lagos"}, {"NG", "lagos state", "Lagos"}, {"NG", "Abuja", "FCT"}, {"NG", "Federal Capital Territory", "FCT"},
		{"NG", "Rivers State", "Rivers"}, {"NG", "akwa-ibom", "Akwa Ibom"}, {"NG", "LA", "Lagos"}, {"NG", "Cross River", "Cross River"},
	}
	for _, c := range cases {
		if got, ok := Region(c.country, c.in); !ok || got != c.want {
			t.Errorf("Region(%s, %q) = %q %v, want %q", c.country, c.in, got, ok, c.want)
		}
	}
	for _, bad := range [][2]string{{"US", ""}, {"US", "Lagos"}, {"NG", "Tennessee"}, {"GB", "Kent"}, {"US", "Tenn"}} {
		if got, ok := Region(bad[0], bad[1]); ok {
			t.Errorf("Region(%s, %q) should not be known, got %q", bad[0], bad[1], got)
		}
	}
	if len(USStates) != 56 || len(NGStates) != 37 {
		t.Errorf("state tables: %d US and %d NG, want 56 and 37", len(USStates), len(NGStates))
	}
}

func TestStateTablesAreSound(t *testing.T) {
	for _, country := range Countries {
		codes, names := map[string]bool{}, map[string]bool{}
		for _, st := range States(country) {
			if codes[st.Code] || names[st.Name] {
				t.Errorf("%s: %s %s is listed twice", country, st.Code, st.Name)
			}
			codes[st.Code], names[st.Name] = true, true
			if !ValidPoint(st.Lat, st.Lng) || (st.Lat == 0 && st.Lng == 0) {
				t.Errorf("%s %s has no centre", country, st.Name)
			}
			// A state's centre must fall in its own country by the same rough test the product uses,
			// apart from the Pacific territories, which the rectangles leave out.
			if got := CountryOfPoint(st.Lat, st.Lng); got != country && st.Code != "GU" && st.Code != "AS" && st.Code != "MP" && st.Code != "VI" {
				t.Errorf("%s %s centre (%v, %v) reads as %q", country, st.Name, st.Lat, st.Lng, got)
			}
		}
	}
	for _, c := range Cities {
		if _, ok := FindState(c.Country, c.Region); !ok {
			t.Errorf("city %s has an unknown state %q", c.Name, c.Region)
		}
		if r, _ := Region(c.Country, c.Region); r != c.Region {
			t.Errorf("city %s stores its state as %q, want %q", c.Name, c.Region, r)
		}
		if got := CountryOfPoint(c.Lat, c.Lng); got != c.Country {
			t.Errorf("city %s (%v, %v) reads as %q", c.Name, c.Lat, c.Lng, got)
		}
		st, _ := FindState(c.Country, c.Region)
		if far := Km(c.Lat, c.Lng, st.Lat, st.Lng); far > 1300 {
			t.Errorf("city %s is %.0f km from the centre of %s", c.Name, far, st.Name)
		}
	}
}

func TestPlaceSlug(t *testing.T) {
	cases := []struct{ city, region, country, slug string }{
		{"Nashville", "TN", "US", "nashville-tn"},
		{"New York", "NY", "US", "new-york-ny"},
		{"St. Louis", "MO", "US", "st-louis-mo"},
		{"O'Fallon", "IL", "US", "ofallon-il"},
		{"Winston-Salem", "NC", "US", "winston-salem-nc"},
		{"Washington", "DC", "US", "washington-dc"},
		{"Springfield", "IL", "US", "springfield-il"},
		{"Springfield", "MO", "US", "springfield-mo"},
		{"Lagos", "Lagos", "NG", "lagos-lagos"},
		{"Ikeja", "Lagos", "NG", "ikeja-lagos"},
		{"Abuja", "FCT", "NG", "abuja-fct"},
		{"Port Harcourt", "Rivers", "NG", "port-harcourt-rivers"},
		{"Uyo", "Akwa Ibom", "NG", "uyo-akwa-ibom"},
		{"Calabar", "Cross River", "NG", "calabar-cross-river"},
		{"", "TN", "US", "tennessee"},
		{"", "NY", "US", "new-york"},
		{"", "Lagos", "NG", "lagos-state"},
		{"", "FCT", "NG", "fct"},
		{"", "", "US", "united-states"},
		{"", "", "NG", "nigeria"},
	}
	seen := map[string]string{}
	for _, c := range cases {
		got := PlaceSlug(c.city, c.region, c.country)
		if got != c.slug {
			t.Errorf("PlaceSlug(%q, %q, %q) = %q, want %q", c.city, c.region, c.country, got, c.slug)
		}
		if was, dup := seen[got]; dup {
			t.Errorf("%q is the slug of both %s and %s, %s", got, was, c.city, c.region)
		}
		seen[got] = c.city + ", " + c.region
		city, region, country, ok := ParseSlug(c.slug)
		if !ok || region != c.region || country != c.country || Slugify(city) != Slugify(c.city) {
			t.Errorf("ParseSlug(%q) = %q %q %q %v", c.slug, city, region, country, ok)
		}
	}
	// A place given by its state's full name lands on the same slug.
	if PlaceSlug("Nashville", "Tennessee", "US") != "nashville-tn" || PlaceSlug("Ikeja", "Lagos State", "NG") != "ikeja-lagos" {
		t.Error("a state's full name should give the same slug as its stored form")
	}
	for _, bad := range []string{"", "nashville", "nashville-zz", "-tn", "lagos", "a b-tn", "nashville--tn"} {
		if city, region, country, ok := ParseSlug(bad); ok {
			t.Errorf("ParseSlug(%q) should fail, got %q %q %q", bad, city, region, country)
		}
	}
	if PlaceSlug("Nashville", "", "US") != "" || PlaceSlug("London", "England", "GB") != "" || PlaceSlug("", "", "GB") != "" {
		t.Error("a place with no known state has no slug")
	}
	// Louisiana and Lagos State share the letters LA; Delaware and Delta share DE. The slugs must not collide.
	if PlaceSlug("Lagos", "LA", "US") == PlaceSlug("Lagos", "Lagos", "NG") || PlaceSlug("Asaba", "DE", "US") == PlaceSlug("Asaba", "Delta", "NG") {
		t.Error("a US and a Nigerian city of one name share a slug")
	}
}

func TestEveryStateAndCityRoundTrips(t *testing.T) {
	seen := map[string]bool{}
	for _, country := range Countries {
		for _, st := range States(country) {
			region, _ := Region(country, st.Name)
			slug := PlaceSlug("", region, country)
			if seen[slug] {
				t.Errorf("state slug %q is used twice", slug)
			}
			seen[slug] = true
			if _, r, c, ok := ParseSlug(slug); !ok || r != region || c != country {
				t.Errorf("state %s: slug %q parses to %q %q %v", st.Name, slug, r, c, ok)
			}
		}
	}
	for _, c := range Cities {
		slug := PlaceSlug(c.Name, c.Region, c.Country)
		if seen[slug] {
			t.Errorf("city slug %q is used twice", slug)
		}
		seen[slug] = true
		if city, r, country, ok := ParseSlug(slug); !ok || r != c.Region || country != c.Country || Slugify(city) != Slugify(c.Name) {
			t.Errorf("city %s: slug %q parses to %q %q %q %v", c.Name, slug, city, r, country, ok)
		}
	}
}

func TestLabelsAndUnits(t *testing.T) {
	cases := []struct{ city, region, country, want string }{
		{"Nashville", "TN", "US", "Nashville, TN"}, {"Lagos", "Lagos", "NG", "Lagos"}, {"Ikeja", "Lagos", "NG", "Ikeja, Lagos"},
		{"Abuja", "FCT", "NG", "Abuja, FCT"}, {"", "TN", "US", "Tennessee"}, {"", "Lagos", "NG", "Lagos State"},
		{"", "FCT", "NG", "Federal Capital Territory"}, {"", "", "NG", "Nigeria"}, {"New York", "NY", "US", "New York, NY"},
	}
	for _, c := range cases {
		if got := Label(c.city, c.region, c.country); got != c.want {
			t.Errorf("Label(%q, %q, %q) = %q, want %q", c.city, c.region, c.country, got, c.want)
		}
	}
	if Unit("US") != "mi" || Unit("NG") != "km" || Unit("") != "km" || Currency("US") != "USD" || Currency("NG") != "NGN" {
		t.Error("units or currency by country are wrong")
	}
	if CleanCountry("usa") != "US" || CleanCountry(" Nigeria ") != "NG" || CleanCountry("ng") != "NG" || CleanCountry("France") != "" {
		t.Error("CleanCountry")
	}
	for _, p := range []struct {
		lat, lng float64
		want     string
	}{{36.16, -86.78, "US"}, {6.52, 3.38, "NG"}, {9.07, 7.48, "NG"}, {61.22, -149.9, "US"}, {21.31, -157.86, "US"}, {51.5, -0.12, ""}, {-33.9, 18.4, ""}} {
		if got := CountryOfPoint(p.lat, p.lng); got != p.want {
			t.Errorf("CountryOfPoint(%v, %v) = %q, want %q", p.lat, p.lng, got, p.want)
		}
	}
}
