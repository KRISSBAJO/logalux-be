package httpapi

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"logaluxe/api/internal/geo"
)

// Finding businesses: by what they do, by the place they are in, by how far
// they are from a point, or by what a map is showing.

// The radius of a search by distance starts at 25 (miles in the United States,
// kilometres elsewhere) and widens through these steps when little is nearby.
const defaultRadius = 25.0

var radiusSteps = []float64{50, 100, 250}

// A search by distance that finds fewer than this many nearby looks further out.
const fewNearby = 3

// haversineSQL is the distance in kilometres from a point to a location row "l".
func haversineSQL(lat, lng string) string {
	return `(6371 * 2 * asin(least(1, sqrt(power(sin(radians(l.lat - ` + lat + `::float8) / 2), 2) + cos(radians(` + lat + `::float8)) * cos(radians(l.lat)) * power(sin(radians(l.lng - ` + lng + `::float8) / 2), 2)))))`
}

func trimNum(n float64) string { return strconv.FormatFloat(round(n, 1), 'f', -1, 64) }

// distanceWords is a distance inside a sentence: "210 miles", "2.3 kilometres".
func distanceWords(km float64, unit string) string {
	return strings.TrimSuffix(geo.DistanceText(km, unit), " "+unit) + " " + geo.UnitWord(unit)
}

