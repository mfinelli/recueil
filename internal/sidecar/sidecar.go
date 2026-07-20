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

// Package sidecar is the plumbing shared by every job that renders
// already-captured HTML through the headless-Chrome sidecar -- today
// internal/screenshot and internal/readability. It owns exactly the parts
// that are identical regardless of what a caller actually wants to do
// with a loaded page: one long-lived chromedp RemoteAllocator connection
// (opening many tabs against one allocator is chromedp's normal usage
// pattern -- there's no benefit to each job holding its own separate
// connection to the same sidecar process) and one long-lived ephemeral
// HTTP server for serving a job's HTML to it.
//
// What it does NOT own: what actually happens once a tab is loaded (a
// screenshot capture vs. injecting and running Readability.js are nothing
// alike), and the retry/backoff/claim bookkeeping around a specific job
// table (screenshot_jobs and readability_jobs have their own sqlc-generated
// types with no common interface worth forcing them through for what's
// ultimately a few dozen lines of straightforward, already-well-tested
// bookkeeping each). NewTab reflects this split directly: it hands back a
// ready-to-navigate tab and URL, and never itself calls chromedp.Run -- a
// shared package can't know whether a caller needs, say, a fixed viewport
// applied before Navigate, so it doesn't try to.
//
// # Serving HTML to the sidecar
//
// The sidecar is a separate process -- often a separate container --
// with no filesystem access to the agent's local archive, so a job's
// decompressed HTML is served over this package's ephemeral HTTP server,
// one random-token path per in-flight render, for the lifetime of the
// Sidecar. See Params' SidecarURL/RenderHost doc (mirrored in
// internal/config) for how the two different deployment shapes (sidecar
// and agent in the same compose network vs. agent running directly on
// the operator's own machine) each configure reachability.
package sidecar

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/chromedp/chromedp"
)

// sidecarPingTimeout bounds New's one-shot startup reachability check --
// see pingSidecar's own doc.
const sidecarPingTimeout = 5 * time.Second

// Params are Sidecar's dependencies, all required except Logger.
type Params struct {
	// SidecarURL is the agent's own outbound address for the shared
	// headless-Chrome sidecar (config's sidecar_url) -- an
	// http(s) base URL, not a raw ws:// one: chromedp.NewRemoteAllocator
	// fetches /json/version itself and swaps in the real
	// webSocketDebuggerUrl, so this package never has to.
	SidecarURL string

	// RenderHost is the hostname the sidecar should use to reach back
	// into this Sidecar's own ephemeral render server (config's
	// sidecar_render_host) -- the opposite direction from
	// SidecarURL. See the package doc and internal/config's own doc
	// comment for the two deployment shapes this needs to cover.
	RenderHost string

	// Logger defaults to slog.Default() if nil.
	Logger *slog.Logger
}

// Sidecar holds a long-lived connection to the headless-Chrome sidecar
// (one RemoteAllocator, shared across however many callers open tabs
// against it) and a long-lived ephemeral HTTP server for serving HTML to
// it. Both are real OS resources, which is why -- unlike, say,
// internal/ingest.Ingester or mirror.Syncer -- this type has a Close.
type Sidecar struct {
	renderHost string
	logger     *slog.Logger

	allocCancel context.CancelFunc
	allocCtx    context.Context

	listener net.Listener
	server   *http.Server

	mu      sync.Mutex
	pending map[string][]byte
}

