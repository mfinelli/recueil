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
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mfinelli/recueil/internal/dbtest"
)

// manualUploadRequest is manualupload_test.go's request builder because none
// of handlers_test.go's requestWithCookie/requestWithCookieBody fit, since
// this is the only route in the whole API that sends multipart/form-data
// instead of JSON. htmlBytes == nil omits the "html" part entirely (a missing
// file, as opposed to an empty one); faviconBytes == nil likewise omits
// "favicon" entirely, since it's optional. faviconFilename only matters when
// faviconBytes is non-nil because ManualUpload derives the stored extension
// from it.
func manualUploadRequest(t *testing.T, server *httptest.Server, cookie *http.Cookie, urlField string, htmlBytes []byte, faviconFilename string, faviconBytes []byte) *http.Response {
	t.Helper()

	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)

	// The url field is always written, even when urlField is "" and
	// several subtests below specifically want a present-but-empty url
	// to confirm ManualUpload treats that as missing.
	require.NoError(t, w.WriteField("url", urlField))
	if htmlBytes != nil {
		part, err := w.CreateFormFile("html", "capture.html")
		require.NoError(t, err)
		_, err = part.Write(htmlBytes)
		require.NoError(t, err)
	}
	if faviconBytes != nil {
		part, err := w.CreateFormFile("favicon", faviconFilename)
		require.NoError(t, err)
		_, err = part.Write(faviconBytes)
		require.NoError(t, err)
	}
	require.NoError(t, w.Close())

	req, err := http.NewRequest(http.MethodPost, server.URL+"/api/manual-upload", &buf)
	require.NoError(t, err)
	req.Header.Set("Content-Type", w.FormDataContentType())
	req.AddCookie(cookie)

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	return resp
}

