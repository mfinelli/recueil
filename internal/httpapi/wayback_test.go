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

package httpapi_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/httplog/v2"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mfinelli/recueil/internal/archive"
	"github.com/mfinelli/recueil/internal/auth"
	"github.com/mfinelli/recueil/internal/db"
	"github.com/mfinelli/recueil/internal/dbtest"
	"github.com/mfinelli/recueil/internal/devices"
	"github.com/mfinelli/recueil/internal/httpapi"
	"github.com/mfinelli/recueil/internal/linkicons"
	"github.com/mfinelli/recueil/internal/mirror"
	"github.com/mfinelli/recueil/internal/pendingcaptures"
	"github.com/mfinelli/recueil/internal/queueitems"
	"github.com/mfinelli/recueil/internal/wayback"
)

// fakeWaybackAPI stands in for archive.org, recording every call so a test can
// assert what was (and was not) sent.
type fakeWaybackAPI struct {
	saved   []string
	status  []string
	saveErr error
	jobs    map[string]wayback.Job
	next    int
}

func newFakeWaybackAPI() *fakeWaybackAPI {
	return &fakeWaybackAPI{jobs: map[string]wayback.Job{}}
}

func (f *fakeWaybackAPI) Save(_ context.Context, pageURL string) (string, error) {
	if f.saveErr != nil {
		return "", f.saveErr
	}
	f.saved = append(f.saved, pageURL)
	f.next++
	return fmt.Sprintf("job-%d", f.next), nil
}

func (f *fakeWaybackAPI) Status(_ context.Context, jobID string) (wayback.Job, error) {
	f.status = append(f.status, jobID)
	if job, ok := f.jobs[jobID]; ok {
		return job, nil
	}
	return wayback.Job{State: wayback.StatePending}, nil
}

