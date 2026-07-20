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
// "chromedp" service, test profile -- see compose.yaml) at
// testSidecarURL, the same "no mocks for DB-touching (or, here,
// browser-touching) code" convention internal/dbtest already establishes
// for Postgres: a fake CDP implementation would just be re-testing this
// package's own assumptions about chromedp's behavior, not chromedp
// itself. `just compose test` must be running for these to pass.
package screenshot_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"log/slog"
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
	"github.com/mfinelli/recueil/internal/screenshot"
)

// testSidecarURL matches compose.yaml's published chromedp port, the
// same hardcoded-localhost convention dbtest.testDatabaseURL already
// uses for Postgres.
const testSidecarURL = "http://127.0.0.1:9222"

// testRenderHost is NOT "127.0.0.1": these tests run as a plain `go test`
// process directly on the host (per compose.yaml's own documented local-dev
// shape), while chromedp runs in its own container -- so the sidecar reaching
// back into this process's ephemeral render server needs the same
// host.docker.internal path config's screenshot_render_host doc describes for
// exactly this split, not the "agent and sidecar share a network namespace"
// case "127.0.0.1" would actually be correct for. Backed by compose.yaml's
// chromedp service's own extra_hosts entry on Linux; Docker Desktop
// (macOS/Windows) resolves it without needing that at all.
const testRenderHost = "host.docker.internal"

const testHTML = `<!doctype html>
<html>
<head><title>Screenshot Test Page</title></head>
<body><h1>Hello, screenshot</h1></body>
</html>`

func newRunner(t *testing.T, pool *pgxpool.Pool, store *archive.Store, sidecarURL string, maxAttempts int) *screenshot.Runner {
	t.Helper()

	r, err := screenshot.New(&screenshot.Params{
		Pool:        pool,
		Queries:     db.New(pool),
		Store:       store,
		SidecarURL:  sidecarURL,
		RenderHost:  testRenderHost,
		Concurrency: 2,
		MaxAttempts: maxAttempts,
		Logger:      slog.Default(),
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = r.Close() })

	return r
}

// newDueScreenshotJob writes testHTML to store, inserts a page/capture row
// referencing it, and explicitly calls CreateScreenshotJob for it --
// mirroring exactly what internal/ingest's writeToPostgres already does at
// the end of a real capture (an application-level step, not a database
// trigger) -- rather than hand-rolling a job row directly.
func newDueScreenshotJob(t *testing.T, pool *pgxpool.Pool, store *archive.Store) (db.Capture, db.ScreenshotJob) {
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
		Title:           pgtype.Text{String: "Screenshot Test Page", Valid: true},
		LatestCaptureAt: pgtype.Timestamptz{Time: time.Now(), Valid: true},
	})
	require.NoError(t, err)

	inserted, err := q.InsertCaptureIdempotent(ctx, db.InsertCaptureIdempotentParams{
		PageID:                    page.ID,
		SourceCaptureID:           pgtype.Text{String: uuid.NewString(), Valid: true},
		Source:                    "extension",
		RawUrl:                    "https://example.com/test",
		Title:                     pgtype.Text{String: "Screenshot Test Page", Valid: true},
		HtmlPath:                  relPath,
		HtmlCompressedSizeBytes:   int32(compressedSize),
		HtmlUncompressedSizeBytes: int32(len(testHTML)),
		ContentHash:               contentHash,
		CapturedAt:                pgtype.Timestamptz{Time: time.Now(), Valid: true},
		Language:                  "english",
	})
	require.NoError(t, err)
	require.True(t, inserted.Inserted, "expected a genuinely new capture")

	// Unlike a DB trigger, this is an explicit application-level step --
	// real ingest.go calls CreateScreenshotJob itself, in the same
	// transaction as the capture insert (see its writeToPostgres), rather
	// than any database-level automation doing it. Mirror that here
	// rather than assuming it happens implicitly.
	require.NoError(t, q.CreateScreenshotJob(ctx, inserted.ID))

	job, err := q.GetScreenshotJobByCaptureID(ctx, inserted.ID)
	require.NoError(t, err)
	assert.Equal(t, "pending", job.Status)

	capture, err := q.GetCaptureByID(ctx, inserted.ID)
	require.NoError(t, err)

	return capture, job
}

func TestNew_FailsIfSidecarUnreachable(t *testing.T) {
	pool := dbtest.Setup(t)
	dbtest.Reset(t, pool)
	store := archive.New(t.TempDir())

	// Port 1 is a privileged port nothing is listening on in any CI or
	// dev environment -- connection refused comes back essentially
	// immediately, so this doesn't need to wait out sidecarPingTimeout.
	_, err := screenshot.New(&screenshot.Params{
		Pool:        pool,
		Queries:     db.New(pool),
		Store:       store,
		SidecarURL:  "http://127.0.0.1:1",
		RenderHost:  testRenderHost,
		Concurrency: 2,
		MaxAttempts: 3,
		Logger:      slog.Default(),
	})
	require.Error(t, err, "New should fail loudly at startup rather than silently retrying every job forever")
}

