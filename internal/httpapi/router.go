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

// Package httpapi is the dashboard-facing HTTP API: registration, login,
// logout, the bootstrap-token-gated first-admin setup, and a
// session-protected /api/auth/me. Routed via chi, with
// auth.RequireSession/RequireAdmin used as ordinary chi middleware (no
// httpapi-specific auth plumbing of its own).
//
// This package holds request validation and wiring only; the actual work
// happens in internal/auth (passwords, sessions, the bootstrap holder),
// internal/db (Postgres), and internal/mirror (pushing the credential
// mirror to the Worker). The device-facing / Worker-facing API surface
// (queue, presigned R2 URLs, /internal/tokens)
// isn't part of this package.
package httpapi

import (
	"context"
	"fmt"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.finelli.dev/healthchecks/v2"

	"github.com/mfinelli/recueil/internal/auth"
	"github.com/mfinelli/recueil/internal/db"
	"github.com/mfinelli/recueil/internal/metrics"
)

type BuildInfo struct {
	Version   string
	GitSHA    string
	BuildDate string
}

func NewRouter(s *Server, pool *pgxpool.Pool, q *db.Queries, build BuildInfo) (http.Handler, error) {
	r := chi.NewRouter()

	hc := healthcheck.Config{
		Version:   build.Version,
		GitSha:    build.GitSHA,
		BuildDate: build.BuildDate,
		Check: func(ctx context.Context) error {
			return pool.Ping(ctx)
		},
	}

	r.Get("/info", http.HandlerFunc(hc.Info()))
	r.Get("/ping", http.HandlerFunc(hc.Ping()))
	r.Get("/health", http.HandlerFunc(hc.Health()))

	r.Post("/api/setup", s.Setup)
	r.Post("/api/auth/register", s.Register)
	r.Post("/api/auth/login", s.Login)
	r.Post("/api/auth/logout", s.Logout)

	r.Group(func(r chi.Router) {
		r.Use(auth.RequireSession(q))
		r.Get("/api/auth/me", s.Me)
	})

	registry, err := metrics.NewRegistry(q)
	if err != nil {
		return nil, fmt.Errorf("building metrics registry: %w", err)
	}
	r.Handle("/metrics", promhttp.HandlerFor(registry, promhttp.HandlerOpts{}))

	return r, nil
}
