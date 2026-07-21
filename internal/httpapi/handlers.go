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
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/mfinelli/recueil/internal/archive"
	"github.com/mfinelli/recueil/internal/auth"
	"github.com/mfinelli/recueil/internal/db"
	"github.com/mfinelli/recueil/internal/devices"
	"github.com/mfinelli/recueil/internal/mirror"
)

type Server struct {
	Queries      *db.Queries
	Pool         *pgxpool.Pool
	Store        *archive.Store
	Mirror       *mirror.Client
	Devices      *devices.Client
	Bootstrap    *auth.BootstrapTokenHolder
	CookieSecure bool
	PairingKey   auth.PairingKey
}

func NewServer(q *db.Queries, pool *pgxpool.Pool, store *archive.Store, m *mirror.Client, d *devices.Client, bootstrap *auth.BootstrapTokenHolder, cookieSecure bool, pairingKey auth.PairingKey) *Server {
	return &Server{Queries: q, Pool: pool, Store: store, Mirror: m, Devices: d, Bootstrap: bootstrap, CookieSecure: cookieSecure, PairingKey: pairingKey}
}

type credentials struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

type setupRequest struct {
	BootstrapToken string `json:"bootstrap_token"`
	Username       string `json:"username"`
	Password       string `json:"password"`
}

type userResponse struct {
	ID       int64  `json:"id"`
	Username string `json:"username"`
	Role     string `json:"role"`
}

type pairingTokenResponse struct {
	PairingToken string `json:"pairing_token"`
}

type setupStatusResponse struct {
	NeedsSetup bool `json:"needs_setup"`
}