// newWaybackTestServer is newLinksTestServer's counterpart with Wayback
// submission switched on (api non-nil) or off (nil): links on, and a real
// wayback.Service over a fake archive.org, which is also returned so a test can
// play the agent's sweep.
func newWaybackTestServer(t *testing.T, pool *pgxpool.Pool, api wayback.API) (*httptest.Server, *wayback.Service) {
	t.Helper()
	q := db.New(pool)
	bootstrap, _, err := auth.NewBootstrapTokenHolder()
	require.NoError(t, err)

	var svc *wayback.Service
	if api != nil {
		svc = wayback.NewService(q, api, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	}
	s := httpapi.NewServer(q, pool, archive.New(t.TempDir()), mirror.NewClient(unreachable, "test-secret"),
		devices.NewClient(unreachable, "test-secret"), queueitems.NewClient(unreachable, "test-secret"),
		pendingcaptures.NewClient(unreachable, "test-secret"), bootstrap, false, testPairingKey(t), true,
		"test-readability-version", "test-ai-model", "https://worker.test.example", testPipeline(t),
		testManualUploadMaxBytes, true, linkicons.New(t.TempDir()), svc)
	logger := httplog.NewLogger("recueil-test")
	logger.Logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	r, err := httpapi.NewRouter(s, pool, q, logger, httpapi.BuildInfo{}, nil)
	require.NoError(t, err)
	srv := httptest.NewServer(r)
	t.Cleanup(srv.Close)
	return srv, svc
}

type waybackBody struct {
	Status          string  `json:"status"`
	SubmittedAt     string  `json:"submitted_at"`
	LastSubmittedAt *string `json:"last_submitted_at"`
	Error           *string `json:"error"`
}

type withWayback struct {
	Wayback *waybackBody `json:"wayback"`
}

func submitLinkWayback(t *testing.T, server *httptest.Server, cookie *http.Cookie, linkID int64) *http.Response {
	t.Helper()
	return requestWithCookie(t, server, http.MethodPost, fmt.Sprintf("/api/links/%d/wayback", linkID), cookie)
}

func submitPageWayback(t *testing.T, server *httptest.Server, cookie *http.Cookie, pageID int64) *http.Response {
	t.Helper()
	return requestWithCookie(t, server, http.MethodPost, fmt.Sprintf("/api/pages/%d/wayback", pageID), cookie)
}

func linkWayback(t *testing.T, server *httptest.Server, cookie *http.Cookie, linkID int64) *waybackBody {
	t.Helper()
	resp := requestWithCookie(t, server, http.MethodGet, fmt.Sprintf("/api/links/%d", linkID), cookie)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	return decodeBody[withWayback](t, resp).Wayback
}

func pageWayback(t *testing.T, server *httptest.Server, cookie *http.Cookie, pageID int64) *waybackBody {
	t.Helper()
	resp := requestWithCookie(t, server, http.MethodGet, fmt.Sprintf("/api/pages/%d", pageID), cookie)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	return decodeBody[withWayback](t, resp).Wayback
}

func TestSubmitLinkWayback(t *testing.T) {
	pool := dbtest.Setup(t)
	api := newFakeWaybackAPI()
	server, svc := newWaybackTestServer(t, pool, api)

	t.Run("starts a submission for the link's normalized URL and reports it pending", func(t *testing.T) {
		user := dbtest.CreateUser(t, pool, "member")
		link := saveLink(t, pool, user.ID, "https://example.com/watch", "Watch")
		cookie := sessionCookieFor(t, pool, &user)
		before := len(api.saved)

		resp := submitLinkWayback(t, server, cookie, link.ID)

		require.Equal(t, http.StatusAccepted, resp.StatusCode)
		got := decodeBody[waybackBody](t, resp)
		assert.Equal(t, "pending", got.Status)
		assert.NotEmpty(t, got.SubmittedAt)
		assert.Nil(t, got.LastSubmittedAt, "nothing has succeeded yet")
		assert.Nil(t, got.Error)
		require.Len(t, api.saved, before+1)
		assert.Equal(t, "https://example.com/watch", api.saved[before], "the normalized URL, not the raw one with its #raw fragment")
	})

	t.Run("shows up on the link's own detail, as pending", func(t *testing.T) {
		user := dbtest.CreateUser(t, pool, "member")
		link := saveLink(t, pool, user.ID, "https://example.com/detail", "Detail")
		cookie := sessionCookieFor(t, pool, &user)
		assert.Nil(t, linkWayback(t, server, cookie, link.ID), "null until submitted")

		require.Equal(t, http.StatusAccepted, submitLinkWayback(t, server, cookie, link.ID).StatusCode)

		got := linkWayback(t, server, cookie, link.ID)
		require.NotNil(t, got)
		assert.Equal(t, "pending", got.Status)
	})

	t.Run("a second submission while one is pending is a 409, and doesn't call archive.org again", func(t *testing.T) {
		user := dbtest.CreateUser(t, pool, "member")
		link := saveLink(t, pool, user.ID, "https://example.com/twice", "Twice")
		cookie := sessionCookieFor(t, pool, &user)
		require.Equal(t, http.StatusAccepted, submitLinkWayback(t, server, cookie, link.ID).StatusCode)
		calls := len(api.saved)

		resp := submitLinkWayback(t, server, cookie, link.ID)

		assert.Equal(t, http.StatusConflict, resp.StatusCode)
		assert.Len(t, api.saved, calls)
	})

	t.Run("after the agent records a result, the detail shows it, and Submit works again", func(t *testing.T) {
		user := dbtest.CreateUser(t, pool, "member")
		link := saveLink(t, pool, user.ID, "https://example.com/done", "Done")
		cookie := sessionCookieFor(t, pool, &user)
		require.Equal(t, http.StatusAccepted, submitLinkWayback(t, server, cookie, link.ID).StatusCode)
		pending := linkWayback(t, server, cookie, link.ID)
		jobID := fmt.Sprintf("job-%d", api.next)
		api.jobs[jobID] = wayback.Job{State: wayback.StateSuccess}

		require.NoError(t, svc.SweepPending(context.Background()), "the agent's sweep, not the dashboard, asks archive.org")

		got := linkWayback(t, server, cookie, link.ID)
		require.NotNil(t, got)
		assert.Equal(t, "succeeded", got.Status)
		require.NotNil(t, got.LastSubmittedAt)
		assert.Equal(t, pending.SubmittedAt, *got.LastSubmittedAt)
		assert.Equal(t, http.StatusAccepted, submitLinkWayback(t, server, cookie, link.ID).StatusCode, "can submit again")
	})

	t.Run("a failure shows archive.org's message, keeps the last success, and can be retried", func(t *testing.T) {
		user := dbtest.CreateUser(t, pool, "member")
		link := saveLink(t, pool, user.ID, "https://example.com/fails", "Fails")
		cookie := sessionCookieFor(t, pool, &user)
		require.Equal(t, http.StatusAccepted, submitLinkWayback(t, server, cookie, link.ID).StatusCode)
		api.jobs[fmt.Sprintf("job-%d", api.next)] = wayback.Job{State: wayback.StateError, Message: "Couldn't resolve host."}
		require.NoError(t, svc.SweepPending(context.Background()))

		got := linkWayback(t, server, cookie, link.ID)

		require.NotNil(t, got)
		assert.Equal(t, "failed", got.Status)
		require.NotNil(t, got.Error)
		assert.Equal(t, "Couldn't resolve host.", *got.Error)
		assert.Nil(t, got.LastSubmittedAt)
		assert.Equal(t, http.StatusAccepted, submitLinkWayback(t, server, cookie, link.ID).StatusCode)
	})

	t.Run("viewing never contacts archive.org", func(t *testing.T) {
		user := dbtest.CreateUser(t, pool, "member")
		link := saveLink(t, pool, user.ID, "https://example.com/quiet", "Quiet")
		cookie := sessionCookieFor(t, pool, &user)
		require.Equal(t, http.StatusAccepted, submitLinkWayback(t, server, cookie, link.ID).StatusCode)
		statusCalls, saveCalls := len(api.status), len(api.saved)

		for range 3 {
			linkWayback(t, server, cookie, link.ID)
		}

		assert.Len(t, api.status, statusCalls, "no status check: that is the agent's job")
		assert.Len(t, api.saved, saveCalls)
	})

	t.Run("archive.org refusing is a 502 with its own message, and records nothing", func(t *testing.T) {
		user := dbtest.CreateUser(t, pool, "member")
		link := saveLink(t, pool, user.ID, "https://example.com/limited", "Limited")
		cookie := sessionCookieFor(t, pool, &user)
		api.saveErr = &wayback.APIError{StatusCode: 429, Message: "This URL has been captured too many times today."}
		defer func() { api.saveErr = nil }()

		resp := submitLinkWayback(t, server, cookie, link.ID)

		assert.Equal(t, http.StatusBadGateway, resp.StatusCode)
		body := decodeBody[map[string]string](t, resp)
		assert.Equal(t, "This URL has been captured too many times today.", body["error"])
		assert.Nil(t, linkWayback(t, server, cookie, link.ID))
	})

	t.Run("an unreachable archive.org is a 502 saying so", func(t *testing.T) {
		user := dbtest.CreateUser(t, pool, "member")
		link := saveLink(t, pool, user.ID, "https://example.com/down", "Down")
		cookie := sessionCookieFor(t, pool, &user)
		api.saveErr = errors.New("dial tcp: connection refused")
		defer func() { api.saveErr = nil }()

		resp := submitLinkWayback(t, server, cookie, link.ID)

		assert.Equal(t, http.StatusBadGateway, resp.StatusCode)
		assert.Equal(t, "could not reach archive.org", decodeBody[map[string]string](t, resp)["error"])
	})

	t.Run("404s a link that isn't the caller's, submitting nothing", func(t *testing.T) {
		owner := dbtest.CreateUser(t, pool, "member")
		other := dbtest.CreateUser(t, pool, "member")
		link := saveLink(t, pool, owner.ID, "https://example.com/private", "Private")
		calls := len(api.saved)

		resp := submitLinkWayback(t, server, sessionCookieFor(t, pool, &other), link.ID)

		assert.Equal(t, http.StatusNotFound, resp.StatusCode)
		assert.Len(t, api.saved, calls)
	})

	t.Run("400s a bad id and requires a session", func(t *testing.T) {
		user := dbtest.CreateUser(t, pool, "member")
		resp := requestWithCookie(t, server, http.MethodPost, "/api/links/abc/wayback", sessionCookieFor(t, pool, &user))
		assert.Equal(t, http.StatusBadRequest, resp.StatusCode)

		resp, err := http.Post(server.URL+"/api/links/1/wayback", "application/json", nil)
		require.NoError(t, err)
		assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
	})
}

func TestSubmitPageWayback(t *testing.T) {
	pool := dbtest.Setup(t)
	api := newFakeWaybackAPI()
	server, _ := newWaybackTestServer(t, pool, api)

	t.Run("submits the page's normalized URL, and the state appears on the page", func(t *testing.T) {
		user := dbtest.CreateUser(t, pool, "member")
		page := dbtest.CreatePage(t, pool, user.ID, "https://example.com/article")
		cookie := sessionCookieFor(t, pool, &user)
		assert.Nil(t, pageWayback(t, server, cookie, page.ID))

		resp := submitPageWayback(t, server, cookie, page.ID)

		require.Equal(t, http.StatusAccepted, resp.StatusCode)
		assert.Equal(t, "pending", decodeBody[waybackBody](t, resp).Status)
		assert.Equal(t, "https://example.com/article", api.saved[len(api.saved)-1])
		got := pageWayback(t, server, cookie, page.ID)
		require.NotNil(t, got)
		assert.Equal(t, "pending", got.Status)
	})

	t.Run("a page and a link with the same URL share one submission", func(t *testing.T) {
		user := dbtest.CreateUser(t, pool, "member")
		page := dbtest.CreatePage(t, pool, user.ID, "https://example.com/both")
		link := saveLink(t, pool, user.ID, "https://example.com/both", "Both")
		cookie := sessionCookieFor(t, pool, &user)

		require.Equal(t, http.StatusAccepted, submitPageWayback(t, server, cookie, page.ID).StatusCode)

		linkState := linkWayback(t, server, cookie, link.ID)
		require.NotNil(t, linkState, "submitted from the page, shown on the link")
		assert.Equal(t, "pending", linkState.Status)
		assert.Equal(t, http.StatusConflict, submitLinkWayback(t, server, cookie, link.ID).StatusCode,
			"and it's one submission, so the link can't start a second")
	})

	t.Run("one user never sees another's submission of the same URL", func(t *testing.T) {
		a := dbtest.CreateUser(t, pool, "member")
		b := dbtest.CreateUser(t, pool, "member")
		pageA := dbtest.CreatePage(t, pool, a.ID, "https://example.com/shared")
		pageB := dbtest.CreatePage(t, pool, b.ID, "https://example.com/shared")
		cookieA := sessionCookieFor(t, pool, &a)
		cookieB := sessionCookieFor(t, pool, &b)

		require.Equal(t, http.StatusAccepted, submitPageWayback(t, server, cookieA, pageA.ID).StatusCode)

		assert.NotNil(t, pageWayback(t, server, cookieA, pageA.ID))
		assert.Nil(t, pageWayback(t, server, cookieB, pageB.ID), "B sees nothing: it would reveal that someone else saved the URL")
		// And B can submit their own.
		assert.Equal(t, http.StatusAccepted, submitPageWayback(t, server, cookieB, pageB.ID).StatusCode)
	})

	t.Run("404s another user's page", func(t *testing.T) {
		owner := dbtest.CreateUser(t, pool, "member")
		other := dbtest.CreateUser(t, pool, "member")
		page := dbtest.CreatePage(t, pool, owner.ID, "https://example.com/theirs")
		calls := len(api.saved)

		resp := submitPageWayback(t, server, sessionCookieFor(t, pool, &other), page.ID)

		assert.Equal(t, http.StatusNotFound, resp.StatusCode)
		assert.Len(t, api.saved, calls)
	})
}

func TestWaybackState_StalePending(t *testing.T) {
	pool := dbtest.Setup(t)
	api := newFakeWaybackAPI()
	server, _ := newWaybackTestServer(t, pool, api)
	user := dbtest.CreateUser(t, pool, "member")
	link := saveLink(t, pool, user.ID, "https://example.com/stale", "Stale")
	cookie := sessionCookieFor(t, pool, &user)
	require.Equal(t, http.StatusAccepted, submitLinkWayback(t, server, cookie, link.ID).StatusCode)

	// The agent has been down for over an hour: the row is still pending.
	_, err := pool.Exec(context.Background(),
		`UPDATE wayback_submissions SET submitted_at = $1 WHERE user_id = $2`,
		pgtype.Timestamptz{Time: time.Now().Add(-wayback.PendingTimeout - time.Minute), Valid: true}, user.ID)
	require.NoError(t, err)

	t.Run("is shown as failed, so Submit isn't left disabled for good", func(t *testing.T) {
		got := linkWayback(t, server, cookie, link.ID)
		require.NotNil(t, got)
		assert.Equal(t, "failed", got.Status)
		require.NotNil(t, got.Error)
		assert.Equal(t, wayback.TimeoutMessage, *got.Error)
	})

	t.Run("and can be submitted again", func(t *testing.T) {
		assert.Equal(t, http.StatusAccepted, submitLinkWayback(t, server, cookie, link.ID).StatusCode)
		got := linkWayback(t, server, cookie, link.ID)
		require.NotNil(t, got)
		assert.Equal(t, "pending", got.Status)
	})
}

func TestWayback_NotRoutedWhenOff(t *testing.T) {
	pool := dbtest.Setup(t)
	server, _ := newWaybackTestServer(t, pool, nil)
	user := dbtest.CreateUser(t, pool, "member")
	cookie := sessionCookieFor(t, pool, &user)
	link := saveLink(t, pool, user.ID, "https://example.com/off", "Off")
	page := dbtest.CreatePage(t, pool, user.ID, "https://example.com/off-page")

	assert.Equal(t, http.StatusNotFound, submitLinkWayback(t, server, cookie, link.ID).StatusCode)
	assert.Equal(t, http.StatusNotFound, submitPageWayback(t, server, cookie, page.ID).StatusCode)
	// Reading still works, and says nothing was submitted.
	assert.Nil(t, linkWayback(t, server, cookie, link.ID))
	assert.Nil(t, pageWayback(t, server, cookie, page.ID))
}

func TestWayback_RecordsAreStillShownWhenSubmissionIsTurnedOff(t *testing.T) {
	pool := dbtest.Setup(t)
	user := dbtest.CreateUser(t, pool, "member")
	link := saveLink(t, pool, user.ID, "https://example.com/was-on", "Was on")
	cookie := sessionCookieFor(t, pool, &user)

	on, _ := newWaybackTestServer(t, pool, newFakeWaybackAPI())
	require.Equal(t, http.StatusAccepted, submitLinkWayback(t, on, cookie, link.ID).StatusCode)

	off, _ := newWaybackTestServer(t, pool, nil)
	got := linkWayback(t, off, cookie, link.ID)
	require.NotNil(t, got, "keys removed later: what was recorded is still true")
	assert.Equal(t, "pending", got.Status)
}

func TestCaptureConfig_WaybackEnabled(t *testing.T) {
	pool := dbtest.Setup(t)
	user := dbtest.CreateUser(t, pool, "member")
	cookie := sessionCookieFor(t, pool, &user)

	type config struct {
		WaybackEnabled bool `json:"wayback_enabled"`
	}
	on, _ := newWaybackTestServer(t, pool, newFakeWaybackAPI())
	off, _ := newWaybackTestServer(t, pool, nil)

	assert.True(t, decodeBody[config](t, requestWithCookie(t, on, http.MethodGet, "/api/capture-config", cookie)).WaybackEnabled)
	assert.False(t, decodeBody[config](t, requestWithCookie(t, off, http.MethodGet, "/api/capture-config", cookie)).WaybackEnabled)
}
