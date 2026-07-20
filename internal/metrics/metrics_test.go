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

package metrics_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mfinelli/recueil/internal/db"
	"github.com/mfinelli/recueil/internal/dbtest"
	"github.com/mfinelli/recueil/internal/metrics"
)

func TestNewRegistry(t *testing.T) {
	pool := dbtest.Setup(t)
	dbtest.Reset(t, pool)
	q := db.New(pool)

	const wantUsers = 3
	for range wantUsers {
		dbtest.CreateUser(t, pool, "member") // registers its own cleanup
	}

	reg, err := metrics.NewRegistry(q)
	require.NoError(t, err)

	count, err := testutil.GatherAndCount(reg)
	require.NoError(t, err)
	t.Logf("registry exposes %d metrics total", count)

	assert.NoError(t, testutil.GatherAndCompare(reg, strings.NewReader(`
# HELP recueil_users_total Current number of user accounts.
# TYPE recueil_users_total gauge
recueil_users_total 3
`), "recueil_users_total"), "recueil_users_total should reflect the real row count")

	// Sanity check the standard collectors are actually wired in, not just
	// the custom one (GatherAndCompare above only inspected our metric).
	families, err := reg.Gather()
	require.NoError(t, err)
	var sawGoCollector, sawProcessCollector bool
	for _, f := range families {
		switch f.GetName() {
		case "go_goroutines":
			sawGoCollector = true
		case "process_start_time_seconds":
			sawProcessCollector = true
		}
	}
	assert.True(t, sawGoCollector, "expected a go_* metric from the Go collector")
	assert.True(t, sawProcessCollector, "expected a process_* metric from the process collector")
}

// TestNewRegistry_IndependentInstances is the whole point of NOT using
// prometheus.DefaultRegisterer (a global): two independently constructed
// registries must never collide with each other just because they were
// both built by this same function. If NewRegistry secretly reached for
// global state, the second call here would fail with "duplicate metrics
// collector registration attempted."
func TestNewRegistry_IndependentInstances(t *testing.T) {
	pool := dbtest.Setup(t)
	q := db.New(pool)

	_, err := metrics.NewRegistry(q)
	require.NoError(t, err)

	_, err = metrics.NewRegistry(q)
	require.NoError(t, err, "a second, independent registry must not collide with the first")
}

// TestNewRegistry_QueryFailureIsGraceful forces a real connection failure
// (closing the pool before scraping) rather than mocking one, consistent
// with how the rest of this project tests DB-touching code. Confirms
// Collect's documented behavior: the scrape still succeeds, every other
// metric is still present, only recueil_users_total is missing.
// newTestCapture inserts just enough of a page/capture row to hang job
// rows off of -- the metrics collector's own queries only care about
// screenshot_jobs/readability_jobs/ai_jobs rows existing, not anything
// about the capture's actual content, so this stays minimal rather than
// mirroring internal/ingest's full real capture flow.
func newTestCapture(t *testing.T, pool *pgxpool.Pool, q *db.Queries) int64 {
	t.Helper()
	ctx := context.Background()

	user := dbtest.CreateUser(t, pool, "member")
	page, err := q.UpsertPage(ctx, db.UpsertPageParams{
		UserID:          user.ID,
		NormalizedUrl:   "https://example.com/" + uuid.NewString(),
		Title:           pgtype.Text{String: "metrics test", Valid: true},
		LatestCaptureAt: pgtype.Timestamptz{Time: time.Now(), Valid: true},
	})
	require.NoError(t, err)

	inserted, err := q.InsertCaptureIdempotent(ctx, db.InsertCaptureIdempotentParams{
		PageID:                    page.ID,
		SourceCaptureID:           pgtype.Text{String: uuid.NewString(), Valid: true},
		Source:                    "extension",
		RawUrl:                    "https://example.com/" + uuid.NewString(),
		HtmlPath:                  "irrelevant/for/this/test.html.zst",
		HtmlCompressedSizeBytes:   1,
		HtmlUncompressedSizeBytes: 1,
		ContentHash:               uuid.NewString(),
		CapturedAt:                pgtype.Timestamptz{Time: time.Now(), Valid: true},
		Language:                  "english",
	})
	require.NoError(t, err)

	return inserted.ID
}

func TestNewRegistry_JobMetrics_AllCombinationsReportEvenAtZero(t *testing.T) {
	pool := dbtest.Setup(t)
	dbtest.Reset(t, pool)
	q := db.New(pool)

	reg, err := metrics.NewRegistry(q)
	require.NoError(t, err)

	// 3 job types x 4 statuses -- every combination should be present
	// and 0, none of them silently absent, confirming collectJobCounts
	// fills in every known combination rather than only what the query
	// happens to return.
	count, err := testutil.GatherAndCount(reg, "recueil_jobs_total")
	require.NoError(t, err)
	assert.Equal(t, 12, count)
}

