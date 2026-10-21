/*
 * recueil: self-hosted webpage bookmarker and archiver
 * Copyright © 2026 Mario Finelli
 *
 * This program is free software: you can redistribute it and/or modify
 * it under the terms of the GNU Affero General Public License as published by
 * the Free Software Foundation, either version 3 of the License, or
 * (at your option) any later version.
 *
 * This program is distributed in the hope that it will be useful,
 * but WITHOUT ANY WARRANTY; without even the implied warranty of
 * MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
 * GNU Affero General Public License for more details.
 *
 * You should have received a copy of the GNU Affero General Public License
 * along with this program. If not, see <https://www.gnu.org/licenses/>.
 */

package pgmigrate_test

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
	"github.com/pressly/goose/v3/database"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mfinelli/recueil/internal/pgmigrate"
)

// Migration 00017 makes every relationship join rows owned by the same user.
// It has to be exercised against data that already exists, which the shared
// test database can't offer, so each test here builds its own schema in the test database,
// migrates it to 00016, seeds it, and then applies 00017. Nothing here touches the shared
// "public" schema the rest of the suite uses, so it's safe to run alongside it.

const testDatabaseURL = "postgres://recueil:recueil@localhost:5432/recueil"

func migrationsDir() string {
	_, thisFile, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(thisFile), "..", "..", "migrations")
}

// upTo returns a copy of the migrations directory holding only versions through last.
func upTo(t *testing.T, last string) string {
	t.Helper()
	dir := t.TempDir()
	entries, err := os.ReadDir(migrationsDir())
	require.NoError(t, err)
	for _, e := range entries {
		if e.Name() > last+"_z" {
			continue
		}
		data, err := os.ReadFile(filepath.Join(migrationsDir(), e.Name()))
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(filepath.Join(dir, e.Name()), data, 0o600))
	}
	return dir
}

// isolatedPool returns a pool whose search_path is a brand new schema, dropped afterwards.
func isolatedPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	ctx := context.Background()
	suffix := make([]byte, 6)
	_, err := rand.Read(suffix)
	require.NoError(t, err)
	schema := "mig_" + hex.EncodeToString(suffix)

	admin, err := pgxpool.New(ctx, testDatabaseURL)
	require.NoError(t, err)
	_, err = admin.Exec(ctx, "CREATE SCHEMA "+schema)
	require.NoError(t, err)

	cfg, err := pgxpool.ParseConfig(testDatabaseURL)
	require.NoError(t, err)
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	require.NoError(t, err)

	t.Cleanup(func() {
		pool.Close()
		_, _ = admin.Exec(ctx, "DROP SCHEMA "+schema+" CASCADE")
		admin.Close()
	})
	return pool
}

func provider(t *testing.T, pool *pgxpool.Pool, dir string) *goose.Provider {
	t.Helper()
	store, err := database.NewStore(database.DialectPostgres, "schema_migrations")
	require.NoError(t, err)
	p, err := goose.NewProvider("", stdlib.OpenDBFromPool(pool), os.DirFS(dir), goose.WithStore(store))
	require.NoError(t, err)
	return p
}

type seeded struct {
	userA, userB    int64
	pa1, pa2, pb    int64
	la1, la2, lb    int64
	ta, tb          int64
	ca, caChild, cb int64
}

func insertID(t *testing.T, pool *pgxpool.Pool, sql string, args ...any) int64 {
	t.Helper()
	var id int64
	require.NoError(t, pool.QueryRow(context.Background(), sql+" RETURNING id", args...).Scan(&id))
	return id
}

func exec(t *testing.T, pool *pgxpool.Pool, sql string, args ...any) {
	t.Helper()
	_, err := pool.Exec(context.Background(), sql, args...)
	require.NoError(t, err)
}

