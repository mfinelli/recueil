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

package ingest_test

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/mfinelli/recueil/internal/archive"
	"github.com/mfinelli/recueil/internal/db"
	"github.com/mfinelli/recueil/internal/dbtest"
	"github.com/mfinelli/recueil/internal/ingest"
	"github.com/mfinelli/recueil/internal/urlnorm"
)

// fakeR2 and fakeWorker are lightweight in-memory fakes for the two
// narrow interfaces Ingester depends on -- see ingest.go's own doc
// comment on r2Client/workerClient for why: internal/r2 and
// WorkerClient each have their own dedicated tests already proving they
// talk to their real backends correctly, so this package's tests focus
// on what it actually owns (the transaction logic, hashing, path
// handling, job enqueueing) against real Postgres and real disk instead.

type fakeR2 struct {
	objects map[string][]byte
}

func newFakeR2() *fakeR2 {
	return &fakeR2{objects: map[string][]byte{}}
}

func (f *fakeR2) Get(_ context.Context, key string) (io.ReadCloser, error) {
	data, ok := f.objects[key]
	if !ok {
		return nil, fmt.Errorf("fake r2: no such key %q", key)
	}
	return io.NopCloser(bytes.NewReader(data)), nil
}

// Delete matches real S3/R2 DeleteObject semantics: deleting an
// already-absent key succeeds rather than erroring. A fake that errored here
// instead would let a test pass for the wrong reason, or fail a
// genuinely-correct retry scenario that depends on this being true.
func (f *fakeR2) Delete(_ context.Context, key string) error {
	delete(f.objects, key)
	return nil
}

type fakeWorker struct {
	pending []ingest.PendingCapture
	fetched []string

	// failMarkFetchedTimes, if positive, makes MarkFetched fail this many
	// times before succeeding -- used to construct a genuine
	// crash-before-cleanup-finished retry scenario (as opposed to
	// artificially re-listing a pc.ID that was already fully,
	// successfully processed, which isn't a scenario that can actually
	// occur once MarkFetched has truly succeeded).
	failMarkFetchedTimes int
}

func (f *fakeWorker) ListPendingCaptures(_ context.Context, limit int) ([]ingest.PendingCapture, error) {
	if limit < len(f.pending) {
		return f.pending[:limit], nil
	}
	return f.pending, nil
}

func (f *fakeWorker) MarkFetched(_ context.Context, captureID string) error {
	if f.failMarkFetchedTimes > 0 {
		f.failMarkFetchedTimes--
		return fmt.Errorf("fake worker: simulated MarkFetched failure")
	}
	f.fetched = append(f.fetched, captureID)
	return nil
}

// newTestPipeline builds the real pipeline (ClearURLs against the actual
// vendored ruleset, plus Canonicalize) -- both already have their own
// thorough test suites, so using the real thing here (rather than a fake)
// costs nothing and makes this test's URL-normalization assertions
// meaningful rather than assumed.
func newTestPipeline(t *testing.T) *urlnorm.Pipeline {
	t.Helper()
	clearURLs, err := urlnorm.NewClearURLs()
	require.NoError(t, err)
	return urlnorm.NewPipeline(clearURLs, urlnorm.Canonicalize{})
}

// strPtr is a small helper for PendingCapture.R2KeyFavicon, a *string
// field (nil meaning "no favicon uploaded") -- Go has no address-of
// operator for a string literal directly.
func strPtr(s string) *string { return &s }