// GET /api/setup-status: unauthenticated -- lets the dashboard's first load
// distinguish "show the setup screen" from "show the login screen" without
// guessing or having to attempt POST /api/setup speculatively just to read
// its 409. Deliberately doesn't leak anything beyond the boolean (not a
// username, not a count) -- an unauthenticated endpoint has no other reason
// to exist here.
func (s *Server) SetupStatus(w http.ResponseWriter, r *http.Request) {
	count, err := s.Queries.CountUsers(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	writeJSON(w, http.StatusOK, setupStatusResponse{NeedsSetup: count == 0})
}

// POST /api/setup: creates the first admin account, gated by the bootstrap
// token printed to the backend's logs on startup. The token is
// consumed only if CreateUser actually succeeds (see auth.BootstrapTokenHolder.Use),
// so a transient failure after a valid token can be retried without a restart.
func (s *Server) Setup(w http.ResponseWriter, r *http.Request) {
	req, err := decodeJSON[setupRequest](r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Username == "" || req.Password == "" || req.BootstrapToken == "" {
		writeError(w, http.StatusBadRequest, "bootstrap_token, username, and password are required")
		return
	}

	ctx := r.Context()

	// Belt-and-suspenders: even though a valid unused token implies this,
	// explicitly confirm no admin has slipped in through a race.
	count, err := s.Queries.CountUsers(ctx)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	if count > 0 {
		writeError(w, http.StatusConflict, "setup has already been completed")
		return
	}

	var user db.User
	var pairingHash string
	var createErr error
	err = s.Bootstrap.Use(req.BootstrapToken, func() error {
		hash, err := auth.HashPassword(req.Password)
		if err != nil {
			return err
		}

		var pairingRaw string
		pairingRaw, pairingHash, err = auth.GeneratePairingToken()
		if err != nil {
			return err
		}
		pairingEnc, err := auth.EncryptPairingToken(s.PairingKey, pairingRaw)
		if err != nil {
			return err
		}

		user, createErr = s.Queries.CreateUser(ctx, db.CreateUserParams{
			Username:        req.Username,
			PasswordHash:    hash,
			PairingTokenEnc: pgtype.Text{String: pairingEnc, Valid: true},
			Role:            "admin",
		})
		return createErr
	})

	if errors.Is(err, auth.ErrInvalidBootstrapToken) {
		writeError(w, http.StatusUnauthorized, "invalid or expired bootstrap token")
		return
	}
	if err != nil {
		writeError(w, http.StatusConflict, "could not create user (username may be taken)")
		return
	}

	if err := s.Mirror.PushUser(ctx, user.ID, &pairingHash); err != nil {
		// Doesn't roll back account creation (see internal/mirror's
		// PushUser doc comment). Dashboard login works immediately; device
		// pairing for this user is broken until a resync runs.
		log.Printf("warning: failed to push pairing-token mirror for new user %d: %v", user.ID, err)
	}

	s.startSession(w, r, &user)
	writeJSON(w, http.StatusCreated, userResponse{ID: user.ID, Username: user.Username, Role: user.Role})
}

// POST /api/auth/register: open registration.
func (s *Server) Register(w http.ResponseWriter, r *http.Request) {
	req, err := decodeJSON[credentials](r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Username == "" || req.Password == "" {
		writeError(w, http.StatusBadRequest, "username and password are required")
		return
	}

	ctx := r.Context()
	hash, err := auth.HashPassword(req.Password)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	pairingRaw, pairingHash, err := auth.GeneratePairingToken()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	pairingEnc, err := auth.EncryptPairingToken(s.PairingKey, pairingRaw)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	user, err := s.Queries.CreateUser(ctx, db.CreateUserParams{
		Username:        req.Username,
		PasswordHash:    hash,
		PairingTokenEnc: pgtype.Text{String: pairingEnc, Valid: true},
		Role:            "member",
	})
	if err != nil {
		writeError(w, http.StatusConflict, "username already taken")
		return
	}

	if err := s.Mirror.PushUser(ctx, user.ID, &pairingHash); err != nil {
		log.Printf("warning: failed to push pairing-token mirror for new user %d: %v", user.ID, err)
	}

	s.startSession(w, r, &user)
	writeJSON(w, http.StatusCreated, userResponse{ID: user.ID, Username: user.Username, Role: user.Role})
}

// POST /api/auth/login
func (s *Server) Login(w http.ResponseWriter, r *http.Request) {
	req, err := decodeJSON[credentials](r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	ctx := r.Context()
	user, err := s.Queries.GetUserByUsername(ctx, req.Username)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "invalid username or password")
		return
	}

	if !auth.VerifyPassword(user.PasswordHash, req.Password) {
		writeError(w, http.StatusUnauthorized, "invalid username or password")
		return
	}

	s.startSession(w, r, &user)
	writeJSON(w, http.StatusOK, userResponse{ID: user.ID, Username: user.Username, Role: user.Role})
}

// POST /api/auth/logout
func (s *Server) Logout(w http.ResponseWriter, r *http.Request) {
	if cookie, err := r.Cookie("recueil_session"); err == nil && cookie.Value != "" {
		if err := s.Queries.DeleteSession(r.Context(), auth.HashToken(cookie.Value)); err != nil {
			log.Printf("warning: failed to delete session: %v", err)
		}
	}
	auth.ClearSessionCookie(w, s.CookieSecure)
	w.WriteHeader(http.StatusNoContent)
}

// GET /api/auth/me
func (s *Server) Me(w http.ResponseWriter, r *http.Request) {
	user, ok := auth.UserFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	writeJSON(w, http.StatusOK, userResponse{ID: user.ID, Username: user.Username, Role: user.Role})
}

// GET /api/pairing-token: decrypts and returns the current user's pairing
// token, so it's always viewable on the dashboard rather than only shown
// once at creation (this credential's stakes differ from a login password or
// session token, and losing it shouldn't force an immediate regenerate).
func (s *Server) GetPairingToken(w http.ResponseWriter, r *http.Request) {
	user, ok := auth.UserFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	if !user.PairingTokenEnc.Valid {
		writeError(w, http.StatusNotFound, "no pairing token; regenerate one")
		return
	}

	raw, err := auth.DecryptPairingToken(s.PairingKey, user.PairingTokenEnc.String)
	if err != nil {
		log.Printf("warning: failed to decrypt pairing token for user %d: %v", user.ID, err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	writeJSON(w, http.StatusOK, pairingTokenResponse{PairingToken: raw})
}

// POST /api/pairing-token/regenerate: issues a new pairing token, replacing
// both the Postgres (encrypted) and D1 (hashed) copies. Any device that
// tries to pair using the previous token will fail; already-issued device
// bearer tokens are unaffected (revocation is never a live push to an
// already-paired device).
func (s *Server) RegeneratePairingToken(w http.ResponseWriter, r *http.Request) {
	user, ok := auth.UserFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	ctx := r.Context()

	raw, hash, err := auth.GeneratePairingToken()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	enc, err := auth.EncryptPairingToken(s.PairingKey, raw)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	if err := s.Queries.UpdatePairingToken(ctx, db.UpdatePairingTokenParams{
		ID:              user.ID,
		PairingTokenEnc: pgtype.Text{String: enc, Valid: true},
	}); err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	if err := s.Mirror.PushUser(ctx, user.ID, &hash); err != nil {
		log.Printf("warning: failed to push regenerated pairing-token mirror for user %d: %v", user.ID, err)
	}

	writeJSON(w, http.StatusOK, pairingTokenResponse{PairingToken: raw})
}

// DELETE /api/pairing-token: revokes without reissuing, blocking further
// device pairing until a regenerate. Already-issued device bearer tokens
// are unaffected, same as regenerate above.
func (s *Server) RevokePairingToken(w http.ResponseWriter, r *http.Request) {
	user, ok := auth.UserFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	ctx := r.Context()

	if err := s.Queries.UpdatePairingToken(ctx, db.UpdatePairingTokenParams{
		ID:              user.ID,
		PairingTokenEnc: pgtype.Text{Valid: false},
	}); err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	if err := s.Mirror.PushUser(ctx, user.ID, nil); err != nil {
		log.Printf("warning: failed to push pairing-token revoke to mirror for user %d: %v", user.ID, err)
	}

	w.WriteHeader(http.StatusNoContent)
}

type deviceListResponse struct {
	Devices []devices.Token `json:"devices"`
}

// GET /api/devices: lists the calling user's own paired devices. Always
// self-scoped.
func (s *Server) ListDevices(w http.ResponseWriter, r *http.Request) {
	user, ok := auth.UserFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	tokens, err := s.Devices.ListTokens(r.Context(), user.ID)
	if err != nil {
		log.Printf("warning: failed to list devices for user %d: %v", user.ID, err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	writeJSON(w, http.StatusOK, deviceListResponse{Devices: tokens})
}

// DELETE /api/devices/{id}: revokes one of the calling user's own
// devices. Always self-scoped, same reasoning as ListDevices above. Not a
// live push -- the device keeps working until its next request to the
// Worker.
func (s *Server) RevokeDevice(w http.ResponseWriter, r *http.Request) {
	user, ok := auth.UserFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	tokenID, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid device id")
		return
	}

	if err := s.Devices.RevokeToken(r.Context(), user.ID, tokenID); err != nil {
		if errors.Is(err, devices.ErrNotFound) {
			writeError(w, http.StatusNotFound, "device not found")
			return
		}
		log.Printf("warning: failed to revoke device %d for user %d: %v", tokenID, user.ID, err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// defaultPageLimit/maxPageLimit bound ?limit= for /api/pages: a sane
// default for the library view, and a ceiling that's generous for a
// personal/family archive without letting a runaway value force one
// query to return everything at once.
const (
	defaultPageLimit = 50
	maxPageLimit     = 200
)

func parseLimitOffset(r *http.Request) (limit, offset int32) {
	limit = defaultPageLimit
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= maxPageLimit {
			limit = int32(n)
		}
	}
	if v := r.URL.Query().Get("offset"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 32); err == nil && n >= 0 {
			offset = int32(n)
		}
	}
	return limit, offset
}

// textOrNil converts a pgtype.Text into the *string shape the dashboard's
// JSON responses use -- nil rather than an empty string for a genuinely
// NULL column, matching how the frontend should treat "no title" /
// "no favicon" differently from "title is the empty string."
func textOrNil(t pgtype.Text) *string {
	if !t.Valid {
		return nil
	}
	return &t.String
}

type pageResponse struct {
	ID                 int64     `json:"id"`
	NormalizedURL      string    `json:"normalized_url"`
	Title              *string   `json:"title"`
	LatestCaptureAt    time.Time `json:"latest_capture_at"`
	ExcludedFromMirror bool      `json:"excluded_from_mirror"`
	FaviconPath        *string   `json:"favicon_path"`
	CreatedAt          time.Time `json:"created_at"`
	UpdatedAt          time.Time `json:"updated_at"`
}

func pageResponseFromPage(p *db.Page) pageResponse {
	return pageResponse{
		ID: p.ID, NormalizedURL: p.NormalizedUrl, Title: textOrNil(p.Title),
		LatestCaptureAt: p.LatestCaptureAt.Time, ExcludedFromMirror: p.ExcludedFromMirror,
		FaviconPath: textOrNil(p.FaviconPath), CreatedAt: p.CreatedAt.Time, UpdatedAt: p.UpdatedAt.Time,
	}
}

func pageResponseFromListRow(p *db.ListPagesRow) pageResponse {
	return pageResponse{
		ID: p.ID, NormalizedURL: p.NormalizedUrl, Title: textOrNil(p.Title),
		LatestCaptureAt: p.LatestCaptureAt.Time, ExcludedFromMirror: p.ExcludedFromMirror,
		FaviconPath: textOrNil(p.FaviconPath), CreatedAt: p.CreatedAt.Time, UpdatedAt: p.UpdatedAt.Time,
	}
}

func pageResponseFromSearchRow(p *db.SearchPagesRow) pageResponse {
	return pageResponse{
		ID: p.ID, NormalizedURL: p.NormalizedUrl, Title: textOrNil(p.Title),
		LatestCaptureAt: p.LatestCaptureAt.Time, ExcludedFromMirror: p.ExcludedFromMirror,
		FaviconPath: textOrNil(p.FaviconPath), CreatedAt: p.CreatedAt.Time, UpdatedAt: p.UpdatedAt.Time,
	}
}

type pageListResponse struct {
	Pages []pageResponse `json:"pages"`
	Total int64          `json:"total"`
}

// GET /api/pages: library browsing. ?q= triggers full-text search
// (matches if any of a page's captures' reader_text matches, not just
// the latest -- see queries/pages.sql's SearchPages); without it, plain
// listing ordered by latest_capture_at. ?limit=/?offset= paginate
// (default 50, max 200); the response's total reflects the full matching
// set regardless of the current page.
func (s *Server) ListPages(w http.ResponseWriter, r *http.Request) {
	user, ok := auth.UserFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	limit, offset := parseLimitOffset(r)
	ctx := r.Context()

	resp := pageListResponse{Pages: []pageResponse{}}

	if q := r.URL.Query().Get("q"); q != "" {
		rows, err := s.Queries.SearchPages(ctx, db.SearchPagesParams{
			UserID: user.ID, Query: q, Limit: limit, Offset: offset,
		})
		if err != nil {
			log.Printf("warning: failed to search pages for user %d: %v", user.ID, err)
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}
		for _, row := range rows {
			resp.Total = row.TotalCount
			resp.Pages = append(resp.Pages, pageResponseFromSearchRow(&row))
		}
		writeJSON(w, http.StatusOK, resp)
		return
	}

	rows, err := s.Queries.ListPages(ctx, db.ListPagesParams{UserID: user.ID, Limit: limit, Offset: offset})
	if err != nil {
		log.Printf("warning: failed to list pages for user %d: %v", user.ID, err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	for _, row := range rows {
		resp.Total = row.TotalCount
		resp.Pages = append(resp.Pages, pageResponseFromListRow(&row))
	}
	writeJSON(w, http.StatusOK, resp)
}

type captureSummaryResponse struct {
	ID                        int64     `json:"id"`
	Source                    string    `json:"source"`
	RawURL                    string    `json:"raw_url"`
	Title                     *string   `json:"title"`
	ThumbnailPath             *string   `json:"thumbnail_path"`
	Language                  string    `json:"language"`
	HTMLCompressedSizeBytes   int32     `json:"html_compressed_size_bytes"`
	HTMLUncompressedSizeBytes int32     `json:"html_uncompressed_size_bytes"`
	CapturedAt                time.Time `json:"captured_at"`
}

type pageTagResponse struct {
	ID     int64  `json:"id"`
	Name   string `json:"name"`
	Source string `json:"source"`
}

type pageCollectionResponse struct {
	ID       int64  `json:"id"`
	Name     string `json:"name"`
	ParentID *int64 `json:"parent_id"`
}

// pageDetailResponse embeds pageResponse so its fields flatten into the
// same top-level JSON object as "captures"/"tags"/"collections" -- the
// page detail view's natural shape is "the page, plus everything
// attached to it," not a nested "page" envelope key.
type pageDetailResponse struct {
	pageResponse
	Captures    []captureSummaryResponse `json:"captures"`
	Tags        []pageTagResponse        `json:"tags"`
	Collections []pageCollectionResponse `json:"collections"`
}

// GET /api/pages/{id}: page detail plus full capture (version) history
// (most recent first), tags, and collection memberships. Captures are
// deliberately a summary per row, not the full row -- reader_text/
// ai_summary are large and belong to GET /api/captures/{id} instead.
func (s *Server) GetPage(w http.ResponseWriter, r *http.Request) {
	user, ok := auth.UserFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid page id")
		return
	}
	ctx := r.Context()

	page, err := s.Queries.GetPageByIDForUser(ctx, db.GetPageByIDForUserParams{ID: id, UserID: user.ID})
	if err != nil {
		writeError(w, http.StatusNotFound, "page not found")
		return
	}

	captures, err := s.Queries.ListCapturesByPage(ctx, page.ID)
	if err != nil {
		log.Printf("warning: failed to list captures for page %d: %v", page.ID, err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	tags, err := s.Queries.ListPageTags(ctx, page.ID)
	if err != nil {
		log.Printf("warning: failed to list tags for page %d: %v", page.ID, err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	collections, err := s.Queries.ListPageCollections(ctx, page.ID)
	if err != nil {
		log.Printf("warning: failed to list collections for page %d: %v", page.ID, err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	resp := pageDetailResponse{
		pageResponse: pageResponseFromPage(&page),
		Captures:     []captureSummaryResponse{},
		Tags:         []pageTagResponse{},
		Collections:  []pageCollectionResponse{},
	}
	for _, c := range captures {
		resp.Captures = append(resp.Captures, captureSummaryResponse{
			ID: c.ID, Source: c.Source, RawURL: c.RawUrl, Title: textOrNil(c.Title),
			ThumbnailPath: textOrNil(c.ThumbnailPath), Language: c.Language,
			HTMLCompressedSizeBytes: c.HtmlCompressedSizeBytes, HTMLUncompressedSizeBytes: c.HtmlUncompressedSizeBytes,
			CapturedAt: c.CapturedAt.Time,
		})
	}
	for _, t := range tags {
		resp.Tags = append(resp.Tags, pageTagResponse{ID: t.TagID, Name: t.Name, Source: t.Source})
	}
	for _, c := range collections {
		resp.Collections = append(resp.Collections, pageCollectionResponse{ID: c.CollectionID, Name: c.Name, ParentID: int8OrNil(c.ParentID)})
	}
	writeJSON(w, http.StatusOK, resp)
}

// serveAsset streams a small binary asset (favicon, thumbnail) off disk.
// Unlike GetCaptureHTML, no zstd/gzip content-negotiation dance -- these
// are already-binary images (PNG/ICO) or small enough SVGs that the same
// passthrough-compression treatment full HTML documents get isn't worth
// the complexity here. Store.Open already transparently decompresses a
// ".zst"-suffixed path (SVG favicons) and passes non-".zst" paths
// (PNG/ICO favicons, all thumbnails) through unmodified, so this doesn't
// need to know or care which case it's in.
func (s *Server) serveAsset(w http.ResponseWriter, relPath string) {
	reader, err := s.Store.Open(relPath)
	if err != nil {
		log.Printf("warning: failed to open asset %q: %v", relPath, err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	defer func() { _ = reader.Close() }()

	w.Header().Set("Content-Type", contentTypeForAsset(relPath))
	if _, err := io.Copy(w, reader); err != nil {
		log.Printf("warning: failed streaming asset %q: %v", relPath, err)
	}
}

// contentTypeForAsset infers a Content-Type from an asset's stored file
// extension. Strips a trailing ".zst" first -- filepath.Ext only ever
// returns the *last* extension, which for "favicon.svg.zst" is ".zst",
// not the ".svg" that actually determines the decompressed content's
// real type.
func contentTypeForAsset(relPath string) string {
	trimmed := strings.TrimSuffix(relPath, ".zst")
	switch strings.ToLower(filepath.Ext(trimmed)) {
	case ".svg":
		return "image/svg+xml"
	case ".png":
		return "image/png"
	case ".ico":
		return "image/x-icon"
	default:
		return "application/octet-stream"
	}
}

// GET /api/pages/{id}/favicon: pages.favicon_path is denormalized from
// the latest capture at ingestion time (UpsertPage), so this is a direct
// read, not a join. No Cache-Control: this URL is page-identity-addressed,
// not content-addressed -- a later re-capture with a different favicon
// changes what this same URL resolves to, so caching it long-lived risks
// serving a stale icon.
func (s *Server) GetPageFavicon(w http.ResponseWriter, r *http.Request) {
	user, ok := auth.UserFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid page id")
		return
	}

	page, err := s.Queries.GetPageByIDForUser(r.Context(), db.GetPageByIDForUserParams{ID: id, UserID: user.ID})
	if err != nil {
		writeError(w, http.StatusNotFound, "page not found")
		return
	}
	if !page.FaviconPath.Valid {
		writeError(w, http.StatusNotFound, "no favicon")
		return
	}

	s.serveAsset(w, page.FaviconPath.String)
}

// GET /api/pages/{id}/thumbnail: resolves the page's most recent
// capture's thumbnail (see GetLatestCaptureByPage's own doc comment for
// why this isn't a denormalized pages column the way favicon_path is).
// A capture with no thumbnail yet (screenshot job hasn't run, or
// genuinely failed) 404s the same as a page with no captures at all --
// the dashboard's grid view falls back to a placeholder either way, so
// there's no need to distinguish the two cases in the response.
func (s *Server) GetPageThumbnail(w http.ResponseWriter, r *http.Request) {
	user, ok := auth.UserFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid page id")
		return
	}
	ctx := r.Context()

	page, err := s.Queries.GetPageByIDForUser(ctx, db.GetPageByIDForUserParams{ID: id, UserID: user.ID})
	if err != nil {
		writeError(w, http.StatusNotFound, "page not found")
		return
	}

	capture, err := s.Queries.GetLatestCaptureByPage(ctx, page.ID)
	if err != nil {
		writeError(w, http.StatusNotFound, "no captures")
		return
	}
	if !capture.ThumbnailPath.Valid {
		writeError(w, http.StatusNotFound, "no thumbnail")
		return
	}

	s.serveAsset(w, capture.ThumbnailPath.String)
}

type patchPageRequest struct {
	ExcludedFromMirror *bool `json:"excluded_from_mirror"`
}

// PATCH /api/pages/{id}: currently only supports toggling
// excluded_from_mirror. A pointer field distinguishes "not provided"
// from an explicit false, so more patchable fields can be added later
// without changing this request shape.
func (s *Server) PatchPage(w http.ResponseWriter, r *http.Request) {
	user, ok := auth.UserFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid page id")
		return
	}
	req, err := decodeJSON[patchPageRequest](r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.ExcludedFromMirror == nil {
		writeError(w, http.StatusBadRequest, "excluded_from_mirror is required")
		return
	}

	page, err := s.Queries.SetPageExcludedFromMirror(r.Context(), db.SetPageExcludedFromMirrorParams{
		ExcludedFromMirror: *req.ExcludedFromMirror, ID: id, UserID: user.ID,
	})
	if err != nil {
		writeError(w, http.StatusNotFound, "page not found")
		return
	}

	writeJSON(w, http.StatusOK, pageResponseFromPage(&page))
}

type captureDetailResponse struct {
	ID                        int64     `json:"id"`
	PageID                    int64     `json:"page_id"`
	Source                    string    `json:"source"`
	RawURL                    string    `json:"raw_url"`
	Title                     *string   `json:"title"`
	ThumbnailPath             *string   `json:"thumbnail_path"`
	FaviconPath               *string   `json:"favicon_path"`
	ReaderText                *string   `json:"reader_text"`
	AISummary                 *string   `json:"ai_summary"`
	AIModel                   *string   `json:"ai_model"`
	Language                  string    `json:"language"`
	HTMLCompressedSizeBytes   int32     `json:"html_compressed_size_bytes"`
	HTMLUncompressedSizeBytes int32     `json:"html_uncompressed_size_bytes"`
	CapturedAt                time.Time `json:"captured_at"`
	CreatedAt                 time.Time `json:"created_at"`
	UpdatedAt                 time.Time `json:"updated_at"`
}

func captureDetailResponseFromCapture(c *db.Capture) captureDetailResponse {
	return captureDetailResponse{
		ID: c.ID, PageID: c.PageID, Source: c.Source, RawURL: c.RawUrl, Title: textOrNil(c.Title),
		ThumbnailPath: textOrNil(c.ThumbnailPath), FaviconPath: textOrNil(c.FaviconPath),
		ReaderText: textOrNil(c.ReaderText), AISummary: textOrNil(c.AiSummary), AIModel: textOrNil(c.AiModel),
		Language: c.Language, HTMLCompressedSizeBytes: c.HtmlCompressedSizeBytes, HTMLUncompressedSizeBytes: c.HtmlUncompressedSizeBytes,
		CapturedAt: c.CapturedAt.Time, CreatedAt: c.CreatedAt.Time, UpdatedAt: c.UpdatedAt.Time,
	}
}

// GET /api/captures/{id}: full capture detail including reader_text and
// AI summary -- the heavier fields GetPage's own capture-history list
// deliberately omits (see captureSummaryResponse).
func (s *Server) GetCapture(w http.ResponseWriter, r *http.Request) {
	user, ok := auth.UserFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid capture id")
		return
	}

	capture, err := s.Queries.GetCaptureByIDForUser(r.Context(), db.GetCaptureByIDForUserParams{ID: id, UserID: user.ID})
	if err != nil {
		writeError(w, http.StatusNotFound, "capture not found")
		return
	}

	writeJSON(w, http.StatusOK, captureDetailResponseFromCapture(&capture))
}

// acceptsZstd does an exact-token check against Accept-Encoding --
// deliberately stricter than chi's own middleware.Compress, which just
// does a substring Contains match (verified against its real source).
// That looseness is a reasonable tradeoff for chi's generic gzip/deflate
// negotiation, but this is the one place recueil hand-rolls its own
// Content-Encoding decision, so it can afford to be exact.
func acceptsZstd(r *http.Request) bool {
	for _, enc := range strings.Split(r.Header.Get("Accept-Encoding"), ",") {
		if strings.TrimSpace(strings.ToLower(enc)) == "zstd" {
			return true
		}
	}
	return false
}

// GET /api/captures/{id}/html: streams the archived HTML. The HTML is
// already stored zstd-compressed on disk (internal/archive); if the
// client's own Accept-Encoding says it can handle zstd, this streams
// those bytes completely unmodified (Content-Encoding: zstd) rather than
// decompressing just to maybe recompress. Otherwise it streams the
// decompressed HTML and leans on the router's own middleware.Compress
// (whose allowed types now include text/html) to gzip it if the client
// asked for gzip instead -- verified against chi's real source that its
// WriteHeader steps aside the moment Content-Encoding is already set, so
// there's no risk of double-compressing the zstd path.
func (s *Server) GetCaptureHTML(w http.ResponseWriter, r *http.Request) {
	user, ok := auth.UserFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid capture id")
		return
	}

	capture, err := s.Queries.GetCaptureByIDForUser(r.Context(), db.GetCaptureByIDForUserParams{ID: id, UserID: user.ID})
	if err != nil {
		writeError(w, http.StatusNotFound, "capture not found")
		return
	}

	w.Header().Set("Vary", "Accept-Encoding")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	// Defense-in-depth, not the primary control: the extension's SingleFile
	// capture already runs with blockScripts: true (see
	// extension/src/capture-inject/bundle-entry.js), so archived HTML
	// shouldn't contain live scripts at all. But this is served
	// same-origin with the dashboard -- if anything ever did slip
	// through (a SingleFile edge case, a future config change), it would
	// otherwise run with access to the logged-in session's cookies and
	// could call the API as the user. Costs nothing to block outright.
	w.Header().Set("Content-Security-Policy", "script-src 'none'")

	if acceptsZstd(r) {
		reader, err := s.Store.OpenRaw(capture.HtmlPath)
		if err != nil {
			log.Printf("warning: failed to open raw html for capture %d: %v", id, err)
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}
		defer func() { _ = reader.Close() }()

		w.Header().Set("Content-Encoding", "zstd")
		w.Header().Set("Content-Length", strconv.FormatInt(int64(capture.HtmlCompressedSizeBytes), 10))
		w.WriteHeader(http.StatusOK)
		if _, err := io.Copy(w, reader); err != nil {
			log.Printf("warning: failed streaming raw html for capture %d: %v", id, err)
		}
		return
	}

	reader, err := s.Store.Open(capture.HtmlPath)
	if err != nil {
		log.Printf("warning: failed to open html for capture %d: %v", id, err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	defer func() { _ = reader.Close() }()

	w.Header().Set("Content-Length", strconv.FormatInt(int64(capture.HtmlUncompressedSizeBytes), 10))
	w.WriteHeader(http.StatusOK)
	if _, err := io.Copy(w, reader); err != nil {
		log.Printf("warning: failed streaming html for capture %d: %v", id, err)
	}
}

type patchCaptureLanguageRequest struct {
	Language string `json:"language"`
}

// PATCH /api/captures/{id}/language: manual language correction. An invalid
// text-search-config name surfaces as a real Postgres error from the UPDATE
// itself -- a regconfig cast performs a pg_ts_config catalog lookup -- so
// there's no need to pre-validate here. ListTextSearchConfigs is a separate
// concern (populating the dashboard's dropdown of valid values), not a
// prerequisite for this endpoint trusting Postgres's own validation.
func (s *Server) PatchCaptureLanguage(w http.ResponseWriter, r *http.Request) {
	user, ok := auth.UserFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid capture id")
		return
	}
	req, err := decodeJSON[patchCaptureLanguageRequest](r)
	if err != nil || req.Language == "" {
		writeError(w, http.StatusBadRequest, "language is required")
		return
	}

	capture, err := s.Queries.SetCaptureLanguage(r.Context(), db.SetCaptureLanguageParams{
		Language: req.Language, ID: id, UserID: user.ID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, "capture not found")
			return
		}
		writeError(w, http.StatusBadRequest, "invalid language")
		return
	}

	writeJSON(w, http.StatusOK, captureDetailResponseFromCapture(&capture))
}

// GET /api/text-search-configs: the valid values for
// PATCH /api/captures/{id}/language's dashboard dropdown -- this
// specific running Postgres instance's own pg_ts_config catalog, not a
// hardcoded list (which configs are actually available depends on the
// Postgres version). A plain query against the raw pool, not a
// sqlc-generated one -- same reasoning internal/ingest's own
// languageConfigExists already documents: sqlc's schema analysis only
// knows about tables defined in our own migrations, not Postgres's
// built-in system catalogs, so a query referencing pg_ts_config doesn't
// fit its normal model.
func (s *Server) ListTextSearchConfigs(w http.ResponseWriter, r *http.Request) {
	rows, err := s.Pool.Query(r.Context(), "SELECT cfgname FROM pg_ts_config ORDER BY cfgname")
	if err != nil {
		log.Printf("warning: failed to list text search configs: %v", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	defer rows.Close()

	configs := []string{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			log.Printf("warning: failed to scan text search config: %v", err)
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}
		configs = append(configs, name)
	}
	if err := rows.Err(); err != nil {
		log.Printf("warning: failed iterating text search configs: %v", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	writeJSON(w, http.StatusOK, map[string][]string{"languages": configs})
}

type tagResponse struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

// GET /api/tags: the user's full tag vocabulary, for the tags management
// screen and for populating an "add tag" autocomplete.
func (s *Server) ListTags(w http.ResponseWriter, r *http.Request) {
	user, ok := auth.UserFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	tags, err := s.Queries.ListTags(r.Context(), user.ID)
	if err != nil {
		log.Printf("warning: failed to list tags for user %d: %v", user.ID, err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	resp := make([]tagResponse, 0, len(tags))
	for _, t := range tags {
		resp = append(resp, tagResponse{ID: t.ID, Name: t.Name})
	}
	writeJSON(w, http.StatusOK, map[string][]tagResponse{"tags": resp})
}

type addPageTagRequest struct {
	Name string `json:"name"`
}

// POST /api/pages/{id}/tags: gets-or-creates a tag by name (UpsertTag),
// then links it to the page with source "manual" -- the same source
// value a person applying a tag through the dashboard should carry,
// distinguishing it from the AI enrichment job's own tags.
func (s *Server) AddPageTag(w http.ResponseWriter, r *http.Request) {
	user, ok := auth.UserFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	pageID, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid page id")
		return
	}
	req, err := decodeJSON[addPageTagRequest](r)
	if err != nil || req.Name == "" {
		writeError(w, http.StatusBadRequest, "name is required")
		return
	}
	ctx := r.Context()

	page, err := s.Queries.GetPageByIDForUser(ctx, db.GetPageByIDForUserParams{ID: pageID, UserID: user.ID})
	if err != nil {
		writeError(w, http.StatusNotFound, "page not found")
		return
	}

	tag, err := s.Queries.UpsertTag(ctx, db.UpsertTagParams{UserID: user.ID, Name: req.Name})
	if err != nil {
		log.Printf("warning: failed to upsert tag for user %d: %v", user.ID, err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	if err := s.Queries.AddPageTag(ctx, db.AddPageTagParams{PageID: page.ID, TagID: tag.ID, Source: "manual"}); err != nil {
		log.Printf("warning: failed to add tag %d to page %d: %v", tag.ID, page.ID, err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	writeJSON(w, http.StatusCreated, tagResponse{ID: tag.ID, Name: tag.Name})
}

// DELETE /api/pages/{id}/tags/{tagId}
func (s *Server) RemovePageTag(w http.ResponseWriter, r *http.Request) {
	user, ok := auth.UserFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	pageID, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid page id")
		return
	}
	tagID, err := strconv.ParseInt(chi.URLParam(r, "tagId"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid tag id")
		return
	}
	ctx := r.Context()

	page, err := s.Queries.GetPageByIDForUser(ctx, db.GetPageByIDForUserParams{ID: pageID, UserID: user.ID})
	if err != nil {
		writeError(w, http.StatusNotFound, "page not found")
		return
	}

	if err := s.Queries.RemovePageTag(ctx, db.RemovePageTagParams{PageID: page.ID, TagID: tagID}); err != nil {
		log.Printf("warning: failed to remove tag %d from page %d: %v", tagID, page.ID, err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// int8OrNil converts a pgtype.Int8 into the *int64 shape the dashboard's
// JSON responses use -- nil rather than 0 for a genuinely NULL
// parent_id (a top-level collection), same reasoning as textOrNil.
func int8OrNil(v pgtype.Int8) *int64 {
	if !v.Valid {
		return nil
	}
	return &v.Int64
}

type collectionResponse struct {
	ID        int64     `json:"id"`
	ParentID  *int64    `json:"parent_id"`
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"created_at"`
}

func collectionResponseFromCollection(c *db.Collection) collectionResponse {
	return collectionResponse{ID: c.ID, ParentID: int8OrNil(c.ParentID), Name: c.Name, CreatedAt: c.CreatedAt.Time}
}

// GET /api/collections: flat list; the dashboard reconstructs the tree
// client-side from (id, parent_id), same as ListCollectionsByUser's own
// doc comment already explains.
func (s *Server) ListCollections(w http.ResponseWriter, r *http.Request) {
	user, ok := auth.UserFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	rows, err := s.Queries.ListCollectionsByUser(r.Context(), user.ID)
	if err != nil {
		log.Printf("warning: failed to list collections for user %d: %v", user.ID, err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	resp := make([]collectionResponse, 0, len(rows))
	for _, c := range rows {
		resp = append(resp, collectionResponseFromCollection(&c))
	}
	writeJSON(w, http.StatusOK, map[string][]collectionResponse{"collections": resp})
}

type createCollectionRequest struct {
	Name     string `json:"name"`
	ParentID *int64 `json:"parent_id"`
}

// POST /api/collections. When parent_id is given, it's verified to
// belong to this user before use -- collections.parent_id's own FK has
// no user_id check, so without this a request could nest a new
// collection under another user's collection id. A duplicate name under
// the same parent (top-level or not) collides with one of the schema's
// two partial unique indexes and surfaces here as a 409.
func (s *Server) CreateCollection(w http.ResponseWriter, r *http.Request) {
	user, ok := auth.UserFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	req, err := decodeJSON[createCollectionRequest](r)
	if err != nil || req.Name == "" {
		writeError(w, http.StatusBadRequest, "name is required")
		return
	}
	ctx := r.Context()

	var parentID pgtype.Int8
	if req.ParentID != nil {
		if _, err := s.Queries.GetCollectionByID(ctx, db.GetCollectionByIDParams{ID: *req.ParentID, UserID: user.ID}); err != nil {
			writeError(w, http.StatusBadRequest, "parent collection not found")
			return
		}
		parentID = pgtype.Int8{Int64: *req.ParentID, Valid: true}
	}

	collection, err := s.Queries.CreateCollection(ctx, db.CreateCollectionParams{
		UserID: user.ID, ParentID: parentID, Name: req.Name,
	})
	if err != nil {
		writeError(w, http.StatusConflict, "a collection with that name already exists here")
		return
	}

	writeJSON(w, http.StatusCreated, collectionResponseFromCollection(&collection))
}

type renameCollectionRequest struct {
	Name string `json:"name"`
}

// PATCH /api/collections/{id}
func (s *Server) RenameCollection(w http.ResponseWriter, r *http.Request) {
	user, ok := auth.UserFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid collection id")
		return
	}
	req, err := decodeJSON[renameCollectionRequest](r)
	if err != nil || req.Name == "" {
		writeError(w, http.StatusBadRequest, "name is required")
		return
	}

	collection, err := s.Queries.RenameCollection(r.Context(), db.RenameCollectionParams{Name: req.Name, ID: id, UserID: user.ID})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, "collection not found")
			return
		}
		writeError(w, http.StatusConflict, "a collection with that name already exists here")
		return
	}

	writeJSON(w, http.StatusOK, collectionResponseFromCollection(&collection))
}

// DELETE /api/collections/{id}: cascades to child collections and
// page_collections rows via the schema's own ON DELETE CASCADE chain.
func (s *Server) DeleteCollection(w http.ResponseWriter, r *http.Request) {
	user, ok := auth.UserFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid collection id")
		return
	}

	rowsAffected, err := s.Queries.DeleteCollection(r.Context(), db.DeleteCollectionParams{ID: id, UserID: user.ID})
	if err != nil {
		log.Printf("warning: failed to delete collection %d: %v", id, err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	if rowsAffected == 0 {
		writeError(w, http.StatusNotFound, "collection not found")
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// GET /api/collections/{id}/pages
func (s *Server) ListCollectionPages(w http.ResponseWriter, r *http.Request) {
	user, ok := auth.UserFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid collection id")
		return
	}
	ctx := r.Context()

	if _, err := s.Queries.GetCollectionByID(ctx, db.GetCollectionByIDParams{ID: id, UserID: user.ID}); err != nil {
		writeError(w, http.StatusNotFound, "collection not found")
		return
	}

	pages, err := s.Queries.ListCollectionPages(ctx, id)
	if err != nil {
		log.Printf("warning: failed to list pages for collection %d: %v", id, err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	resp := make([]pageResponse, 0, len(pages))
	for _, p := range pages {
		resp = append(resp, pageResponseFromPage(&p))
	}
	writeJSON(w, http.StatusOK, map[string][]pageResponse{"pages": resp})
}

type addPageToCollectionRequest struct {
	CollectionID int64 `json:"collection_id"`
}

// POST /api/pages/{id}/collections
func (s *Server) AddPageToCollection(w http.ResponseWriter, r *http.Request) {
	user, ok := auth.UserFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	pageID, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid page id")
		return
	}
	req, err := decodeJSON[addPageToCollectionRequest](r)
	if err != nil || req.CollectionID == 0 {
		writeError(w, http.StatusBadRequest, "collection_id is required")
		return
	}
	ctx := r.Context()

	page, err := s.Queries.GetPageByIDForUser(ctx, db.GetPageByIDForUserParams{ID: pageID, UserID: user.ID})
	if err != nil {
		writeError(w, http.StatusNotFound, "page not found")
		return
	}
	if _, err := s.Queries.GetCollectionByID(ctx, db.GetCollectionByIDParams{ID: req.CollectionID, UserID: user.ID}); err != nil {
		writeError(w, http.StatusNotFound, "collection not found")
		return
	}

	if err := s.Queries.AddPageToCollection(ctx, db.AddPageToCollectionParams{PageID: page.ID, CollectionID: req.CollectionID}); err != nil {
		log.Printf("warning: failed to add page %d to collection %d: %v", page.ID, req.CollectionID, err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// DELETE /api/pages/{id}/collections/{collectionId}
func (s *Server) RemovePageFromCollection(w http.ResponseWriter, r *http.Request) {
	user, ok := auth.UserFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	pageID, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid page id")
		return
	}
	collectionID, err := strconv.ParseInt(chi.URLParam(r, "collectionId"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid collection id")
		return
	}
	ctx := r.Context()

	page, err := s.Queries.GetPageByIDForUser(ctx, db.GetPageByIDForUserParams{ID: pageID, UserID: user.ID})
	if err != nil {
		writeError(w, http.StatusNotFound, "page not found")
		return
	}

	if err := s.Queries.RemovePageFromCollection(ctx, db.RemovePageFromCollectionParams{PageID: page.ID, CollectionID: collectionID}); err != nil {
		log.Printf("warning: failed to remove page %d from collection %d: %v", page.ID, collectionID, err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) startSession(w http.ResponseWriter, r *http.Request, user *db.User) {
	raw, hash, err := auth.GenerateSessionToken()
	if err != nil {
		log.Printf("failed to generate session token: %v", err)
		return
	}
	_, err = s.Queries.CreateSession(r.Context(), db.CreateSessionParams{
		SessionHash: hash,
		UserID:      user.ID,
		ExpiresAt:   pgtype.Timestamptz{Time: auth.SessionExpiry(), Valid: true},
	})
	if err != nil {
		log.Printf("failed to create session: %v", err)
		return
	}
	auth.SetSessionCookie(w, raw, s.CookieSecure)
}

func decodeJSON[T any](r *http.Request) (T, error) {
	var v T
	err := json.NewDecoder(r.Body).Decode(&v)
	return v, err
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}