func TestRunner_RunOnce_ReclaimsStaleProcessingJob(t *testing.T) {
	pool := dbtest.Setup(t)
	dbtest.Reset(t, pool)
	q := db.New(pool)
	store := archive.New(t.TempDir())

	capture, job := newDueScreenshotJob(t, pool, store)

	// Simulate a prior claimant that crashed mid-render: 'processing',
	// claimed well past claimStaleTimeout (15 minutes), never reaching a
	// terminal status.
	_, err := pool.Exec(context.Background(),
		"UPDATE screenshot_jobs SET status = 'processing', claimed_at = NOW() - INTERVAL '20 minutes' WHERE id = $1",
		job.ID)
	require.NoError(t, err)

	r := newRunner(t, pool, store, testSidecarURL, 3)

	require.NoError(t, r.RunOnce(context.Background()))

	reclaimed, err := q.GetScreenshotJobByCaptureID(context.Background(), capture.ID)
	require.NoError(t, err)
	assert.Equal(t, "done", reclaimed.Status, "a stale 'processing' job should be reclaimed and completed, not left stuck")
}

func TestRunner_RunOnce_RendersScreenshotForDueJob(t *testing.T) {
	pool := dbtest.Setup(t)
	dbtest.Reset(t, pool)
	q := db.New(pool)
	store := archive.New(t.TempDir())

	capture, _ := newDueScreenshotJob(t, pool, store)

	r := newRunner(t, pool, store, testSidecarURL, 3)

	require.NoError(t, r.RunOnce(context.Background()))

	job, err := q.GetScreenshotJobByCaptureID(context.Background(), capture.ID)
	require.NoError(t, err)
	assert.Equal(t, "done", job.Status)
	assert.True(t, job.CompletedAt.Valid)
	assert.False(t, job.Error.Valid)

	updated, err := q.GetCaptureByID(context.Background(), capture.ID)
	require.NoError(t, err)
	require.True(t, updated.ThumbnailPath.Valid, "expected thumbnail_path to be set")

	rc, err := store.Open(updated.ThumbnailPath.String)
	require.NoError(t, err)
	defer func() { _ = rc.Close() }()

	shot, err := io.ReadAll(rc)
	require.NoError(t, err)
	// PNG magic bytes -- confirms chromedp.CaptureScreenshot actually
	// wrote a real fixed-viewport PNG, not just some non-empty bytes.
	assert.True(t, bytes.HasPrefix(shot, []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}))

	require.True(t, updated.ThumbnailSizeBytes.Valid)
	assert.Equal(t, int32(len(shot)), updated.ThumbnailSizeBytes.Int32)

	// thumbnail_hash is recorded as its own column (migration 00009), not
	// only ever implicit in the filename -- confirm it's both a real
	// sha256 of the actual stored bytes and consistent with the filename
	// archive.WriteAsset actually chose.
	require.True(t, updated.ThumbnailHash.Valid)
	wantHash := sha256.Sum256(shot)
	assert.Equal(t, hex.EncodeToString(wantHash[:]), updated.ThumbnailHash.String)
	assert.Contains(t, updated.ThumbnailPath.String, updated.ThumbnailHash.String)
}

func TestRunner_RunOnce_OneFailureDoesNotBlockTheRestOfTheBatch(t *testing.T) {
	pool := dbtest.Setup(t)
	dbtest.Reset(t, pool)
	q := db.New(pool)
	store := archive.New(t.TempDir())

	goodCapture, _ := newDueScreenshotJob(t, pool, store)

	// A due job whose captures.html_path points nowhere -- opening it
	// fails immediately, before ever touching the sidecar, giving a
	// cheap, deterministic per-job failure alongside the real one.
	brokenCapture, _ := newDueScreenshotJob(t, pool, store)
	_, err := pool.Exec(context.Background(),
		"UPDATE captures SET html_path = 'does/not/exist.html.zst' WHERE id = $1", brokenCapture.ID)
	require.NoError(t, err)

	r := newRunner(t, pool, store, testSidecarURL, 3)

	require.NoError(t, r.RunOnce(context.Background()))

	goodJob, err := q.GetScreenshotJobByCaptureID(context.Background(), goodCapture.ID)
	require.NoError(t, err)
	assert.Equal(t, "done", goodJob.Status)

	brokenJob, err := q.GetScreenshotJobByCaptureID(context.Background(), brokenCapture.ID)
	require.NoError(t, err)
	assert.Equal(t, "pending", brokenJob.Status, "should be scheduled for retry, not blocking the batch")
	assert.Equal(t, int32(1), brokenJob.Attempts)
	assert.True(t, brokenJob.Error.Valid)
	assert.True(t, brokenJob.NextAttemptAt.Valid)
	assert.True(t, brokenJob.NextAttemptAt.Time.After(time.Now()))
}

func TestRunner_RunOnce_FailsPermanentlyAfterMaxAttempts(t *testing.T) {
	pool := dbtest.Setup(t)
	dbtest.Reset(t, pool)
	q := db.New(pool)
	store := archive.New(t.TempDir())

	capture, _ := newDueScreenshotJob(t, pool, store)
	_, err := pool.Exec(context.Background(),
		"UPDATE captures SET html_path = 'does/not/exist.html.zst' WHERE id = $1", capture.ID)
	require.NoError(t, err)

	// maxAttempts=1: the very first failure already exhausts the
	// budget, so this test doesn't need to fast-forward
	// next_attempt_at or call RunOnce more than once to reach 'failed'.
	r := newRunner(t, pool, store, testSidecarURL, 1)

	require.NoError(t, r.RunOnce(context.Background()))

	job, err := q.GetScreenshotJobByCaptureID(context.Background(), capture.ID)
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

	r := newRunner(t, pool, store, testSidecarURL, 3)

	assert.NoError(t, r.RunOnce(context.Background()))
}