// seedVersion16 inserts two users' data in the shape migration 00016 left, with every
// relationship consistent (each joins rows of one user).
func seedVersion16(t *testing.T, pool *pgxpool.Pool) seeded {
	t.Helper()
	var s seeded
	s.userA = insertID(t, pool, "INSERT INTO users (username, password_hash) VALUES ('a', 'x')")
	s.userB = insertID(t, pool, "INSERT INTO users (username, password_hash) VALUES ('b', 'x')")
	page := func(user int64, url string) int64 {
		return insertID(t, pool, "INSERT INTO pages (user_id, normalized_url, latest_capture_at) VALUES ($1, $2, now())", user, url)
	}
	link := func(user int64, url string) int64 {
		return insertID(t, pool, "INSERT INTO links (user_id, raw_url, normalized_url) VALUES ($1, $2, $2)", user, url)
	}
	tag := func(user int64, name string) int64 {
		return insertID(t, pool, "INSERT INTO tags (user_id, name, slug) VALUES ($1, $2, $2)", user, name)
	}
	coll := func(user int64, name string, parent any) int64 {
		return insertID(t, pool, "INSERT INTO collections (user_id, parent_id, name, slug) VALUES ($1, $3, $2, $2)", user, name, parent)
	}
	s.pa1, s.pa2, s.pb = page(s.userA, "https://example.com/a1"), page(s.userA, "https://example.com/a2"), page(s.userB, "https://example.com/b")
	s.la1, s.la2, s.lb = link(s.userA, "https://example.com/la1"), link(s.userA, "https://example.com/la2"), link(s.userB, "https://example.com/lb")
	s.ta, s.tb = tag(s.userA, "alpha"), tag(s.userB, "beta")
	s.ca = coll(s.userA, "shelf-a", nil)
	s.caChild = coll(s.userA, "child-a", s.ca)
	s.cb = coll(s.userB, "shelf-b", nil)

	// A consistent relationship of every kind, for each user where it makes sense.
	exec(t, pool, "INSERT INTO page_tags (page_id, tag_id) VALUES ($1, $2), ($3, $4)", s.pa1, s.ta, s.pb, s.tb)
	exec(t, pool, "INSERT INTO page_collections (page_id, collection_id) VALUES ($1, $2), ($3, $4)", s.pa1, s.ca, s.pb, s.cb)
	exec(t, pool, "INSERT INTO page_relations (page_id_a, page_id_b) VALUES (LEAST($1::bigint, $2::bigint), GREATEST($1::bigint, $2::bigint))", s.pa1, s.pa2)
	exec(t, pool, "INSERT INTO link_tags (link_id, tag_id) VALUES ($1, $2), ($3, $4)", s.la1, s.ta, s.lb, s.tb)
	exec(t, pool, "INSERT INTO link_collections (link_id, collection_id) VALUES ($1, $2), ($3, $4)", s.la1, s.ca, s.lb, s.cb)
	exec(t, pool, "INSERT INTO link_relations (link_id_a, link_id_b) VALUES (LEAST($1::bigint, $2::bigint), GREATEST($1::bigint, $2::bigint))", s.la1, s.la2)
	exec(t, pool, "INSERT INTO page_link_relations (page_id, link_id) VALUES ($1, $2), ($3, $4)", s.pa1, s.la1, s.pb, s.lb)
	return s
}

func count(t *testing.T, pool *pgxpool.Pool, sql string, args ...any) int {
	t.Helper()
	var n int
	require.NoError(t, pool.QueryRow(context.Background(), sql, args...).Scan(&n))
	return n
}

// hasConstraint is scoped to the schema under test: constraint names are catalog-wide, and
// the shared "public" schema holds the same ones.
func hasConstraint(t *testing.T, pool *pgxpool.Pool, name string) bool {
	t.Helper()
	return count(t, pool, `SELECT COUNT(*) FROM pg_constraint c JOIN pg_namespace n ON n.oid = c.connamespace
		WHERE c.conname = $1 AND n.nspname = current_schema()`, name) > 0
}