func TestManualUpload(t *testing.T) {
	pool := dbtest.Setup(t)
	sampleHTML := []byte(`<html lang="fr"><head><title>Bonjour le monde</title></head><body>hi</body></html>`)

	t.Run("uploads html+url and creates a new page/capture", func(t *testing.T) {
		user := dbtest.CreateUser(t, pool, "member")
		server, _ := newTestServer(t, pool, unreachable)
		cookie := sessionCookieFor(t, pool, &user)

		resp := manualUploadRequest(t, server, cookie, "https://example.com/article", sampleHTML, "", nil)
		require.Equal(t, http.StatusCreated, resp.StatusCode)

		var got struct {
			PageID    int64 `json:"page_id"`
			CaptureID int64 `json:"capture_id"`
		}
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&got))
		assert.NotZero(t, got.PageID)
		assert.NotZero(t, got.CaptureID)

		pageResp := requestWithCookie(t, server, http.MethodGet, "/api/pages/"+itoa(got.PageID), cookie)
		require.Equal(t, http.StatusOK, pageResp.StatusCode)
		var page struct {
			Title    *string `json:"title"`
			Captures []struct {
				ID       int64  `json:"id"`
				Source   string `json:"source"`
				Language string `json:"language"`
			} `json:"captures"`
		}
		require.NoError(t, json.NewDecoder(pageResp.Body).Decode(&page))
		require.NotNil(t, page.Title)
		assert.Equal(t, "Bonjour le monde", *page.Title)
		require.Len(t, page.Captures, 1)
		assert.Equal(t, "manual_upload", page.Captures[0].Source)
		// sampleHTML declares lang="fr" and confirms language detection runs
		// the same as any other capture path, not just "simple" by default.
		assert.Equal(t, "french", page.Captures[0].Language)
	})

	t.Run("a second upload of the same url groups under the same page as a new capture", func(t *testing.T) {
		user := dbtest.CreateUser(t, pool, "member")
		server, _ := newTestServer(t, pool, unreachable)
		cookie := sessionCookieFor(t, pool, &user)

		first := manualUploadRequest(t, server, cookie, "https://example.com/reupload", sampleHTML, "", nil)
		require.Equal(t, http.StatusCreated, first.StatusCode)
		var firstGot struct {
			PageID    int64 `json:"page_id"`
			CaptureID int64 `json:"capture_id"`
		}
		require.NoError(t, json.NewDecoder(first.Body).Decode(&firstGot))

		second := manualUploadRequest(t, server, cookie, "https://example.com/reupload", sampleHTML, "", nil)
		require.Equal(t, http.StatusCreated, second.StatusCode)
		var secondGot struct {
			PageID    int64 `json:"page_id"`
			CaptureID int64 `json:"capture_id"`
		}
		require.NoError(t, json.NewDecoder(second.Body).Decode(&secondGot))

		assert.Equal(t, firstGot.PageID, secondGot.PageID, "same normalized_url -- one page")
		assert.NotEqual(t, firstGot.CaptureID, secondGot.CaptureID, "each upload is its own capture/version")
	})

	t.Run("accepts an svg favicon", func(t *testing.T) {
		user := dbtest.CreateUser(t, pool, "member")
		server, _ := newTestServer(t, pool, unreachable)
		cookie := sessionCookieFor(t, pool, &user)

		favicon := []byte(`<svg xmlns="http://www.w3.org/2000/svg"></svg>`)
		resp := manualUploadRequest(t, server, cookie, "https://example.com/with-favicon", sampleHTML, "favicon.svg", favicon)
		require.Equal(t, http.StatusCreated, resp.StatusCode)

		var got struct {
			PageID int64 `json:"page_id"`
		}
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&got))

		pageResp := requestWithCookie(t, server, http.MethodGet, "/api/pages/"+itoa(got.PageID), cookie)
		var page struct {
			FaviconPath *string `json:"favicon_path"`
		}
		require.NoError(t, json.NewDecoder(pageResp.Body).Decode(&page))
		require.NotNil(t, page.FaviconPath)
	})

	t.Run("rejects a favicon with a disallowed extension", func(t *testing.T) {
		user := dbtest.CreateUser(t, pool, "member")
		server, _ := newTestServer(t, pool, unreachable)
		cookie := sessionCookieFor(t, pool, &user)

		resp := manualUploadRequest(t, server, cookie, "https://example.com/bad-favicon", sampleHTML, "favicon.jpg", []byte("not really a jpeg"))
		assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
	})

	t.Run("missing url is rejected", func(t *testing.T) {
		user := dbtest.CreateUser(t, pool, "member")
		server, _ := newTestServer(t, pool, unreachable)
		cookie := sessionCookieFor(t, pool, &user)

		resp := manualUploadRequest(t, server, cookie, "", sampleHTML, "", nil)
		assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
	})

	t.Run("missing html file is rejected", func(t *testing.T) {
		user := dbtest.CreateUser(t, pool, "member")
		server, _ := newTestServer(t, pool, unreachable)
		cookie := sessionCookieFor(t, pool, &user)

		resp := manualUploadRequest(t, server, cookie, "https://example.com/no-html", nil, "", nil)
		assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
	})

	t.Run("empty html file is rejected", func(t *testing.T) {
		user := dbtest.CreateUser(t, pool, "member")
		server, _ := newTestServer(t, pool, unreachable)
		cookie := sessionCookieFor(t, pool, &user)

		resp := manualUploadRequest(t, server, cookie, "https://example.com/empty-html", []byte{}, "", nil)
		assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
	})

	t.Run("a request over the configured size ceiling is rejected", func(t *testing.T) {
		user := dbtest.CreateUser(t, pool, "member")
		server, _ := newTestServer(t, pool, unreachable)
		cookie := sessionCookieFor(t, pool, &user)

		// testManualUploadMaxBytes is 1MB comfortably exceeded here
		// without needing a near-100MB body.
		oversized := bytes.Repeat([]byte("a"), testManualUploadMaxBytes+1024)
		resp := manualUploadRequest(t, server, cookie, "https://example.com/oversized", oversized, "", nil)
		assert.True(t, resp.StatusCode == http.StatusBadRequest || resp.StatusCode == http.StatusRequestEntityTooLarge,
			"expected a 4xx rejection, got %d", resp.StatusCode)
	})

	t.Run("without a session cookie returns 401", func(t *testing.T) {
		server, _ := newTestServer(t, pool, unreachable)

		var buf bytes.Buffer
		w := multipart.NewWriter(&buf)
		require.NoError(t, w.WriteField("url", "https://example.com/no-auth"))
		part, err := w.CreateFormFile("html", "capture.html")
		require.NoError(t, err)
		_, err = part.Write(sampleHTML)
		require.NoError(t, err)
		require.NoError(t, w.Close())

		req, err := http.NewRequest(http.MethodPost, server.URL+"/api/manual-upload", &buf)
		require.NoError(t, err)
		req.Header.Set("Content-Type", w.FormDataContentType())

		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
	})

	t.Run("a plain JSON body (no multipart) is rejected, not silently ignored", func(t *testing.T) {
		user := dbtest.CreateUser(t, pool, "member")
		server, _ := newTestServer(t, pool, unreachable)
		cookie := sessionCookieFor(t, pool, &user)

		req, err := http.NewRequest(http.MethodPost, server.URL+"/api/manual-upload", strings.NewReader(`{"url":"https://example.com"}`))
		require.NoError(t, err)
		req.Header.Set("Content-Type", "application/json")
		req.AddCookie(cookie)

		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
	})
}

// itoa avoids importing strconv into every call site above -- just one
// small local wrapper instead.
func itoa(i int64) string {
	return strconv.FormatInt(i, 10)
}