func TestIngester_RunOnce_Success(t *testing.T) {
	ctx := context.Background()
	pool := dbtest.Setup(t)
	dbtest.Reset(t, pool)
	queries := db.New(pool)

	user := dbtest.CreateUser(t, pool, "member")

	const r2Key = "pending/1/capture-1/page.html"
	html := []byte(`<html><head><title>  Example &amp; Page  </title></head><body>hello</body></html>`)

	r2 := newFakeR2()
	r2.objects[r2Key] = html

	worker := &fakeWorker{
		pending: []ingest.PendingCapture{
			{
				ID:         "capture-1",
				UserID:     user.ID,
				URL:        "https://example.com/page?utm_source=newsletter&id=42",
				R2KeyHTML:  r2Key,
				CapturedAt: "2026-07-12T12:00:00.000Z",
				CreatedAt:  "2026-07-12T12:00:05.000Z",
			},
		},
	}

	store := archive.New(t.TempDir())
	ing := ingest.New(ingest.Params{
		Pool:     pool,
		Queries:  queries,
		Worker:   worker,
		R2:       r2,
		Store:    store,
		Pipeline: newTestPipeline(t),
	})

	require.NoError(t, ing.RunOnce(ctx))

	var (
		pageID                    int64
		normalizedURL             string
		pageTitle                 string
		captureTitle              string
		captureRawURL             string
		htmlPath                  string
		htmlCompressedSizeBytes   int32
		htmlUncompressedSizeBytes int32
		contentHash               string
		sourceCaptureID           pgtype.Text
		source                    string
	)
	err := pool.QueryRow(ctx, `
		SELECT p.id, p.normalized_url, p.title, c.title, c.raw_url,
		       c.html_path, c.html_compressed_size_bytes, c.html_uncompressed_size_bytes,
		       c.content_hash, c.source_capture_id, c.source
		FROM captures c JOIN pages p ON p.id = c.page_id
		WHERE c.raw_url = $1`, "https://example.com/page?utm_source=newsletter&id=42").Scan(
		&pageID, &normalizedURL, &pageTitle, &captureTitle, &captureRawURL,
		&htmlPath, &htmlCompressedSizeBytes, &htmlUncompressedSizeBytes,
		&contentHash, &sourceCaptureID, &source,
	)
	require.NoError(t, err)

	// utm_source stripped by ClearURLs; the rest of the pipeline's
	// canonicalization (query sorting, etc.) applied on top.
	assert.Equal(t, "https://example.com/page?id=42", normalizedURL)
	assert.Equal(t, "Example & Page", pageTitle, "title should be HTML-unescaped and trimmed")
	assert.Equal(t, "Example & Page", captureTitle)
	assert.Equal(t, "https://example.com/page?utm_source=newsletter&id=42", captureRawURL,
		"raw_url must be byte-for-byte the original, never normalized")
	assert.NotEmpty(t, htmlPath)
	assert.Positive(t, htmlCompressedSizeBytes)
	assert.Positive(t, htmlUncompressedSizeBytes)
	assert.Equal(t, int32(len(html)), htmlUncompressedSizeBytes,
		"uncompressed size should be exactly the original byte count, not the compressed one")
	assert.Len(t, contentHash, 64, "sha256 hex digest")
	assert.Equal(t, "extension", source)
	assert.False(t, sourceCaptureID.Valid,
		"source_capture_id should be cleared to NULL once ingestion fully completes -- it's transient, not a permanent record")

	// The HTML actually landed on disk, compressed, and is readable back.
	reader, err := store.Open(htmlPath)
	require.NoError(t, err)
	defer func() { _ = reader.Close() }()
	roundTripped, err := io.ReadAll(reader)
	require.NoError(t, err)
	assert.Equal(t, html, roundTripped)

	// Jobs were enqueued for the new capture.
	var screenshotJobs, readabilityJobs int
	require.NoError(t, pool.QueryRow(ctx, `
		SELECT count(*) FROM screenshot_jobs sj JOIN captures c ON c.id = sj.capture_id
		WHERE c.raw_url = $1`, "https://example.com/page?utm_source=newsletter&id=42").Scan(&screenshotJobs))
	require.NoError(t, pool.QueryRow(ctx, `
		SELECT count(*) FROM readability_jobs rj JOIN captures c ON c.id = rj.capture_id
		WHERE c.raw_url = $1`, "https://example.com/page?utm_source=newsletter&id=42").Scan(&readabilityJobs))
	assert.Equal(t, 1, screenshotJobs)
	assert.Equal(t, 1, readabilityJobs)

	// Cleanup calls happened: R2 object gone, Worker told it's fetched.
	_, stillPresent := r2.objects[r2Key]
	assert.False(t, stillPresent, "R2 object should have been deleted after ingestion")
	assert.Equal(t, []string{"capture-1"}, worker.fetched)
}

