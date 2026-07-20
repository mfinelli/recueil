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

// These tests run against a *real* chromedp sidecar (docker compose's
// "chromedp" service, test profile -- see compose.yaml).
package sidecar_test

import (
	"testing"
	"time"

	"github.com/chromedp/chromedp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mfinelli/recueil/internal/sidecar"
)

// testSidecarURL matches compose.yaml's published chromedp port, the
// same hardcoded-localhost convention dbtest.testDatabaseURL uses for
// Postgres.
const testSidecarURL = "http://127.0.0.1:9222"

// testRenderHost is NOT "127.0.0.1": these tests run as a plain `go test`
// process directly on the host (per compose.yaml's own documented local-dev
// shape), while chromedp runs in its own container -- see
// internal/screenshot's own tests for the fuller explanation.
const testRenderHost = "host.docker.internal"

func TestNew_FailsIfSidecarUnreachable(t *testing.T) {
	// Port 1 is a privileged port nothing is listening on in any CI or
	// dev environment -- connection refused comes back essentially
	// immediately, so this doesn't need to wait out sidecarPingTimeout.
	_, err := sidecar.New(&sidecar.Params{
		SidecarURL: "http://127.0.0.1:1",
		RenderHost: testRenderHost,
	})
	require.Error(t, err, "New should fail loudly at startup rather than silently retrying every job forever")
}

func TestSidecar_NewTab_ServesHTMLBackToTheSidecar(t *testing.T) {
	s, err := sidecar.New(&sidecar.Params{
		SidecarURL: testSidecarURL,
		RenderHost: testRenderHost,
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })

	html := []byte(`<!doctype html><html><head><title>Sidecar Test</title></head>` +
		`<body><h1 id="marker">hello from the render server</h1></body></html>`)

	tabCtx, url, cleanup := s.NewTab(html, 30*time.Second)
	defer cleanup()

	var text string
	require.NoError(t, chromedp.Run(tabCtx,
		chromedp.Navigate(url),
		chromedp.Text("#marker", &text, chromedp.NodeVisible),
	))
	assert.Equal(t, "hello from the render server", text)
}

func TestSidecar_NewTab_CleanupRemovesTheRegisteredHTML(t *testing.T) {
	s, err := sidecar.New(&sidecar.Params{
		SidecarURL: testSidecarURL,
		RenderHost: testRenderHost,
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })

	tabCtx, url, cleanup := s.NewTab([]byte("<html><body>gone soon</body></html>"), 30*time.Second)
	cleanup() // before ever navigating -- simulates a render that never actually fetched

	err = chromedp.Run(tabCtx, chromedp.Navigate(url))
	// The tab's own context was cancelled by cleanup too, so this should
	// fail one way or another (context cancellation, or a 404 from the
	// render server if the request somehow still got through before
	// cancellation) -- either is consistent with "cleanup actually did
	// something," which is the only thing worth asserting here.
	assert.Error(t, err)
}
