// Package store persists Kaleid's catalog and records in PostgreSQL with
// pgvector. The catalog (tenants, databases, collections) lives in the
// "kaleid" schema; each collection's records live in their own table in the
// "kaleid_data" schema so every collection gets a correctly-typed vector
// column and an HNSW index with the right operator class.
package store

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"hash/fnv"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	pgxvec "github.com/pgvector/pgvector-go/pgx"
)

//go:embed migrations/*.sql
var migrationFS embed.FS

// Options configure the store.
type Options struct {
	// MaxScanTuples bounds pgvector's iterative index scan per query.
	MaxScanTuples int
	// ExactFallback re-runs a filtered KNN query as an exact scan when the
	// approximate scan returned fewer rows than requested but more exist.
	ExactFallback bool
	// IndexThreshold is the record count at which a collection's HNSW index
	// is built. Smaller collections are searched exactly, which is both fast
	// and perfectly accurate, and building the graph in bulk once is far
	// cheaper than maintaining it row by row. 0 builds the index immediately.
	IndexThreshold int
	// IndexBuildMemory is maintenance_work_mem used for index builds.
	IndexBuildMemory string
}

// Store is the PostgreSQL-backed repository.
type Store struct {
	pool *pgxpool.Pool
	opts Options
}

// DefaultDatabaseID is the id of default_tenant/default_database.
var DefaultDatabaseID = uuid.MustParse("00000000-0000-0000-0000-000000000000")

// Open connects, runs migrations, and returns a ready Store.
func Open(ctx context.Context, url string, opts Options) (*Store, error) {
	if opts.MaxScanTuples <= 0 {
		opts.MaxScanTuples = 20000
	}
	if opts.IndexThreshold < 0 {
		opts.IndexThreshold = 0
	}
	if opts.IndexBuildMemory == "" {
		opts.IndexBuildMemory = "256MB"
	}
	if err := migrate(ctx, url); err != nil {
		return nil, fmt.Errorf("migrate: %w", err)
	}
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		return nil, err
	}
	cfg.AfterConnect = func(ctx context.Context, conn *pgx.Conn) error {
		return pgxvec.RegisterTypes(ctx, conn)
	}
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, err
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, err
	}
	return &Store{pool: pool, opts: opts}, nil
}

// Close releases the pool.
func (s *Store) Close() { s.pool.Close() }

// Ping checks database connectivity.
func (s *Store) Ping(ctx context.Context) error { return s.pool.Ping(ctx) }

func migrate(ctx context.Context, url string) error {
	conn, err := pgx.Connect(ctx, url)
	if err != nil {
		return err
	}
	defer conn.Close(ctx)
	// Serialize concurrent migrators (multiple replicas starting at once).
	if _, err := conn.Exec(ctx, `SELECT pg_advisory_lock(727274)`); err != nil {
		return err
	}
	defer conn.Exec(context.Background(), `SELECT pg_advisory_unlock(727274)`) //nolint:errcheck
	if _, err := conn.Exec(ctx, `CREATE SCHEMA IF NOT EXISTS kaleid;
		CREATE TABLE IF NOT EXISTS kaleid.schema_migrations (version text PRIMARY KEY, applied_at timestamptz NOT NULL DEFAULT now())`); err != nil {
		return err
	}
	entries, err := migrationFS.ReadDir("migrations")
	if err != nil {
		return err
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	sort.Strings(names)
	for _, name := range names {
		version := strings.TrimSuffix(name, ".sql")
		var exists bool
		if err := conn.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM kaleid.schema_migrations WHERE version=$1)`, version).Scan(&exists); err != nil {
			return err
		}
		if exists {
			continue
		}
		body, err := migrationFS.ReadFile("migrations/" + name)
		if err != nil {
			return err
		}
		tx, err := conn.Begin(ctx)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, string(body)); err != nil {
			_ = tx.Rollback(ctx)
			return fmt.Errorf("%s: %w", name, err)
		}
		if _, err := tx.Exec(ctx, `INSERT INTO kaleid.schema_migrations(version) VALUES ($1)`, version); err != nil {
			_ = tx.Rollback(ctx)
			return err
		}
		if err := tx.Commit(ctx); err != nil {
			return err
		}
	}
	return nil
}

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

// dataTable returns the quoted record table name for a collection.
func dataTable(id uuid.UUID) string {
	return `kaleid_data."c_` + strings.ReplaceAll(id.String(), "-", "") + `"`
}

// sparseTable returns the quoted sparse postings table name for a collection.
func sparseTable(id uuid.UUID) string {
	return `kaleid_data."s_` + strings.ReplaceAll(id.String(), "-", "") + `"`
}

func indexName(id uuid.UUID, suffix string) string {
	return `"c_` + strings.ReplaceAll(id.String(), "-", "") + "_" + suffix + `"`
}

// lockKey derives a stable advisory lock key for a collection.
func lockKey(id uuid.UUID) int64 {
	h := fnv.New64a()
	h.Write(id[:])
	return int64(h.Sum64())
}

// withTx runs fn in a transaction.
func (s *Store) withTx(ctx context.Context, fn func(pgx.Tx) error) error {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return err
	}
	defer tx.Rollback(context.Background()) //nolint:errcheck
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// Now returns the database server time (used for heartbeat parity tests).
func (s *Store) Now(ctx context.Context) (time.Time, error) {
	var t time.Time
	err := s.pool.QueryRow(ctx, `SELECT now()`).Scan(&t)
	return t, err
}