func TestIngester_RunOnce_Favicon(t *testing.T) {
	ctx := context.Background()
	pool := dbtest.Setup(t)
	dbtest.Reset(t, pool)
	queries := db.New(pool)

	user := dbtest.CreateUser(t, pool, "member")

	const htmlKey = "pending/1/capture-favicon/page.html"
	const faviconKey = "pending/1/capture-favicon/favicon.svg"
	html := []byte(`<html><title>Has A Favicon</title></html>`)
	favicon := []byte(`<svg><!-- fake favicon --></svg>`)

	r2 := newFakeR2()
	r2.objects[htmlKey] = html
	r2.objects[faviconKey] = favicon

	worker := &fakeWorker{
		pending: []ingest.PendingCapture{
			{
				ID:           "capture-favicon",
				UserID:       user.ID,
				URL:          "https://example.com/has-favicon",
				R2KeyHTML:    htmlKey,
				R2KeyFavicon: strPtr(faviconKey),
				CapturedAt:   "2026-07-12T12:00:00.000Z",
				CreatedAt:    "2026-07-12T12:00:05.000Z",
			},
		},
	}

	store := archive.New(t.TempDir())
	ing := ingest.New(ingest.Params{
		Pool:     pool,
		Queries:  queries,
		Worker:   worker,
		R2:       r2,
		Store:    store,
		Pipeline: newTestPipeline(t),
	})

	require.NoError(t, ing.RunOnce(ctx))

	var (
		htmlPath           string
		captureFaviconPath pgtype.Text
		pageFaviconPath    pgtype.Text
	)
	err := pool.QueryRow(ctx, `
		SELECT c.html_path, c.favicon_path, p.favicon_path
		FROM captures c JOIN pages p ON p.id = c.page_id
		WHERE c.raw_url = $1`, "https://example.com/has-favicon").Scan(
		&htmlPath, &captureFaviconPath, &pageFaviconPath)
	require.NoError(t, err)

	require.True(t, captureFaviconPath.Valid)
	assert.Equal(t, captureFaviconPath.String, pageFaviconPath.String,
		"pages.favicon_path should be denormalized from this (the latest) capture, same as title")

	// Lives alongside the HTML in the same capture directory (see
	// internal/archive's CaptureDir), not scattered into its own.
	assert.Equal(t, filepath.Dir(htmlPath), filepath.Dir(captureFaviconPath.String))

	// svg is the one format that gets zstd'd (see internal/archive's
	// WriteAsset doc) -- and it's readable back byte-for-byte.
	assert.True(t, strings.HasSuffix(captureFaviconPath.String, ".svg.zst"))
	reader, err := store.Open(captureFaviconPath.String)
	require.NoError(t, err)
	defer func() { _ = reader.Close() }()
	roundTripped, err := io.ReadAll(reader)
	require.NoError(t, err)
	assert.Equal(t, favicon, roundTripped)

	// Cleaned up from R2 alongside the HTML object, same crash-recovery
	// ordering (disk write and Postgres commit both already durable by
	// the time either gets deleted).
	_, stillPresent := r2.objects[faviconKey]
	assert.False(t, stillPresent, "favicon R2 object should have been deleted after ingestion")
}

