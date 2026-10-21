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

package wayback_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mfinelli/recueil/internal/wayback"
)

type seen struct {
	method, path, authorization, accept, userAgent, contentType string
	form                                                        url.Values
}

// fakeArchive is a one-route-at-a-time stand-in for SPN2: it records each request
// and answers with whatever the test set.
func fakeArchive(t *testing.T, status int, body string) (*wayback.Client, *[]seen) {
	t.Helper()
	var got []seen
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		form, _ := url.ParseQuery(string(raw))
		got = append(got, seen{
			method: r.Method, path: r.URL.Path, authorization: r.Header.Get("Authorization"),
			accept: r.Header.Get("Accept"), userAgent: r.Header.Get("User-Agent"),
			contentType: r.Header.Get("Content-Type"), form: form,
		})
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(server.Close)
	return wayback.NewClientWithBaseURL(server.URL+"/", "access", "secret", server.Client()), &got
}

func TestClient_Save(t *testing.T) {
	ctx := context.Background()

	t.Run("posts the URL form-encoded with the account's keys, and returns the job id", func(t *testing.T) {
		client, got := fakeArchive(t, 200, `{"url":"https://example.com/a","job_id":"spn2-abc123"}`)

		jobID, err := client.Save(ctx, "https://example.com/a?x=1&y=2")
		require.NoError(t, err)
		assert.Equal(t, "spn2-abc123", jobID)

		require.Len(t, *got, 1)
		req := (*got)[0]
		assert.Equal(t, http.MethodPost, req.method)
		assert.Equal(t, "/save", req.path)
		assert.Equal(t, "LOW access:secret", req.authorization)
		assert.Equal(t, "application/json", req.accept)
		assert.Equal(t, "application/x-www-form-urlencoded", req.contentType)
		assert.Equal(t, "recueil/1.0 (+https://github.com/mfinelli/recueil)", req.userAgent,
			"names the project, so archive.org knows where to ask about these requests")
		assert.Equal(t, "https://example.com/a?x=1&y=2", req.form.Get("url"), "the URL survives form encoding intact")
		assert.Equal(t, "1", req.form.Get("skip_first_archive"))
	})

	t.Run("accepts a response that carries a message beside the job id", func(t *testing.T) {
		client, _ := fakeArchive(t, 200, `{"url":"https://example.com/","job_id":"spn2-x","message":"The same snapshot had been made 1 second ago."}`)
		jobID, err := client.Save(ctx, "https://example.com/")
		require.NoError(t, err)
		assert.Equal(t, "spn2-x", jobID)
	})

	t.Run("a refusal is an APIError carrying archive.org's own words", func(t *testing.T) {
		client, _ := fakeArchive(t, 429, `{"status":"error","message":"You have already reached the limit of active sessions.","status_ext":"error:user-session-limit"}`)
		_, err := client.Save(ctx, "https://example.com/")
		var apiErr *wayback.APIError
		require.ErrorAs(t, err, &apiErr)
		assert.Equal(t, 429, apiErr.StatusCode)
		assert.Equal(t, "You have already reached the limit of active sessions.", apiErr.Message)
	})

	t.Run("falls back to the status extension, then the raw body, then the status text", func(t *testing.T) {
		for body, want := range map[string]string{
			`{"status_ext":"error:blocked-url"}`: "error:blocked-url",
			`plain text failure`:                 "plain text failure",
			``:                                   "Internal Server Error",
		} {
			client, _ := fakeArchive(t, 500, body)
			_, err := client.Save(ctx, "https://example.com/")
			var apiErr *wayback.APIError
			require.ErrorAs(t, err, &apiErr, body)
			assert.Equal(t, want, apiErr.Message, body)
		}
	})

	t.Run("a success with no job id is an error naming archive.org's message", func(t *testing.T) {
		client, _ := fakeArchive(t, 200, `{"message":"Please try again later."}`)
		_, err := client.Save(ctx, "https://example.com/")
		var apiErr *wayback.APIError
		require.ErrorAs(t, err, &apiErr)
		assert.Equal(t, "Please try again later.", apiErr.Message)
	})

	t.Run("a body that isn't JSON is an error", func(t *testing.T) {
		client, _ := fakeArchive(t, 200, `<html>`)
		_, err := client.Save(ctx, "https://example.com/")
		assert.ErrorContains(t, err, "reading the capture response")
	})

	t.Run("an unreachable archive is an error", func(t *testing.T) {
		client, _ := fakeArchive(t, 200, `{}`)
		server := httptest.NewServer(http.NotFoundHandler())
		server.Close()
		dead := wayback.NewClientWithBaseURL(server.URL, "a", "s", &http.Client{Timeout: time.Second})
		_, err := dead.Save(ctx, "https://example.com/")
		assert.ErrorContains(t, err, "requesting a capture")
		_ = client
	})
}

