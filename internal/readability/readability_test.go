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

// These tests run against a *real* chromedp sidecar (docker compose's
// "chromedp" service, test profile -- see compose.yaml) and the actual
// vendored Readability.js on disk under node_modules -- same "no mocks
// for browser-touching code" convention internal/screenshot's own tests
// establish, extended here to "no faking the extraction library either."
// `pnpm install` and `just compose test` must both have run for these to
// pass.
package readability_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mfinelli/recueil/internal/archive"
	"github.com/mfinelli/recueil/internal/db"
	"github.com/mfinelli/recueil/internal/dbtest"
	"github.com/mfinelli/recueil/internal/readability"
	"github.com/mfinelli/recueil/internal/sidecar"
)

const testSidecarURL = "http://127.0.0.1:9222"
const testRenderHost = "host.docker.internal"
const testVersion = "0.0.0-test"

// testHTML is deliberately more than a bare "hello world": Readability
// judges a page "not extractable" (parse() returns null) below a
// minimum character threshold, so this needs enough real paragraph text
// to actually pass its own readability heuristics -- a trivial one-line
// page would make every test here fail at the extraction step itself,
// not at anything this package's own logic is responsible for.
const testHTML = `<!doctype html>
<html>
<head><title>Readability Test Article</title></head>
<body>
<article>
<h1>A Readability Test Article</h1>
<p>This paragraph exists purely to give Readability.js enough real
prose to work with, since its own internal heuristics judge a page not
worth extracting at all below some minimum amount of text -- a bare
one-line page would fail that heuristic long before any code in this
package's own Runner ever got a chance to do anything wrong or right.</p>
<p>A second paragraph, for the same reason, padding this out further so
the extraction reliably succeeds across Readability.js versions whose
exact threshold might shift slightly from one release to the next.</p>
</article>
</body>
</html>`

// readabilitySource reads the real, currently-installed Readability.js
// straight off disk -- the same file main.go embeds in a real build,
// just reached via a relative path here since go:embed can't cross
// package-directory boundaries.
func readabilitySource(t *testing.T) string {
	t.Helper()

	path := filepath.Join("..", "..", "node_modules", "@mozilla", "readability", "Readability.js")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Errorf("Readability.js not found at %s -- run `pnpm add @mozilla/readability` first: %v", path, err)
	}
	return string(data)
}

func newRunner(t *testing.T, pool *pgxpool.Pool, store *archive.Store, maxAttempts int) *readability.Runner {
	t.Helper()

	sc, err := sidecar.New(&sidecar.Params{
		SidecarURL: testSidecarURL,
		RenderHost: testRenderHost,
		Logger:     slog.Default(),
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = sc.Close() })

	r, err := readability.New(&readability.Params{
		Pool:        pool,
		Queries:     db.New(pool),
		Store:       store,
		Sidecar:     sc,
		Source:      readabilitySource(t),
		Version:     testVersion,
		Concurrency: 2,
		MaxAttempts: maxAttempts,
		Logger:      slog.Default(),
	})
	require.NoError(t, err)

	return r
}

