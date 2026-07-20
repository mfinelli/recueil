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

// Package metrics builds the Prometheus registry served at /metrics: the
// standard Go runtime and process collectors, plus Recueil-specific
// gauges -- user count, and background job counts/ages by type and status.
package metrics

import (
	"context"
	"log"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"

	"github.com/mfinelli/recueil/internal/db"
)

// jobTypes and jobStatuses enumerate every known (job, status)
// combination recueil_jobs_total can report. Emitting all of them
// explicitly every scrape -- including combinations with a real count of
// 0 -- rather than only the combinations CountJobsByStatus's query
// happens to return is deliberate: PromQL's rate()/sum() functions behave
// far more predictably against a time series that's continuously present
// at 0 than one that silently appears and disappears as data comes and
// goes.
var jobTypes = []string{"screenshot", "readability", "ai"}
var jobStatuses = []string{"pending", "processing", "done", "failed"}

// collector queries the database fresh on every scrape rather than
// maintaining its own cached/periodically-updated state. Simple, and
// correct by construction (no separate "when did we last update this"
// staleness to reason about). At typical scrape intervals (15-60s) against
// a handful of cheap COUNT(*)-style queries, the added DB load is
// negligible; if a much heavier aggregate ever landed here, that calculus
// would be worth revisiting.
type collector struct {
	queries                 *db.Queries
	usersDesc               *prometheus.Desc
	jobsDesc                *prometheus.Desc
	jobOldestPendingAgeDesc *prometheus.Desc
}

func newCollector(queries *db.Queries) prometheus.Collector {
	return &collector{
		queries: queries,
		usersDesc: prometheus.NewDesc(
			"recueil_users_total",
			"Current number of user accounts.",
			nil, nil,
		),
		jobsDesc: prometheus.NewDesc(
			"recueil_jobs_total",
			"Current number of background jobs, by job type and status.",
			[]string{"job", "status"}, nil,
		),
		jobOldestPendingAgeDesc: prometheus.NewDesc(
			"recueil_job_oldest_pending_age_seconds",
			"Age in seconds of the oldest still-pending job of this type. "+
				"Absent (not zero) for a job type with no pending jobs right now.",
			[]string{"job"}, nil,
		),
	}
}

func (c *collector) Describe(ch chan<- *prometheus.Desc) {
	ch <- c.usersDesc
	ch <- c.jobsDesc
	ch <- c.jobOldestPendingAgeDesc
}

// Collect can't return an error (it's not part of the interface), so each
// metric's own query failure is logged and skipped independently -- one
// failing metric doesn't block the others in this same Collect call, the
// same "one failed collector never fails the whole scrape" principle
// NewRegistry's own registry-level composition already relies on, just
// applied one level down now that there's more than one metric here.
func (c *collector) Collect(ch chan<- prometheus.Metric) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	c.collectUsers(ctx, ch)
	c.collectJobCounts(ctx, ch)
	c.collectOldestPendingAge(ctx, ch)
}

func (c *collector) collectUsers(ctx context.Context, ch chan<- prometheus.Metric) {
	count, err := c.queries.CountUsers(ctx)
	if err != nil {
		log.Printf("metrics: failed to count users: %v", err)
		return
	}
	ch <- prometheus.MustNewConstMetric(c.usersDesc, prometheus.GaugeValue, float64(count))
}

func (c *collector) collectJobCounts(ctx context.Context, ch chan<- prometheus.Metric) {
	rows, err := c.queries.CountJobsByStatus(ctx)
	if err != nil {
		log.Printf("metrics: failed to count jobs by status: %v", err)
		return
	}

	counts := make(map[[2]string]int64, len(rows))
	for _, row := range rows {
		counts[[2]string{row.Job, row.Status}] = row.Count
	}

	for _, job := range jobTypes {
		for _, status := range jobStatuses {
			ch <- prometheus.MustNewConstMetric(c.jobsDesc, prometheus.GaugeValue,
				float64(counts[[2]string{job, status}]), job, status)
		}
	}
}

// collectOldestPendingAge emits one gauge per job type that currently has
// at least one pending job -- not one per job type unconditionally:
// OldestPendingJobAgeSeconds's own query is built so a job type with zero
// pending jobs produces no row at all, so there's nothing to emit for it, and
// 0 would misleadingly claim a job's been pending for no time at all rather
// than there being none.
func (c *collector) collectOldestPendingAge(ctx context.Context, ch chan<- prometheus.Metric) {
	rows, err := c.queries.OldestPendingJobAgeSeconds(ctx)
	if err != nil {
		log.Printf("metrics: failed to compute oldest pending job age: %v", err)
		return
	}
	for _, row := range rows {
		ch <- prometheus.MustNewConstMetric(c.jobOldestPendingAgeDesc, prometheus.GaugeValue, row.AgeSeconds, row.Job)
	}
}

// NewRegistry builds a registry scoped to this call (deliberately not
// prometheus.DefaultRegisterer, which is global, mutable, package-level
// state shared across the whole process).
func NewRegistry(queries *db.Queries) (*prometheus.Registry, error) {
	reg := prometheus.NewRegistry()

	if err := reg.Register(collectors.NewGoCollector()); err != nil {
		return nil, err
	}
	if err := reg.Register(collectors.NewProcessCollector(collectors.ProcessCollectorOpts{})); err != nil {
		return nil, err
	}
	if err := reg.Register(newCollector(queries)); err != nil {
		return nil, err
	}

	return reg, nil
}