func TestNewRegistry_JobMetrics_ReflectsRealCounts(t *testing.T) {
	pool := dbtest.Setup(t)
	dbtest.Reset(t, pool)
	q := db.New(pool)
	ctx := context.Background()

	doneCapture := newTestCapture(t, pool, q)
	require.NoError(t, q.CreateScreenshotJob(ctx, doneCapture))
	_, err := pool.Exec(ctx, "UPDATE screenshot_jobs SET status = 'done' WHERE capture_id = $1", doneCapture)
	require.NoError(t, err)

	failedCapture := newTestCapture(t, pool, q)
	require.NoError(t, q.CreateScreenshotJob(ctx, failedCapture))
	_, err = pool.Exec(ctx, "UPDATE screenshot_jobs SET status = 'failed' WHERE capture_id = $1", failedCapture)
	require.NoError(t, err)

	pendingCapture := newTestCapture(t, pool, q)
	require.NoError(t, q.CreateReadabilityJob(ctx, pendingCapture))

	reg, err := metrics.NewRegistry(q)
	require.NoError(t, err)

	assert.NoError(t, testutil.GatherAndCompare(reg, strings.NewReader(`
# HELP recueil_jobs_total Current number of background jobs, by job type and status.
# TYPE recueil_jobs_total gauge
recueil_jobs_total{job="ai",status="done"} 0
recueil_jobs_total{job="ai",status="failed"} 0
recueil_jobs_total{job="ai",status="pending"} 0
recueil_jobs_total{job="ai",status="processing"} 0
recueil_jobs_total{job="readability",status="done"} 0
recueil_jobs_total{job="readability",status="failed"} 0
recueil_jobs_total{job="readability",status="pending"} 1
recueil_jobs_total{job="readability",status="processing"} 0
recueil_jobs_total{job="screenshot",status="done"} 1
recueil_jobs_total{job="screenshot",status="failed"} 1
recueil_jobs_total{job="screenshot",status="pending"} 0
recueil_jobs_total{job="screenshot",status="processing"} 0
`), "recueil_jobs_total"))
}

func TestNewRegistry_JobOldestPendingAge(t *testing.T) {
	pool := dbtest.Setup(t)
	dbtest.Reset(t, pool)
	q := db.New(pool)
	ctx := context.Background()

	capture := newTestCapture(t, pool, q)
	require.NoError(t, q.CreateScreenshotJob(ctx, capture))
	// Backdate created_at so the resulting age is deterministically
	// checkable, rather than asserting against wall-clock time taken to
	// run this test.
	_, err := pool.Exec(ctx,
		"UPDATE screenshot_jobs SET created_at = NOW() - INTERVAL '90 seconds' WHERE capture_id = $1", capture)
	require.NoError(t, err)

	reg, err := metrics.NewRegistry(q)
	require.NoError(t, err)

	families, err := reg.Gather()
	require.NoError(t, err)

	var sawScreenshot, sawReadability, sawAI bool
	for _, f := range families {
		if f.GetName() != "recueil_job_oldest_pending_age_seconds" {
			continue
		}
		for _, m := range f.Metric {
			var job string
			for _, l := range m.Label {
				if l.GetName() == "job" {
					job = l.GetValue()
				}
			}
			switch job {
			case "screenshot":
				sawScreenshot = true
				assert.InDelta(t, 90, m.Gauge.GetValue(), 5,
					"expected roughly 90s of age for the one pending screenshot job")
			case "readability":
				sawReadability = true
			case "ai":
				sawAI = true
			}
		}
	}

	assert.True(t, sawScreenshot, "expected an age metric for the job type with a real pending row")
	assert.False(t, sawReadability, "job types with zero pending rows should be absent, not zero")
	assert.False(t, sawAI, "job types with zero pending rows should be absent, not zero")
}

func TestNewRegistry_QueryFailureIsGraceful(t *testing.T) {
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, "postgres://recueil:recueil@localhost:5432/recueil")
	require.NoError(t, err)

	q := db.New(pool)
	reg, err := metrics.NewRegistry(q)
	require.NoError(t, err)

	pool.Close() // force every subsequent query through q to fail

	families, err := reg.Gather()
	require.NoError(t, err, "a failed collector must not fail the whole scrape")

	var sawUsersMetric, sawJobsMetric, sawJobAgeMetric, sawGoCollector bool
	for _, f := range families {
		switch f.GetName() {
		case "recueil_users_total":
			sawUsersMetric = true
		case "recueil_jobs_total":
			sawJobsMetric = true
		case "recueil_job_oldest_pending_age_seconds":
			sawJobAgeMetric = true
		case "go_goroutines":
			sawGoCollector = true
		}
	}
	assert.False(t, sawUsersMetric, "recueil_users_total should be absent when the query fails, not zero or an error")
	assert.False(t, sawJobsMetric, "recueil_jobs_total should be absent when its query fails too")
	assert.False(t, sawJobAgeMetric, "recueil_job_oldest_pending_age_seconds should be absent when its query fails too")
	assert.True(t, sawGoCollector, "other collectors must still report even when one fails")

	// Double-close should also be safe.
	pool.Close()
}
