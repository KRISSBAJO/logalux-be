package httpapi

import (
	"context"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// database lets a complete payment use the same transaction, including nested checkout savepoints.
type database interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
	QueryRow(context.Context, string, ...any) pgx.Row
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
	Begin(context.Context) (pgx.Tx, error)
	Ping(context.Context) error
	Acquire(context.Context) (*pgxpool.Conn, error)
}

type paymentDatabase struct {
	database
	tx pgx.Tx
}

func (d *paymentDatabase) Query(c context.Context, q string, a ...any) (pgx.Rows, error) {
	return d.tx.Query(c, q, a...)
}
func (d *paymentDatabase) QueryRow(c context.Context, q string, a ...any) pgx.Row {
	return d.tx.QueryRow(c, q, a...)
}
func (d *paymentDatabase) Exec(c context.Context, q string, a ...any) (pgconn.CommandTag, error) {
	return d.tx.Exec(c, q, a...)
}
func (d *paymentDatabase) Begin(c context.Context) (pgx.Tx, error) { return d.tx.Begin(c) }

// External notifications must not observe or announce a rolled-back payment.
func (s *Server) afterPaymentCommit(fn func(*Server)) bool {
	if s.paymentEffects == nil {
		return false
	}
	parent := *s
	if d, ok := s.pool.(*paymentDatabase); ok {
		parent.pool = d.database
	}
	parent.paymentEffects = nil
	*s.paymentEffects = append(*s.paymentEffects, func() { fn(&parent) })
	return true
}
