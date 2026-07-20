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

// Package dbtest is the Postgres integration-test harness: connects to the
// dedicated test container started via compose.yaml (see `just compose test`),
// applies migrations (via internal/pgmigrate), and provides
// t.Cleanup-registering fixture factories for common rows.
//
// Reads migrations straight off disk (os.DirFS), not via go:embed (unlike
// the production binary, tests always run with the full repo checked out,
// so there's no need for the binary-self-containment property go:embed
// exists for).
package dbtest

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/mfinelli/recueil/internal/db"
	"github.com/mfinelli/recueil/internal/pgmigrate"
)

const testDatabaseURL = "postgres://recueil:recueil@localhost:5432/recueil"

// migrationsDir locates the repo's migrations/ directory relative to this
// source file's own location, not relative to the calling test's working
// directory. go test sets the process cwd to whichever package's tests are
// running so a caller-relative path like "../../migrations" would happen to
// work today (every current caller lives under internal/<pkg>/, the same
// depth), but that's an implicit, easy-to-silently-break assumption about
// every future caller's location. Anchoring to runtime.Caller(0) instead
// ties the path to dbtest.go's own fixed position in the repo, which
// doesn't change no matter what calls it.
func migrationsDir() string {
	_, thisFile, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(thisFile), "..", "..", "migrations")
}

// Setup opens a pool to the test Postgres container and applies all
// pending migrations against it (via internal/pgmigrate), reusing the same
// pool for the actual test afterward rather than opening a separate connection
// just to migrate.
func Setup(t *testing.T) *pgxpool.Pool {
	t.Helper()
	ctx := context.Background()

	pool, err := pgxpool.New(ctx, testDatabaseURL)
	require.NoError(t, err, "malformed database URL")
	t.Cleanup(pool.Close)

	if err := pgmigrate.Run(ctx, pool, os.DirFS(migrationsDir())); err != nil {
		t.Fatalf("test database not available (%v)", err)
	}

	return pool
}

// Reset truncates every table in the public schema (cascading), except
// goose's own bookkeeping table (wiping that would make the next Setup() call
// think no migrations had ever been applied, and it would then try, and fail,
// to re-run CREATE TABLE against tables that already exist).
//
// Tables are discovered dynamically via pg_tables rather than named in a
// hardcoded list, specifically so this doesn't need updating every time a
// migration adds a new table.
func Reset(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	ctx := context.Background()

	rows, err := pool.Query(ctx,
		`SELECT tablename FROM pg_tables WHERE schemaname = 'public' AND tablename != 'schema_migrations'`)
	require.NoError(t, err)

	var tables []string
	for rows.Next() {
		var name string
		require.NoError(t, rows.Scan(&name))
		tables = append(tables, name)
	}
	rows.Close()
	require.NoError(t, rows.Err())

	if len(tables) == 0 {
		return
	}

	quoted := make([]string, len(tables))
	for i, name := range tables {
		quoted[i] = pgx.Identifier{name}.Sanitize()
	}

	_, err = pool.Exec(ctx, fmt.Sprintf("TRUNCATE %s CASCADE", strings.Join(quoted, ", ")))
	require.NoError(t, err)
}

// CreateUser inserts a test user with a randomized, collision-safe
// username and registers a t.Cleanup to delete it. Sessions created
// against this user (see CreateSession) don't need their own cleanup since
// they cascade-delete via ON DELETE CASCADE when this runs.
func CreateUser(t *testing.T, pool *pgxpool.Pool, role string) db.User {
	t.Helper()
	ctx := context.Background()
	q := db.New(pool)

	user, err := q.CreateUser(ctx, db.CreateUserParams{
		Username:        "test-user-" + randomSuffix(t),
		PasswordHash:    "unused-in-these-tests",
		PairingTokenEnc: pgtype.Text{String: "unused-in-these-tests", Valid: true},
		Role:            role,
	})
	require.NoError(t, err)

	t.Cleanup(func() {
		// Raw SQL deliberately, not a sqlc query: there's no DeleteUser
		// feature yet to back one. If/when one exists, switch this to call
		// it instead of maintaining a second deletion path.
		_, _ = pool.Exec(context.Background(), "DELETE FROM users WHERE id = $1", user.ID)
	})

	return user
}

