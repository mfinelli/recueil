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

	"github.com/mfinelli/recueil/internal/auth"
	"github.com/mfinelli/recueil/internal/db"
)

// The dashboard's API for links -- bookmark-only entries. Only
// routed when links are enabled (Server.EnableLinks, see router.go), so with
// the feature off every route here simply 404s.
//
// Not to be confused with the page-to-page relationship routes
// (/pages/{id}/links, AddPageLink and friends), which relate two archived
// pages to each other and are unrelated to this. This file's routes are all
// under /api/links.

// linkResponse is a link as the dashboard sees it. MatchingPageID is the
// same-URL hint: the id of the user's archived page with this link's
// normalized URL, if there is one. Always null on a response that came from a
// write rather than a read of the full row's context.
type linkResponse struct {
	ID            int64   `json:"id"`
	RawURL        string  `json:"raw_url"`
	NormalizedURL string  `json:"normalized_url"`
	Title         *string `json:"title"`
	FaviconPath   *string `json:"favicon_path"`
	Notes         *string `json:"notes"`
	// Source is where the link's current title and favicon came from:
	// "extension" or "manual" (ingest.LinkSource*), as a capture's source is.
	Source string `json:"source"`
	// ExcludedFromMirror opts the link out of the browser-bookmark sync.
	ExcludedFromMirror bool      `json:"excluded_from_mirror"`
	MatchingPageID     *int64    `json:"matching_page_id"`
	CreatedAt          time.Time `json:"created_at"`
	UpdatedAt          time.Time `json:"updated_at"`
}

func linkResponseFromLink(l *db.Link, matchingPageID *int64) linkResponse {
	return linkResponse{
		ID: l.ID, RawURL: l.RawUrl, NormalizedURL: l.NormalizedUrl,
		Title: textOrNil(l.Title), FaviconPath: textOrNil(l.FaviconPath), Notes: textOrNil(l.Notes), Source: l.Source, ExcludedFromMirror: l.ExcludedFromMirror,
		MatchingPageID: int8OrNil(pgtype.Int8{Int64: derefInt64(matchingPageID), Valid: matchingPageID != nil}),
		CreatedAt:      l.CreatedAt.Time, UpdatedAt: l.UpdatedAt.Time,
	}
}

func linkResponseFromListRow(l *db.ListLinksRow) linkResponse {
	return linkResponse{
		ID: l.ID, RawURL: l.RawUrl, NormalizedURL: l.NormalizedUrl,
		Title: textOrNil(l.Title), FaviconPath: textOrNil(l.FaviconPath), Notes: textOrNil(l.Notes), Source: l.Source, ExcludedFromMirror: l.ExcludedFromMirror,
		MatchingPageID: int8OrNil(l.MatchingPageID),
		CreatedAt:      l.CreatedAt.Time, UpdatedAt: l.UpdatedAt.Time,
	}
}

func linkResponseFromSearchRow(l *db.SearchLinksRow) linkResponse {
	return linkResponse{
		ID: l.ID, RawURL: l.RawUrl, NormalizedURL: l.NormalizedUrl,
		Title: textOrNil(l.Title), FaviconPath: textOrNil(l.FaviconPath), Notes: textOrNil(l.Notes), Source: l.Source, ExcludedFromMirror: l.ExcludedFromMirror,
		MatchingPageID: int8OrNil(l.MatchingPageID),
		CreatedAt:      l.CreatedAt.Time, UpdatedAt: l.UpdatedAt.Time,
	}
}

func derefInt64(p *int64) int64 {
	if p == nil {
		return 0
	}
	return *p
}

// linkListResponse is a page of links. Tag and Collection are present only when
// the list was filtered by them (GET /api/links?tag=&collection=), naming what it
// was filtered by so the dashboard can say so without another request.
type linkListResponse struct {
	Links      []linkResponse          `json:"links"`
	Total      int64                   `json:"total"`
	Tag        *tagResponse            `json:"tag,omitempty"`
	Collection *pageCollectionResponse `json:"collection,omitempty"`
}

