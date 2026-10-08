package geo

import "strings"

// A City is a well-known city the place picker can offer as a person types,
// before anything has to be looked up. Region is as a location stores it.
// Any other city or town is found through the lookup service and remembered.
type City struct {
	Name, Region, Country string
	Lat, Lng              float64
}

// Cities is a starting list of large cities, not the list of places LogaLuxe
// serves: that comes from where businesses are. Positions are city centres to
// two decimal places (about a kilometre).
var Cities = []City{
	{"New York", "NY", "US", 40.71, -74.01}, {"Los Angeles", "CA", "US", 34.05, -118.24}, {"Chicago", "IL", "US", 41.88, -87.63},
	{"Houston", "TX", "US", 29.76, -95.37}, {"Phoenix", "AZ", "US", 33.45, -112.07}, {"Philadelphia", "PA", "US", 39.95, -75.17},
	{"San Antonio", "TX", "US", 29.42, -98.49}, {"San Diego", "CA", "US", 32.72, -117.16}, {"Dallas", "TX", "US", 32.78, -96.80},
	{"San Jose", "CA", "US", 37.34, -121.89}, {"Austin", "TX", "US", 30.27, -97.74}, {"Jacksonville", "FL", "US", 30.33, -81.66},
	{"Fort Worth", "TX", "US", 32.76, -97.33}, {"Columbus", "OH", "US", 39.96, -83.00}, {"Charlotte", "NC", "US", 35.23, -80.84},
	{"San Francisco", "CA", "US", 37.77, -122.42}, {"Indianapolis", "IN", "US", 39.77, -86.16}, {"Seattle", "WA", "US", 47.61, -122.33},
	{"Denver", "CO", "US", 39.74, -104.99}, {"Washington", "DC", "US", 38.91, -77.04}, {"Boston", "MA", "US", 42.36, -71.06},
	{"El Paso", "TX", "US", 31.76, -106.49}, {"Nashville", "TN", "US", 36.16, -86.78}, {"Detroit", "MI", "US", 42.33, -83.05},
	{"Oklahoma City", "OK", "US", 35.47, -97.52}, {"Portland", "OR", "US", 45.52, -122.68}, {"Las Vegas", "NV", "US", 36.17, -115.14},
	{"Memphis", "TN", "US", 35.15, -90.05}, {"Louisville", "KY", "US", 38.25, -85.76}, {"Baltimore", "MD", "US", 39.29, -76.61},
	{"Milwaukee", "WI", "US", 43.04, -87.91}, {"Albuquerque", "NM", "US", 35.08, -106.65}, {"Tucson", "AZ", "US", 32.22, -110.97},
	{"Fresno", "CA", "US", 36.74, -119.79}, {"Sacramento", "CA", "US", 38.58, -121.49}, {"Kansas City", "MO", "US", 39.10, -94.58},
	{"Atlanta", "GA", "US", 33.75, -84.39}, {"Miami", "FL", "US", 25.76, -80.19}, {"Raleigh", "NC", "US", 35.78, -78.64},
	{"Omaha", "NE", "US", 41.26, -95.93}, {"Minneapolis", "MN", "US", 44.98, -93.27}, {"Tulsa", "OK", "US", 36.15, -95.99},
	{"Cleveland", "OH", "US", 41.50, -81.69}, {"New Orleans", "LA", "US", 29.95, -90.07}, {"Tampa", "FL", "US", 27.95, -82.46},
	{"Orlando", "FL", "US", 28.54, -81.38}, {"Pittsburgh", "PA", "US", 40.44, -80.00}, {"Cincinnati", "OH", "US", 39.10, -84.51},
	{"St. Louis", "MO", "US", 38.63, -90.20}, {"Honolulu", "HI", "US", 21.31, -157.86}, {"Anchorage", "AK", "US", 61.22, -149.90},
	{"Salt Lake City", "UT", "US", 40.76, -111.89}, {"Birmingham", "AL", "US", 33.52, -86.80}, {"Richmond", "VA", "US", 37.54, -77.44},
	{"Newark", "NJ", "US", 40.74, -74.17}, {"Knoxville", "TN", "US", 35.96, -83.92}, {"Chattanooga", "TN", "US", 35.05, -85.31},
	{"Baton Rouge", "LA", "US", 30.45, -91.19}, {"Jackson", "MS", "US", 32.30, -90.18}, {"Columbia", "SC", "US", 34.00, -81.03},
	{"Charleston", "SC", "US", 32.78, -79.93}, {"Savannah", "GA", "US", 32.08, -81.09}, {"Buffalo", "NY", "US", 42.89, -78.88},
	{"Hartford", "CT", "US", 41.76, -72.67}, {"Providence", "RI", "US", 41.82, -71.41}, {"Boise", "ID", "US", 43.62, -116.20},
	{"Little Rock", "AR", "US", 34.75, -92.29}, {"Des Moines", "IA", "US", 41.59, -93.62},

	{"Lagos", "Lagos", "NG", 6.52, 3.38}, {"Ikeja", "Lagos", "NG", 6.60, 3.35}, {"Abuja", "FCT", "NG", 9.07, 7.48},
	{"Port Harcourt", "Rivers", "NG", 4.82, 7.05}, {"Ibadan", "Oyo", "NG", 7.38, 3.95}, {"Kano", "Kano", "NG", 12.00, 8.59},
	{"Benin City", "Edo", "NG", 6.34, 5.60}, {"Kaduna", "Kaduna", "NG", 10.51, 7.42}, {"Enugu", "Enugu", "NG", 6.46, 7.55},
	{"Abeokuta", "Ogun", "NG", 7.15, 3.36}, {"Jos", "Plateau", "NG", 9.90, 8.86}, {"Ilorin", "Kwara", "NG", 8.48, 4.54},
	{"Owerri", "Imo", "NG", 5.48, 7.03}, {"Uyo", "Akwa Ibom", "NG", 5.04, 7.91}, {"Calabar", "Cross River", "NG", 4.98, 8.34},
	{"Warri", "Delta", "NG", 5.52, 5.75}, {"Asaba", "Delta", "NG", 6.20, 6.73}, {"Onitsha", "Anambra", "NG", 6.14, 6.80},
	{"Awka", "Anambra", "NG", 6.21, 7.07}, {"Aba", "Abia", "NG", 5.11, 7.37}, {"Maiduguri", "Borno", "NG", 11.83, 13.15},
	{"Sokoto", "Sokoto", "NG", 13.01, 5.25}, {"Akure", "Ondo", "NG", 7.26, 5.21}, {"Osogbo", "Osun", "NG", 7.78, 4.54},
	{"Yola", "Adamawa", "NG", 9.20, 12.50}, {"Makurdi", "Benue", "NG", 7.73, 8.54}, {"Minna", "Niger", "NG", 9.58, 6.55},
	{"Lokoja", "Kogi", "NG", 7.80, 6.73}, {"Ado-Ekiti", "Ekiti", "NG", 7.62, 5.22},
}

// FindCity looks a well-known city up by name. With no region it returns the
// first of that name in the country ("" for either country).
func FindCity(name, region, country string) (City, bool) {
	n := fold(name)
	if n == "" {
		return City{}, false
	}
	for _, c := range Cities {
		if fold(c.Name) == n && (country == "" || c.Country == country) && (region == "" || strings.EqualFold(c.Region, region)) {
			return c, true
		}
	}
	return City{}, false
}
