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

	var sawUsersMetric, sawGoCollector bool
	for _, f := range families {
		if f.GetName() == "recueil_users_total" {
			sawUsersMetric = true
		}
		if f.GetName() == "go_goroutines" {
			sawGoCollector = true
		}
	}
	assert.False(t, sawUsersMetric, "recueil_users_total should be absent when the query fails, not zero or an error")
	assert.True(t, sawGoCollector, "other collectors must still report even when one fails")

	// Double-close should also be safe.
	pool.Close()
}
