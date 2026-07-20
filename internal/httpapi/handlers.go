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

// resolveTargetUserID decides whose devices a request is allowed to act
// on: a member can only ever target themselves; an admin may target any user
// via ?user_id=, defaulting to themselves when omitted (so the same admin
// account viewing their own devices doesn't need the query param at
// all). ok is false for a member explicitly requesting someone else's
// user_id (403) or a malformed user_id value (400) -- the caller
// distinguishes the two via badRequest.
func resolveTargetUserID(r *http.Request, user db.User) (target int64, ok bool, badRequest bool) {
	raw := r.URL.Query().Get("user_id")
	if raw == "" {
		return user.ID, true, false
	}
	requested, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return 0, false, true
	}
	if user.Role != "admin" && requested != user.ID {
		return 0, false, false
	}
	return requested, true, false
}

type deviceListResponse struct {
	Devices []devices.Token `json:"devices"`
}

// GET /api/devices: lists the target user's paired devices (see
// resolveTargetUserID for the member-vs-admin ?user_id= scoping).
func (s *Server) ListDevices(w http.ResponseWriter, r *http.Request) {
	user, ok := auth.UserFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	targetUserID, ok, badRequest := resolveTargetUserID(r, user)
	if badRequest {
		writeError(w, http.StatusBadRequest, "invalid user_id")
		return
	}
	if !ok {
		writeError(w, http.StatusForbidden, "cannot view another user's devices")
		return
	}

	tokens, err := s.Devices.ListTokens(r.Context(), targetUserID)
	if err != nil {
		log.Printf("warning: failed to list devices for user %d: %v", targetUserID, err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	writeJSON(w, http.StatusOK, deviceListResponse{Devices: tokens})
}

// DELETE /api/devices/{id}: revokes one device (see resolveTargetUserID
// for the member-vs-admin ?user_id= scoping). This is not a live push --
// the device keeps working until its next request to the Worker.
func (s *Server) RevokeDevice(w http.ResponseWriter, r *http.Request) {
	user, ok := auth.UserFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	targetUserID, ok, badRequest := resolveTargetUserID(r, user)
	if badRequest {
		writeError(w, http.StatusBadRequest, "invalid user_id")
		return
	}
	if !ok {
		writeError(w, http.StatusForbidden, "cannot revoke another user's device")
		return
	}

	tokenID, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid device id")
		return
	}

	if err := s.Devices.RevokeToken(r.Context(), targetUserID, tokenID); err != nil {
		if errors.Is(err, devices.ErrNotFound) {
			writeError(w, http.StatusNotFound, "device not found")
			return
		}
		log.Printf("warning: failed to revoke device %d for user %d: %v", tokenID, targetUserID, err)
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

func pageResponseFromPage(p db.Page) pageResponse {
	return pageResponse{
		ID: p.ID, NormalizedURL: p.NormalizedUrl, Title: textOrNil(p.Title),
		LatestCaptureAt: p.LatestCaptureAt.Time, ExcludedFromMirror: p.ExcludedFromMirror,
		FaviconPath: textOrNil(p.FaviconPath), CreatedAt: p.CreatedAt.Time, UpdatedAt: p.UpdatedAt.Time,
	}
}

func pageResponseFromListRow(p db.ListPagesRow) pageResponse {
	return pageResponse{
		ID: p.ID, NormalizedURL: p.NormalizedUrl, Title: textOrNil(p.Title),
		LatestCaptureAt: p.LatestCaptureAt.Time, ExcludedFromMirror: p.ExcludedFromMirror,
		FaviconPath: textOrNil(p.FaviconPath), CreatedAt: p.CreatedAt.Time, UpdatedAt: p.UpdatedAt.Time,
	}
}

func pageResponseFromSearchRow(p db.SearchPagesRow) pageResponse {
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
			resp.Pages = append(resp.Pages, pageResponseFromSearchRow(row))
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
		resp.Pages = append(resp.Pages, pageResponseFromListRow(row))
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

// pageDetailResponse embeds pageResponse so its fields flatten into the
// same top-level JSON object as "captures" -- the page detail view's
// natural shape is "the page, plus its history," not a nested "page"
// envelope key.
type pageDetailResponse struct {
	pageResponse
	Captures []captureSummaryResponse `json:"captures"`
}

// GET /api/pages/{id}: page detail plus full capture (version) history,
// most recent first. Deliberately a summary per capture, not the full
// row -- reader_text/ai_summary are large and belong to a future
// GET /api/captures/{id} detail endpoint, not this list.
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

	resp := pageDetailResponse{pageResponse: pageResponseFromPage(page), Captures: []captureSummaryResponse{}}
	for _, c := range captures {
		resp.Captures = append(resp.Captures, captureSummaryResponse{
			ID: c.ID, Source: c.Source, RawURL: c.RawUrl, Title: textOrNil(c.Title),
			ThumbnailPath: textOrNil(c.ThumbnailPath), Language: c.Language,
			HTMLCompressedSizeBytes: c.HtmlCompressedSizeBytes, HTMLUncompressedSizeBytes: c.HtmlUncompressedSizeBytes,
			CapturedAt: c.CapturedAt.Time,
		})
	}
	writeJSON(w, http.StatusOK, resp)
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

	writeJSON(w, http.StatusOK, pageResponseFromPage(page))
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

func captureDetailResponseFromCapture(c db.Capture) captureDetailResponse {
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

	writeJSON(w, http.StatusOK, captureDetailResponseFromCapture(capture))
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

	writeJSON(w, http.StatusOK, captureDetailResponseFromCapture(capture))
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
