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
	"errors"
	"log"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"

	"github.com/mfinelli/recueil/internal/auth"
	"github.com/mfinelli/recueil/internal/db"
	"github.com/mfinelli/recueil/internal/wayback"
)

// Wayback Machine submission for the dashboard:
//
//   - A POST starts a submission: one quick call to archive.org, which returns a
//     job id and then does the capturing itself, and a pending row recording it.
//   - Reading a page or link includes how its URL stands. That is only ever a read
//     of the stored row: nothing here asks archive.org how a job went. That is the
//     agent's job, on its own schedule, so a result appears on the first load
//     after its next cycle, and viewing a screen never contacts archive.org.
//
// The state is keyed by (user, normalized URL), so a page and a link with the same
// URL share it, and one user never sees another's.

// waybackResponse is how a URL stands with the Wayback Machine. Status is
// "pending", "succeeded" or "failed". SubmittedAt is the latest attempt;
// LastSubmittedAt is the latest SUCCESSFUL one, which a later failed attempt
// leaves alone (null if none has succeeded); Error is the latest failure's
// message.
type waybackResponse struct {
	Status          string     `json:"status"`
	SubmittedAt     time.Time  `json:"submitted_at"`
	LastSubmittedAt *time.Time `json:"last_submitted_at"`
	Error           *string    `json:"error"`
}

// waybackResponseFrom shapes a stored submission. A row still pending past
// wayback.PendingTimeout is shown as failed, though the sweep may not have got to
// recording that yet (the agent being down, say): archive.org no longer remembers
// the job by then, so it can't succeed, and showing it as pending would leave the
// Submit button disabled for good.
func waybackResponseFrom(sub db.WaybackSubmission, now time.Time) waybackResponse {
	resp := waybackResponse{
		Status:      sub.Status,
		SubmittedAt: sub.SubmittedAt.Time,
		Error:       textOrNil(sub.Error),
	}
	if sub.LastSubmittedAt.Valid {
		t := sub.LastSubmittedAt.Time
		resp.LastSubmittedAt = &t
	}
	if sub.Status == "pending" && now.Sub(sub.SubmittedAt.Time) >= wayback.PendingTimeout {
		resp.Status = "failed"
		msg := wayback.TimeoutMessage
		resp.Error = &msg
	}
	return resp
}

// waybackFor reads the stored state for a URL; nil if it was never submitted. A
// plain database read, independent of whether submission is configured, so a
// record made before the keys were removed is still shown.
func (s *Server) waybackFor(ctx context.Context, userID int64, normalizedURL string) (*waybackResponse, error) {
	sub, err := s.Queries.GetWaybackSubmission(ctx, db.GetWaybackSubmissionParams{UserID: userID, NormalizedUrl: normalizedURL})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	resp := waybackResponseFrom(sub, time.Now())
	return &resp, nil
}

// submitWayback starts a submission for normalizedURL and writes the response.
func (s *Server) submitWayback(w http.ResponseWriter, r *http.Request, userID int64, normalizedURL string) {
	sub, err := s.Wayback.Submit(r.Context(), userID, normalizedURL)
	var apiErr *wayback.APIError
	switch {
	case errors.Is(err, wayback.ErrInProgress):
		writeError(w, http.StatusConflict, "a submission for this URL is already in progress")
		return
	case errors.As(err, &apiErr):
		// archive.org refused (a rate limit, a URL it won't capture): its own words
		// are the useful part, and nothing was recorded.
		writeError(w, http.StatusBadGateway, apiErr.Message)
		return
	case err != nil:
		log.Printf("warning: failed to submit %q to the Wayback Machine for user %d: %v", normalizedURL, userID, err)
		writeError(w, http.StatusBadGateway, "could not reach archive.org")
		return
	}
	writeJSON(w, http.StatusAccepted, waybackResponseFrom(sub, time.Now()))
}

// POST /api/links/{id}/wayback: submit a link's URL to the Wayback Machine.
// Routed only when links and Wayback submission are both on.
func (s *Server) SubmitLinkWayback(w http.ResponseWriter, r *http.Request) {
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
	s.submitWayback(w, r, user.ID, link.NormalizedUrl)
}

// POST /api/pages/{id}/wayback: submit a page's URL to the Wayback Machine.
// Routed only when Wayback submission is on.
func (s *Server) SubmitPageWayback(w http.ResponseWriter, r *http.Request) {
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
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "page not found")
		return
	}
	if err != nil {
		log.Printf("warning: failed to load page %d: %v", id, err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	s.submitWayback(w, r, user.ID, page.NormalizedUrl)
}
