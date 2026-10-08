package httpapi

import (
	"net/http"
	"net/url"

	"github.com/go-chi/chi/v5"
)

// Screens of the phone app a provider's page may hand back to. Nothing else is ever redirected to.
var appScreens = map[string]string{"payouts": "logaluxe://m/payouts"}

// GET /v1/app-return/{screen}?stripe=done
// A provider can only return to a web address. This one sends the person on into the phone app.
func (s *Server) appReturn(w http.ResponseWriter, r *http.Request) {
	to, ok := appScreens[chi.URLParam(r, "screen")]
	if !ok {
		http.NotFound(w, r)
		return
	}
	q := url.Values{}
	if v := r.URL.Query().Get("stripe"); v == "done" || v == "retry" {
		q.Set("stripe", v)
	}
	if len(q) > 0 {
		to += "?" + q.Encode()
	}
	http.Redirect(w, r, to, http.StatusFound)
}
