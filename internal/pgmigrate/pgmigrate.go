// Package pgmigrate applies pending Postgres schema migrations using goose
// as a library.
//
// Bookkeeping lives in a table named "schema_migrations" (via WithStore),
// not goose's default "goose_db_version" which is consistent with D1's
// migration bookkeeping table name (internal/d1migrate).
package pgmigrate

import (
	"context"
	"fmt"
	"io/fs"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
	"github.com/pressly/goose/v3/database"
	"github.com/pressly/goose/v3/lock"
)

// Run applies any pending migrations in `migrations` against pool. Safe to
// call on every startup, and safe to call concurrently.
func Run(ctx context.Context, pool *pgxpool.Pool, migrations fs.FS) error {
	db := stdlib.OpenDBFromPool(pool)
	defer db.Close() // closes only this *sql.DB wrapper, not the underlying pool

	store, err := database.NewStore(database.DialectPostgres, "schema_migrations")
	if err != nil {
		return fmt.Errorf("creating migration store: %w", err)
	}

	sessionLocker, err := lock.NewPostgresSessionLocker()
	if err != nil {
		return fmt.Errorf("creating session locker: %w", err)
	}

	// The empty dialect string here is required, not incidental: goose's
	// NewProvider rejects a non-empty dialect whenever a custom Store is
	// also supplied via WithStore.
	provider, err := goose.NewProvider("", db, migrations,
		goose.WithStore(store),
		goose.WithSessionLocker(sessionLocker),
	)
	if err != nil {
		return fmt.Errorf("creating migration provider: %w", err)
	}

	if _, err := provider.Up(ctx); err != nil {
		return fmt.Errorf("applying migrations: %w", err)
	}

	return nil
}