func TestIngester_RunOnce_MissingFaviconObjectDoesNotFailTheCapture(t *testing.T) {
	// A favicon is best-effort: R2KeyFavicon being set but the object
	// itself being unreachable (upload failed, R2 hiccup, whatever)
	// must never take down an otherwise-good HTML capture with it.
	ctx := context.Background()
	pool := dbtest.Setup(t)
	dbtest.Reset(t, pool)
	queries := db.New(pool)

	user := dbtest.CreateUser(t, pool, "member")

	const htmlKey = "pending/1/capture-badfavicon/page.html"
	html := []byte(`<html><title>Favicon Missing</title></html>`)

	r2 := newFakeR2()
	r2.objects[htmlKey] = html
	// Deliberately no object at the favicon key.

	worker := &fakeWorker{
		pending: []ingest.PendingCapture{
			{
				ID:           "capture-badfavicon",
				UserID:       user.ID,
				URL:          "https://example.com/favicon-missing",
				R2KeyHTML:    htmlKey,
				R2KeyFavicon: strPtr("pending/1/capture-badfavicon/favicon.png"),
				CapturedAt:   "2026-07-12T12:00:00.000Z",
				CreatedAt:    "2026-07-12T12:00:05.000Z",
			},
		},
	}

	store := archive.New(t.TempDir())
	ing := ingest.New(ingest.Params{
		Pool:     pool,
		Queries:  queries,
		Worker:   worker,
		R2:       r2,
		Store:    store,
		Pipeline: newTestPipeline(t),
	})

	require.NoError(t, ing.RunOnce(ctx), "a missing favicon object must not fail the whole capture")

	var faviconPath pgtype.Text
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT favicon_path FROM captures WHERE raw_url = $1`,
		"https://example.com/favicon-missing").Scan(&faviconPath))
	assert.False(t, faviconPath.Valid, "favicon_path should stay NULL, not error the capture out")

	// The capture itself still fully succeeded and was cleaned up normally.
	assert.Equal(t, []string{"capture-badfavicon"}, worker.fetched)
}

func TestIngester_RunOnce_IdempotentRetry(t *testing.T) {
	ctx := context.Background()
	pool := dbtest.Setup(t)
	dbtest.Reset(t, pool)
	queries := db.New(pool)

	user := dbtest.CreateUser(t, pool, "member")
	const r2Key = "pending/1/capture-retry/page.html"
	html := []byte(`<html><title>Retry Test</title></html>`)

	r2 := newFakeR2()
	r2.objects[r2Key] = html
	worker := &fakeWorker{
		pending: []ingest.PendingCapture{
			{
				ID: "capture-retry", UserID: user.ID,
				URL: "https://example.com/retry", R2KeyHTML: r2Key,
				CapturedAt: "2026-07-12T12:00:00Z", CreatedAt: "2026-07-12T12:00:00Z",
			},
		},
		// The first MarkFetched call fails -- simulating a crash right
		// at that point, genuinely *before* cleanup finished. This is
		// the actual scenario source_capture_id exists to protect
		// against; re-listing a pc.ID that already fully succeeded
		// (including a successful MarkFetched) isn't a real scenario,
		// since D1 would never return it again once that's happened.
		failMarkFetchedTimes: 1,
	}
	store := archive.New(t.TempDir())
	ing := ingest.New(ingest.Params{
		Pool: pool, Queries: queries, Worker: worker, R2: r2, Store: store,
		Pipeline: newTestPipeline(t),
	})

	// First attempt: the Postgres write and R2 delete both succeed, but
	// MarkFetched fails -- RunOnce itself doesn't propagate this (a
	// single item's failure is logged, not fatal to the batch), but the
	// item was NOT fully completed.
	require.NoError(t, ing.RunOnce(ctx))
	require.Empty(t, worker.fetched, "MarkFetched failed on this attempt, so nothing should be recorded as fetched yet")

	// Second attempt (the same pc.ID is still pending, since D1 was
	// never told it's fetched): the R2 object is genuinely already gone
	// (the first attempt's delete succeeded) -- captureAndCommit's R2 Get
	// will fail, and processOne's fallback must recognize the
	// already-committed row and finish cleanup rather than erroring.
	require.NoError(t, ing.RunOnce(ctx))

	var captureCount int
	require.NoError(t, pool.QueryRow(ctx,
		"SELECT count(*) FROM captures WHERE raw_url = $1", "https://example.com/retry",
	).Scan(&captureCount))
	assert.Equal(t, 1, captureCount, "a retry must not create a duplicate capture row")

	var jobCount int
	require.NoError(t, pool.QueryRow(ctx, `
		SELECT count(*) FROM screenshot_jobs sj JOIN captures c ON c.id = sj.capture_id
		WHERE c.raw_url = $1`, "https://example.com/retry").Scan(&jobCount))
	assert.Equal(t, 1, jobCount, "a retry must not create duplicate job rows")

	// Cleanup completes on the second attempt.
	assert.Equal(t, []string{"capture-retry"}, worker.fetched)

	// And source_capture_id was cleared once cleanup genuinely finished.
	var sourceCaptureID pgtype.Text
	require.NoError(t, pool.QueryRow(ctx,
		"SELECT source_capture_id FROM captures WHERE raw_url = $1", "https://example.com/retry",
	).Scan(&sourceCaptureID))
	assert.False(t, sourceCaptureID.Valid, "source_capture_id should be cleared to NULL once ingestion is fully done")
}

func TestIngester_RunOnce_OneFailureDoesNotBlockTheRestOfTheBatch(t *testing.T) {
	ctx := context.Background()
	pool := dbtest.Setup(t)
	dbtest.Reset(t, pool)
	queries := db.New(pool)

	user := dbtest.CreateUser(t, pool, "member")

	r2 := newFakeR2()
	// Deliberately no R2 object for "capture-broken" -- its Get will fail.
	r2.objects["pending/1/capture-ok/page.html"] = []byte(`<html><title>OK</title></html>`)

	worker := &fakeWorker{
		pending: []ingest.PendingCapture{
			{
				ID: "capture-broken", UserID: user.ID,
				URL: "https://example.com/broken", R2KeyHTML: "pending/1/capture-broken/page.html",
				CapturedAt: "2026-07-12T12:00:00Z", CreatedAt: "2026-07-12T12:00:00Z",
			},
			{
				ID: "capture-ok", UserID: user.ID,
				URL: "https://example.com/ok", R2KeyHTML: "pending/1/capture-ok/page.html",
				CapturedAt: "2026-07-12T12:00:00Z", CreatedAt: "2026-07-12T12:00:00Z",
			},
		},
	}
	store := archive.New(t.TempDir())
	ing := ingest.New(ingest.Params{
		Pool: pool, Queries: queries, Worker: worker, R2: r2, Store: store,
		Pipeline: newTestPipeline(t),
	})

	// RunOnce itself must not return an error just because one item in
	// the batch failed.
	require.NoError(t, ing.RunOnce(ctx))

	var okCount, brokenCount int
	require.NoError(t, pool.QueryRow(ctx,
		"SELECT count(*) FROM captures WHERE raw_url = $1", "https://example.com/ok",
	).Scan(&okCount))
	require.NoError(t, pool.QueryRow(ctx,
		"SELECT count(*) FROM captures WHERE raw_url = $1", "https://example.com/broken",
	).Scan(&brokenCount))

	assert.Equal(t, 1, okCount, "the good capture in the batch should still be ingested")
	assert.Equal(t, 0, brokenCount, "the broken capture should not have a partial/corrupt row")
	assert.Equal(t, []string{"capture-ok"}, worker.fetched, "only the successful capture should be marked fetched")
}

func TestIngester_RunOnce_LanguageDetection(t *testing.T) {
	ctx := context.Background()
	pool := dbtest.Setup(t)
	dbtest.Reset(t, pool)
	queries := db.New(pool)

	user := dbtest.CreateUser(t, pool, "member")

	tests := []struct {
		name         string
		html         string
		wantLanguage string
	}{
		{
			name:         "a recognized, available language tag resolves to its postgres config",
			html:         `<html lang="fr"><head><title>Bonjour</title></head></html>`,
			wantLanguage: "french",
		},
		{
			name:         "a region subtag is stripped before mapping",
			html:         `<html lang="en-US"><head><title>Hello</title></head></html>`,
			wantLanguage: "english",
		},
		{
			name:         "no lang attribute at all falls back to simple",
			html:         `<html><head><title>No Language Tag</title></head></html>`,
			wantLanguage: "simple",
		},
		{
			name:         "a language with no postgres stemmer falls back to simple",
			html:         `<html lang="zh"><head><title>No Stemmer</title></head></html>`,
			wantLanguage: "simple",
		},
	}

	for i, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r2Key := fmt.Sprintf("pending/1/lang-test-%d/page.html", i)
			r2 := newFakeR2()
			r2.objects[r2Key] = []byte(tt.html)
			worker := &fakeWorker{
				pending: []ingest.PendingCapture{
					{
						ID:         fmt.Sprintf("lang-test-%d", i),
						UserID:     user.ID,
						URL:        fmt.Sprintf("https://example.com/lang-test-%d", i),
						R2KeyHTML:  r2Key,
						CapturedAt: "2026-07-12T12:00:00Z",
						CreatedAt:  "2026-07-12T12:00:00Z",
					},
				},
			}
			store := archive.New(t.TempDir())
			ing := ingest.New(ingest.Params{
				Pool: pool, Queries: queries, Worker: worker, R2: r2, Store: store,
				Pipeline: newTestPipeline(t),
			})

			require.NoError(t, ing.RunOnce(ctx))

			var language string
			require.NoError(t, pool.QueryRow(ctx,
				"SELECT language FROM captures WHERE raw_url = $1",
				fmt.Sprintf("https://example.com/lang-test-%d", i),
			).Scan(&language))
			assert.Equal(t, tt.wantLanguage, language)
		})
	}
}

func TestIngester_RunOnce_SourceCaptureIDCollision(t *testing.T) {
	ctx := context.Background()
	pool := dbtest.Setup(t)
	dbtest.Reset(t, pool)
	queries := db.New(pool)

	user := dbtest.CreateUser(t, pool, "member")
	store := archive.New(t.TempDir())

	// A UUID that's about to be shared by two genuinely different
	// captures -- simulating the scenario in question: a real (if
	// astronomically unlikely) collision, not a retry.
	const collidingID = "shared-id-0000-0000-0000-000000000000"

	// An already-ingested, completely unrelated capture already occupies
	// this exact source_capture_id -- with a real file on disk, not just
	// a database row, so this test actually exercises the disk-layer
	// half of the fix (archive.Store is keyed by content_hash, not
	// capture_id, specifically so this pre-existing file can't be
	// clobbered by the colliding capture below -- see archive.go's
	// package doc).
	existingContent := []byte(`<html><title>Earlier Capture</title></html>`)
	// Deliberately not what the new, colliding capture's content will
	// hash to -- this is what makes it a genuine collision, not a retry.
	existingContentHash := "0000000000000000000000000000000000000000000000000000000000aa"
	existingHTMLPath, _, err := store.WriteHTML(existingContentHash, existingContent)
	require.NoError(t, err)

	existingPage, err := queries.UpsertPage(ctx, db.UpsertPageParams{
		UserID:          user.ID,
		NormalizedUrl:   "https://unrelated-earlier-capture.example/page",
		Title:           pgtype.Text{String: "Earlier Capture", Valid: true},
		LatestCaptureAt: pgtype.Timestamptz{Time: time.Now(), Valid: true},
	})
	require.NoError(t, err)

	existingCapture, err := queries.InsertCaptureIdempotent(ctx, db.InsertCaptureIdempotentParams{
		PageID:                    existingPage.ID,
		SourceCaptureID:           pgtype.Text{String: collidingID, Valid: true},
		Source:                    "extension",
		RawUrl:                    "https://unrelated-earlier-capture.example/page",
		Title:                     pgtype.Text{String: "Earlier Capture", Valid: true},
		HtmlPath:                  existingHTMLPath,
		HtmlCompressedSizeBytes:   123,
		HtmlUncompressedSizeBytes: 456,
		ContentHash:               existingContentHash,
		CapturedAt:                pgtype.Timestamptz{Time: time.Now(), Valid: true},
		Language:                  "simple",
	})
	require.NoError(t, err)
	require.True(t, existingCapture.Inserted)

	// A genuinely different capture now arrives, coincidentally reusing
	// that exact same source_capture_id.
	const r2Key = "pending/1/collision/page.html"
	html := []byte(`<html><title>A Totally Different Page</title></html>`)
	r2 := newFakeR2()
	r2.objects[r2Key] = html
	worker := &fakeWorker{
		pending: []ingest.PendingCapture{
			{
				ID:         collidingID,
				UserID:     user.ID,
				URL:        "https://genuinely-different.example/other-page",
				R2KeyHTML:  r2Key,
				CapturedAt: "2026-07-12T12:00:00Z",
				CreatedAt:  "2026-07-12T12:00:00Z",
			},
		},
	}
	ing := ingest.New(ingest.Params{
		Pool: pool, Queries: queries, Worker: worker, R2: r2, Store: store,
		Pipeline: newTestPipeline(t),
	})

	require.NoError(t, ing.RunOnce(ctx))

	// The original, unrelated capture's DB row must be completely
	// untouched -- not overwritten, not merged, not corrupted.
	var stillOriginalHash string
	require.NoError(t, pool.QueryRow(ctx,
		"SELECT content_hash FROM captures WHERE id = $1", existingCapture.ID,
	).Scan(&stillOriginalHash))
	assert.Equal(t, existingContentHash, stillOriginalHash)

	// And its file on disk must be completely untouched too -- this is
	// the actual disk-layer bug this test exists to catch: a
	// capture_id-keyed disk store would have let the colliding capture's
	// write silently clobber this exact file via os.Rename.
	existingReader, err := store.Open(existingHTMLPath)
	require.NoError(t, err)
	defer func() { _ = existingReader.Close() }()
	stillOriginalContent, err := io.ReadAll(existingReader)
	require.NoError(t, err)
	assert.Equal(t, existingContent, stillOriginalContent,
		"the pre-existing capture's file on disk must not have been overwritten by the colliding capture")

	// The new, genuinely different capture must have actually been
	// inserted -- with its own real data intact, not silently discarded
	// in favor of the pre-existing row. Note: we can't observe the
	// *regenerated* source_capture_id here to confirm collision handling
	// specifically fired -- it's cleared to NULL once ingestion
	// completes, same as any successful capture (see the Success test).
	// What's still directly verifiable, and is really the meaningful
	// outcome, is that both captures ended up as distinct, independently
	// correct rows rather than the second one being silently discarded.
	var newCaptureID int64
	var newSourceCaptureID pgtype.Text
	var newHTMLPath string
	err = pool.QueryRow(ctx, `
		SELECT id, source_capture_id, html_path FROM captures
		WHERE raw_url = $1`, "https://genuinely-different.example/other-page",
	).Scan(&newCaptureID, &newSourceCaptureID, &newHTMLPath)
	require.NoError(t, err, "the colliding capture's real data must not have been silently discarded")
	assert.False(t, newSourceCaptureID.Valid,
		"source_capture_id should be cleared to NULL once ingestion fully completes, same as any successful capture")
	assert.NotEqual(t, existingCapture.ID, newCaptureID)
	assert.NotEqual(t, existingHTMLPath, newHTMLPath,
		"the two captures' content differs, so they must land at different content-hash-derived disk paths")

	// The new capture's own file is independently readable with its own
	// correct content.
	newReader, err := store.Open(newHTMLPath)
	require.NoError(t, err)
	defer func() { _ = newReader.Close() }()
	newContent, err := io.ReadAll(newReader)
	require.NoError(t, err)
	assert.Equal(t, html, newContent)

	// It got its own screenshot/readability jobs too, same as any other
	// genuinely new capture.
	var screenshotJobs, readabilityJobs int
	require.NoError(t, pool.QueryRow(ctx,
		"SELECT count(*) FROM screenshot_jobs WHERE capture_id = $1", newCaptureID,
	).Scan(&screenshotJobs))
	require.NoError(t, pool.QueryRow(ctx,
		"SELECT count(*) FROM readability_jobs WHERE capture_id = $1", newCaptureID,
	).Scan(&readabilityJobs))
	assert.Equal(t, 1, screenshotJobs)
	assert.Equal(t, 1, readabilityJobs)

	// Cleanup still proceeds normally despite the mid-flight ID swap.
	assert.Equal(t, []string{collidingID}, worker.fetched,
		"MarkFetched is still called against the original D1 pending_captures id -- the regenerated id only exists in Postgres")
}
