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
// strictly self-scoped -- see ListDevices' own doc comment for why
// cross-user device management isn't a web capability here),
// session-protected failed-queue-item review/retry (GET /api/queue-items,
// POST /api/queue-items/{id}/retry, also strictly self-scoped, same
// reasoning), session-protected failed-job review/retry (GET /api/jobs,
// POST /api/jobs/{kind}/{id}/retry for the screenshot/readability/AI
// enrichment jobs -- {kind} one of "screenshot"/"readability"/"ai" -- also
// strictly self-scoped, same reasoning), session-protected library
// browsing/search (GET /api/pages, GET/PATCH /api/pages/{id}), session-protected capture detail/HTML/language
// correction (GET /api/captures/{id}, GET /api/captures/{id}/html,
// PATCH /api/captures/{id}/language, GET /api/text-search-configs), and
// session-protected tags/collections (GET /api/tags,
// POST/DELETE /api/pages/{id}/tags[/{tagId}], full collections CRUD under
// /api/collections, and page<->collection membership under
// /api/pages/{id}/collections).
// Routed via chi, with auth.RequireSession used as ordinary chi
// middleware (no httpapi-specific auth plumbing of its own). RequireAdmin
// exists in internal/auth but isn't used here -- there's currently no
// dashboard capability that operates on another user's data at all,
// mirroring how user creation itself is CLI-only, not a dashboard feature.
//
// This package holds request validation and wiring only; the actual work
// happens in internal/auth (passwords, sessions, the bootstrap holder),
// internal/db (Postgres), internal/archive (reading archived HTML off
// disk), internal/mirror (pushing the credential mirror to the Worker),
// internal/devices (the Manage Devices Worker calls), and internal/queueitems
// (the failed-queue-item Worker calls). The device-facing / Worker-facing
// API surface (queue, presigned R2 URLs, /internal/tokens itself) isn't
// part of this package.
//
// NewRouter's dashboard parameter is the embedded Svelte build's dist/
// directory (see main.go's go:embed directive and cmd/server.go, which
// treats a missing/incomplete embed as a fatal startup error, the same
// as the Postgres/D1 migrations). Router-level code still tolerates a
// nil dashboard (skips registering the catch-all entirely) purely so
// this package's own tests -- which never exercise dashboard-serving --
// don't need to construct a real one just to call NewRouter.
package httpapi

import (
	"context"
	"fmt"
	"io/fs"
	"net/http"
	"strings"
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

func NewRouter(s *Server, pool *pgxpool.Pool, q *db.Queries, logger *httplog.Logger, build BuildInfo, dashboard fs.FS) (http.Handler, error) {
	r := chi.NewRouter()

	r.Use(httplog.RequestLogger(logger, []string{}))
	r.Use(middleware.CleanPath)
	r.Use(middleware.RequestSize(1 << 20)) // 1MB cap on request bodies
	r.Use(middleware.Timeout(30 * time.Second))
	r.Use(middleware.Compress(5, "application/json", "text/plain", "text/html",
		"text/javascript", "application/javascript", "text/css", "image/svg+xml"))
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

		r.Get("/setup-status", s.SetupStatus)
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
			r.Get("/queue-items", s.ListFailedQueueItems)
			r.Post("/queue-items/{id}/retry", s.RetryQueueItem)
			r.Get("/jobs", s.ListFailedJobs)
			r.Post("/jobs/{kind}/{id}/retry", s.RetryJob)
			r.Get("/pages", s.ListPages)
			r.Get("/pages/{id}", s.GetPage)
			r.Patch("/pages/{id}", s.PatchPage)
			r.Get("/pages/{id}/favicon", s.GetPageFavicon)
			r.Get("/pages/{id}/thumbnail", s.GetPageThumbnail)
			r.Get("/captures/{id}", s.GetCapture)
			r.Get("/captures/{id}/html", s.GetCaptureHTML)
			r.Patch("/captures/{id}/language", s.PatchCaptureLanguage)
			r.Get("/text-search-configs", s.ListTextSearchConfigs)
			r.Get("/tags", s.ListTags)
			r.Post("/pages/{id}/tags", s.AddPageTag)
			r.Delete("/pages/{id}/tags/{tagId}", s.RemovePageTag)
			r.Get("/collections", s.ListCollections)
			r.Post("/collections", s.CreateCollection)
			r.Patch("/collections/{id}", s.RenameCollection)
			r.Delete("/collections/{id}", s.DeleteCollection)
			r.Get("/collections/{id}/pages", s.ListCollectionPages)
			r.Post("/pages/{id}/collections", s.AddPageToCollection)
			r.Delete("/pages/{id}/collections/{collectionId}", s.RemovePageFromCollection)
		})
	})

	// dashboard is nil only in this package's own tests (which never
	// exercise dashboard-serving); cmd/server.go always supplies a real
	// one in production, failing startup otherwise. chi resolves the
	// more specific /api, /info, /ping, /health, /metrics routes above
	// regardless of where this wildcard is registered -- its own
	// radix-tree matching, not registration order, is what keeps this
	// from ever shadowing them -- but it's still registered last here
	// for readability.
	if dashboard != nil {
		r.Get("/*", spaHandler(dashboard))
	}

	return r, nil
}

// spaHandler serves the embedded dashboard build, falling back to
// index.html for any path that isn't a real file in it. That fallback is
// what makes svelte-spa-router's client-side routes (e.g. /pages/5) work
// on a real browser refresh or a direct link, not just on in-app
// navigation -- those paths only exist client-side, never as actual
// files in the Vite build.
func spaHandler(dashboard fs.FS) http.HandlerFunc {
	fileServer := http.FileServerFS(dashboard)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, "/")
		if path == "" {
			path = "index.html"
		}
		if _, err := fs.Stat(dashboard, path); err != nil {
			r = r.Clone(r.Context())
			r.URL.Path = "/"
		}
		fileServer.ServeHTTP(w, r)
	})
}
