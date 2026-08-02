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
// gauges -- user/page/capture counts, storage bytes by kind, background
// job counts/ages by type and status, and recueil agent's own last-
// success age by cycle. Everything here is Postgres-only by design: none
// of it ever calls the Cloudflare Worker, so scrape frequency has no
// effect on Worker request volume/free-tier budget.
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

// storageBytesKinds enumerates every label GetSystemStats' byte totals can
// report, the same explicit-enumeration reasoning as jobTypes/jobStatuses
// above -- iterated in collectStorageStats against a map built from
// GetSystemStatsRow's fields, so the two lists have to be kept in
// sync by hand; nothing here is generated from the struct.
var storageBytesKinds = []string{"html_compressed", "html_uncompressed", "favicon", "screenshot"}

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
	pagesDesc               *prometheus.Desc
	capturesDesc            *prometheus.Desc
	storageBytesDesc        *prometheus.Desc
	agentLastSuccessDesc    *prometheus.Desc
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
		pagesDesc: prometheus.NewDesc(
			"recueil_pages_total",
			"Current number of archived pages, system-wide.",
			nil, nil,
		),
		capturesDesc: prometheus.NewDesc(
			"recueil_captures_total",
			"Current number of captures (a page's version history), system-wide.",
			nil, nil,
		),
		storageBytesDesc: prometheus.NewDesc(
			"recueil_storage_bytes",
			"Bytes of local archive storage in use, by content kind.",
			[]string{"kind"}, nil,
		),
		agentLastSuccessDesc: prometheus.NewDesc(
			"recueil_agent_last_success_seconds",
			"Seconds since recueil agent last completed this cycle "+
				"successfully. Absent (not zero) if it never has.",
			[]string{"cycle"}, nil,
		),
	}
}

func (c *collector) Describe(ch chan<- *prometheus.Desc) {
	ch <- c.usersDesc
	ch <- c.jobsDesc
	ch <- c.jobOldestPendingAgeDesc
	ch <- c.pagesDesc
	ch <- c.capturesDesc
	ch <- c.storageBytesDesc
	ch <- c.agentLastSuccessDesc
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
	c.collectStorageStats(ctx, ch)
	c.collectAgentLastSuccess(ctx, ch)
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

// collectStorageStats reuses GetSystemStats -- the same query the
// dashboard's admin stats screen already runs -- rather than adding a
// metrics-specific one. Page/capture counts double as free ingestion-rate
// visibility (rate()/increase() over either gauge), which is the closer
// signal to "is the queue actually draining" than a raw queue-depth count
// would be anyway, and getting it doesn't require ever asking the Worker.
func (c *collector) collectStorageStats(ctx context.Context, ch chan<- prometheus.Metric) {
	stats, err := c.queries.GetSystemStats(ctx)
	if err != nil {
		log.Printf("metrics: failed to compute storage stats: %v", err)
		return
	}

	ch <- prometheus.MustNewConstMetric(c.pagesDesc, prometheus.GaugeValue, float64(stats.PageCount))
	ch <- prometheus.MustNewConstMetric(c.capturesDesc, prometheus.GaugeValue, float64(stats.CaptureCount))

	// One MustNewConstMetric call per kind, driven by storageBytesKinds
	// so the emitted set matches jobsDesc's always-emit-every-combination
	// behavior above -- see that comment for why. bytesByKind's four
	// entries are kept in sync with storageBytesKinds and
	// GetSystemStatsRow's fields by hand; nothing enforces it structurally.
	bytesByKind := map[string]int64{
		"html_compressed":   stats.HtmlCompressedBytes,
		"html_uncompressed": stats.HtmlUncompressedBytes,
		"favicon":           stats.FaviconBytes,
		"screenshot":        stats.ScreenshotBytes,
	}
	for _, kind := range storageBytesKinds {
		ch <- prometheus.MustNewConstMetric(c.storageBytesDesc, prometheus.GaugeValue,
			float64(bytesByKind[kind]), kind)
	}
}

// collectAgentLastSuccess emits one gauge per cycle that has ever recorded
// a heartbeat -- not one per known cycle unconditionally, the same
// absent-not-zero reasoning as collectOldestPendingAge: a cycle that's
// never succeeded (a freshly-migrated instance, or an agent that's never
// managed to complete one) has no row in agent_heartbeats at all, and 0
// would misleadingly claim it succeeded moments ago.
func (c *collector) collectAgentLastSuccess(ctx context.Context, ch chan<- prometheus.Metric) {
	rows, err := c.queries.AgentHeartbeatAges(ctx)
	if err != nil {
		log.Printf("metrics: failed to compute agent heartbeat ages: %v", err)
		return
	}
	for _, row := range rows {
		ch <- prometheus.MustNewConstMetric(c.agentLastSuccessDesc, prometheus.GaugeValue, row.AgeSeconds, row.Cycle)
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
