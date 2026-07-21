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

// Package ingest is the backend's ingestion pipeline: discover captures the
// extension has finished uploading, pull them from R2, store them locally,
// and record them in Postgres. Deliberately built as a callable unit
// (RunOnce) with no scheduler of its own.
package ingest

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"html"
	"io"
	"log/slog"
	"math"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/mfinelli/recueil/internal/archive"
	"github.com/mfinelli/recueil/internal/db"
	"github.com/mfinelli/recueil/internal/urlnorm"
)

// defaultBatchLimit bounds how many captures a single RunOnce call
// processes -- matches the Worker's own GET /internal/pending-captures
// default limit (terraform/worker/index.js), so a single poll cycle's work is
// naturally bounded without RunOnce needing its own separate cap.
const defaultBatchLimit = 50

// r2Client and workerClient are narrow interfaces over *r2.Client and
// *WorkerClient -- just the methods Ingester actually calls. Depending on
// interfaces here (rather than the concrete types directly) means this
// package's own tests can substitute lightweight in-memory fakes for R2
// and the Worker, while still exercising real Postgres (via dbtest), real
// local disk (via archive.Store against a t.TempDir()), and real URL
// normalization -- trusting internal/r2's and this package's own
// WorkerClient tests to separately validate that those two dependencies
// are correct, rather than re-proving it here via a hand-rolled fake S3
// server.
type r2Client interface {
	Get(ctx context.Context, key string) (io.ReadCloser, error)
	Delete(ctx context.Context, key string) error
}

type workerClient interface {
	ListPendingCaptures(ctx context.Context, limit int) ([]PendingCapture, error)
	MarkFetched(ctx context.Context, captureID string) error
}

// Params are Ingester's dependencies, all required. Grouped into a struct
// (rather than a long positional-argument New(...)) since there are enough
// of them that argument order would otherwise be a real mistake risk.
type Params struct {
	Pool     *pgxpool.Pool
	Queries  *db.Queries
	Worker   workerClient
	R2       r2Client
	Store    *archive.Store
	Pipeline *urlnorm.Pipeline
	// Logger defaults to slog.Default() if nil.
	Logger *slog.Logger
}

type Ingester struct {
	pool     *pgxpool.Pool
	queries  *db.Queries
	worker   workerClient
	r2       r2Client
	store    *archive.Store
	pipeline *urlnorm.Pipeline
	logger   *slog.Logger
}

func New(p Params) *Ingester {
	logger := p.Logger
	if logger == nil {
		logger = slog.Default()
	}
	return &Ingester{
		pool:     p.Pool,
		queries:  p.Queries,
		worker:   p.Worker,
		r2:       p.R2,
		store:    p.Store,
		pipeline: p.Pipeline,
		logger:   logger,
	}
}

// RunOnce processes one bounded batch of pending captures. A failure
// processing a single capture is logged and does not stop the rest of the
// batch: the principle that a capture's core validity is decoupled from any
// one enrichment step failing extends naturally to ingestion itself here --
// one corrupt or unreachable capture blob shouldn't block every other
// legitimate one in the same poll cycle, and a failed item simply gets picked
// up again on the next poll (nothing marks it fetched until it actually
// succeeds). RunOnce only returns an error when it can't even get a batch to
// work on.
func (ing *Ingester) RunOnce(ctx context.Context) error {
	pending, err := ing.worker.ListPendingCaptures(ctx, defaultBatchLimit)
	if err != nil {
		return fmt.Errorf("ingest: listing pending captures: %w", err)
	}

	for _, pc := range pending {
		if err := ing.processOne(ctx, &pc); err != nil {
			ing.logger.ErrorContext(ctx, "ingest: failed to process capture",
				"capture_id", pc.ID, "url", pc.URL, "error", err)
			continue
		}
	}
	return nil
}