// New pings the sidecar once to confirm it's actually reachable, then
// starts the ephemeral render server and dials the sidecar for real. The
// caller is responsible for calling Close when done.
//
// Failing loudly here (rather than lazily discovering an unreachable
// sidecar on the first job's render call) is deliberate: it's what lets
// an orchestrator's restart-until-healthy policy -- Docker Compose's
// restart policy, systemd's Restart=on-failure, a Kubernetes liveness
// probe, whatever the deployment uses -- actually do its job. Without
// this, a misconfigured or not-yet-ready sidecar would leave the agent
// process running indefinitely with every job silently failing and
// retrying forever, instead of the process itself exiting non-zero so
// something notices and restarts it once the sidecar catches up.
func New(p *Params) (*Sidecar, error) {
	logger := p.Logger
	if logger == nil {
		logger = slog.Default()
	}

	if err := pingSidecar(p.SidecarURL); err != nil {
		return nil, fmt.Errorf("sidecar: sidecar not reachable at startup: %w", err)
	}

	// Bound to every interface: this is a container-to-container (or
	// container-to-host) connection, entirely separate from whatever
	// hostname RenderHost tells the *sidecar* to use to reach back in.
	ln, err := net.Listen("tcp", "0.0.0.0:0")
	if err != nil {
		return nil, fmt.Errorf("sidecar: starting render server listener: %w", err)
	}

	s := &Sidecar{
		renderHost: p.RenderHost,
		logger:     logger,
		listener:   ln,
		pending:    make(map[string][]byte),
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/", s.handleRender)
	s.server = &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() {
		if err := s.server.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("sidecar: render server stopped unexpectedly", "error", err)
		}
	}()

	// Intentionally parented on context.Background(), not any per-call
	// ctx a caller might pass to NewTab: this connection is meant to
	// outlive any single render, same lifetime as the Sidecar itself,
	// torn down only by an explicit Close.
	allocCtx, allocCancel := chromedp.NewRemoteAllocator(context.Background(), p.SidecarURL)
	s.allocCtx = allocCtx
	s.allocCancel = allocCancel

	return s, nil
}

// pingSidecar performs one bounded-time HTTP GET against the sidecar's
// /json/version endpoint -- the same endpoint chromedp's own
// RemoteAllocator uses internally to discover the real
// webSocketDebuggerUrl -- purely to confirm something's actually
// listening and answering there before New commits to starting the
// render server and dialing for real.
func pingSidecar(sidecarURL string) error {
	client := http.Client{Timeout: sidecarPingTimeout}

	resp, err := client.Get(strings.TrimRight(sidecarURL, "/") + "/json/version")
	if err != nil {
		return fmt.Errorf("GET /json/version: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GET /json/version: unexpected status %d", resp.StatusCode)
	}
	return nil
}

// Close tears down the sidecar connection and the render server. Safe to
// call once, at shutdown.
func (s *Sidecar) Close() error {
	s.allocCancel()
	return s.server.Close()
}

// NewTab registers htmlData as servable, opens a fresh chromedp tab (a
// child of the shared allocator context, bounded by timeout), and hands
// back that tab's context together with the URL it'll need to
// chromedp.Navigate to in order to load it. Callers own their entire
// action sequence from there -- this deliberately never calls
// chromedp.Run itself; see the package doc for why.
//
// cleanup releases both the tab and the registered HTML, and must be
// called exactly once (typically via defer) regardless of whether the
// render that follows succeeds -- a stuck/never-fetched render (the
// sidecar crashing mid-request, say) would otherwise leak the HTML
// registration for as long as the Sidecar itself lives.
func (s *Sidecar) NewTab(htmlData []byte, timeout time.Duration) (tabCtx context.Context, url string, cleanup func()) {
	token, unregister := s.registerHTML(htmlData)

	tabCtx, cancelTab := chromedp.NewContext(s.allocCtx)
	tabCtx, cancelTimeout := context.WithTimeout(tabCtx, timeout)

	url = fmt.Sprintf("%s/%s", s.baseURL(), token)
	cleanup = func() {
		cancelTimeout()
		cancelTab()
		unregister()
	}

	return tabCtx, url, cleanup
}

// baseURL is the http address the sidecar should navigate to for this
// Sidecar's render server, combining the configured, deployment-specific
// RenderHost with the port the ephemeral listener actually got assigned
// -- never configured directly (see Params.RenderHost's doc for why the
// port doesn't need to be).
func (s *Sidecar) baseURL() string {
	port := s.listener.Addr().(*net.TCPAddr).Port
	return fmt.Sprintf("http://%s:%d", s.renderHost, port)
}

// registerHTML makes htmlData servable at a fresh random-token path and
// returns a cleanup func that removes it again.
func (s *Sidecar) registerHTML(htmlData []byte) (token string, cleanup func()) {
	buf := make([]byte, 16)
	_, _ = rand.Read(buf)
	token = hex.EncodeToString(buf)

	s.mu.Lock()
	s.pending[token] = htmlData
	s.mu.Unlock()

	return token, func() {
		s.mu.Lock()
		delete(s.pending, token)
		s.mu.Unlock()
	}
}

func (s *Sidecar) handleRender(w http.ResponseWriter, req *http.Request) {
	token := strings.TrimPrefix(req.URL.Path, "/")

	s.mu.Lock()
	data, ok := s.pending[token]
	s.mu.Unlock()

	if !ok {
		http.NotFound(w, req)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(data)
}
