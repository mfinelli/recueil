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

package httpapi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"html"
	"io"
	"log"
	"math"
	"net/http"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/mfinelli/recueil/internal/auth"
	"github.com/mfinelli/recueil/internal/db"
)

// maxManualUploadMultipartMemory bounds how much of a manual-upload
// request's multipart body ParseMultipartForm buffers in memory before
// spilling further parts to temp files on disk -- Go's net/http
// default is 32MB. The real ceiling on total request size is
// middleware.RequestSize(s.ManualUploadMaxBytes) at the router level; this is
// only about the memory/disk split for whatever falls under that ceiling.
const maxManualUploadMultipartMemory = 32 << 20

// manualUploadFaviconExtensions mirrors internal/ingest's favicon handling:
// SVG/PNG/ICO are the only favicon shapes anything else in recueil ever
// produces or compresses, so a manually-uploaded favicon is held to the same
// closed set.
var manualUploadFaviconExtensions = map[string]bool{"svg": true, "png": true, "ico": true}

type manualUploadResponse struct {
	PageID    int64 `json:"page_id"`
	CaptureID int64 `json:"capture_id"`
}

// POST /api/manual-upload: for a page captured somewhere the extension wasn't
// installed (e.g., an email attachment, a device without the extension, a file
// handed over by someone else, etc.) this accepts an already-captured, fully
// inlined SingleFile-style HTML file plus its URL and an optional favicon,
// directly from the dashboard. A single authenticated POST straight into the
// backend: R2, D1, and the Worker are never involved, unlike the
// extension/queue capture path, which this deliberately does not share code
// with since that package's pipeline is built tightly around pulling an
// already-uploaded blob from R2 via a pendingcaptures.PendingCapture, and
// bending it to also accept bytes already in hand here isn't worth the
// coupling. The overlap that matters (hashing, local-disk storage, URL
// normalization, title/language extraction) is small enough to duplicate
// directly against archive.Store/urlnorm.Pipeline/db.Queries instead.
func (s *Server) ManualUpload(w http.ResponseWriter, r *http.Request) {
	user, ok := auth.UserFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	ctx := r.Context()

	if err := r.ParseMultipartForm(maxManualUploadMultipartMemory); err != nil {
		writeError(w, http.StatusBadRequest, "invalid multipart request (or file too large)")
		return
	}
	defer func() {
		if r.MultipartForm != nil {
			_ = r.MultipartForm.RemoveAll()
		}
	}()

	rawURL := strings.TrimSpace(r.FormValue("url"))
	if rawURL == "" {
		writeError(w, http.StatusBadRequest, "url is required")
		return
	}

	htmlData, err := readMultipartFile(r, "html")
	if err != nil {
		writeError(w, http.StatusBadRequest, "html file is required")
		return
	}
	if len(htmlData) == 0 {
		writeError(w, http.StatusBadRequest, "html file is empty")
		return
	}

	faviconData, faviconExt, err := readOptionalFavicon(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	normalizedURL, err := s.Pipeline.Normalize(ctx, rawURL)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid url")
		return
	}

	sum := sha256.Sum256(htmlData)
	contentHash := hex.EncodeToString(sum[:])

	title := extractManualUploadTitle(htmlData)

	language, err := s.resolveManualUploadLanguageConfig(ctx, extractManualUploadLanguage(htmlData))
	if err != nil {
		log.Printf("warning: failed to resolve language config for manual upload (user %d): %v", user.ID, err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	relDir, err := s.Store.NewCapture()
	if err != nil {
		log.Printf("warning: failed to create local archive directory for manual upload (user %d): %v", user.ID, err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	relPath, compressedSize, err := s.Store.WriteHTML(relDir, htmlData)
	if err != nil {
		log.Printf("warning: failed to write manual upload html to local archive (user %d): %v", user.ID, err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	if compressedSize > math.MaxInt32 || len(htmlData) > math.MaxInt32 {
		writeError(w, http.StatusBadRequest, "file too large")
		return
	}

	// Favicon storage is best-effort, same as internal/ingest's
	// captureFavicon: a failure here is logged and the upload
	// continues without one.
	var faviconPath string
	var faviconSizeBytes int32
	var faviconHash string
	if faviconData != nil {
		faviconPath, faviconSizeBytes, faviconHash = s.writeManualUploadFavicon(relDir, faviconData, faviconExt, user.ID)
	}

	captureID, pageID, err := s.writeManualUploadToPostgres(ctx, &manualUploadWriteInput{
		userID:                user.ID,
		normalizedURL:         normalizedURL,
		rawURL:                rawURL,
		title:                 title,
		htmlPath:              relPath,
		htmlCompressedBytes:   int32(compressedSize),
		htmlUncompressedBytes: int32(len(htmlData)),
		contentHash:           contentHash,
		language:              language,
		faviconPath:           faviconPath,
		faviconSizeBytes:      faviconSizeBytes,
		faviconHash:           faviconHash,
	})
	if err != nil {
		log.Printf("warning: failed to write manual upload to postgres (user %d): %v", user.ID, err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	writeJSON(w, http.StatusCreated, manualUploadResponse{PageID: pageID, CaptureID: captureID})
}

// readMultipartFile reads a named multipart file part fully into memory
// and closes it. Returns an error if the part is missing entirely which is
// distinct from a present-but-empty part, and which readMultipartFile itself
// doesn't reject (ManualUpload checks htmlData's length separately, since
// "field missing" and "field present but empty" warrant different
// messages).
func readMultipartFile(r *http.Request, field string) ([]byte, error) {
	file, _, err := r.FormFile(field)
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()
	return io.ReadAll(file)
}

// readOptionalFavicon reads the "favicon" part if present, validating its
// filename extension against manualUploadFaviconExtensions. Returns
// (nil, "", nil) when no favicon part was uploaded at all (a favicon is
// always optional, unlike the html field).
func readOptionalFavicon(r *http.Request) (data []byte, ext string, err error) {
	files := r.MultipartForm.File["favicon"]
	if len(files) == 0 {
		return nil, "", nil
	}

	header := files[0]
	ext = strings.ToLower(strings.TrimPrefix(filepath.Ext(header.Filename), "."))
	if !manualUploadFaviconExtensions[ext] {
		return nil, "", fmt.Errorf("favicon must be .svg, .png, or .ico")
	}

	file, openErr := header.Open()
	if openErr != nil {
		return nil, "", fmt.Errorf("could not read favicon file")
	}
	defer func() { _ = file.Close() }()

	data, err = io.ReadAll(file)
	if err != nil {
		return nil, "", fmt.Errorf("could not read favicon file")
	}
	if len(data) == 0 {
		return nil, "", fmt.Errorf("favicon file is empty")
	}
	return data, ext, nil
}

// writeManualUploadFavicon stores an already-read, already-validated
// favicon alongside the HTML in the same capture directory.
func (s *Server) writeManualUploadFavicon(relDir string, data []byte, ext string, userID int64) (faviconPath string, writtenSize int32, faviconHash string) {
	sum := sha256.Sum256(data)
	faviconHash = hex.EncodeToString(sum[:])

	// Only SVG (text-based) gets zstd'd.
	compress := ext == "svg"

	faviconPath, writtenSizeRaw, err := s.Store.WriteAsset(relDir, "favicon", ext, data, compress)
	if err != nil {
		log.Printf("warning: failed to write manual upload favicon to local archive (user %d), continuing without one: %v", userID, err)
		return "", 0, ""
	}
	if writtenSizeRaw > math.MaxInt32 {
		log.Printf("warning: manual upload favicon size exceeds int32 range (user %d), continuing without one", userID)
		return "", 0, ""
	}

	return faviconPath, int32(writtenSizeRaw), faviconHash
}

// manualUploadTitleRegexp is extractManualUploadTitle's copy of
// internal/ingest's titleRegexp.
var manualUploadTitleRegexp = regexp.MustCompile(`(?is)<title[^>]*>(.*?)</title>`)

// extractManualUploadTitle is internal/ingest's extractTitle.
func extractManualUploadTitle(htmlBytes []byte) string {
	m := manualUploadTitleRegexp.FindSubmatch(htmlBytes)
	if m == nil {
		return ""
	}
	return strings.TrimSpace(html.UnescapeString(string(m[1])))
}

// manualUploadLanguageTagPattern is internal/ingest's languageTagPattern
// (internal/ingest/language.go).
var manualUploadLanguageTagPattern = regexp.MustCompile(`(?is)<html\b[^>]*\blang\s*=\s*(?:"([^"]*)"|'([^']*)'|([^\s"'=<>` + "`" + `]+))`)

// manualUploadPostgresLanguageConfigs is internal/ingest's
// postgresLanguageConfigs.
var manualUploadPostgresLanguageConfigs = map[string]string{
	"ar": "arabic", "hy": "armenian", "eu": "basque", "ca": "catalan",
	"da": "danish", "nl": "dutch", "en": "english", "et": "estonian",
	"fi": "finnish", "fr": "french", "de": "german", "el": "greek",
	"hi": "hindi", "hu": "hungarian", "id": "indonesian", "ga": "irish",
	"it": "italian", "lt": "lithuanian", "ne": "nepali", "no": "norwegian",
	"nb": "norwegian", "nn": "norwegian", "pt": "portuguese", "ro": "romanian",
	"ru": "russian", "es": "spanish", "sv": "swedish", "tr": "turkish",
}

// extractManualUploadLanguage is internal/ingest's extractLanguage.
func extractManualUploadLanguage(htmlBytes []byte) string {
	m := manualUploadLanguageTagPattern.FindSubmatch(htmlBytes)
	if m == nil {
		return ""
	}
	var raw []byte
	for _, group := range m[1:] {
		if len(group) > 0 {
			raw = group
			break
		}
	}
	tag := strings.ToLower(strings.TrimSpace(string(raw)))
	primary, _, _ := strings.Cut(tag, "-")
	return primary
}

// resolveManualUploadLanguageConfig is internal/ingest's
// resolveLanguageConfig/languageConfigExists, duplicated (against s.Pool
// rather than an *Ingester's pool field).
func (s *Server) resolveManualUploadLanguageConfig(ctx context.Context, langTag string) (string, error) {
	if langTag == "" {
		return "simple", nil
	}
	candidate, ok := manualUploadPostgresLanguageConfigs[langTag]
	if !ok {
		return "simple", nil
	}

	var exists bool
	err := s.Pool.QueryRow(ctx,
		"SELECT EXISTS(SELECT 1 FROM pg_ts_config WHERE cfgname = $1)", candidate,
	).Scan(&exists)
	if err != nil {
		return "", fmt.Errorf("checking language config %q: %w", candidate, err)
	}
	if !exists {
		return "simple", nil
	}
	return candidate, nil
}

// int32OrNull is internal/ingest's int32OrNull: presence is established
// independently (faviconPath != "") rather than inferred from the value
// itself, so a genuinely zero-byte favicon isn't indistinguishable from
// "no favicon at all."
func int32OrNull(v int32, present bool) pgtype.Int4 {
	return pgtype.Int4{Int32: v, Valid: present}
}

type manualUploadWriteInput struct {
	userID                int64
	normalizedURL         string
	rawURL                string
	title                 string
	htmlPath              string
	htmlCompressedBytes   int32
	htmlUncompressedBytes int32
	contentHash           string
	language              string
	faviconPath           string
	faviconSizeBytes      int32
	faviconHash           string
}

// writeManualUploadToPostgres performs the page upsert, capture insert,
// and (for a genuinely new capture) the screenshot/readability job
// inserts as one transaction.
//
// source_capture_id is a freshly backend-generated UUID but unlike
// internal/ingest's insertCaptureWithCollisionHandling, there's no
// retry-of-the-same-upload case to distinguish from a genuine collision here:
// every call mints its own fresh UUID up front, so a unique-constraint
// violation on it would mean an actual (astronomically unlikely) collision,
// not a legitimate retry and rare enough that surfacing it as a plain 500 for
// the person to simply try again is the right level of handling.
func (s *Server) writeManualUploadToPostgres(ctx context.Context, in *manualUploadWriteInput) (captureID, pageID int64, err error) {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return 0, 0, fmt.Errorf("beginning transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	qtx := s.Queries.WithTx(tx)
	capturedAt := time.Now()

	page, err := qtx.UpsertPage(ctx, db.UpsertPageParams{
		UserID:          in.userID,
		NormalizedUrl:   in.normalizedURL,
		Title:           textOrNull(in.title),
		LatestCaptureAt: pgtype.Timestamptz{Time: capturedAt, Valid: true},
		FaviconPath:     textOrNull(in.faviconPath),
	})
	if err != nil {
		return 0, 0, fmt.Errorf("upserting page: %w", err)
	}

	capture, err := qtx.InsertCaptureIdempotent(ctx, db.InsertCaptureIdempotentParams{
		PageID:                    page.ID,
		SourceCaptureID:           pgtype.Text{String: uuid.NewString(), Valid: true},
		Source:                    "manual_upload",
		RawUrl:                    in.rawURL,
		Title:                     textOrNull(in.title),
		HtmlPath:                  in.htmlPath,
		HtmlCompressedSizeBytes:   in.htmlCompressedBytes,
		HtmlUncompressedSizeBytes: in.htmlUncompressedBytes,
		ContentHash:               in.contentHash,
		CapturedAt:                pgtype.Timestamptz{Time: capturedAt, Valid: true},
		Language:                  in.language,
		FaviconPath:               textOrNull(in.faviconPath),
		FaviconSizeBytes:          int32OrNull(in.faviconSizeBytes, in.faviconPath != ""),
		FaviconHash:               textOrNull(in.faviconHash),
	})
	if err != nil {
		return 0, 0, fmt.Errorf("inserting capture: %w", err)
	}

	if capture.Inserted {
		if err := qtx.CreateScreenshotJob(ctx, capture.ID); err != nil {
			return 0, 0, fmt.Errorf("enqueuing screenshot job: %w", err)
		}
		if err := qtx.CreateReadabilityJob(ctx, capture.ID); err != nil {
			return 0, 0, fmt.Errorf("enqueuing readability job: %w", err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return 0, 0, fmt.Errorf("committing transaction: %w", err)
	}

	return capture.ID, page.ID, nil
}
