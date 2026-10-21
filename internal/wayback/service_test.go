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

package wayback_test

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mfinelli/recueil/internal/db"
	"github.com/mfinelli/recueil/internal/dbtest"
	"github.com/mfinelli/recueil/internal/wayback"
)

// fakeAPI stands in for archive.org: Save hands out sequential job ids unless
// told to fail, and Status answers from a per-job table.
type fakeAPI struct {
	saved      []string
	nextJob    int
	saveErr    error
	jobs       map[string]wayback.Job
	statusErr  map[string]error
	statusSeen []string
}

func newFakeAPI() *fakeAPI {
	return &fakeAPI{jobs: map[string]wayback.Job{}, statusErr: map[string]error{}}
}

func (f *fakeAPI) Save(_ context.Context, pageURL string) (string, error) {
	if f.saveErr != nil {
		return "", f.saveErr
	}
	f.saved = append(f.saved, pageURL)
	f.nextJob++
	return "job-" + string(rune('0'+f.nextJob)), nil
}

func (f *fakeAPI) Status(_ context.Context, jobID string) (wayback.Job, error) {
	f.statusSeen = append(f.statusSeen, jobID)
	if err := f.statusErr[jobID]; err != nil {
		return wayback.Job{}, err
	}
	if job, ok := f.jobs[jobID]; ok {
		return job, nil
	}
	return wayback.Job{State: wayback.StatePending}, nil
}

type svcEnv struct {
	ctx  context.Context
	pool *pgxpool.Pool
	q    *db.Queries
	user db.User
	api  *fakeAPI
	svc  *wayback.Service
	now  time.Time
	logs *bytes.Buffer
}

func newSvcEnv(t *testing.T) *svcEnv {
	t.Helper()
	pool := dbtest.Setup(t)
	dbtest.Reset(t, pool)
	e := &svcEnv{
		ctx: context.Background(), pool: pool, q: db.New(pool), user: dbtest.CreateUser(t, pool, "member"),
		api: newFakeAPI(), now: time.Now().UTC().Truncate(time.Second), logs: &bytes.Buffer{},
	}
	e.svc = wayback.NewService(e.q, e.api, func() time.Time { return e.now }, slog.New(slog.NewTextHandler(e.logs, nil)))
	return e
}

func (e *svcEnv) get(t *testing.T, url string) db.WaybackSubmission {
	t.Helper()
	sub, err := e.q.GetWaybackSubmission(e.ctx, db.GetWaybackSubmissionParams{UserID: e.user.ID, NormalizedUrl: url})
	require.NoError(t, err)
	return sub
}

// backdate makes a submission look older than it is, by rewriting its
// submitted_at, so the timeout rules can be exercised without waiting.
func (e *svcEnv) backdate(t *testing.T, sub *db.WaybackSubmission, by time.Duration) db.WaybackSubmission {
	t.Helper()
	_, err := e.pool.Exec(e.ctx, `UPDATE wayback_submissions SET submitted_at = $2 WHERE id = $1`,
		sub.ID, pgtype.Timestamptz{Time: e.now.Add(-by), Valid: true})
	require.NoError(t, err)
	return e.get(t, sub.NormalizedUrl)
}

const testURL = "https://example.com/page"

