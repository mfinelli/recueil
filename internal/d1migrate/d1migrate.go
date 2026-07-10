// Package d1migrate applies pending D1 schema migrations at backend
// startup, calling Cloudflare's D1 query API directly rather than going
// through the Worker or requiring wrangler to be installed. This is the one
// place the backend talks to Cloudflare directly instead of via the Worker.
//
// Applied migrations are tracked in a schema_migrations table this package
// owns and creates itself (deliberately not wrangler's `d1_migrations`
// table, since wrangler is never involved in this path and reusing its
// table name would risk two independent, uncoordinated bookkeeping systems
// touching the same table if wrangler were ever also pointed at this
// database).
package d1migrate

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"regexp"
	"sort"
	"strings"

	"github.com/cloudflare/cloudflare-go/v7"
	"github.com/cloudflare/cloudflare-go/v7/d1"
	"github.com/cloudflare/cloudflare-go/v7/option"
)

// bootstrapID must be the lowest-sorting migration: it creates
// schema_migrations itself, so it's the one migration that can't be gated
// by checking schema_migrations (that table doesn't exist yet when it
// runs). Its own SQL is idempotent (CREATE TABLE IF NOT EXISTS), so running
// it unconditionally on every startup is safe.
const bootstrapID = "0000_schema_migrations"

// Migration filenames become the id stored in schema_migrations and get
// interpolated directly into SQL (see applyAndRecord) rather than passed as
// a bound parameter, so they're constrained to a safe charset up front.
// These filenames come from our own embedded migrations directory, never
// from external input.
var idPattern = regexp.MustCompile(`^[a-zA-Z0-9_]+$`)

// Config holds what's needed to reach the D1 query API for a specific
// database. WorkerServiceSecret is not used here (this is a separate,
// narrower-scoped Cloudflare API token (D1:Edit on this database only), not
// the backend<->Worker shared secret).
type Config struct {
	AccountID  string
	DatabaseID string
	APIToken   string
}

// Run applies any migrations in `migrations` (the fs.Sub of a go:embed'd
// directory) not yet recorded in schema_migrations, in filename sort order.
// Safe to call on every startup (a no-op once nothing's pending).
func Run(ctx context.Context, cfg Config, migrations fs.FS) error {
	client := cloudflare.NewClient(option.WithAPIToken(cfg.APIToken))

	ids, files, err := readMigrations(migrations)
	if err != nil {
		return err
	}

	if len(ids) == 0 {
		return errors.New("no migration files found")
	}

	if ids[0] != bootstrapID {
		return fmt.Errorf("expected first migration to be %q, got %q",
			bootstrapID, ids[0])
	}

	// INSERT OR IGNORE here specifically because this one runs
	// unconditionally every startup, not gated by an "already applied"
	// check like everything else; re-running it must be a safe no-op.
	if err := applyAndRecord(ctx, client, cfg, migrations,
		files[bootstrapID], bootstrapID, true); err != nil {
		return fmt.Errorf("applying %s: %w", bootstrapID, err)
	}

	applied, err := appliedIDs(ctx, client, cfg)
	if err != nil {
		return fmt.Errorf("reading schema_migrations: %w", err)
	}

	for _, id := range ids[1:] {
		if applied[id] {
			continue
		}

		// Plain INSERT (not OR IGNORE): we've just confirmed this id
		// isn't in schema_migrations, so a conflict here means
		// something's actually wrong (e.g. a concurrent second backend
		// instance also migrating) and should fail loudly rather than
		// be silently swallowed.
		if err := applyAndRecord(ctx, client, cfg, migrations,
			files[id], id, false); err != nil {
			return fmt.Errorf("applying %s: %w", id, err)
		}
	}

	return nil
}

func readMigrations(migrations fs.FS) (ids []string, files map[string]string,
	err error) {
	entries, err := fs.ReadDir(migrations, ".")
	if err != nil {
		return nil, nil, fmt.Errorf("reading migrations dir: %w", err)
	}

	files = make(map[string]string)
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}
		id := strings.TrimSuffix(e.Name(), ".sql")
		if !idPattern.MatchString(id) {
			return nil, nil, fmt.Errorf(
				"migration filename %q invalid characters",
				e.Name())
		}
		ids = append(ids, id)
		files[id] = e.Name()
	}
	sort.Strings(ids)

	return ids, files, nil
}

// applyAndRecord runs a migration file's SQL and records it as applied in a
// single request. D1 executes semicolon-joined statements sent in one query
// as a batch inside its own implicit transaction, so bundling the
// migration's DDL with the schema_migrations insert here means there's no
// window where one succeeds and the other doesn't.
func applyAndRecord(
	ctx context.Context,
	client *cloudflare.Client,
	cfg Config,
	migrations fs.FS,
	filename, id string,
	ignoreConflict bool,
) error {
	sqlBytes, err := fs.ReadFile(migrations, filename)
	if err != nil {
		return fmt.Errorf("reading %s: %w", filename, err)
	}

	insertVerb := "INSERT INTO"
	if ignoreConflict {
		insertVerb = "INSERT OR IGNORE INTO"
	}
	combined := fmt.Sprintf("%s\n%s schema_migrations (id) VALUES ('%s');",
		string(sqlBytes), insertVerb, id)

	_, err = client.D1.Database.Query(ctx, cfg.DatabaseID,
		queryParams(cfg, combined))

	return err
}

func appliedIDs(
	ctx context.Context,
	client *cloudflare.Client,
	cfg Config,
) (map[string]bool, error) {
	page, err := client.D1.Database.Query(ctx, cfg.DatabaseID,
		queryParams(cfg, "SELECT id FROM schema_migrations;"))
	if err != nil {
		return nil, err
	}

	applied := make(map[string]bool)
	for _, result := range page.Result {
		for _, row := range result.Results {
			rowMap, ok := row.(map[string]any)
			if !ok {
				continue
			}
			if id, ok := rowMap["id"].(string); ok {
				applied[id] = true
			}
		}
	}

	return applied, nil
}

func queryParams(cfg Config, sql string) d1.DatabaseQueryParams {
	return d1.DatabaseQueryParams{
		AccountID: cloudflare.F(cfg.AccountID),
		Body: d1.DatabaseQueryParamsBodyD1SingleQuery{
			Sql: cloudflare.F(sql),
		},
	}
}