// newDueReadabilityJob writes testHTML to store, inserts a page/capture
// row referencing it, and explicitly calls CreateReadabilityJob for it --
// mirroring internal/screenshot's own newDueScreenshotJob (and, in turn,
// real ingest.go's writeToPostgres): an application-level step, not a
// database trigger.
func newDueReadabilityJob(t *testing.T, pool *pgxpool.Pool, store *archive.Store) (db.Capture, db.ReadabilityJob) {
	t.Helper()
	ctx := context.Background()
	q := db.New(pool)

	user := dbtest.CreateUser(t, pool, "member")

	sum := sha256.Sum256([]byte(testHTML))
	contentHash := hex.EncodeToString(sum[:])

	relPath, compressedSize, err := store.WriteHTML(contentHash, []byte(testHTML))
	require.NoError(t, err)

	page, err := q.UpsertPage(ctx, db.UpsertPageParams{
		UserID:          user.ID,
		NormalizedUrl:   "https://example.com/" + uuid.NewString(),
		Title:           pgtype.Text{String: "Readability Test Article", Valid: true},
		LatestCaptureAt: pgtype.Timestamptz{Time: time.Now(), Valid: true},
	})
	require.NoError(t, err)

	inserted, err := q.InsertCaptureIdempotent(ctx, db.InsertCaptureIdempotentParams{
		PageID:                    page.ID,
		SourceCaptureID:           pgtype.Text{String: uuid.NewString(), Valid: true},
		Source:                    "extension",
		RawUrl:                    "https://example.com/test",
		Title:                     pgtype.Text{String: "Readability Test Article", Valid: true},
		HtmlPath:                  relPath,
		HtmlCompressedSizeBytes:   int32(compressedSize),
		HtmlUncompressedSizeBytes: int32(len(testHTML)),
		ContentHash:               contentHash,
		CapturedAt:                pgtype.Timestamptz{Time: time.Now(), Valid: true},
		Language:                  "english",
	})
	require.NoError(t, err)
	require.True(t, inserted.Inserted, "expected a genuinely new capture")

	require.NoError(t, q.CreateReadabilityJob(ctx, inserted.ID))

	job, err := q.GetReadabilityJobByCaptureID(ctx, inserted.ID)
	require.NoError(t, err)
	assert.Equal(t, "pending", job.Status)

	capture, err := q.GetCaptureByID(ctx, inserted.ID)
	require.NoError(t, err)

	return capture, job
}

func TestRunner_RunOnce_ExtractsReaderTextForDueJob(t *testing.T) {
	pool := dbtest.Setup(t)
	dbtest.Reset(t, pool)
	q := db.New(pool)
	store := archive.New(t.TempDir())

	capture, _ := newDueReadabilityJob(t, pool, store)

	r := newRunner(t, pool, store, 3)

	require.NoError(t, r.RunOnce(context.Background()))

	job, err := q.GetReadabilityJobByCaptureID(context.Background(), capture.ID)
	require.NoError(t, err)
	assert.Equal(t, "done", job.Status)
	assert.True(t, job.CompletedAt.Valid)
	assert.False(t, job.Error.Valid)

	updated, err := q.GetCaptureByID(context.Background(), capture.ID)
	require.NoError(t, err)

	require.True(t, updated.ReaderText.Valid, "expected reader_text to be set")
	assert.Contains(t, updated.ReaderText.String, "give Readability.js enough real")

	require.True(t, updated.ReaderTextHash.Valid)
	wantHash := sha256.Sum256([]byte(updated.ReaderText.String))
	assert.Equal(t, hex.EncodeToString(wantHash[:]), updated.ReaderTextHash.String)

	require.True(t, updated.ReadabilityVersion.Valid)
	assert.Equal(t, testVersion, updated.ReadabilityVersion.String)
}

func TestRunner_RunOnce_EmptyVersionIsStoredAsNull(t *testing.T) {
	pool := dbtest.Setup(t)
	dbtest.Reset(t, pool)
	q := db.New(pool)
	store := archive.New(t.TempDir())

	capture, _ := newDueReadabilityJob(t, pool, store)

	sc, err := sidecar.New(&sidecar.Params{SidecarURL: testSidecarURL, RenderHost: testRenderHost, Logger: slog.Default()})
	require.NoError(t, err)
	t.Cleanup(func() { _ = sc.Close() })

	// Version deliberately left unset -- this is exactly the "go test,
	// not a `make`-built binary" case the package doc calls out.
	r, err := readability.New(&readability.Params{
		Pool: pool, Queries: db.New(pool), Store: store, Sidecar: sc,
		Source: readabilitySource(t), Concurrency: 2, MaxAttempts: 3, Logger: slog.Default(),
	})
	require.NoError(t, err)

	require.NoError(t, r.RunOnce(context.Background()))

	updated, err := q.GetCaptureByID(context.Background(), capture.ID)
	require.NoError(t, err)
	require.True(t, updated.ReaderText.Valid, "extraction should still succeed without a version string")
	assert.False(t, updated.ReadabilityVersion.Valid, "empty Version should be stored as NULL, not an empty string")
}