// escapeLike escapes the three characters that mean something to LIKE/ILIKE
// (the escape character itself, %, and _) with a backslash, Postgres's default,
// so a search for "50%" or "a_b" matches literally rather than as a wildcard.
func escapeLike(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}

// GET /api/links: the user's links, newest saved first. ?q= narrows it to links
// whose title, URL or notes contain the text (case-insensitively, as a plain
// substring: a link has no body text to run full-text search over). ?limit= and
// ?offset= paginate exactly as GET /api/pages does (default 50, max 200), and
// the response's total is the full matching set regardless of the current page.
func (s *Server) ListLinks(w http.ResponseWriter, r *http.Request) {
	user, ok := auth.UserFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	limit, offset := parseLimitOffset(r)
	ctx := r.Context()

	resp := linkListResponse{Links: []linkResponse{}}

	// ?tag= (a slug) and ?collection= (an id) narrow the list; each must be the
	// caller's own, or it's a 404, as GET /api/tags/{slug}/pages and
	// /api/collections/{id}/pages are. Combined with ?q= and with each other they
	// all apply at once.
	var tagID, collectionID pgtype.Int8
	if slug := r.URL.Query().Get("tag"); slug != "" {
		tag, err := s.Queries.GetTagBySlug(ctx, db.GetTagBySlugParams{Slug: slug, UserID: user.ID})
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, "tag not found")
			return
		}
		if err != nil {
			log.Printf("warning: failed to look up tag %q for user %d: %v", slug, user.ID, err)
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}
		tagID = pgtype.Int8{Int64: tag.ID, Valid: true}
		resp.Tag = &tagResponse{ID: tag.ID, Name: tag.Name, Slug: tag.Slug}
	}
	if raw := r.URL.Query().Get("collection"); raw != "" {
		id, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid collection id")
			return
		}
		collection, err := s.Queries.GetCollectionByID(ctx, db.GetCollectionByIDParams{ID: id, UserID: user.ID})
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, "collection not found")
			return
		}
		if err != nil {
			log.Printf("warning: failed to look up collection %d for user %d: %v", id, user.ID, err)
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}
		collectionID = pgtype.Int8{Int64: collection.ID, Valid: true}
		resp.Collection = &pageCollectionResponse{
			ID: collection.ID, Name: collection.Name, ParentID: int8OrNil(collection.ParentID),
		}
	}

	if q := strings.TrimSpace(r.URL.Query().Get("q")); q != "" {
		rows, err := s.Queries.SearchLinks(ctx, db.SearchLinksParams{
			UserID:  user.ID,
			Pattern: pgtype.Text{String: "%" + escapeLike(q) + "%", Valid: true},
			TagID:   tagID, CollectionID: collectionID,
			Limit: limit, Offset: offset,
		})
		if err != nil {
			log.Printf("warning: failed to search links for user %d: %v", user.ID, err)
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}
		for i := range rows {
			resp.Total = rows[i].TotalCount
			resp.Links = append(resp.Links, linkResponseFromSearchRow(&rows[i]))
		}
		writeJSON(w, http.StatusOK, resp)
		return
	}

	rows, err := s.Queries.ListLinks(ctx, db.ListLinksParams{
		UserID: user.ID, TagID: tagID, CollectionID: collectionID, Limit: limit, Offset: offset,
	})
	if err != nil {
		log.Printf("warning: failed to list links for user %d: %v", user.ID, err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	for i := range rows {
		resp.Total = rows[i].TotalCount
		resp.Links = append(resp.Links, linkResponseFromListRow(&rows[i]))
	}
	writeJSON(w, http.StatusOK, resp)
}

// linkDetailResponse embeds linkResponse so its fields flatten into the same
// top-level JSON object as "tags" and "collections", as pageDetailResponse
// does. Tags carry no source: every link tag is a manual one.
//
// MatchingPageTitle is the matching page's title (null if there's no matching
// page, or the page has none), so the detail screen's same-URL callout can name
// it without fetching the page. A list row doesn't need it and doesn't carry it.
type linkDetailResponse struct {
	linkResponse
	MatchingPageTitle *string                  `json:"matching_page_title"`
	Tags              []tagResponse            `json:"tags"`
	Collections       []pageCollectionResponse `json:"collections"`
}

// parseLinkID reads {id} from the URL, writing the 400 itself on failure.
func parseLinkID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid link id")
		return 0, false
	}
	return id, true
}

