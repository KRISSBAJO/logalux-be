package httpapi

import (
	"net/http/httptest"
	"testing"
)

func TestAdminFirstLoginAllowedRoutes(t *testing.T) {
	for _, tc := range []struct {
		method, path string
		allowed      bool
	}{
		{"GET", "/v1/admin/me", true}, {"POST", "/v1/admin/password", true}, {"POST", "/v1/admin/logout", true},
		{"GET", "/v1/admin/team", false}, {"GET", "/v1/admin/overview", false}, {"POST", "/v1/admin/fees", false}, {"GET", "/v1/admin/password", false},
	} {
		if got := adminPasswordChangeAllowed(httptest.NewRequest(tc.method, tc.path, nil)); got != tc.allowed {
			t.Errorf("%s %s: %v", tc.method, tc.path, got)
		}
	}
}