func TestService_Submit(t *testing.T) {
	t.Run("asks archive.org to capture the URL and records a pending attempt with its job id", func(t *testing.T) {
		e := newSvcEnv(t)

		sub, err := e.svc.Submit(e.ctx, e.user.ID, testURL)
		require.NoError(t, err)

		assert.Equal(t, []string{testURL}, e.api.saved)
		assert.Equal(t, "pending", sub.Status)
		assert.Equal(t, "job-1", sub.JobID.String)
		assert.Equal(t, e.user.ID, sub.UserID)
		assert.False(t, sub.LastSubmittedAt.Valid, "nothing has succeeded yet")
		assert.False(t, sub.Error.Valid)
	})

	t.Run("refuses a second submission while one is in flight, without calling archive.org", func(t *testing.T) {
		e := newSvcEnv(t)
		first, err := e.svc.Submit(e.ctx, e.user.ID, testURL)
		require.NoError(t, err)

		again, err := e.svc.Submit(e.ctx, e.user.ID, testURL)

		assert.ErrorIs(t, err, wayback.ErrInProgress)
		assert.Equal(t, first.ID, again.ID, "returns the one in flight")
		assert.Len(t, e.api.saved, 1)
	})

	t.Run("a pending attempt that has outlived archive.org's memory of it doesn't block a new one", func(t *testing.T) {
		e := newSvcEnv(t)
		first, err := e.svc.Submit(e.ctx, e.user.ID, testURL)
		require.NoError(t, err)
		e.backdate(t, &first, wayback.PendingTimeout+time.Minute)

		second, err := e.svc.Submit(e.ctx, e.user.ID, testURL)

		require.NoError(t, err)
		assert.Equal(t, first.ID, second.ID, "the same row, replaced")
		assert.Equal(t, "job-2", second.JobID.String)
		assert.Len(t, e.api.saved, 2)
	})

	t.Run("a failed or succeeded attempt can be submitted again, keeping the last success", func(t *testing.T) {
		e := newSvcEnv(t)
		first, err := e.svc.Submit(e.ctx, e.user.ID, testURL)
		require.NoError(t, err)
		e.api.jobs["job-1"] = wayback.Job{State: wayback.StateSuccess}
		done, err := e.svc.Resolve(e.ctx, &first)
		require.NoError(t, err)
		require.Equal(t, "succeeded", done.Status)

		e.now = e.now.Add(2 * time.Hour)
		again, err := e.svc.Submit(e.ctx, e.user.ID, testURL)

		require.NoError(t, err)
		assert.Equal(t, "pending", again.Status)
		assert.Equal(t, done.LastSubmittedAt, again.LastSubmittedAt, "the earlier success is still the last one")
	})

	t.Run("records nothing when archive.org refuses the request, and returns its error", func(t *testing.T) {
		e := newSvcEnv(t)
		e.api.saveErr = &wayback.APIError{StatusCode: 429, Message: "slow down"}

		_, err := e.svc.Submit(e.ctx, e.user.ID, testURL)

		var apiErr *wayback.APIError
		require.ErrorAs(t, err, &apiErr)
		assert.Equal(t, "slow down", apiErr.Message)
		_, getErr := e.q.GetWaybackSubmission(e.ctx, db.GetWaybackSubmissionParams{UserID: e.user.ID, NormalizedUrl: testURL})
		assert.Error(t, getErr, "no row")
	})

	t.Run("is per user and per URL", func(t *testing.T) {
		e := newSvcEnv(t)
		other := dbtest.CreateUser(t, e.pool, "member")
		_, err := e.svc.Submit(e.ctx, e.user.ID, testURL)
		require.NoError(t, err)

		_, err = e.svc.Submit(e.ctx, other.ID, testURL)
		require.NoError(t, err)
		_, err = e.svc.Submit(e.ctx, e.user.ID, "https://example.com/other")
		require.NoError(t, err)

		assert.Len(t, e.api.saved, 3)
	})
}

