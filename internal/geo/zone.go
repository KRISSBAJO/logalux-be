package geo

import "strings"

// Zone is the time zone of a place.
//
// Nigeria keeps one zone. In the United States the state decides, except in
// the thirteen states two zones share. There the county decides when it is
// known (an address found on the map comes with its county); when it is not,
// the point decides from a line drawn by longitude, which follows the real
// boundary closely but not county by county. With neither a county nor a
// point the state's larger zone is used.
//
// Known edges, all rural:
//   - Arizona is America/Phoenix throughout; the Navajo Nation, which keeps
//     daylight time, is not told apart.
//   - West Wendover, Nevada keeps Mountain time and is given Pacific.
//   - Counties a line splits (Gulf in Florida, Idaho County, Malheur in Oregon,
//     Cherry in Nebraska, Dunn, McKenzie and Sioux in North Dakota, Stanley in
//     South Dakota) follow the point, or the county seat's zone without one.
//   - Without a county, a point within a few miles of the line in Kentucky,
//     Tennessee, Indiana, the Dakotas, Nebraska and Kansas can land on the
//     wrong side.
//
// It returns "" for a country LogaLuxe does not serve or a state it does not know.
func Zone(country, region, county string, lat, lng float64, hasPoint bool) string {
	if country == "NG" {
		return zLagos
	}
	if country != "US" {
		return ""
	}
	st, ok := FindState("US", region)
	if !ok {
		return ""
	}
	c := foldCounty(county)
	in := func(list string) bool { return c != "" && strings.Contains(list, "|"+c+"|") }
	switch st.Code {
	case "FL":
		switch {
		case c == "gulf":
			if hasPoint && lat >= 29.97 {
				return zCentral
			}
			return zEastern
		case c != "":
			if in("|escambia|santa rosa|okaloosa|walton|holmes|washington|bay|jackson|calhoun|") {
				return zCentral
			}
			return zEastern
		case hasPoint && lng < -85.1:
			return zCentral
		}
	case "IN":
		switch {
		case c != "":
			if in("|lake|porter|laporte|la porte|newton|jasper|starke|gibson|posey|vanderburgh|warrick|spencer|perry|") {
				return zCentral
			}
			return zEastern
		case hasPoint && ((lat >= 41.17 && lng < -86.47) || (lat >= 40.73 && lat < 41.17 && lng < -86.93) || (lat < 38.25 && lng < -86.6) || (lat < 38.53 && lng < -87.45)):
			return zCentral
		}
	case "KY":
		switch {
		case c != "":
			if in("|adair|allen|ballard|barren|breckinridge|butler|caldwell|calloway|carlisle|christian|clinton|crittenden|cumberland|daviess|edmonson|fulton|graves|grayson|green|hancock|hart|henderson|hickman|hopkins|livingston|logan|lyon|marshall|mccracken|mclean|metcalfe|monroe|muhlenberg|ohio|russell|simpson|todd|trigg|union|warren|webster|") {
				return zCentral
			}
			return zEastern
		case hasPoint:
			line := -84.95
			if lat >= 37.4 {
				line = -86.25
			} else if lat >= 37.2 {
				line = -85.42
			}
			if lng < line {
				return zCentral
			}
		}
	case "TN":
		switch {
		case c != "":
			if in("|anderson|blount|bradley|campbell|carter|claiborne|cocke|grainger|greene|hamblen|hamilton|hancock|hawkins|jefferson|johnson|knox|loudon|mcminn|meigs|monroe|morgan|polk|rhea|roane|scott|sevier|sullivan|unicoi|union|washington|") {
				return zEastern
			}
			return zCentral
		case hasPoint:
			line := -84.75
			if lat < 35.25 {
				line = -85.45
			} else if lat < 35.75 {
				line = -85.1
			}
			if lng >= line {
				return zEastern
			}
		}
	case "MI":
		switch {
		case c != "":
			if in("|gogebic|iron|dickinson|menominee|") {
				return zCentral
			}
			return zEastern
		case hasPoint && ((lng < -87.6 && lat < 46.45) || (lng < -89.4 && lat < 46.75)):
			return zCentral
		}
	case "KS":
		switch {
		case c != "":
			if in("|sherman|wallace|greeley|hamilton|") {
				return zMountain
			}
			return zCentral
		case hasPoint && lng < -101.48 && lat > 37.74 && lat < 39.57:
			return zMountain
		}
	case "TX":
		switch {
		case c != "":
			if in("|el paso|hudspeth|") {
				return zMountain
			}
			return zCentral
		case hasPoint && lng < -104.92:
			return zMountain
		}
	case "OR":
		switch {
		case c == "malheur":
			if hasPoint && lat < 42.45 {
				return zPacific
			}
			return zMountain
		case c != "":
			return zPacific
		case hasPoint && lng > -118.2 && lat > 42.45 && lat < 44.5:
			return zMountain
		}
	case "ID":
		switch {
		case c == "idaho":
			if hasPoint && lat < 45.55 {
				return zMountain
			}
			return zPacific
		case c != "":
			if in("|boundary|bonner|kootenai|shoshone|benewah|latah|clearwater|nez perce|lewis|") {
				return zPacific
			}
			return zMountain
		case hasPoint && lat > 45.55:
			return zPacific
		}
	case "ND":
		west := hasPoint && ((lat < 47.0 && lng < -101.4) || (lat < 47.6 && lng < -102.6))
		switch {
		case in("|adams|billings|bowman|golden valley|grant|hettinger|slope|stark|"):
			return zMountain
		case in("|dunn|mckenzie|sioux|"):
			if west {
				return zMountain
			}
			return zCentral
		case c != "":
			return zCentral
		case west:
			return zMountain
		}
	case "SD":
		west := hasPoint && lng < -100.5
		switch {
		case in("|bennett|butte|corson|custer|dewey|fall river|haakon|harding|jackson|lawrence|meade|oglala lakota|shannon|pennington|perkins|ziebach|"):
			return zMountain
		case c == "stanley":
			return zMountain
		case c != "":
			return zCentral
		case west:
			return zMountain
		}
	case "NE":
		west := hasPoint && lng < -101.0
		switch {
		case in("|arthur|banner|box butte|chase|cheyenne|dawes|deuel|dundy|garden|grant|hooker|keith|kimball|morrill|perkins|scotts bluff|sheridan|sioux|"):
			return zMountain
		case c == "cherry":
			if west {
				return zMountain
			}
			return zCentral
		case c != "":
			return zCentral
		case west:
			return zMountain
		}
	case "AK":
		if hasPoint && (lng < -169.5 || lng > 0) {
			return zAleutian
		}
	}
	return st.Zone
}

// foldCounty reads "Davidson County" and "St. Louis Parish" as "davidson" and "st louis".
func foldCounty(county string) string {
	c := fold(county)
	for _, suffix := range []string{" county", " parish", " borough", " census area", " municipality"} {
		c = strings.TrimSuffix(c, suffix)
	}
	return c
}

// ZoneLabel is a zone as a person reads it: "Central time", "West Africa time".
func ZoneLabel(zone string) string {
	switch zone {
	case zEastern, "America/Detroit", "America/Indiana/Indianapolis", "America/Kentucky/Louisville":
		return "Eastern time"
	case zCentral:
		return "Central time"
	case zMountain, "America/Boise":
		return "Mountain time"
	case zArizona:
		return "Arizona time"
	case zPacific:
		return "Pacific time"
	case zAlaska:
		return "Alaska time"
	case zAleutian:
		return "Aleutian time"
	case zHawaii:
		return "Hawaii time"
	case zLagos:
		return "West Africa time"
	case "America/Puerto_Rico", "America/St_Thomas":
		return "Atlantic time"
	case "Pacific/Guam":
		return "Chamorro time"
	case "Pacific/Pago_Pago":
		return "Samoa time"
	}
	return zone
}
