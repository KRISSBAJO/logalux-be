package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

type M = map[string]any

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, M{"error": msg})
}

func readJSON(r *http.Request, dst any) error {
	dec := json.NewDecoder(http.MaxBytesReader(nil, r.Body, 1<<20))
	dec.DisallowUnknownFields()
	return dec.Decode(dst)
}

// rows runs a query and returns each row as a map, which marshals straight to JSON.
func rows(ctx context.Context, pool database, sql string, args ...any) ([]M, error) {
	rs, err := pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	out, err := pgx.CollectRows(rs, pgx.RowToMap)
	if err != nil {
		return nil, err
	}
	if out == nil {
		out = []M{}
	}
	for _, m := range out {
		tidy(m)
	}
	return out, nil
}

// row returns one row as a map, or pgx.ErrNoRows.
func row(ctx context.Context, pool database, sql string, args ...any) (M, error) {
	rs, err := pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	m, err := pgx.CollectOneRow(rs, pgx.RowToMap)
	if err != nil {
		return nil, err
	}
	tidy(m)
	return m, nil
}

// tidy makes pgx's raw types JSON-friendly: uuid bytes become strings, numerics become floats.
func tidy(m M) {
	for k, v := range m {
		switch t := v.(type) {
		case [16]byte:
			m[k] = fmt.Sprintf("%x-%x-%x-%x-%x", t[0:4], t[4:6], t[6:8], t[8:10], t[10:16])
		case pgtype.Numeric:
			if f, err := t.Float64Value(); err == nil && f.Valid {
				m[k] = f.Float64
			}
		}
	}
}

func isNoRows(err error) bool { return errors.Is(err, pgx.ErrNoRows) }