// linkForUser loads link id for user, writing the 404 (or 500) itself and
// returning false when there isn't one to use.
func (s *Server) linkForUser(w http.ResponseWriter, r *http.Request, id, userID int64) (db.Link, bool) {
	link, err := s.Queries.GetLinkByIDForUser(r.Context(), db.GetLinkByIDForUserParams{ID: id, UserID: userID})
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "link not found")
		return db.Link{}, false
	}
	if err != nil {
		log.Printf("warning: failed to load link %d: %v", id, err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return db.Link{}, false
	}
	return link, true
}

// matchingPage is the same-URL hint for one link: the user's archived page with
// the same normalized URL (its id and title), nil if there isn't one.
func (s *Server) matchingPage(r *http.Request, userID int64, normalizedURL string) (*db.GetPageRefByNormalizedURLRow, error) {
	ref, err := s.Queries.GetPageRefByNormalizedURL(r.Context(), db.GetPageRefByNormalizedURLParams{
		UserID: userID, NormalizedUrl: normalizedURL,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &ref, nil
}

// GET /api/links/{id}: one link, with its tags, collections and same-URL hint.
func (s *Server) GetLink(w http.ResponseWriter, r *http.Request) {
	user, ok := auth.UserFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	id, ok := parseLinkID(w, r)
	if !ok {
		return
	}
	link, ok := s.linkForUser(w, r, id, user.ID)
	if !ok {
		return
	}
	ctx := r.Context()

	page, err := s.matchingPage(r, user.ID, link.NormalizedUrl)
	if err != nil {
		log.Printf("warning: failed to look up the page matching link %d: %v", link.ID, err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	tags, err := s.Queries.ListLinkTags(ctx, link.ID)
	if err != nil {
		log.Printf("warning: failed to list tags for link %d: %v", link.ID, err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	collections, err := s.Queries.ListLinkCollections(ctx, link.ID)
	if err != nil {
		log.Printf("warning: failed to list collections for link %d: %v", link.ID, err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	var pageID *int64
	var pageTitle *string
	if page != nil {
		pageID = &page.ID
		pageTitle = textOrNil(page.Title)
	}
	resp := linkDetailResponse{
		linkResponse:      linkResponseFromLink(&link, pageID),
		MatchingPageTitle: pageTitle,
		Tags:              []tagResponse{},
		Collections:       []pageCollectionResponse{},
	}
	for _, t := range tags {
		resp.Tags = append(resp.Tags, tagResponse{ID: t.TagID, Name: t.Name, Slug: t.Slug})
	}
	for _, c := range collections {
		resp.Collections = append(resp.Collections, pageCollectionResponse{
			ID: c.CollectionID, Name: c.Name, ParentID: int8OrNil(c.ParentID),
		})
	}
	writeJSON(w, http.StatusOK, resp)
}

type patchLinkRequest struct {
	ExcludedFromMirror *bool   `json:"excluded_from_mirror"`
	Title              *string `json:"title"`
	Notes              *string `json:"notes"`
}

// PATCH /api/links/{id}: sets the link's title, notes and/or whether it's
// excluded from the browser-bookmark sync, the link counterpart of
// PATCH /api/pages/{id}'s fields of the same names. At least one must be
// provided (pointer fields tell "not provided" from an explicit false or
// empty); if more than one is, each is applied as its own update and the
// response reflects whichever ran last. Excluding a link takes it out of the
// bookmark sync on its next cycle, even if it was already synced.
//
// A title is a plain overwrite, with no record that it was edited: the next
// save of the link replaces it with whatever the tab says, the same way
// a recapture replaces a page's, and that is deliberate. An empty title is
// refused, as for pages. Notes are the user's own and no save touches them; an
// empty or whitespace-only value clears them back to NULL rather than
// storing "".
func (s *Server) PatchLink(w http.ResponseWriter, r *http.Request) {
	user, ok := auth.UserFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	id, ok := parseLinkID(w, r)
	if !ok {
		return
	}
	req, err := decodeJSON[patchLinkRequest](r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.ExcludedFromMirror == nil && req.Title == nil && req.Notes == nil {
		writeError(w, http.StatusBadRequest, "excluded_from_mirror, title, or notes is required")
		return
	}
	var title string
	if req.Title != nil {
		title = strings.TrimSpace(*req.Title)
		if title == "" {
			writeError(w, http.StatusBadRequest, "title cannot be empty")
			return
		}
	}

	ctx := r.Context()
	var link db.Link

	if req.ExcludedFromMirror != nil {
		link, err = s.Queries.SetLinkExcludedFromMirror(ctx, db.SetLinkExcludedFromMirrorParams{
			ExcludedFromMirror: *req.ExcludedFromMirror, ID: id, UserID: user.ID,
		})
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, "link not found")
			return
		}
		if err != nil {
			log.Printf("warning: failed to set excluded_from_mirror on link %d: %v", id, err)
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}
	}

	if req.Title != nil {
		link, err = s.Queries.SetLinkTitle(ctx, db.SetLinkTitleParams{
			Title: pgtype.Text{String: title, Valid: true}, ID: id, UserID: user.ID,
		})
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, "link not found")
			return
		}
		if err != nil {
			log.Printf("warning: failed to set title on link %d: %v", id, err)
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}
	}

	if req.Notes != nil {
		notes := pgtype.Text{}
		if trimmed := strings.TrimSpace(*req.Notes); trimmed != "" {
			notes = pgtype.Text{String: trimmed, Valid: true}
		}
		link, err = s.Queries.SetLinkNotes(ctx, db.SetLinkNotesParams{Notes: notes, ID: id, UserID: user.ID})
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, "link not found")
			return
		}
		if err != nil {
			log.Printf("warning: failed to set notes on link %d: %v", id, err)
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}
	}

	writeJSON(w, http.StatusOK, linkResponseFromLink(&link, nil))
}

// DELETE /api/links/{id}: deletes the link. Its tag and collection memberships
// go with it (the schema cascades). A pure database operation, like deleting a
// page: the link's favicon file is deliberately left where it is for the next
// `recueil gc` pass to reclaim.
func (s *Server) DeleteLink(w http.ResponseWriter, r *http.Request) {
	user, ok := auth.UserFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	id, ok := parseLinkID(w, r)
	if !ok {
		return
	}

	rowsAffected, err := s.Queries.DeleteLink(r.Context(), db.DeleteLinkParams{ID: id, UserID: user.ID})
	if err != nil {
		log.Printf("warning: failed to delete link %d: %v", id, err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	if rowsAffected == 0 {
		writeError(w, http.StatusNotFound, "link not found")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// GET /api/links/{id}/favicon: serves the favicon from the link store. No
// Cache-Control, for the same reason as GET /api/pages/{id}/favicon: the URL is
// addressed by the link, not by the favicon's content, so a re-save that
// changes the favicon changes what it resolves to.
func (s *Server) GetLinkFavicon(w http.ResponseWriter, r *http.Request) {
	user, ok := auth.UserFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	id, ok := parseLinkID(w, r)
	if !ok {
		return
	}
	link, ok := s.linkForUser(w, r, id, user.ID)
	if !ok {
		return
	}
	if !link.FaviconPath.Valid || s.LinkStore == nil {
		writeError(w, http.StatusNotFound, "no favicon")
		return
	}

	reader, err := s.LinkStore.Archive().Open(link.FaviconPath.String)
	if err != nil {
		log.Printf("warning: failed to open favicon %q for link %d: %v", link.FaviconPath.String, link.ID, err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	defer func() { _ = reader.Close() }()

	w.Header().Set("Content-Type", contentTypeForAsset(link.FaviconPath.String))
	if _, err := io.Copy(w, reader); err != nil {
		log.Printf("warning: failed streaming favicon %q: %v", link.FaviconPath.String, err)
	}
}

// POST /api/links/{id}/tags: AddPageTag for a link -- gets-or-creates the tag by
// name (the same tags pages use) and adds it to the link, with the same slug
// rules and the same 400/409 answers. There is no source to record: every link
// tag is manual.
func (s *Server) AddLinkTag(w http.ResponseWriter, r *http.Request) {
	user, ok := auth.UserFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	id, ok := parseLinkID(w, r)
	if !ok {
		return
	}
	req, err := decodeJSON[addPageTagRequest](r)
	if err != nil || req.Name == "" {
		writeError(w, http.StatusBadRequest, "name is required")
		return
	}
	candidateSlug, ok := resolveSlug(req.Name, nil)
	if !ok {
		writeError(w, http.StatusBadRequest, "could not derive a URL-friendly slug from that tag name")
		return
	}
	link, ok := s.linkForUser(w, r, id, user.ID)
	if !ok {
		return
	}
	ctx := r.Context()

	tag, err := s.Queries.UpsertTag(ctx, db.UpsertTagParams{UserID: user.ID, Name: req.Name, Slug: candidateSlug})
	if err != nil {
		log.Printf("warning: failed to upsert tag for user %d: %v", user.ID, err)
		writeError(w, http.StatusConflict, "a different tag already uses that URL")
		return
	}
	if err := s.Queries.AddLinkTag(ctx, db.AddLinkTagParams{LinkID: link.ID, TagID: tag.ID}); err != nil {
		log.Printf("warning: failed to add tag %d to link %d: %v", tag.ID, link.ID, err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	writeJSON(w, http.StatusCreated, tagResponse{ID: tag.ID, Name: tag.Name, Slug: tag.Slug})
}

// DELETE /api/links/{id}/tags/{tagId}
func (s *Server) RemoveLinkTag(w http.ResponseWriter, r *http.Request) {
	user, ok := auth.UserFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	id, ok := parseLinkID(w, r)
	if !ok {
		return
	}
	tagID, err := strconv.ParseInt(chi.URLParam(r, "tagId"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid tag id")
		return
	}
	link, ok := s.linkForUser(w, r, id, user.ID)
	if !ok {
		return
	}
	if err := s.Queries.RemoveLinkTag(r.Context(), db.RemoveLinkTagParams{LinkID: link.ID, TagID: tagID}); err != nil {
		log.Printf("warning: failed to remove tag %d from link %d: %v", tagID, link.ID, err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// POST /api/links/{id}/collections: AddPageToCollection for a link. Both the
// link and the collection must belong to the requesting user, checked
// independently.
func (s *Server) AddLinkToCollection(w http.ResponseWriter, r *http.Request) {
	user, ok := auth.UserFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	id, ok := parseLinkID(w, r)
	if !ok {
		return
	}
	req, err := decodeJSON[addPageToCollectionRequest](r)
	if err != nil || req.CollectionID == 0 {
		writeError(w, http.StatusBadRequest, "collection_id is required")
		return
	}
	link, ok := s.linkForUser(w, r, id, user.ID)
	if !ok {
		return
	}
	ctx := r.Context()
	if _, err := s.Queries.GetCollectionByID(ctx, db.GetCollectionByIDParams{ID: req.CollectionID, UserID: user.ID}); err != nil {
		writeError(w, http.StatusNotFound, "collection not found")
		return
	}
	if err := s.Queries.AddLinkToCollection(ctx, db.AddLinkToCollectionParams{LinkID: link.ID, CollectionID: req.CollectionID}); err != nil {
		log.Printf("warning: failed to add link %d to collection %d: %v", link.ID, req.CollectionID, err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// DELETE /api/links/{id}/collections/{collectionId}
func (s *Server) RemoveLinkFromCollection(w http.ResponseWriter, r *http.Request) {
	user, ok := auth.UserFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	id, ok := parseLinkID(w, r)
	if !ok {
		return
	}
	collectionID, err := strconv.ParseInt(chi.URLParam(r, "collectionId"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid collection id")
		return
	}
	link, ok := s.linkForUser(w, r, id, user.ID)
	if !ok {
		return
	}
	if err := s.Queries.RemoveLinkFromCollection(r.Context(), db.RemoveLinkFromCollectionParams{LinkID: link.ID, CollectionID: collectionID}); err != nil {
		log.Printf("warning: failed to remove link %d from collection %d: %v", link.ID, collectionID, err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