func TestRunner_RunOnce_ReclaimsStaleProcessingJob(t *testing.T) {
	pool := dbtest.Setup(t)
	dbtest.Reset(t, pool)
	q := db.New(pool)
	store := archive.New(t.TempDir())

	capture, job := newDueReadabilityJob(t, pool, store)

	_, err := pool.Exec(context.Background(),
		"UPDATE readability_jobs SET status = 'processing', claimed_at = NOW() - INTERVAL '20 minutes' WHERE id = $1",
		job.ID)
	require.NoError(t, err)

	r := newRunner(t, pool, store, 3)

	require.NoError(t, r.RunOnce(context.Background()))

	reclaimed, err := q.GetReadabilityJobByCaptureID(context.Background(), capture.ID)
	require.NoError(t, err)
	assert.Equal(t, "done", reclaimed.Status, "a stale 'processing' job should be reclaimed and completed, not left stuck")
}

func TestRunner_RunOnce_OneFailureDoesNotBlockTheRestOfTheBatch(t *testing.T) {
	pool := dbtest.Setup(t)
	dbtest.Reset(t, pool)
	q := db.New(pool)
	store := archive.New(t.TempDir())

	goodCapture, _ := newDueReadabilityJob(t, pool, store)

	brokenCapture, _ := newDueReadabilityJob(t, pool, store)
	_, err := pool.Exec(context.Background(),
		"UPDATE captures SET html_path = 'does/not/exist.html.zst' WHERE id = $1", brokenCapture.ID)
	require.NoError(t, err)

	r := newRunner(t, pool, store, 3)

	require.NoError(t, r.RunOnce(context.Background()))

	goodJob, err := q.GetReadabilityJobByCaptureID(context.Background(), goodCapture.ID)
	require.NoError(t, err)
	assert.Equal(t, "done", goodJob.Status)

	brokenJob, err := q.GetReadabilityJobByCaptureID(context.Background(), brokenCapture.ID)
	require.NoError(t, err)
	assert.Equal(t, "pending", brokenJob.Status, "should be scheduled for retry, not blocking the batch")
	assert.Equal(t, int32(1), brokenJob.Attempts)
	assert.True(t, brokenJob.Error.Valid)
}

func TestRunner_RunOnce_FailsPermanentlyAfterMaxAttempts(t *testing.T) {
	pool := dbtest.Setup(t)
	dbtest.Reset(t, pool)
	q := db.New(pool)
	store := archive.New(t.TempDir())

	capture, _ := newDueReadabilityJob(t, pool, store)
	_, err := pool.Exec(context.Background(),
		"UPDATE captures SET html_path = 'does/not/exist.html.zst' WHERE id = $1", capture.ID)
	require.NoError(t, err)

	r := newRunner(t, pool, store, 1)

	require.NoError(t, r.RunOnce(context.Background()))

	job, err := q.GetReadabilityJobByCaptureID(context.Background(), capture.ID)
	require.NoError(t, err)
	assert.Equal(t, "failed", job.Status)
	assert.Equal(t, int32(1), job.Attempts)
	assert.True(t, job.Error.Valid)
	assert.True(t, job.CompletedAt.Valid)
}

func TestRunner_RunOnce_NoDueJobsIsANoOp(t *testing.T) {
	pool := dbtest.Setup(t)
	dbtest.Reset(t, pool)
	store := archive.New(t.TempDir())

	r := newRunner(t, pool, store, 3)

	assert.NoError(t, r.RunOnce(context.Background()))
}