func TestService_Resolve(t *testing.T) {
	t.Run("a successful job succeeds the attempt, making its submission time the last success", func(t *testing.T) {
		e := newSvcEnv(t)
		sub, err := e.svc.Submit(e.ctx, e.user.ID, testURL)
		require.NoError(t, err)
		e.api.jobs["job-1"] = wayback.Job{State: wayback.StateSuccess, Timestamp: e.now}

		got, err := e.svc.Resolve(e.ctx, &sub)

		require.NoError(t, err)
		assert.Equal(t, "succeeded", got.Status)
		assert.True(t, got.LastSubmittedAt.Valid)
		assert.True(t, got.LastSubmittedAt.Time.Equal(sub.SubmittedAt.Time), "the attempt's own submitted_at")
		assert.False(t, got.Error.Valid)
	})

	t.Run("a failed job fails the attempt with archive.org's message, leaving an earlier success", func(t *testing.T) {
		e := newSvcEnv(t)
		first, err := e.svc.Submit(e.ctx, e.user.ID, testURL)
		require.NoError(t, err)
		e.api.jobs["job-1"] = wayback.Job{State: wayback.StateSuccess}
		done, err := e.svc.Resolve(e.ctx, &first)
		require.NoError(t, err)

		e.now = e.now.Add(2 * time.Hour)
		second, err := e.svc.Submit(e.ctx, e.user.ID, testURL)
		require.NoError(t, err)
		e.api.jobs["job-2"] = wayback.Job{State: wayback.StateError, Message: "Couldn't resolve host."}
		failed, err := e.svc.Resolve(e.ctx, &second)

		require.NoError(t, err)
		assert.Equal(t, "failed", failed.Status)
		assert.Equal(t, "Couldn't resolve host.", failed.Error.String)
		assert.Equal(t, done.LastSubmittedAt, failed.LastSubmittedAt, "the last success is untouched by a later failure")
	})

	t.Run("a job still pending is left pending", func(t *testing.T) {
		e := newSvcEnv(t)
		sub, err := e.svc.Submit(e.ctx, e.user.ID, testURL)
		require.NoError(t, err)

		got, err := e.svc.Resolve(e.ctx, &sub)

		require.NoError(t, err)
		assert.Equal(t, "pending", got.Status)
	})

	t.Run("a job still pending past the time limit is given up on", func(t *testing.T) {
		e := newSvcEnv(t)
		sub, err := e.svc.Submit(e.ctx, e.user.ID, testURL)
		require.NoError(t, err)
		sub = e.backdate(t, &sub, wayback.PendingTimeout+time.Second)

		got, err := e.svc.Resolve(e.ctx, &sub)

		require.NoError(t, err)
		assert.Equal(t, "failed", got.Status)
		assert.Contains(t, got.Error.String, "did not report a result")
	})

	t.Run("a status that can't be fetched leaves it pending and returns the error, until the time limit", func(t *testing.T) {
		e := newSvcEnv(t)
		sub, err := e.svc.Submit(e.ctx, e.user.ID, testURL)
		require.NoError(t, err)
		e.api.statusErr["job-1"] = errors.New("connection refused")

		got, err := e.svc.Resolve(e.ctx, &sub)
		assert.ErrorContains(t, err, "connection refused")
		assert.Equal(t, "pending", got.Status)

		old := e.backdate(t, &sub, wayback.PendingTimeout+time.Second)
		got, err = e.svc.Resolve(e.ctx, &old)
		require.NoError(t, err)
		assert.Equal(t, "failed", got.Status, "no more waiting once archive.org can't remember the job anyway")
	})

	t.Run("something already resolved comes back unchanged without asking archive.org", func(t *testing.T) {
		e := newSvcEnv(t)
		sub, err := e.svc.Submit(e.ctx, e.user.ID, testURL)
		require.NoError(t, err)
		e.api.jobs["job-1"] = wayback.Job{State: wayback.StateSuccess}
		done, err := e.svc.Resolve(e.ctx, &sub)
		require.NoError(t, err)
		e.api.statusSeen = nil

		again, err := e.svc.Resolve(e.ctx, &done)

		require.NoError(t, err)
		assert.Equal(t, done.Status, again.Status)
		assert.Empty(t, e.api.statusSeen)
	})

	t.Run("two resolvers at once: the second finds it already settled and changes nothing", func(t *testing.T) {
		e := newSvcEnv(t)
		sub, err := e.svc.Submit(e.ctx, e.user.ID, testURL)
		require.NoError(t, err)
		e.api.jobs["job-1"] = wayback.Job{State: wayback.StateSuccess}

		stale := sub // what a second resolver read before the first finished
		first, err := e.svc.Resolve(e.ctx, &sub)
		require.NoError(t, err)
		second, err := e.svc.Resolve(e.ctx, &stale)

		require.NoError(t, err)
		assert.Equal(t, "succeeded", second.Status)
		assert.Equal(t, first.LastSubmittedAt, second.LastSubmittedAt)
	})

	t.Run("a result for an attempt since replaced by a newer one is not applied to the newer one", func(t *testing.T) {
		e := newSvcEnv(t)
		old, err := e.svc.Submit(e.ctx, e.user.ID, testURL)
		require.NoError(t, err)
		e.backdate(t, &old, wayback.PendingTimeout+time.Minute)
		newer, err := e.svc.Submit(e.ctx, e.user.ID, testURL)
		require.NoError(t, err)
		require.NotEqual(t, old.JobID, newer.JobID)

		// A resolver that read the OLD attempt and learns its job succeeded.
		e.api.jobs[old.JobID.String] = wayback.Job{State: wayback.StateSuccess}
		got, err := e.svc.Resolve(e.ctx, &old)

		require.NoError(t, err)
		assert.Equal(t, "pending", got.Status, "the newer attempt is untouched")
		assert.Equal(t, newer.JobID.String, got.JobID.String)
	})
}

