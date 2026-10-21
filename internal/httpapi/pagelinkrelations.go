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
	"log"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"

	"github.com/mfinelli/recueil/internal/auth"
	"github.com/mfinelli/recueil/internal/db"
)

// Relating a page to a link, from either end: the cross-kind counterpart of
// the page-to-page and link-to-link relations, over one table,
// page_link_relations. Both ends have their own routes, named by what they
// relate:
//
//	POST   /api/pages/{id}/related-links            {"link_id": n}
//	DELETE /api/pages/{id}/related-links/{linkId}
//	POST   /api/links/{id}/related-pages            {"page_id": n}
//	DELETE /api/links/{id}/related-pages/{pageId}
//
// The two ends write the same row, and a relation made from either is visible from
// both (a page's detail lists related_links and a link's lists related_pages). A page
// and a link with the same URL may be related: the same-URL hint already ties those,
// and relating them is harmless, so nothing filters them out. Only routed with links
// enabled, since there are no links to relate otherwise.

func parsePageID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid page id")
		return 0, false
	}
	return id, true
}

// pageForUser loads a page the requester owns, answering 404 (not 403) otherwise so
// whether another user's page exists isn't revealed; the counterpart of linkForUser.
func (s *Server) pageForUser(w http.ResponseWriter, r *http.Request, id, userID int64) (db.Page, bool) {
	page, err := s.Queries.GetPageByIDForUser(r.Context(), db.GetPageByIDForUserParams{ID: id, UserID: userID})
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "page not found")
		return db.Page{}, false
	}
	if err != nil {
		log.Printf("warning: failed to load page %d: %v", id, err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return db.Page{}, false
	}
	return page, true
}

type addPageRelatedLinkRequest struct {
	LinkID int64 `json:"link_id"`
}

type addLinkRelatedPageRequest struct {
	PageID int64 `json:"page_id"`
}

// POST /api/pages/{id}/related-links: relates the page to one of the requester's
// links, both checked independently. Relating a pair that's already related is a
// no-op that answers the same way.
func (s *Server) AddPageRelatedLink(w http.ResponseWriter, r *http.Request) {
	user, ok := auth.UserFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	pageID, ok := parsePageID(w, r)
	if !ok {
		return
	}
	req, err := decodeJSON[addPageRelatedLinkRequest](r)
	if err != nil || req.LinkID == 0 {
		writeError(w, http.StatusBadRequest, "link_id is required")
		return
	}
	page, ok := s.pageForUser(w, r, pageID, user.ID)
	if !ok {
		return
	}
	link, ok := s.linkForUser(w, r, req.LinkID, user.ID)
	if !ok {
		return
	}

	if err := s.Queries.AddPageLinkRelation(r.Context(), db.AddPageLinkRelationParams{PageID: page.ID, LinkID: link.ID}); err != nil {
		log.Printf("warning: failed to relate page %d to link %d: %v", page.ID, link.ID, err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	writeJSON(w, http.StatusCreated, relatedLinkFromLink(&link))
}

// DELETE /api/pages/{id}/related-links/{relatedLinkId}: only the {id} side is verified
// as the requester's, as RemovePageRelation does: the delete is scoped to a pair that
// involves an already-verified page, so it matches nothing if the link isn't related
// to it (or isn't the requester's).
func (s *Server) RemovePageRelatedLink(w http.ResponseWriter, r *http.Request) {
	user, ok := auth.UserFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	pageID, ok := parsePageID(w, r)
	if !ok {
		return
	}
	linkID, err := strconv.ParseInt(chi.URLParam(r, "relatedLinkId"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid related link id")
		return
	}
	page, ok := s.pageForUser(w, r, pageID, user.ID)
	if !ok {
		return
	}

	if err := s.Queries.RemovePageLinkRelation(r.Context(), db.RemovePageLinkRelationParams{PageID: page.ID, LinkID: linkID}); err != nil {
		log.Printf("warning: failed to remove the relation between page %d and link %d: %v", page.ID, linkID, err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// POST /api/links/{id}/related-pages: the other end of AddPageRelatedLink, writing
// the same row.
func (s *Server) AddLinkRelatedPage(w http.ResponseWriter, r *http.Request) {
	user, ok := auth.UserFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	linkID, ok := parseLinkID(w, r)
	if !ok {
		return
	}
	req, err := decodeJSON[addLinkRelatedPageRequest](r)
	if err != nil || req.PageID == 0 {
		writeError(w, http.StatusBadRequest, "page_id is required")
		return
	}
	link, ok := s.linkForUser(w, r, linkID, user.ID)
	if !ok {
		return
	}
	page, ok := s.pageForUser(w, r, req.PageID, user.ID)
	if !ok {
		return
	}

	if err := s.Queries.AddPageLinkRelation(r.Context(), db.AddPageLinkRelationParams{PageID: page.ID, LinkID: link.ID}); err != nil {
		log.Printf("warning: failed to relate link %d to page %d: %v", link.ID, page.ID, err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	writeJSON(w, http.StatusCreated, relatedPageFromPage(&page))
}

// DELETE /api/links/{id}/related-pages/{relatedPageId}: the other end of
// RemovePageRelatedLink, scoped the same way.
func (s *Server) RemoveLinkRelatedPage(w http.ResponseWriter, r *http.Request) {
	user, ok := auth.UserFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	linkID, ok := parseLinkID(w, r)
	if !ok {
		return
	}
	pageID, err := strconv.ParseInt(chi.URLParam(r, "relatedPageId"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid related page id")
		return
	}
	link, ok := s.linkForUser(w, r, linkID, user.ID)
	if !ok {
		return
	}

	if err := s.Queries.RemovePageLinkRelation(r.Context(), db.RemovePageLinkRelationParams{PageID: pageID, LinkID: link.ID}); err != nil {
		log.Printf("warning: failed to remove the relation between link %d and page %d: %v", link.ID, pageID, err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func relatedLinkFromPageRelationRow(l *db.ListPageRelatedLinksRow) relatedLinkResponse {
	return relatedLinkResponse{ID: l.ID, Title: textOrNil(l.Title), NormalizedURL: l.NormalizedUrl, FaviconPath: textOrNil(l.FaviconPath)}
}

func relatedPageFromLinkRelationRow(p *db.ListLinkRelatedPagesRow) relatedPageResponse {
	return relatedPageResponse{ID: p.ID, Title: textOrNil(p.Title), NormalizedURL: p.NormalizedUrl, FaviconPath: textOrNil(p.FaviconPath)}
}