func TestEnforceSameOwner_BackfillsExistingRows(t *testing.T) {
	ctx := context.Background()
	pool := isolatedPool(t)
	require.NoError(t, pgmigrate.Run(ctx, pool, os.DirFS(upTo(t, "00016"))))
	s := seedVersion16(t, pool)

	require.NoError(t, pgmigrate.Run(ctx, pool, os.DirFS(migrationsDir())))

	// Every relation row's user_id is the owner of the rows it joins, taken from the parent.
	for _, tc := range []struct{ name, sql string }{
		{"page_tags", "SELECT COUNT(*) FROM page_tags r JOIN pages p ON p.id = r.page_id WHERE r.user_id = p.user_id"},
		{"page_collections", "SELECT COUNT(*) FROM page_collections r JOIN pages p ON p.id = r.page_id WHERE r.user_id = p.user_id"},
		{"page_relations", "SELECT COUNT(*) FROM page_relations r JOIN pages p ON p.id = r.page_id_a WHERE r.user_id = p.user_id"},
		{"link_tags", "SELECT COUNT(*) FROM link_tags r JOIN links l ON l.id = r.link_id WHERE r.user_id = l.user_id"},
		{"link_collections", "SELECT COUNT(*) FROM link_collections r JOIN links l ON l.id = r.link_id WHERE r.user_id = l.user_id"},
		{"link_relations", "SELECT COUNT(*) FROM link_relations r JOIN links l ON l.id = r.link_id_a WHERE r.user_id = l.user_id"},
		{"page_link_relations", "SELECT COUNT(*) FROM page_link_relations r JOIN pages p ON p.id = r.page_id WHERE r.user_id = p.user_id"},
	} {
		total := count(t, pool, "SELECT COUNT(*) FROM "+tc.name)
		assert.Positive(t, total, tc.name)
		assert.Equal(t, total, count(t, pool, tc.sql), "%s: every row's user_id is its owner's", tc.name)
	}
	assert.Equal(t, 2, count(t, pool, "SELECT COUNT(*) FROM page_tags WHERE user_id = $1 OR user_id = $2", s.userA, s.userB))

	// And now Postgres refuses a mismatched pair, which it didn't before.
	_, err := pool.Exec(ctx, "INSERT INTO page_tags (page_id, tag_id, user_id) VALUES ($1, $2, $3)", s.pa2, s.tb, s.userA)
	assert.Error(t, err)
	_, err = pool.Exec(ctx, "UPDATE collections SET parent_id = $1 WHERE id = $2", s.cb, s.caChild)
	assert.Error(t, err, "a collection can't be moved under another user's")
}

func TestEnforceSameOwner_RefusesInconsistentRowsBeforeChangingAnything(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name, message string
		corrupt       func(t *testing.T, pool *pgxpool.Pool, s *seeded)
	}{
		{"page_tags", "rows in page_tags join a page and a tag owned by different users", func(t *testing.T, p *pgxpool.Pool, s *seeded) {
			exec(t, p, "INSERT INTO page_tags (page_id, tag_id) VALUES ($1, $2)", s.pa2, s.tb)
		}},
		{"page_collections", "rows in page_collections join a page and a collection owned by different users", func(t *testing.T, p *pgxpool.Pool, s *seeded) {
			exec(t, p, "INSERT INTO page_collections (page_id, collection_id) VALUES ($1, $2)", s.pa2, s.cb)
		}},
		{"page_relations", "rows in page_relations join two pages owned by different users", func(t *testing.T, p *pgxpool.Pool, s *seeded) {
			exec(t, p, "INSERT INTO page_relations (page_id_a, page_id_b) VALUES (LEAST($1::bigint, $2::bigint), GREATEST($1::bigint, $2::bigint))", s.pa2, s.pb)
		}},
		{"link_tags", "rows in link_tags join a link and a tag owned by different users", func(t *testing.T, p *pgxpool.Pool, s *seeded) {
			exec(t, p, "INSERT INTO link_tags (link_id, tag_id) VALUES ($1, $2)", s.la2, s.tb)
		}},
		{"link_collections", "rows in link_collections join a link and a collection owned by different users", func(t *testing.T, p *pgxpool.Pool, s *seeded) {
			exec(t, p, "INSERT INTO link_collections (link_id, collection_id) VALUES ($1, $2)", s.la2, s.cb)
		}},
		{"link_relations", "rows in link_relations join two links owned by different users", func(t *testing.T, p *pgxpool.Pool, s *seeded) {
			exec(t, p, "INSERT INTO link_relations (link_id_a, link_id_b) VALUES (LEAST($1::bigint, $2::bigint), GREATEST($1::bigint, $2::bigint))", s.la2, s.lb)
		}},
		{"page_link_relations", "rows in page_link_relations join a page and a link owned by different users", func(t *testing.T, p *pgxpool.Pool, s *seeded) {
			exec(t, p, "INSERT INTO page_link_relations (page_id, link_id) VALUES ($1, $2)", s.pa2, s.lb)
		}},
		{"a collection's parent", "collections have a parent owned by a different user", func(t *testing.T, p *pgxpool.Pool, s *seeded) {
			exec(t, p, "UPDATE collections SET parent_id = $1 WHERE id = $2", s.cb, s.caChild)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pool := isolatedPool(t)
			require.NoError(t, pgmigrate.Run(ctx, pool, os.DirFS(upTo(t, "00016"))))
			s := seedVersion16(t, pool)
			tc.corrupt(t, pool, &s)

			err := pgmigrate.Run(ctx, pool, os.DirFS(migrationsDir()))

			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.message, "says what to fix, and where")
			assert.Contains(t, err.Error(), "fix or delete them before upgrading")
			// Nothing was applied: no user_id column anywhere, and no unique constraint.
			assert.Zero(t, count(t, pool,
				"SELECT COUNT(*) FROM information_schema.columns WHERE table_schema = current_schema() AND table_name = 'page_tags' AND column_name = 'user_id'"))
			assert.False(t, hasConstraint(t, pool, "pages_id_user_id_key"))
		})
	}
}