// processOne ingests a single pending capture, following the crash-recovery
// ordering: local disk write, then the Postgres transaction commits, then the
// R2 object is deleted, then the D1 flag is set, then (once both cleanup
// calls have actually succeeded) source_capture_id is cleared.
func (ing *Ingester) processOne(ctx context.Context, pc *PendingCapture) error {
	captureRowID, commitErr := ing.captureAndCommit(ctx, pc)
	if commitErr != nil {
		existing, lookupErr := ing.queries.GetCaptureBySourceCaptureID(ctx, pgtype.Text{String: pc.ID, Valid: true})
		switch {
		case lookupErr == nil:
			captureRowID = existing.ID
			ing.logger.InfoContext(ctx,
				"ingest: this attempt failed, but a prior attempt already committed this capture -- finishing cleanup",
				"capture_id", pc.ID, "attempt_error", commitErr)
		case errors.Is(lookupErr, pgx.ErrNoRows):
			// Never actually committed -- commitErr is a real,
			// unresolved failure.
			return commitErr
		default:
			return fmt.Errorf("checking for an already-committed capture after a failed attempt: %w", lookupErr)
		}
	}

	// R2's DeleteObject (and R2's own S3-compatible equivalent) is
	// documented to be idempotent -- deleting an already-gone key
	// returns success, not an error -- so no special "tolerate already
	// deleted" handling is needed here even on a retry where this
	// already succeeded once.
	if err := ing.r2.Delete(ctx, pc.R2KeyHTML); err != nil {
		return fmt.Errorf("deleting R2 object: %w", err)
	}
	if pc.R2KeyFavicon != nil && *pc.R2KeyFavicon != "" {
		// Best-effort, same as capturing it was: a favicon object that
		// fails to delete here just sits in R2's temporary buffer
		// harmlessly (it was never the canonical copy -- captureFavicon
		// already stored that locally, or logged why it couldn't) and
		// isn't worth failing this whole cleanup pass over.
		if err := ing.r2.Delete(ctx, *pc.R2KeyFavicon); err != nil {
			ing.logger.WarnContext(ctx, "ingest: failed to delete favicon R2 object (harmless)",
				"capture_id", pc.ID, "r2_key_favicon", *pc.R2KeyFavicon, "error", err)
		}
	}
	if err := ing.worker.MarkFetched(ctx, pc.ID); err != nil {
		return fmt.Errorf("marking capture fetched: %w", err)
	}

	// source_capture_id has now served its only purpose (letting the
	// fallback check above recognize an already-committed row without
	// redoing the pipeline) -- clear it. Keyed by the row's own primary
	// key, not by pc.ID or whatever value ended up stored: collision
	// handling in captureAndCommit may have regenerated a different
	// value than pc.ID. A failure here is harmless and deliberately not
	// treated as this function's own failure: D1 will never return this
	// pc.ID again either way (MarkFetched just succeeded), so nothing
	// will ever look this value up again regardless of whether the clear
	// actually lands.
	if err := ing.queries.ClearSourceCaptureID(ctx, captureRowID); err != nil {
		ing.logger.WarnContext(ctx, "ingest: failed to clear source_capture_id after successful ingestion (harmless)",
			"capture_id", pc.ID, "error", err)
	}

	return nil
}