func TestClient_Status(t *testing.T) {
	ctx := context.Background()

	t.Run("a finished job gives the capture time and final URL", func(t *testing.T) {
		client, got := fakeArchive(t, 200, `{"status":"success","job_id":"spn2-abc","original_url":"https://example.com/final","timestamp":"20260915143005","duration_sec":6.2,"resources":["https://example.com/"]}`)

		job, err := client.Status(ctx, "spn2-abc")
		require.NoError(t, err)
		assert.Equal(t, wayback.StateSuccess, job.State)
		assert.Equal(t, "https://example.com/final", job.OriginalURL)
		assert.True(t, job.Timestamp.Equal(time.Date(2026, 9, 15, 14, 30, 5, 0, time.UTC)))

		req := (*got)[0]
		assert.Equal(t, http.MethodGet, req.method)
		assert.Equal(t, "/save/status/spn2-abc", req.path)
		assert.Equal(t, "LOW access:secret", req.authorization)
	})

	t.Run("an unparseable timestamp is just left zero", func(t *testing.T) {
		client, _ := fakeArchive(t, 200, `{"status":"success","timestamp":"soon"}`)
		job, err := client.Status(ctx, "j")
		require.NoError(t, err)
		assert.Equal(t, wayback.StateSuccess, job.State)
		assert.True(t, job.Timestamp.IsZero())
	})

	t.Run("a failed job carries archive.org's message", func(t *testing.T) {
		client, _ := fakeArchive(t, 200, `{"status":"error","exception":"[Errno -2] Name or service not known","status_ext":"error:invalid-host-resolution","message":"Couldn't resolve host for http://example5123.com."}`)
		job, err := client.Status(ctx, "j")
		require.NoError(t, err)
		assert.Equal(t, wayback.StateError, job.State)
		assert.Equal(t, "Couldn't resolve host for http://example5123.com.", job.Message)
	})

	t.Run("a failed job with no message falls back to the status extension, the exception, then a default", func(t *testing.T) {
		for body, want := range map[string]string{
			`{"status":"error","status_ext":"error:blocked"}`: "error:blocked",
			`{"status":"error","exception":"boom"}`:           "boom",
			`{"status":"error"}`:                              "the capture failed",
		} {
			client, _ := fakeArchive(t, 200, body)
			job, err := client.Status(ctx, "j")
			require.NoError(t, err, body)
			assert.Equal(t, want, job.Message, body)
		}
	})

	t.Run("pending, and anything unrecognized, is not a result yet", func(t *testing.T) {
		for _, body := range []string{`{"status":"pending","resources":[]}`, `{"status":"queued"}`, `{}`} {
			client, _ := fakeArchive(t, 200, body)
			job, err := client.Status(ctx, "j")
			require.NoError(t, err, body)
			assert.Equal(t, wayback.StatePending, job.State, body)
		}
	})

	t.Run("a job id is escaped into the path", func(t *testing.T) {
		client, got := fakeArchive(t, 200, `{"status":"pending"}`)
		_, err := client.Status(ctx, "a/b c")
		require.NoError(t, err)
		assert.Equal(t, "/save/status/a/b c", (*got)[0].path, "decoded by the server; the wire form was escaped")
	})

	t.Run("an HTTP failure is an APIError", func(t *testing.T) {
		client, _ := fakeArchive(t, 404, `{"message":"job not found"}`)
		_, err := client.Status(ctx, "gone")
		var apiErr *wayback.APIError
		require.ErrorAs(t, err, &apiErr)
		assert.Equal(t, 404, apiErr.StatusCode)
		assert.Equal(t, "job not found", apiErr.Message)
	})
}