// 00017 changes tables a v1 binary writes to, so rolling back to v1 reverts it: its Down
// has to restore the earlier schema and keep every row.
func TestEnforceSameOwner_DownRestoresTheEarlierSchema(t *testing.T) {
	ctx := context.Background()
	pool := isolatedPool(t)
	require.NoError(t, pgmigrate.Run(ctx, pool, os.DirFS(upTo(t, "00016"))))
	s := seedVersion16(t, pool)
	require.NoError(t, pgmigrate.Run(ctx, pool, os.DirFS(migrationsDir())))
	p := provider(t, pool, migrationsDir())

	_, err := p.DownTo(ctx, 16)
	require.NoError(t, err)

	hasColumn := func(table string) bool {
		return count(t, pool, "SELECT COUNT(*) FROM information_schema.columns WHERE table_schema = current_schema() AND table_name = $1 AND column_name = 'user_id'", table) > 0
	}
	for _, table := range []string{"page_tags", "page_collections", "page_relations", "link_tags", "link_collections", "link_relations", "page_link_relations"} {
		assert.False(t, hasColumn(table), "%s has no user_id again", table)
	}
	for _, name := range []string{
		"page_tags_page_id_fkey", "page_tags_tag_id_fkey", "page_collections_collection_id_fkey",
		"page_relations_page_id_a_fkey", "link_tags_link_id_fkey", "page_link_relations_link_id_fkey",
		"collections_parent_id_fkey",
	} {
		assert.True(t, hasConstraint(t, pool, name), "%s is back", name)
	}
	for _, name := range []string{"pages_id_user_id_key", "tags_id_user_id_key", "collections_id_user_id_key", "links_id_user_id_key", "page_tags_page_id_user_id_fkey", "collections_parent_id_user_id_fkey"} {
		assert.False(t, hasConstraint(t, pool, name), "%s is gone", name)
	}
	// What v1 does: insert a relation without a user_id, even a cross-user one, as it
	// always could.
	exec(t, pool, "INSERT INTO page_tags (page_id, tag_id) VALUES ($1, $2)", s.pa2, s.ta)
	// Every row survived the round trip.
	assert.Equal(t, 3, count(t, pool, "SELECT COUNT(*) FROM page_tags"))
	assert.Equal(t, 2, count(t, pool, "SELECT COUNT(*) FROM page_collections"))

	// And it applies again.
	_, err = p.Up(ctx)
	require.NoError(t, err)
	assert.True(t, hasColumn("page_tags"))
	assert.Equal(t, 3, count(t, pool, "SELECT COUNT(*) FROM page_tags"))
}
