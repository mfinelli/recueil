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
	"log"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"github.com/mfinelli/recueil/internal/auth"
	"github.com/mfinelli/recueil/internal/db"
)

// Relating links to each other: the link counterpart of the page-to-page
// routes (AddPageRelation and friends), built the same way and for the same
// reasons, one relation per pair, visible from both ends. Only routed with links
// enabled.

// relatedLinkResponse is a link as it appears in another link's list of related
// links, and in the candidate search: just enough for a row (id, title, URL, favicon).
type relatedLinkResponse struct {
	ID            int64   `json:"id"`
	Title         *string `json:"title"`
	NormalizedURL string  `json:"normalized_url"`
	FaviconPath   *string `json:"favicon_path"`
}

func relatedLinkFromListRow(l *db.ListLinkRelationsRow) relatedLinkResponse {
	return relatedLinkResponse{ID: l.ID, Title: textOrNil(l.Title), NormalizedURL: l.NormalizedUrl, FaviconPath: textOrNil(l.FaviconPath)}
}

func relatedLinkFromLink(l *db.Link) relatedLinkResponse {
	return relatedLinkResponse{ID: l.ID, Title: textOrNil(l.Title), NormalizedURL: l.NormalizedUrl, FaviconPath: textOrNil(l.FaviconPath)}
}

type addLinkRelationRequest struct {
	LinkID int64 `json:"link_id"`
}

// POST /api/links/{id}/related-links: relates two links, both of which must belong to the
// requesting user (checked independently). AddLinkRelation canonicalizes which is stored
// as link_id_a, so the order doesn't matter, and relating a pair that's already related
// is a no-op that answers the same way.
func (s *Server) AddLinkRelation(w http.ResponseWriter, r *http.Request) {
	user, ok := auth.UserFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	id, ok := parseLinkID(w, r)
	if !ok {
		return
	}
	req, err := decodeJSON[addLinkRelationRequest](r)
	if err != nil || req.LinkID == 0 {
		writeError(w, http.StatusBadRequest, "link_id is required")
		return
	}
	if req.LinkID == id {
		writeError(w, http.StatusBadRequest, "a link cannot be related to itself")
		return
	}

	link, ok := s.linkForUser(w, r, id, user.ID)
	if !ok {
		return
	}
	related, ok := s.linkForUser(w, r, req.LinkID, user.ID)
	if !ok {
		return
	}

	if err := s.Queries.AddLinkRelation(r.Context(), db.AddLinkRelationParams{LinkA: link.ID, LinkB: related.ID, UserID: user.ID}); err != nil {
		log.Printf("warning: failed to relate link %d to link %d: %v", link.ID, related.ID, err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	writeJSON(w, http.StatusCreated, relatedLinkFromLink(&related))
}

// DELETE /api/links/{id}/related-links/{relatedLinkId}: only the {id} side is verified as the
// requester's, as RemovePageRelation does for pages: the delete is scoped to a pair
// involving an already-verified link, so it matches nothing if the other isn't
// actually related to it.
func (s *Server) RemoveLinkRelation(w http.ResponseWriter, r *http.Request) {
	user, ok := auth.UserFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	id, ok := parseLinkID(w, r)
	if !ok {
		return
	}
	relatedID, err := strconv.ParseInt(chi.URLParam(r, "relatedLinkId"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid related link id")
		return
	}
	link, ok := s.linkForUser(w, r, id, user.ID)
	if !ok {
		return
	}

	if err := s.Queries.RemoveLinkRelation(r.Context(), db.RemoveLinkRelationParams{LinkA: link.ID, LinkB: relatedID}); err != nil {
		log.Printf("warning: failed to remove the relation between link %d and link %d: %v", link.ID, relatedID, err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// GET /api/links/relation-candidates?q=...&exclude=...: title-or-URL search for the "relate
// this link to..." picker, excluding the link being related from. It doesn't also
// exclude links already related to it: the screen has that list and filters
// client-side, as for pages. Capped at relationCandidateLimit, as the page picker is.
func (s *Server) SearchLinkRelationCandidates(w http.ResponseWriter, r *http.Request) {
	user, ok := auth.UserFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	resp := struct {
		Links []relatedLinkResponse `json:"links"`
	}{Links: []relatedLinkResponse{}}

	query := r.URL.Query().Get("q")
	if query == "" {
		writeJSON(w, http.StatusOK, resp)
		return
	}
	var excludeLinkID int64
	if v := r.URL.Query().Get("exclude"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			excludeLinkID = n
		}
	}

	links, err := s.Queries.SearchLinkRelationCandidates(r.Context(), db.SearchLinkRelationCandidatesParams{
		UserID: user.ID, ExcludeLinkID: excludeLinkID, Query: query, Limit: relationCandidateLimit,
	})
	if err != nil {
		log.Printf("warning: failed to search link candidates for user %d: %v", user.ID, err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	for i := range links {
		resp.Links = append(resp.Links, relatedLinkFromLink(&links[i]))
	}
	writeJSON(w, http.StatusOK, resp)
}
