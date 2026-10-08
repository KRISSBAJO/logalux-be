package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// geocode turns an address into a map position using OpenStreetMap's
// Nominatim service. It is best effort: when it fails, the business simply
// has no pin until someone sets the position by hand. Nominatim asks for at
// most one request a second and a real User-Agent; this is only called when a
// staff member saves an address.
func geocode(ctx context.Context, parts ...string) (lat, lng float64, ok bool) {
	var clean []string
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			clean = append(clean, p)
		}
	}
	if len(clean) < 2 { // a city alone would put every business on the same spot
		return 0, 0, false
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://nominatim.openstreetmap.org/search?format=jsonv2&limit=1&q="+url.QueryEscape(strings.Join(clean, ", ")), nil)
	if err != nil {
		return 0, 0, false
	}
	req.Header.Set("User-Agent", "LogaLuxe/1.0 (+https://logaluxe.com/help)")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, 0, false
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return 0, 0, false
	}
	var out []struct {
		Lat string `json:"lat"`
		Lon string `json:"lon"`
	}
	if err := json.NewDecoder(res.Body).Decode(&out); err != nil || len(out) == 0 {
		return 0, 0, false
	}
	lat, err1 := strconv.ParseFloat(out[0].Lat, 64)
	lng, err2 := strconv.ParseFloat(out[0].Lon, 64)
	return lat, lng, err1 == nil && err2 == nil
}
