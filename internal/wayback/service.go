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

package wayback

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/mfinelli/recueil/internal/db"
)

// PendingTimeout is how long a submission may stay pending before it is given
// up on. archive.org only keeps a job's status for about an hour, so past that
// there is no way left to learn how it went.
const PendingTimeout = time.Hour

// ErrInProgress is returned by Submit when a submission for the same URL is
// already in flight, so a second click doesn't make a second capture.
var ErrInProgress = errors.New("wayback: a submission for this URL is already in progress")

// API is the part of Client a Service needs, so tests can stand in for
// archive.org.
type API interface {
	Save(ctx context.Context, pageURL string) (string, error)
	Status(ctx context.Context, jobID string) (Job, error)
}

// Service records Wayback submissions in Postgres and resolves them.
//
// There is no job queue and no retry: a submission is one request to archive.org
// plus a row holding its job id, and a failed one is retried by the user pressing
// the button again. What does run in the background is Resolve, called by the
// agent's sweep (SweepPending) for every submission still pending.
type Service struct {
	queries *db.Queries
	api     API
	now     func() time.Time
	logger  *slog.Logger
}

// NewService returns a Service. now and logger default to time.Now and
// slog.Default() when nil.
func NewService(queries *db.Queries, api API, now func() time.Time, logger *slog.Logger) *Service {
	if now == nil {
		now = time.Now
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &Service{queries: queries, api: api, now: now, logger: logger}
}

// Submit asks archive.org to capture normalizedURL for userID and records the
// attempt as pending. It refuses (ErrInProgress) while an earlier attempt for the
// same URL is still genuinely in flight. If archive.org refuses the request (a
// rate limit, a URL it won't capture) nothing is recorded and the error, an
// *APIError when it was archive.org's own, comes back for the caller to show.
func (s *Service) Submit(ctx context.Context, userID int64, normalizedURL string) (db.WaybackSubmission, error) {
	existing, err := s.queries.GetWaybackSubmission(ctx, db.GetWaybackSubmissionParams{UserID: userID, NormalizedUrl: normalizedURL})
	switch {
	case err == nil:
		if existing.Status == "pending" && s.now().Sub(existing.SubmittedAt.Time) < PendingTimeout {
			return existing, ErrInProgress
		}
	case !errors.Is(err, pgx.ErrNoRows):
		return db.WaybackSubmission{}, fmt.Errorf("wayback: looking up the previous submission: %w", err)
	}

	jobID, err := s.api.Save(ctx, normalizedURL)
	if err != nil {
		return db.WaybackSubmission{}, err
	}

	row, err := s.queries.StartWaybackSubmission(ctx, db.StartWaybackSubmissionParams{
		UserID: userID, NormalizedUrl: normalizedURL, JobID: textOf(jobID),
	})
	if err != nil {
		return db.WaybackSubmission{}, fmt.Errorf("wayback: recording the submission: %w", err)
	}
	return row, nil
}

// Resolve brings a submission up to date: for one that's pending it asks
// archive.org how the job went and records the answer. Anything not pending comes
// back unchanged. A job still pending is left that way until PendingTimeout has
// passed, after which it is failed rather than waited on forever; one whose status
// can't be fetched right now (archive.org unreachable) is also left pending and the
// error returned, to be tried again later, under the same time limit.
func (s *Service) Resolve(ctx context.Context, sub db.WaybackSubmission) (db.WaybackSubmission, error) {
	if sub.Status != "pending" {
		return sub, nil
	}
	expired := s.now().Sub(sub.SubmittedAt.Time) >= PendingTimeout

	job, err := s.api.Status(ctx, sub.JobID.String)
	if err != nil {
		if expired {
			return s.fail(ctx, sub, "archive.org did not report a result within an hour")
		}
		return sub, err
	}

	switch job.State {
	case StateSuccess:
		return s.settle(ctx, sub, func() (db.WaybackSubmission, error) {
			return s.queries.SucceedWaybackSubmission(ctx, db.SucceedWaybackSubmissionParams{ID: sub.ID, JobID: sub.JobID})
		})
	case StateError:
		return s.fail(ctx, sub, job.Message)
	default:
		if expired {
			return s.fail(ctx, sub, "archive.org did not report a result within an hour")
		}
		return sub, nil
	}
}

func (s *Service) fail(ctx context.Context, sub db.WaybackSubmission, message string) (db.WaybackSubmission, error) {
	return s.settle(ctx, sub, func() (db.WaybackSubmission, error) {
		return s.queries.FailWaybackSubmission(ctx, db.FailWaybackSubmissionParams{
			ID: sub.ID, JobID: sub.JobID, Error: textOf(message),
		})
	})
}

// settle runs one of the guarded updates. They only apply to a row that is still
// pending with the same job id, so when a newer attempt has replaced this one
// since it was read (or a second sweep got there first) nothing is updated; the
// current row is returned instead, which is just what the caller wanted to know.
func (s *Service) settle(ctx context.Context, sub db.WaybackSubmission, update func() (db.WaybackSubmission, error)) (db.WaybackSubmission, error) {
	row, err := update()
	if err == nil {
		return row, nil
	}
	if errors.Is(err, pgx.ErrNoRows) {
		current, getErr := s.queries.GetWaybackSubmission(ctx, db.GetWaybackSubmissionParams{
			UserID: sub.UserID, NormalizedUrl: sub.NormalizedUrl,
		})
		if getErr != nil {
			return db.WaybackSubmission{}, fmt.Errorf("wayback: reloading the submission: %w", getErr)
		}
		return current, nil
	}
	return db.WaybackSubmission{}, fmt.Errorf("wayback: recording the outcome: %w", err)
}

// SweepPending resolves every submission still pending: what the agent runs each
// local cycle, so the outcome of a submission is recorded even if the user closed
// the tab. A submission that can't be resolved right now (archive.org
// unreachable) is logged and left for the next sweep; only a database failure is
// returned, since archive.org having a bad moment is not the agent failing.
func (s *Service) SweepPending(ctx context.Context) error {
	pending, err := s.queries.ListPendingWaybackSubmissions(ctx)
	if err != nil {
		return fmt.Errorf("wayback: listing pending submissions: %w", err)
	}
	for _, sub := range pending {
		if _, err := s.Resolve(ctx, sub); err != nil {
			var dbErr interface{ SQLState() string }
			if errors.As(err, &dbErr) {
				return err
			}
			s.logger.WarnContext(ctx, "wayback: couldn't check a pending submission, will try again",
				"submission_id", sub.ID, "error", err)
		}
	}
	return nil
}

func textOf(s string) pgtype.Text {
	return pgtype.Text{String: s, Valid: true}
}