// captureAndCommit runs the full pipeline for a genuinely new capture:
// pull from R2, hash, compress to local disk, normalize the URL, and
// commit to Postgres. Returns the new (or, via collision-handling,
// possibly a pre-existing) captures row's id.
func (ing *Ingester) captureAndCommit(ctx context.Context, pc *PendingCapture) (int64, error) {
	capturedAt, err := parseD1Timestamp(pc.CapturedAt)
	if err != nil {
		return 0, fmt.Errorf("parsing captured_at %q: %w", pc.CapturedAt, err)
	}

	body, err := ing.r2.Get(ctx, pc.R2KeyHTML)
	if err != nil {
		return 0, fmt.Errorf("pulling blob from R2: %w", err)
	}
	data, err := io.ReadAll(body)
	_ = body.Close()
	if err != nil {
		return 0, fmt.Errorf("reading blob body: %w", err)
	}

	sum := sha256.Sum256(data)
	contentHash := hex.EncodeToString(sum[:])

	relPath, compressedSize, err := ing.store.WriteHTML(contentHash, data)
	if err != nil {
		return 0, fmt.Errorf("writing to local archive: %w", err)
	}
	if compressedSize > math.MaxInt32 {
		return 0, fmt.Errorf("compressed size %d exceeds captures.html_compressed_size_bytes's int32 range", compressedSize)
	}
	// len(data) is the uncompressed size -- already fully in memory as the
	// exact bytes just written, no separate computation needed. Stored
	// alongside the compressed size so the dashboard can surface real
	// compression-ratio numbers.
	uncompressedSize := len(data)
	if uncompressedSize > math.MaxInt32 {
		return 0, fmt.Errorf("uncompressed size %d exceeds captures.html_uncompressed_size_bytes's int32 range", uncompressedSize)
	}

	title := extractTitle(data)

	language, err := ing.resolveLanguageConfig(ctx, extractLanguage(data))
	if err != nil {
		return 0, fmt.Errorf("resolving language: %w", err)
	}

	normalizedURL, err := ing.pipeline.Normalize(ctx, pc.URL)
	if err != nil {
		return 0, fmt.Errorf("normalizing url %q: %w", pc.URL, err)
	}

	// Favicon is best-effort: a fetch/store failure here is logged and
	// otherwise ignored, never treated as a reason to fail the whole
	// capture -- an unreachable or malformed favicon object is a cosmetic
	// loss, not a reason to lose an otherwise-good HTML capture.
	var faviconPath string
	var faviconSizeBytes int32
	var faviconHash string
	if pc.R2KeyFavicon != nil && *pc.R2KeyFavicon != "" {
		faviconPath, faviconSizeBytes, faviconHash = ing.captureFavicon(ctx, pc, contentHash)
	}

	captureID, inserted, err := ing.writeToPostgres(ctx, pc, &writeInput{
		normalizedURL:         normalizedURL,
		title:                 title,
		htmlPath:              relPath,
		htmlCompressedBytes:   int32(compressedSize),
		htmlUncompressedBytes: int32(uncompressedSize),
		contentHash:           contentHash,
		capturedAt:            capturedAt,
		language:              language,
		faviconPath:           faviconPath,
		faviconSizeBytes:      faviconSizeBytes,
		faviconHash:           faviconHash,
	})
	if err != nil {
		return 0, fmt.Errorf("writing to postgres: %w", err)
	}

	if inserted {
		ing.logger.InfoContext(ctx, "ingest: captured", "capture_id", pc.ID, "url", pc.URL)
	}

	return captureID, nil
}

// captureFavicon pulls the favicon object from R2 (if the extension
// actually uploaded one), stores it alongside the HTML in the same capture
// directory (see internal/archive's package doc for why it's keyed by its
// own content hash, not htmlHash), and returns the resulting relative path,
// its on-disk size, and its own content hash (the same hash already used
// as its filename -- see migration 00009's own doc for why it's also
// stored as a column rather than only ever implicit in the path) -- or
// ("", 0, "") if anything goes wrong, having already logged why. Never
// returns an error: a favicon is cosmetic, and a broken/unreachable one is
// not a reason to fail an otherwise-good capture.
func (ing *Ingester) captureFavicon(ctx context.Context, pc *PendingCapture, htmlHash string) (faviconPath string, writtenSize int32, faviconHash string) {
	key := *pc.R2KeyFavicon

	body, err := ing.r2.Get(ctx, key)
	if err != nil {
		ing.logger.WarnContext(ctx, "ingest: failed to pull favicon from R2, continuing without one",
			"capture_id", pc.ID, "r2_key_favicon", key, "error", err)
		return "", 0, ""
	}
	data, err := io.ReadAll(body)
	_ = body.Close()
	if err != nil {
		ing.logger.WarnContext(ctx, "ingest: failed to read favicon blob, continuing without one",
			"capture_id", pc.ID, "r2_key_favicon", key, "error", err)
		return "", 0, ""
	}

	// The favicon's extension is carried in the R2 key itself
	// (pending/{userId}/{captureId}/favicon.{ext} -- see
	// terraform/worker/index.js's faviconObjectKey), the same way the HTML
	// object's ".html" suffix is implicit rather than a separate field.
	ext := strings.TrimPrefix(filepath.Ext(key), ".")
	if ext == "" {
		ing.logger.WarnContext(ctx, "ingest: favicon r2 key has no extension, continuing without one",
			"capture_id", pc.ID, "r2_key_favicon", key)
		return "", 0, ""
	}

	sum := sha256.Sum256(data)
	faviconHash = hex.EncodeToString(sum[:])

	// Only SVG (text-based) gets zstd'd -- see internal/archive's
	// WriteAsset doc for why already-compressed binary formats (png, ico)
	// aren't worth it.
	compress := ext == "svg"

	faviconPath, writtenSizeRaw, err := ing.store.WriteAsset(htmlHash, faviconHash, ext, data, compress)
	if err != nil {
		ing.logger.WarnContext(ctx, "ingest: failed to write favicon to local archive, continuing without one",
			"capture_id", pc.ID, "error", err)
		return "", 0, ""
	}
	if writtenSizeRaw > math.MaxInt32 {
		// A multi-gigabyte favicon is absurd on its face -- treat it the
		// same as any other favicon failure (cosmetic loss, not worth
		// failing the capture over) rather than overflowing the int32
		// column or silently truncating a bogus value into it.
		ing.logger.WarnContext(ctx, "ingest: favicon size exceeds int32 range, continuing without one",
			"capture_id", pc.ID, "written_size", writtenSize)
		return "", 0, ""
	}

	writtenSize = int32(writtenSizeRaw)
	return faviconPath, writtenSize, faviconHash
}

