// Package postgres holds the pgx connection pool and every repository backing Postgres-persisted
// state (accounts, workspaces, the semantic layer, run/step state, reports, the audit/egress log —
// per architecture.md, ALL durable state lives in this one database).
//
// This is shared infrastructure: NewPool is created exactly once in cmd/api/main.go and threaded
// through to every repository, rather than opened ad hoc per package. Later waves add repositories
// here (e.g. workspaces.go, runs.go) alongside users.go, all sharing the one Pool.
package postgres

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Pool wraps a pgx connection pool.
type Pool struct {
	*pgxpool.Pool
}

// NewPool opens a connection pool against databaseURL and verifies connectivity with a ping.
//
// Failing fast here, rather than lazily on first query, matches config.Load's philosophy: a service
// that starts against an unreachable database should refuse to start, not accept traffic and fail
// requests one at a time.
func NewPool(ctx context.Context, databaseURL string) (*Pool, error) {
	cfg, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		return nil, fmt.Errorf("parsing database URL: %w", err)
	}

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("opening connection pool: %w", err)
	}

	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("pinging database: %w", err)
	}

	return &Pool{pool}, nil
}
