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
	"encoding/base64"
	"errors"
	"log"
	"math"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/mfinelli/recueil/internal/auth"
	"github.com/mfinelli/recueil/internal/db"
	"github.com/mfinelli/recueil/internal/ingest"
)

// Adding a link by hand: the dashboard's Add link form, the counterpart of
// POST /api/manual-upload for pages. Recueil never visits a page itself, so
// a link added this way has only what the person typed: a URL, and optionally
// a title and a favicon file.

// maxManualLinkFaviconBytes is the largest favicon, in raw bytes, that Add link
// accepts. The route sits in the /api group, whose own 1 MiB request limit
// applies, and a favicon travels in the JSON body as base64 (a third larger), so
// this leaves comfortable room for the rest of the body; it is also several
// times the size of any real favicon, and of the 128 KB the extension sends
// inline. (Unlike manual upload, which takes a whole archived page and so needs
// its own multipart route and a far larger limit, a favicon is small enough
// that this is an ordinary JSON route.)
const maxManualLinkFaviconBytes = 512 << 10

// maxManualLinkTitleLength is the same defensive ceiling on a title that the
// Worker applies to one saved from the extension (a count of characters
// there): nothing specifies a title length and real ones run to a few hundred
// characters, so a longer one is cut rather than refused.
const maxManualLinkTitleLength = 4096

type createLinkRequest struct {
	URL         string  `json:"url"`
	Title       *string `json:"title"`
	FaviconExt  *string `json:"favicon_ext"`
	FaviconData *string `json:"favicon_data"`
}

