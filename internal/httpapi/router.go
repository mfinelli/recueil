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
// logout, the bootstrap-token-gated first-admin setup, a session-protected
// /api/auth/me, session-protected pairing-token management
// (view/regenerate/revoke), session-protected Manage Devices (list/revoke,
// member-vs-admin scoped -- see resolveTargetUserID), and session-protected
// library browsing/search (GET /api/pages, GET/PATCH /api/pages/{id}).
// Routed via chi, with auth.RequireSession used as ordinary chi
// middleware (no httpapi-specific auth plumbing of its own); RequireAdmin
// exists in internal/auth but isn't used here yet -- Manage Devices'
// admin-vs-member scoping happens inside the handlers themselves
// (per-request, since a member and an admin hit the *same* routes with
// different allowed ?user_id= values), not as an all-or-nothing
// route-level gate.
//
// This package holds request validation and wiring only; the actual work
// happens in internal/auth (passwords, sessions, the bootstrap holder),
// internal/db (Postgres), internal/mirror (pushing the credential mirror
// to the Worker), and internal/devices (the Manage Devices Worker calls).
// The device-facing / Worker-facing API surface (queue, presigned R2
// URLs, /internal/tokens itself) isn't part of this package.
package httpapi

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/httplog/v2"
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

func NewRouter(s *Server, pool *pgxpool.Pool, q *db.Queries, logger *httplog.Logger, build BuildInfo) (http.Handler, error) {
	r := chi.NewRouter()

	r.Use(httplog.RequestLogger(logger, []string{}))
	r.Use(middleware.CleanPath)
	r.Use(middleware.RequestSize(1 << 20)) // 1MB cap on request bodies
	r.Use(middleware.Timeout(30 * time.Second))
	r.Use(middleware.Compress(5, "application/json", "text/plain"))
	r.Use(middleware.GetHead)

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

	registry, err := metrics.NewRegistry(q)
	if err != nil {
		return nil, fmt.Errorf("building metrics registry: %w", err)
	}
	r.Handle("/metrics", promhttp.HandlerFor(registry, promhttp.HandlerOpts{}))

	r.Route("/api", func(r chi.Router) {
		r.Use(middleware.AllowContentType("application/json"))

		r.Post("/setup", s.Setup)
		r.Post("/auth/register", s.Register)
		r.Post("/auth/login", s.Login)
		r.Post("/auth/logout", s.Logout)

		r.Group(func(r chi.Router) {
			r.Use(auth.RequireSession(q))
			r.Get("/auth/me", s.Me)
			r.Get("/pairing-token", s.GetPairingToken)
			r.Post("/pairing-token/regenerate", s.RegeneratePairingToken)
			r.Delete("/pairing-token", s.RevokePairingToken)
			r.Get("/devices", s.ListDevices)
			r.Delete("/devices/{id}", s.RevokeDevice)
			r.Get("/pages", s.ListPages)
			r.Get("/pages/{id}", s.GetPage)
			r.Patch("/pages/{id}", s.PatchPage)
		})
	})

	return r, nil
}
