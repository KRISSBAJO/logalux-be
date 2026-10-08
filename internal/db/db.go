// Package db owns the connection pool, migrations, and the dev seed.
package db

import (
	"context"
	"embed"
	"fmt"
	"io/fs"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed migrations/*.sql
var migrations embed.FS

//go:embed seed.sql
var seedSQL string

//go:embed seed_merchant.sql
var seedMerchantSQL string

//go:embed seed_ops.sql
var seedOpsSQL string

//go:embed seed_places.sql
var seedPlacesSQL string

func Connect(ctx context.Context, url string) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		return nil, err
	}
	cfg.MaxConns = 10
	var pool *pgxpool.Pool
	// Postgres in Compose can take a few seconds to accept connections.
	for attempt := 1; attempt <= 20; attempt++ {
		pool, err = pgxpool.NewWithConfig(ctx, cfg)
		if err == nil {
			if err = pool.Ping(ctx); err == nil {
				return pool, nil
			}
			pool.Close()
		}
		slog.Warn("database not ready, retrying", "attempt", attempt, "err", err)
		time.Sleep(1500 * time.Millisecond)
	}
	return nil, fmt.Errorf("connect database: %w", err)
}

// Migrate applies every file in migrations/ in name order, once each.
func Migrate(ctx context.Context, pool *pgxpool.Pool) error {
	_, err := pool.Exec(ctx, `create table if not exists schema_migrations (name text primary key, applied_at timestamptz not null default now())`)
	if err != nil {
		return err
	}
	entries, err := fs.ReadDir(migrations, "migrations")
	if err != nil {
		return err
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".sql") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	for _, name := range names {
		var done bool
		if err := pool.QueryRow(ctx, `select exists(select 1 from schema_migrations where name=$1)`, name).Scan(&done); err != nil {
			return err
		}
		if done {
			continue
		}
		body, err := migrations.ReadFile("migrations/" + name)
		if err != nil {
			return err
		}
		tx, err := pool.Begin(ctx)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, string(body)); err != nil {
			_ = tx.Rollback(ctx)
			return fmt.Errorf("migration %s: %w", name, err)
		}
		if _, err := tx.Exec(ctx, `insert into schema_migrations(name) values($1)`, name); err != nil {
			_ = tx.Rollback(ctx)
			return err
		}
		if err := tx.Commit(ctx); err != nil {
			return err
		}
		slog.Info("applied migration", "name", name)
	}
	return nil
}

// Seed loads sample data if the businesses table is empty.
func Seed(ctx context.Context, pool *pgxpool.Pool) error {
	var n int
	if err := pool.QueryRow(ctx, `select count(*) from businesses`).Scan(&n); err != nil {
		return err
	}
	if n > 0 {
		return nil
	}
	if _, err := pool.Exec(ctx, seedSQL); err != nil {
		return fmt.Errorf("seed: %w", err)
	}
	slog.Info("seeded sample data")
	return nil
}

// SeedOps loads sample payouts once, for databases seeded before they existed.
func SeedOps(ctx context.Context, pool *pgxpool.Pool) error {
	var n int
	if err := pool.QueryRow(ctx, `select count(*) from payouts`).Scan(&n); err != nil {
		return err
	}
	if n > 0 {
		return nil
	}
	if _, err := pool.Exec(ctx, seedOpsSQL); err != nil {
		return fmt.Errorf("seed ops: %w", err)
	}
	return nil
}

// SeedMerchant loads sample history for the merchant screens: paid visits,
// sales, ledger lines and payouts. It runs once, while no sale exists.
func SeedMerchant(ctx context.Context, pool *pgxpool.Pool) error {
	var n int
	if err := pool.QueryRow(ctx, `select count(*) from sales`).Scan(&n); err != nil {
		return err
	}
	if n > 0 {
		return nil
	}
	if _, err := pool.Exec(ctx, seedMerchantSQL); err != nil {
		return fmt.Errorf("seed merchant: %w", err)
	}
	slog.Info("seeded sample history for the merchant screens")
	return nil
}

// SeedPlaces adds the sample businesses in other cities (Atlanta, Houston,
// New York, Los Angeles, Memphis, Abuja, Port Harcourt). It runs once, on a
// database that holds the sample data and does not have them yet; it changes
// nothing that was seeded before.
func SeedPlaces(ctx context.Context, pool *pgxpool.Pool) error {
	var samples, done bool
	if err := pool.QueryRow(ctx, `select exists(select 1 from businesses where slug='ada'), exists(select 1 from businesses where slug='peachtree')`).Scan(&samples, &done); err != nil {
		return err
	}
	if !samples || done {
		return nil
	}
	if _, err := pool.Exec(ctx, seedPlacesSQL); err != nil {
		return fmt.Errorf("seed places: %w", err)
	}
	slog.Info("seeded sample businesses in more cities")
	return nil
}
