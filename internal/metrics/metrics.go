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
// gauges
package metrics

import (
	"context"
	"log"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"

	"github.com/mfinelli/recueil/internal/db"
)

// collector queries the database fresh on every scrape rather than
// maintaining its own cached/periodically-updated state. Simple, and
// correct by construction (no separate "when did we last update this"
// staleness to reason about). At typical scrape intervals (15-60s) against
// a handful of cheap COUNT(*)-style queries, the added DB load is
// negligible; if a much heavier aggregate ever landed here, that calculus
// would be worth revisiting.
type collector struct {
	queries   *db.Queries
	usersDesc *prometheus.Desc
}

func newCollector(queries *db.Queries) prometheus.Collector {
	return &collector{
		queries: queries,
		usersDesc: prometheus.NewDesc(
			"recueil_users_total",
			"Current number of user accounts.",
			nil, nil,
		),
	}
}

func (c *collector) Describe(ch chan<- *prometheus.Desc) {
	ch <- c.usersDesc
}

// Collect can't return an error (it's not part of the interface), so a
// failed query is logged and skipped for this scrape rather than panicking
// or blocking the registry's other collectors.
func (c *collector) Collect(ch chan<- prometheus.Metric) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	count, err := c.queries.CountUsers(ctx)
	if err != nil {
		log.Printf("metrics: failed to count users: %v", err)
		return
	}
	ch <- prometheus.MustNewConstMetric(c.usersDesc, prometheus.GaugeValue, float64(count))
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
