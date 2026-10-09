package httpapi

import (
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
)

// The column allowlist is shared by all admin list queries; no SQL identifier comes from the URL.
var adminSortColumns = map[string]string{
	"bookings": " id created_at starts_at client_name business total_cents status ",
	"clients":  " id created_at name business spent_cents bookings ",
	"orders":   " id created_at customer_name total_cents status ",
	"payouts":  " id scheduled_for business amount_cents status ",
	"audit":    " id created_at actor action target ",
	"support":  " id updated_at subject status priority ",
}

func adminPage(r *http.Request) (page, per int) {
	page, _ = strconv.Atoi(r.URL.Query().Get("page"))
	per, _ = strconv.Atoi(r.URL.Query().Get("per_page"))
	if page < 1 {
		page = 1
	}
	if page > 1000000 {
		page = 1000000
	}
	if per < 1 {
		per = 50
	}
	if per > 200 {
		per = 200
	}
	return
}

func (s *Server) adminRows(r *http.Request, query string, args ...any) ([]M, M, error) {
	page, per := adminPage(r)
	query = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(query), "limit 200"))
	at := strings.LastIndex(strings.ToLower(query), "order by ")
	if at < 0 {
		return nil, nil, fmt.Errorf("list requires deterministic ordering")
	}
	base, original := strings.TrimSpace(query[:at]), strings.TrimSpace(query[at+9:])
	var total int
	if err := s.pool.QueryRow(r.Context(), "select count(*) from ("+base+") counted", args...).Scan(&total); err != nil {
		return nil, nil, err
	}
	pages := (total + per - 1) / per
	if pages > 0 && page > pages {
		page = pages
	}
	order := original
	// Default ordering uses source aliases, so retain it inside the subquery.
	outer := ""
	sort := r.URL.Query().Get("sort")
	endpoint := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
	if sort != "" && slices.Contains(strings.Fields(adminSortColumns[endpoint]), sort) {
		dir := "asc"
		if r.URL.Query().Get("direction") == "desc" {
			dir = "desc"
		}
		outer = " order by \"" + sort + "\" " + dir + " nulls last, id asc"
	} else {
		order += ", 1 asc"
	}
	args = append(args, per, (page-1)*per)
	out, err := rows(r.Context(), s.pool, "select * from ("+base+" order by "+order+") result"+outer+fmt.Sprintf(" limit $%d offset $%d", len(args)-1, len(args)), args...)
	return out, M{"page": page, "per_page": per, "total": total, "pages": (total + per - 1) / per, "has_next": page*per < total}, err
}