// GET /v1/businesses
//
//	q, category, market, where, sort, limit, offset, quiet   as before
//	lat, lng            a point: results carry their distance from it
//	radius, unit, widen with a point and no place: only businesses within the radius, nearest first
//	place               a place slug (nashville-tn, lagos-state, nigeria), or city + region + country, or country alone
//	scope               US or NG: only businesses of that country, in every mode ("market" is the older name)
//	bbox                south,west,north,east: only businesses inside it ("search this area")
func (s *Server) listBusinesses(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	q := r.URL.Query()
	// A page of results. A city can hold hundreds of businesses, so never send them all as cards.
	limit, _ := strconv.Atoi(q.Get("limit"))
	if limit <= 0 || limit > 60 {
		limit = 60
	}
	offset, _ := strconv.Atoi(q.Get("offset"))
	if offset < 0 {
		offset = 0
	}
	sortBy := q.Get("sort")
	// The country being browsed. Someone in the United States is not shown a barber in Nigeria unless they ask.
	scope := strings.ToUpper(strings.TrimSpace(firstNonEmpty(q.Get("scope"), q.Get("market"))))
	if scope != "" && !geo.Supported(scope) {
		writeErr(w, 400, "scope must be US or NG")
		return
	}

	lat, lng, hasPoint, ok := pointParam(q.Get("lat"), q.Get("lng"))
	if !ok {
		writeErr(w, 400, "lat and lng must be a point on Earth")
		return
	}

	// The place, by slug or by its parts.
	var where *place
	city, region, country := strings.Join(strings.Fields(q.Get("city")), " "), strings.TrimSpace(q.Get("region")), geo.CleanCountry(q.Get("country"))
	if slug := strings.TrimSpace(q.Get("place")); slug != "" {
		p, _, found := s.resolvePlace(ctx, slug, false)
		if !found {
			writeErr(w, 404, "we do not know that place")
			return
		}
		where, city, region, country = &p, p.City, p.Region, p.Country
	} else if city != "" || region != "" || q.Get("country") != "" {
		if country == "" && q.Get("country") != "" {
			writeErr(w, 400, "country must be US or NG")
			return
		}
		if region != "" {
			found := false
			for _, c := range geo.Countries {
				if country != "" && c != country {
					continue
				}
				if rg, ok := geo.Region(c, region); ok {
					region, country, found = rg, c, true
					break
				}
			}
			if !found {
				writeErr(w, 400, "we do not know that state")
				return
			}
		}
		if country == "" {
			writeErr(w, 400, "a city needs its state, or at least its country")
			return
		}
		if region != "" || city == "" { // a city and state, a state, or the whole country
			p := newPlace(city, region, country, 0, 0)
			if known, _, found := s.resolvePlace(ctx, p.Slug, false); found {
				p = known
			}
			where = &p
		}
	}
	inPlace := country != ""

	// What the map shows.
	var box []float64
	if raw := strings.TrimSpace(q.Get("bbox")); raw != "" {
		for _, part := range strings.Split(raw, ",") {
			n, err := strconv.ParseFloat(strings.TrimSpace(part), 64)
			if err != nil {
				box = nil
				break
			}
			box = append(box, n)
		}
		if len(box) != 4 || !geo.ValidPoint(box[0], box[1]) || !geo.ValidPoint(box[2], box[3]) || box[0] >= box[2] || box[1] >= box[3] {
			writeErr(w, 400, "bbox is south,west,north,east in degrees")
			return
		}
	}

	// Miles in the United States, kilometres in Nigeria and everywhere else.
	unit := "km"
	switch {
	case q.Get("unit") == "mi" || q.Get("unit") == "km":
		unit = q.Get("unit")
	case inPlace:
		unit = geo.Unit(country)
	case hasPoint:
		unit = geo.Unit(geo.CountryOfPoint(lat, lng))
	case box != nil:
		unit = geo.Unit(geo.CountryOfPoint((box[0]+box[2])/2, (box[1]+box[3])/2))
	case scope != "":
		unit = geo.Unit(scope)
	}

	mode := "all"
	switch {
	case box != nil:
		mode = "bbox"
	case inPlace:
		mode = "place"
	case hasPoint:
		mode = "near"
	}

	const matches = `b.status='live' and coalesce((b.settings->'booking'->>'on_search')::boolean, true)
		  and ($1='' or b.name ilike '%'||$1||'%' or b.tagline ilike '%'||$1||'%' or b.category ilike '%'||$1||'%'
		       or exists (select 1 from services sv where sv.business_id=b.id and sv.online and sv.name ilike '%'||$1||'%'))
		  and ($2='' or b.category=$2)
		  and ($3='' or b.market=$3)
		  and ($4='' or l.city ilike '%'||$4||'%' or l.name ilike '%'||$4||'%' or l.address ilike '%'||$4||'%' or l.region ilike $4)`
	const fromPrice = `(select min(price_cents) from services sv where sv.business_id=b.id and sv.online and sv.category <> 'Add-ons')`
	// Someone who goes to their clients is offered only where they say they go.
	const reaches = `(not l.travels or l.travel_radius_km is null or l.km <= l.travel_radius_km)`

	// source builds "from businesses joined to the one location that answers this search" and its arguments.
	// withinKm > 0 keeps locations within that distance of the point (a rectangle first, so the index on
	// position does the narrowing, then the exact distance).
	source := func(withinKm float64) (string, []any) {
		args := []any{strings.TrimSpace(q.Get("q")), q.Get("category"), scope, strings.TrimSpace(q.Get("where"))}
		arg := func(v any) string { args = append(args, v); return "$" + strconv.Itoa(len(args)) }
		km := "null::float8"
		if hasPoint {
			km = haversineSQL(arg(lat), arg(lng))
		}
		var conds []string
		join := "join"
		if mode == "all" {
			conds, join = append(conds, "l.is_primary"), "left join" // a business with no location yet is still listed, as before
		}
		if mode == "near" {
			conds = append(conds, "l.lat is not null and l.lng is not null")
		}
		if inPlace {
			conds = append(conds, "l.country = "+arg(country))
			if region != "" {
				conds = append(conds, "l.region = "+arg(region))
			}
			if city != "" {
				conds = append(conds, citySlugSQL+" = "+arg(geo.Slugify(city)))
			}
		}
		if box != nil {
			conds = append(conds, fmt.Sprintf("l.lat between %s and %s and l.lng between %s and %s", arg(box[0]), arg(box[2]), arg(box[1]), arg(box[3])))
		}
		after := ""
		if mode == "near" {
			after = " and " + reaches
			if withinKm > 0 {
				if south, west, north, east, whole := geo.Box(lat, lng, withinKm); !whole {
					conds = append(conds, fmt.Sprintf("l.lat between %s and %s and l.lng between %s and %s", arg(south), arg(north), arg(west), arg(east)))
				}
				after += " and l.km <= " + arg(withinKm)
			}
		}
		sql := `from businesses b ` + join + ` (
			select distinct on (l.business_id) l.business_id, l.id as location_id, l.name, l.address, l.city, l.region, l.country, l.hours, l.lat, l.lng,
			       l.travels, l.travel_radius_km, l.position_source, ` + km + ` as km
			from locations l where ` + strings.Join(conds, " and ") + `
			order by l.business_id, km asc nulls last, l.is_primary desc
		) l on l.business_id = b.id
		where ` + matches + after
		return sql, args
	}

	// How far to look.
	asked, used := 0.0, 0.0 // in the unit; used == 0 means no limit
	widened, withinAsked := false, -1
	if mode == "near" {
		asked, _ = strconv.ParseFloat(q.Get("radius"), 64)
		if asked <= 0 {
			asked = defaultRadius
		}
		if asked > 500 {
			asked = 500
		}
		used = asked
		steps := []float64{asked}
		if q.Get("widen") != "0" {
			for _, st := range radiusSteps {
				if st > asked {
					steps = append(steps, st)
				}
			}
		}
		// One pass counts how many match inside each step.
		from, args := source(geo.ToKm(steps[len(steps)-1], unit))
		cols := make([]string, len(steps))
		for i, st := range steps {
			args = append(args, geo.ToKm(st, unit))
			cols[i] = "count(*) filter (where l.km <= $" + strconv.Itoa(len(args)) + ")::int"
		}
		counts := make([]int, len(steps))
		dest := make([]any, len(steps))
		for i := range counts {
			dest[i] = &counts[i]
		}
		if err := s.pool.QueryRow(ctx, "select "+strings.Join(cols, ", ")+" "+from, args...).Scan(dest...); err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		withinAsked = counts[0]
		last := len(steps) - 1
		if counts[0] < fewNearby && len(steps) > 1 {
			pick := -1
			for i, n := range counts {
				if n >= fewNearby {
					pick = i
					break
				}
			}
			if pick < 0 { // never enough: go only as far as adds someone
				for i, n := range counts {
					if n == counts[last] {
						pick = i
						break
					}
				}
			}
			if counts[last] == 0 {
				used = 0 // nothing within the furthest step: the nearest, however far
			} else {
				used = steps[pick]
			}
			widened = used != asked
		}
	}

	from, args := source(geo.ToKm(used, unit))
	order := map[string]string{"reviews": "b.review_count desc, b.rating desc", "price": "from_cents asc nulls last, b.rating desc", "distance": "l.km asc nulls last, b.rating desc"}[sortBy]
	if sortBy == "distance" && !hasPoint {
		order = ""
	}
	recommended := false
	if order == "" {
		if mode == "near" && sortBy != "top" {
			order = "l.km asc, b.rating desc" // by distance, nearest first
		} else {
			// A business that bids for new clients is listed first, and marked as promoted.
			order, recommended = "boost desc, b.rating desc, b.review_count desc", true
			if mode == "near" && widened && asked > 0 {
				// The search had to look further out: whoever is really within the distance asked for leads the list.
				order = fmt.Sprintf("(l.km <= %.3f) desc, ", geo.ToKm(asked, unit)) + order
			}
		}
	}
	// The columns a card needs, shared with the passes that fill a short list.
	listCols := `b.id, b.slug, b.name, b.tagline, b.category, b.market, b.currency, b.timezone, b.rating, b.review_count,
		       b.verification_status, b.tone, b.highlights, (select sm.id from site_media sm where sm.slot='logo' and sm.ref = b.slug and sm.active limit 1) as logo_id,
		       l.name as area, l.city, l.region, l.country, l.hours, l.lat, l.lng, l.location_id, coalesce(l.travels, false) as travels, l.travel_radius_km,
		       case when l.lat is null then 'none' when l.travels or l.position_source = 'city' then 'area' else 'exact' end as pin,
		       l.km as distance_km, count(*) over() as total, ` + boostSQL + `::float8 as boost,
		       (select count(*) from staff st where st.business_id=b.id and st.bookable) as staff_count,
		       ` + fromPrice + ` as from_cents,
		       -- The first few services, so a result can show real prices without opening the page.
		       coalesce((select jsonb_agg(x) from (select sv.name, sv.price_cents, sv.duration_min from services sv
		         where sv.business_id=b.id and sv.online and sv.category <> 'Add-ons'
		         order by ($1 <> '' and sv.name ilike '%'||$1||'%') desc, sv.sort, sv.name limit 3) x), '[]'::jsonb) as services
`
	out, err := rows(ctx, s.pool, `select `+listCols+` `+from+` order by `+order+`, b.name limit `+strconv.Itoa(limit)+` offset `+strconv.Itoa(offset), args...)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	total := 0
	var seen, promoted []string
	for _, b := range out {
		if n, ok := b["total"].(int64); ok {
			total = int(n)
		}
		delete(b, "total")
		boost, _ := b["boost"].(float64)
		b["promoted"] = boost > 0 && recommended
		delete(b, "boost")
		seen = append(seen, fmt.Sprint(b["id"]))
		if boost > 0 && recommended {
			promoted = append(promoted, fmt.Sprint(b["id"]))
		}
		setDistance(b, unit)
	}
	// fill=N: a row of cards is never left short. When fewer than N are near, the best of the country
	// follow, then the best anywhere, each marked with its tier so a screen can say where they are from.
	fill, _ := strconv.Atoi(q.Get("fill"))
	if fill > 24 {
		fill = 24
	}
	filled := M{}
	if fill > 0 && offset == 0 && (mode == "near" || mode == "place") {
		for _, b := range out {
			b["tier"] = "near"
		}
		filled["near"] = len(out)
		more := func(tier, countryCond string) {
			if len(out) >= fill {
				return
			}
			var ids []string
			for _, b := range out {
				ids = append(ids, fmt.Sprint(b["id"]))
			}
			targs := []any{strings.TrimSpace(q.Get("q")), q.Get("category"), "", "", ids}
			km := "null::float8"
			if hasPoint {
				targs = append(targs, lat, lng)
				km = haversineSQL("$6", "$7")
			}
			tfrom := `from businesses b join (
				select distinct on (l.business_id) l.business_id, l.id as location_id, l.name, l.address, l.city, l.region, l.country, l.hours, l.lat, l.lng,
				       l.travels, l.travel_radius_km, l.position_source, ` + km + ` as km
				from locations l where ` + countryCond + `
				order by l.business_id, l.is_primary desc
			) l on l.business_id = b.id
			where ` + matches + ` and not (b.id = any($5::uuid[]))`
			extra, err := rows(ctx, s.pool, `select `+listCols+` `+tfrom+` order by b.rating desc, b.review_count desc, b.name limit `+strconv.Itoa(fill-len(out)), targs...)
			if err != nil {
				return
			}
			for _, b := range extra {
				delete(b, "total")
				delete(b, "boost")
				b["promoted"] = false
				b["tier"] = tier
				setDistance(b, unit)
				out = append(out, b)
				seen = append(seen, fmt.Sprint(b["id"]))
			}
			filled[tier] = len(extra)
		}
		if scope != "" {
			more("country", "l.country = '"+scope+"'")
		} else {
			filled["country"] = 0
		}
		more("anywhere", "true")
		filled["short"] = 0
		if len(out) < fill {
			filled["short"] = fill - len(out)
		}
	}
	// A sitemap build or another machine reading the list is not a person looking at it.
	if q.Get("quiet") != "1" {
		s.countLeadEvents("impression", seen...)
		s.countLeadEvents("promoted_impression", promoted...)
	}
	// Every match as a light map pin, so the map shows the whole area while the list shows one page.
	pinOrder := "b.rating desc, b.review_count desc"
	if mode == "near" {
		pinOrder = "l.km asc"
	}
	pins, _ := rows(ctx, s.pool, `
		select b.slug, b.name, b.rating, b.currency, l.lat, l.lng, `+fromPrice+` as from_cents,
		       case when l.travels or l.position_source = 'city' then 'area' else 'exact' end as pin, l.km as distance_km
		`+from+` and l.lat is not null and l.lng is not null
		order by `+pinOrder+` limit 800`, args...)
	for _, p := range pins {
		setDistance(p, unit)
	}
	if total == 0 && offset > 0 { // a page past the end
		_ = s.pool.QueryRow(ctx, `select count(*) `+from, args...).Scan(&total)
	}

	// Say plainly what was searched, and what was done when little was nearby.
	g := M{"mode": mode, "unit": unit, "scope": scope, "origin": nil, "place": where, "radius_asked": nil, "radius_used": nil, "widened": widened, "within_asked": nil, "nearest": nil, "notice": "", "nearest_places": []place{}}
	if hasPoint {
		g["origin"] = M{"lat": lat, "lng": lng}
	}
	if box != nil {
		g["bbox"] = M{"south": box[0], "west": box[1], "north": box[2], "east": box[3]}
	}
	centreLat, centreLng, hasCentre := lat, lng, hasPoint
	if mode == "place" && where != nil && (where.Lat != 0 || where.Lng != 0) {
		centreLat, centreLng, hasCentre = where.Lat, where.Lng, true
	}
	if mode == "near" {
		g["radius_asked"], g["within_asked"] = asked, withinAsked
		if used > 0 {
			g["radius_used"] = used
		}
		// The nearest match, whatever order the list is in.
		if total > 0 && (widened || sortBy != "") {
			if n, err := row(ctx, s.pool, `select l.city, l.region, l.country, l.km as distance_km `+from+` order by l.km asc limit 1`, args...); err == nil {
				g["nearest"] = nearestOut(n, unit)
			}
		} else if total > 0 && len(out) > 0 && offset == 0 {
			g["nearest"] = nearestOut(out[0], unit)
		}
		words := trimNum(asked) + " " + geo.UnitWord(unit)
		switch {
		case widened && withinAsked == 0:
			if n, ok := g["nearest"].(M); ok {
				verb := "The nearest are in "
				if total == 1 {
					verb = "The nearest is in "
				}
				g["notice"] = "Nothing within " + words + ". " + verb + fmt.Sprint(n["label"]) + ", " + distanceWords(n["distance_km"].(float64), unit) + " away."
			}
		case widened:
			g["notice"] = "Only " + strconv.Itoa(withinAsked) + " within " + words + ", so this list goes out to " + trimNum(used) + " " + geo.UnitWord(unit) + "."
		case total == 0:
			g["notice"] = "Nothing within " + words + "."
		}
	}
	if hasCentre && (total == 0 || widened) {
		if live, err := s.livePlaces(ctx, false); err == nil {
			skip := ""
			if mode == "place" && where != nil {
				skip = where.Slug
			}
			if scope != "" { // only places in the country being browsed
				var own []place
				for _, l := range live {
					if l.Country == scope {
						own = append(own, l)
					}
				}
				live = own
			}
			g["nearest_places"] = nearestPlaces(live, centreLat, centreLng, unit, skip, 3)
		}
	}
	if fill > 0 && len(filled) > 0 {
		g["fill"] = filled
		near, country, anywhere := int(toInt(filled["near"])), int(toInt(filled["country"])), int(toInt(filled["anywhere"]))
		word := "professionals"
		if near == 1 {
			word = "professional"
		}
		here := "here"
		if where != nil {
			here = "near " + where.Label
			if where.Kind == "state" || where.Kind == "country" {
				here = "in " + where.Label
			}
		} else if n, ok := g["nearest"].(M); ok && near > 0 {
			here = "near " + fmt.Sprint(n["label"])
		}
		land := geo.CountryName(scope)
		if scope == "US" {
			land = "the United States"
		}
		sentence := ""
		switch {
		case near > 0 && near < fill && country > 0 && anywhere > 0:
			sentence = "Only " + strconv.Itoa(near) + " " + word + " " + here + " yet. The best elsewhere in " + land + " follow, then the best on LogaLuxe elsewhere."
		case near == 0 && country > 0 && anywhere > 0:
			sentence = "No professional is listed " + here + " yet. The best in " + land + " follow, then the best on LogaLuxe elsewhere."
		case near == 0 && country > 0:
			sentence = "No professional is listed " + here + " yet. These are the best in " + land + "."
		case near == 0 && anywhere > 0:
			sentence = "No professional is listed " + here + " yet. These are the best on LogaLuxe so far."
		case near > 0 && near < fill && country > 0:
			sentence = "Only " + strconv.Itoa(near) + " " + word + " " + here + " yet. The best elsewhere in " + land + " follow."
		case near > 0 && near < fill && anywhere > 0:
			sentence = "Only " + strconv.Itoa(near) + " " + word + " " + here + " yet. The best elsewhere on LogaLuxe follow."
		}
		g["fill_notice"] = sentence
	}
	writeJSON(w, 200, M{"businesses": out, "total": total, "pins": pins, "limit": limit, "offset": offset, "geo": g})
}

// setDistance writes a row's distance the three ways a screen needs it: kilometres, the unit's number, and text.
func setDistance(b M, unit string) {
	km, ok := b["distance_km"].(float64)
	if !ok {
		b["distance_km"], b["distance"], b["distance_unit"], b["distance_text"] = nil, nil, unit, ""
		return
	}
	b["distance_km"], b["distance"], b["distance_unit"], b["distance_text"] = round(km, 2), round(geo.FromKm(km, unit), 1), unit, geo.DistanceText(km, unit)
}

func nearestOut(b M, unit string) M {
	km, _ := b["distance_km"].(float64)
	city, region, country := fmt.Sprint(b["city"]), fmt.Sprint(b["region"]), fmt.Sprint(b["country"])
	return M{"city": city, "region": region, "country": country, "label": geo.Label(city, region, country), "slug": geo.PlaceSlug(city, region, country),
		"distance_km": round(km, 2), "distance": round(geo.FromKm(km, unit), 1), "distance_text": geo.DistanceText(km, unit)}
}