// CreateSession inserts a session row directly from the given params; the
// caller (e.g. internal/auth's own tests) is responsible for generating a
// real token/hash pair via auth.GenerateSessionToken, since this package
// doesn't import auth. No separate cleanup: cascades away with the owning
// user via CreateUser's cleanup.
func CreateSession(t *testing.T, pool *pgxpool.Pool, params db.CreateSessionParams) db.Session {
	t.Helper()
	session, err := db.New(pool).CreateSession(context.Background(), params)
	require.NoError(t, err)
	return session
}

// CreatePage inserts a page for userID via the real UpsertPage query
// (get-or-create), not a bespoke INSERT -- exercising the same path
// ingestion itself uses. No separate cleanup: cascades away with the
// owning user via CreateUser's cleanup.
func CreatePage(t *testing.T, pool *pgxpool.Pool, userID int64, normalizedURL string) db.Page {
	t.Helper()
	page, err := db.New(pool).UpsertPage(context.Background(), db.UpsertPageParams{
		UserID:          userID,
		NormalizedUrl:   normalizedURL,
		Title:           pgtype.Text{String: "Test Page", Valid: true},
		LatestCaptureAt: pgtype.Timestamptz{Time: time.Now(), Valid: true},
	})
	require.NoError(t, err)
	return page
}

// CreateCapture inserts a capture for pageID via the real
// InsertCaptureIdempotent query, then re-fetches the full row (that query
// only returns the columns ingestion itself needs, not reader_text/etc.,
// so a plain GetCaptureByID after insert is simpler than duplicating its
// column list here). No separate cleanup: cascades away with the owning
// page/user.
func CreateCapture(t *testing.T, pool *pgxpool.Pool, pageID int64) db.Capture {
	t.Helper()
	ctx := context.Background()
	q := db.New(pool)

	inserted, err := q.InsertCaptureIdempotent(ctx, db.InsertCaptureIdempotentParams{
		PageID:                    pageID,
		SourceCaptureID:           pgtype.Text{String: "test-source-capture-" + randomSuffix(t), Valid: true},
		Source:                    "extension",
		RawUrl:                    "https://example.com/test-" + randomSuffix(t),
		Title:                     pgtype.Text{String: "Test Capture", Valid: true},
		HtmlPath:                  "test/path.html.zst",
		HtmlCompressedSizeBytes:   100,
		HtmlUncompressedSizeBytes: 500,
		ContentHash:               "test-hash-" + randomSuffix(t),
		CapturedAt:                pgtype.Timestamptz{Time: time.Now(), Valid: true},
		Language:                  "simple",
	})
	require.NoError(t, err)

	capture, err := q.GetCaptureByID(ctx, inserted.ID)
	require.NoError(t, err)
	return capture
}

// SetCaptureReaderText populates a capture's reader_text (and therefore
// its FTS index, reader_text_tsv, which recomputes automatically as a
// generated column) via the real SetCaptureReadability query -- for tests
// exercising full-text search, which needs actual indexed content, not
// just a bare capture row.
func SetCaptureReaderText(t *testing.T, pool *pgxpool.Pool, captureID int64, readerText string) {
	t.Helper()
	err := db.New(pool).SetCaptureReadability(context.Background(), db.SetCaptureReadabilityParams{
		ID:                 captureID,
		ReaderText:         pgtype.Text{String: readerText, Valid: true},
		ReaderTextHash:     pgtype.Text{String: "test-reader-text-hash", Valid: true},
		ReadabilityVersion: pgtype.Text{String: "test", Valid: true},
	})
	require.NoError(t, err)
}

// TODO: we should switch to faker or similar
func randomSuffix(t *testing.T) string {
	t.Helper()
	buf := make([]byte, 8)
	_, err := rand.Read(buf)
	require.NoError(t, err)
	return hex.EncodeToString(buf)
}