func TestService_SweepPending(t *testing.T) {
	t.Run("resolves every pending submission, whoever is looking", func(t *testing.T) {
		e := newSvcEnv(t)
		a, err := e.svc.Submit(e.ctx, e.user.ID, "https://example.com/a")
		require.NoError(t, err)
		b, err := e.svc.Submit(e.ctx, e.user.ID, "https://example.com/b")
		require.NoError(t, err)
		c, err := e.svc.Submit(e.ctx, e.user.ID, "https://example.com/c")
		require.NoError(t, err)
		e.api.jobs[a.JobID.String] = wayback.Job{State: wayback.StateSuccess}
		e.api.jobs[b.JobID.String] = wayback.Job{State: wayback.StateError, Message: "no"}
		// c stays pending.

		require.NoError(t, e.svc.SweepPending(e.ctx))

		assert.Equal(t, "succeeded", e.get(t, a.NormalizedUrl).Status)
		assert.Equal(t, "failed", e.get(t, b.NormalizedUrl).Status)
		assert.Equal(t, "pending", e.get(t, c.NormalizedUrl).Status)
	})

	t.Run("a submission that can't be checked is logged and skipped, and the rest are still resolved", func(t *testing.T) {
		e := newSvcEnv(t)
		a, err := e.svc.Submit(e.ctx, e.user.ID, "https://example.com/a")
		require.NoError(t, err)
		b, err := e.svc.Submit(e.ctx, e.user.ID, "https://example.com/b")
		require.NoError(t, err)
		e.api.statusErr[a.JobID.String] = errors.New("archive.org is down")
		e.api.jobs[b.JobID.String] = wayback.Job{State: wayback.StateSuccess}

		require.NoError(t, e.svc.SweepPending(e.ctx), "archive.org having a bad moment isn't the agent failing")

		assert.Equal(t, "pending", e.get(t, a.NormalizedUrl).Status)
		assert.Equal(t, "succeeded", e.get(t, b.NormalizedUrl).Status)
		assert.Contains(t, e.logs.String(), "archive.org is down")
	})

	t.Run("does nothing when nothing is pending, and doesn't revisit the resolved", func(t *testing.T) {
		e := newSvcEnv(t)
		sub, err := e.svc.Submit(e.ctx, e.user.ID, testURL)
		require.NoError(t, err)
		e.api.jobs[sub.JobID.String] = wayback.Job{State: wayback.StateSuccess}
		require.NoError(t, e.svc.SweepPending(e.ctx))
		e.api.statusSeen = nil

		require.NoError(t, e.svc.SweepPending(e.ctx))

		assert.Empty(t, e.api.statusSeen)
	})

	t.Run("gives up on one past the time limit even with archive.org unreachable", func(t *testing.T) {
		e := newSvcEnv(t)
		sub, err := e.svc.Submit(e.ctx, e.user.ID, testURL)
		require.NoError(t, err)
		e.backdate(t, &sub, wayback.PendingTimeout+time.Minute)
		e.api.statusErr[sub.JobID.String] = errors.New("unreachable")

		require.NoError(t, e.svc.SweepPending(e.ctx))

		assert.Equal(t, "failed", e.get(t, testURL).Status)
	})
}