type writeInput struct {
	normalizedURL         string
	title                 string
	htmlPath              string
	htmlCompressedBytes   int32
	htmlUncompressedBytes int32
	contentHash           string
	capturedAt            time.Time
	language              string
	faviconPath           string
	faviconSizeBytes      int32
	faviconHash           string
}

// writeToPostgres performs the page upsert, idempotent capture insert, and
// (only for a genuinely new capture) the screenshot/readability job
// inserts as one transaction -- either all of it lands, or none of it
// does, so a crash mid-transaction can never leave a capture row without
// its corresponding job rows.
func (ing *Ingester) writeToPostgres(ctx context.Context, pc *PendingCapture, in *writeInput) (captureID int64, inserted bool, err error) {
	tx, err := ing.pool.Begin(ctx)
	if err != nil {
		return 0, false, fmt.Errorf("beginning transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	qtx := ing.queries.WithTx(tx)

	page, err := qtx.UpsertPage(ctx, db.UpsertPageParams{
		UserID:          pc.UserID,
		NormalizedUrl:   in.normalizedURL,
		Title:           textOrNull(in.title),
		LatestCaptureAt: pgtype.Timestamptz{Time: in.capturedAt, Valid: true},
		FaviconPath:     textOrNull(in.faviconPath),
	})
	if err != nil {
		return 0, false, fmt.Errorf("upserting page: %w", err)
	}

	captureParams := db.InsertCaptureIdempotentParams{
		PageID:                    page.ID,
		SourceCaptureID:           pgtype.Text{String: pc.ID, Valid: true},
		Source:                    "extension",
		RawUrl:                    pc.URL,
		Title:                     textOrNull(in.title),
		HtmlPath:                  in.htmlPath,
		HtmlCompressedSizeBytes:   in.htmlCompressedBytes,
		HtmlUncompressedSizeBytes: in.htmlUncompressedBytes,
		ContentHash:               in.contentHash,
		CapturedAt:                pgtype.Timestamptz{Time: in.capturedAt, Valid: true},
		Language:                  in.language,
		FaviconPath:               textOrNull(in.faviconPath),
		FaviconSizeBytes:          int32OrNull(in.faviconSizeBytes, in.faviconPath != ""),
		FaviconHash:               textOrNull(in.faviconHash),
	}
	capture, err := ing.insertCaptureWithCollisionHandling(ctx, qtx, &captureParams)
	if err != nil {
		return 0, false, fmt.Errorf("inserting capture: %w", err)
	}

	if capture.Inserted {
		if err := qtx.CreateScreenshotJob(ctx, capture.ID); err != nil {
			return 0, false, fmt.Errorf("enqueuing screenshot job: %w", err)
		}
		if err := qtx.CreateReadabilityJob(ctx, capture.ID); err != nil {
			return 0, false, fmt.Errorf("enqueuing readability job: %w", err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return 0, false, fmt.Errorf("committing transaction: %w", err)
	}

	return capture.ID, capture.Inserted, nil
}

// maxCaptureIDCollisionRetries bounds how many times
// insertCaptureWithCollisionHandling will regenerate source_capture_id and
// retry before giving up loudly. A real UUIDv4 collision is astronomically
// unlikely (122 bits of randomness -- see google/uuid's own doc comment on
// NewRandom for the back-of-envelope odds), so more than one regeneration
// should never actually be needed in practice; this bound exists to fail
// loudly rather than loop forever if it somehow is, e.g. a client with a
// broken (non-random, or seeded) UUID generator.
const maxCaptureIDCollisionRetries = 5

// insertCaptureWithCollisionHandling inserts a capture, distinguishing a
// legitimate retry of the same upload from a genuine source_capture_id
// collision between two different captures.
//
// The disambiguating signal is content_hash: a retry of the *same* upload
// will always carry the *same* content_hash as whatever's already stored
// under that source_capture_id (it's a hash of the identical HTML bytes,
// pulled from the identical R2 object). A genuine collision -- two
// different captures that happen to share a source_capture_id -- will
// essentially never also share a content_hash. So: matching content_hash
// on conflict means "this is a retry, no-op is correct." Differing
// content_hash means a real collision, and the fix is to generate a fresh
// UUID for *this* capture and try the insert again -- not to fail, and
// not to silently drop the data.
func (ing *Ingester) insertCaptureWithCollisionHandling(
	ctx context.Context,
	qtx *db.Queries,
	params *db.InsertCaptureIdempotentParams,
) (db.InsertCaptureIdempotentRow, error) {
	for attempt := 0; attempt <= maxCaptureIDCollisionRetries; attempt++ {
		capture, err := qtx.InsertCaptureIdempotent(ctx, *params)
		if err != nil {
			return db.InsertCaptureIdempotentRow{}, err
		}
		if capture.Inserted {
			return capture, nil
		}
		if capture.ContentHash == params.ContentHash {
			// Same content already stored under this source_capture_id:
			// a legitimate retry of the same upload, not a collision.
			return capture, nil
		}

		ing.logger.WarnContext(ctx,
			"ingest: source_capture_id collision between two different captures, regenerating and retrying",
			"attempted_source_capture_id", params.SourceCaptureID.String,
			"attempt", attempt+1,
		)
		params.SourceCaptureID = pgtype.Text{String: uuid.NewString(), Valid: true}
	}

	return db.InsertCaptureIdempotentRow{}, fmt.Errorf(
		"could not insert capture after %d source_capture_id collisions", maxCaptureIDCollisionRetries+1)
}

// titleRegexp is deliberately a simple, tolerant pattern (case-insensitive,
// dot-matches-newline for a title tag split across lines) rather than a
// full HTML parser -- extracting one well-known tag's text content from
// already-trusted, already-captured HTML doesn't need one.
var titleRegexp = regexp.MustCompile(`(?is)<title[^>]*>(.*?)</title>`)

// extractTitle parses the page title from the captured HTML's <title> tag
// at ingestion time, uniformly for every capture regardless of source.
// This isn't a Readability output, and it isn't currently transmitted by the
// extension either: SingleFile's own getPageData return includes a title
// but nothing in POST /queue/:id/complete request body carries it through to
// the Worker/D1. Parsing it here from the raw HTML is therefore the one real
// source of truth for a capture's title today.
func extractTitle(htmlBytes []byte) string {
	m := titleRegexp.FindSubmatch(htmlBytes)
	if m == nil {
		return ""
	}
	return strings.TrimSpace(html.UnescapeString(string(m[1])))
}

// parseD1Timestamp parses captured_at as sent by a device in the
// POST /queue/:id/complete request body -- expected to be an ISO 8601 /
// RFC 3339 string, tried first with (optional) fractional seconds, then
// without.
func parseD1Timestamp(s string) (time.Time, error) {
	if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
		return t, nil
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t, nil
	}
	return time.Time{}, fmt.Errorf("unrecognized timestamp format %q", s)
}

func textOrNull(s string) pgtype.Text {
	return pgtype.Text{String: s, Valid: s != ""}
}

// int32OrNull wraps an int32 size value as valid only when present has
// been established independently (faviconPath != "") rather than
// inferring presence from the value itself -- a genuinely zero-byte
// favicon (valid, if unusual) shouldn't be indistinguishable from "no
// favicon at all" the way testing v == 0 alone would make it.
func int32OrNull(v int32, present bool) pgtype.Int4 {
	return pgtype.Int4{Int32: v, Valid: present}
}
