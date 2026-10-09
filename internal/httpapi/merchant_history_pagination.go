package httpapi

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
)

// historyRows receives trusted SQL and explicit projected sort columns. URL values
// are parameters; counts, search and paging share the same merchant-scoped source.
func (s *Server) historyRows(r *http.Request, key, query, defaultOrder, allowed string, args ...any) ([]M, M, error) {
	q := r.URL.Query()
	page, _ := strconv.Atoi(q.Get(key + "_page"))
	if page < 1 {
		page = 1
	}
	per, _ := strconv.Atoi(q.Get(key + "_per_page"))
	if per < 1 {
		per = 50
	}
	if per > 200 {
		per = 200
	}
	at := strings.LastIndex(strings.ToLower(query), "order by ")
	if at < 0 {
		return nil, nil, fmt.Errorf("history requires ordering")
	}
	base := "select * from (" + strings.TrimSpace(query[:at]) + ") history"
	args = append(args, strings.TrimSpace(q.Get(key+"_q")))
	base += fmt.Sprintf(" where ($%d = '' or exists(select 1 from jsonb_each_text(to_jsonb(history)) searchable where strpos(lower(searchable.value), lower($%d)) > 0))", len(args), len(args))
	var total int
	if err := s.pool.QueryRow(r.Context(), "select count(*) from ("+base+") counted", args...).Scan(&total); err != nil {
		return nil, nil, err
	}
	pages := (total + per - 1) / per
	if pages < 1 {
		pages = 1
	}
	if page > pages {
		page = pages
	}
	order := defaultOrder
	sort := q.Get(key + "_sort")
	for _, column := range strings.Fields(allowed) {
		if column == sort {
			direction := "asc"
			if q.Get(key+"_direction") == "desc" {
				direction = "desc"
			}
			order = `"` + column + `" ` + direction + " nulls last"
			break
		}
	}
	// Every scoped source projects a unique row identity (or a grouped month).
	// Keep equal timestamps/amounts stable without sorting complete JSON payloads.
	tie := "id asc"
	if key == "statements" {
		tie = "month asc"
	}
	if key == "payments" {
		tie = "reference asc"
	}
	order += ", " + tie
	args = append(args, per, (page-1)*per)
	out, err := rows(r.Context(), s.pool, base+" order by "+order+fmt.Sprintf(" limit $%d offset $%d", len(args)-1, len(args)), args...)
	return out, M{"page": page, "per_page": per, "total": total, "pages": pages, "has_next": page < pages, "sort_columns": strings.Fields(allowed)}, err
}

// Brand returns (empty business ID) and merchant returns share the scoped query.
// State totals and the chosen answer are independent of the loaded history page.
func (s *Server) returnHistoryPage(r *http.Request, businessID string) ([]M, M, M, M, error) {
	out, page, err := s.historyRows(r, "returns", returnHistorySQL, "(status = 'requested') desc, created_at desc", "created_at customer_name status reason refund_cents", businessID, firstNonEmpty(r.URL.Query().Get("returns_status"), r.URL.Query().Get("status")))
	if err != nil {
		return nil, nil, nil, nil, err
	}
	counts, err := row(r.Context(), s.pool, `select count(*) as all, count(*) filter(where status='requested') as requested, count(*) filter(where status='approved') as approved, count(*) filter(where status='refused') as refused, count(*) filter(where provider_refund_status='pending') as provider_pending from order_returns where (($1='' and business_id is null) or business_id::text=$1)`, businessID)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	var selected M
	if id := r.URL.Query().Get("answer"); id != "" {
		selected, err = row(r.Context(), s.pool, "select * from ("+returnHistorySQL+") selected where id::text=$3", businessID, "", id)
		if err != nil && !isNoRows(err) {
			return nil, nil, nil, nil, err
		}
	}
	return out, page, counts, selected, nil
}