// POST /api/links: adds a link by hand. Body: url (required), and optionally
// title, and a favicon as favicon_ext ("svg", "png" or "ico") plus favicon_data
// (standard base64) -- the same two fields the extension's save sends.
//
// This is create-only: if the user already has a link with this URL (by
// normalized URL) it answers 409 with that link's id and changes nothing. An
// extension save refreshes an existing link, but there a missing title or
// favicon means the site has none; here it means "I didn't give one", and a
// refresh would silently wipe the existing link's. A page with the same URL is
// no conflict; that's just the same-URL hint.
//
// The title is stored as given (trimmed; empty means none, shown as the URL),
// and nothing is fetched: no title lookup, no favicon lookup. The favicon is
// stored in the link store exactly as the extension's is, and, as with manual
// upload, a favicon that can't be written is logged and the link is saved
// without it. The new link's source is "manual".
func (s *Server) CreateLink(w http.ResponseWriter, r *http.Request) {
	user, ok := auth.UserFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	ctx := r.Context()

	req, err := decodeJSON[createLinkRequest](r)
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeError(w, http.StatusRequestEntityTooLarge, "request too large: the favicon may be at most 512 KB")
			return
		}
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	rawURL := strings.TrimSpace(req.URL)
	if rawURL == "" {
		writeError(w, http.StatusBadRequest, "url is required")
		return
	}
	normalizedURL, err := s.Pipeline.Normalize(ctx, rawURL)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid url")
		return
	}

	var title pgtype.Text
	if req.Title != nil {
		if trimmed := strings.TrimSpace(*req.Title); trimmed != "" {
			if runes := []rune(trimmed); len(runes) > maxManualLinkTitleLength {
				trimmed = string(runes[:maxManualLinkTitleLength])
			}
			title = pgtype.Text{String: trimmed, Valid: true}
		}
	}

	faviconData, faviconExt, status, msg := decodeManualLinkFavicon(req)
	if status != 0 {
		writeError(w, status, msg)
		return
	}

	// Checked before anything is written, so the common case (adding a URL
	// that's already a link) leaves no orphaned favicon behind; the insert
	// below still guards the race of two adds at once.
	existing, err := s.Queries.GetLinkByNormalizedURL(ctx, db.GetLinkByNormalizedURLParams{
		UserID: user.ID, NormalizedUrl: normalizedURL,
	})
	switch {
	case err == nil:
		writeLinkExists(w, existing.ID)
		return
	case !errors.Is(err, pgx.ErrNoRows):
		log.Printf("warning: failed to look up an existing link for user %d: %v", user.ID, err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	params := db.CreateLinkIfAbsentParams{
		UserID:        user.ID,
		RawUrl:        rawURL,
		NormalizedUrl: normalizedURL,
		Title:         title,
		Source:        ingest.LinkSourceManual,
	}
	if faviconData != nil && s.LinkStore != nil {
		favicon, err := s.LinkStore.Write(faviconData, faviconExt)
		if err != nil {
			log.Printf("warning: failed to write the favicon for a link added by hand (user %d), continuing without one: %v", user.ID, err)
		} else {
			params.FaviconPath = pgtype.Text{String: favicon.Path, Valid: true}
			params.FaviconSizeBytes = pgtype.Int4{Int32: favicon.SizeBytes, Valid: true}
			params.FaviconHash = pgtype.Text{String: favicon.Hash, Valid: true}
		}
	}

	link, err := s.Queries.CreateLinkIfAbsent(ctx, params)
	if errors.Is(err, pgx.ErrNoRows) {
		// Another add for the same URL won the race; its link is the one that
		// exists. Any favicon just written is orphaned, for gc.
		raced, lookupErr := s.Queries.GetLinkByNormalizedURL(ctx, db.GetLinkByNormalizedURLParams{
			UserID: user.ID, NormalizedUrl: normalizedURL,
		})
		if lookupErr != nil {
			log.Printf("warning: failed to look up the link that won a race for user %d: %v", user.ID, lookupErr)
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}
		writeLinkExists(w, raced.ID)
		return
	}
	if err != nil {
		log.Printf("warning: failed to create a link for user %d: %v", user.ID, err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	writeJSON(w, http.StatusCreated, linkResponseFromLink(&link, nil))
}

// writeLinkExists is the 409 for adding a URL that's already a link. It carries
// the existing link's id so the form can point at it, which writeError's
// message-only shape can't.
func writeLinkExists(w http.ResponseWriter, linkID int64) {
	writeJSON(w, http.StatusConflict, map[string]any{
		"error":   "this URL is already saved as a link",
		"link_id": linkID,
	})
}

// decodeManualLinkFavicon validates and decodes the favicon fields of a
// createLinkRequest. It returns (nil, "", 0, "") for no favicon, the decoded
// bytes and extension when there is one, or a non-zero HTTP status and message
// when the fields are wrong.
func decodeManualLinkFavicon(req createLinkRequest) (data []byte, ext string, status int, msg string) {
	hasExt := req.FaviconExt != nil && *req.FaviconExt != ""
	hasData := req.FaviconData != nil && *req.FaviconData != ""
	if !hasExt && !hasData {
		return nil, "", 0, ""
	}
	if hasExt != hasData {
		return nil, "", http.StatusBadRequest, "favicon_ext and favicon_data go together"
	}

	ext = strings.ToLower(*req.FaviconExt)
	if !manualUploadFaviconExtensions[ext] {
		return nil, "", http.StatusBadRequest, "favicon must be .svg, .png, or .ico"
	}
	// Cheap length test first, so an oversized payload is never decoded.
	if len(*req.FaviconData) > base64.StdEncoding.EncodedLen(maxManualLinkFaviconBytes) {
		return nil, "", http.StatusRequestEntityTooLarge, "favicon is too large (at most 512 KB)"
	}
	data, err := base64.StdEncoding.DecodeString(*req.FaviconData)
	if err != nil {
		return nil, "", http.StatusBadRequest, "favicon is not valid base64"
	}
	if len(data) == 0 {
		return nil, "", http.StatusBadRequest, "favicon file is empty"
	}
	if len(data) > maxManualLinkFaviconBytes || len(data) > math.MaxInt32 {
		return nil, "", http.StatusRequestEntityTooLarge, "favicon is too large (at most 512 KB)"
	}
	return data, ext, 0, ""
}
