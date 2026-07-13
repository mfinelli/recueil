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

// Deliberately package r2, not r2_test: pointing the S3 client at a fake
// httptest.Server requires constructing a Client with a custom-configured
// *s3.Client (baseURL override), which New()'s public API intentionally
// doesn't expose -- this package always talks to R2, and a public
// "custom endpoint" knob would exist for no reason other than testing.
// This is the same "genuinely needed unexported access" reasoning
// internal/auth's tests already use (see IMPLEMENTATION.md).
package r2

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newTestClient(t *testing.T, baseURL string) *Client {
	t.Helper()

	awsCfg, err := config.LoadDefaultConfig(context.Background(),
		config.WithCredentialsProvider(credentials.NewStaticCredentialsProvider("test-key", "test-secret", "")),
		config.WithRegion("auto"),
	)
	require.NoError(t, err)

	s3Client := s3.NewFromConfig(awsCfg, func(o *s3.Options) {
		o.BaseEndpoint = aws.String(baseURL)
		o.UsePathStyle = true
	})

	return &Client{s3: s3Client, bucket: "test-bucket"}
}

func TestNew(t *testing.T) {
	// LoadDefaultConfig + a static credentials provider resolves entirely
	// in-memory -- no network call happens at construction time, only on
	// the first real request -- so this can assert success without a
	// fake server.
	client, err := New(Config{
		AccountID:       "test-account",
		BucketName:      "test-bucket",
		AccessKeyID:     "test-key",
		AccessKeySecret: "test-secret",
	})
	require.NoError(t, err)
	assert.NotNil(t, client)
	assert.Equal(t, "test-bucket", client.bucket)
}

func TestClient_Get(t *testing.T) {
	t.Run("returns the object body and the request hits the expected path-style URL", func(t *testing.T) {
		var gotMethod, gotPath string
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			gotMethod = r.Method
			gotPath = r.URL.Path
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("<html>hello</html>"))
		}))
		defer server.Close()

		client := newTestClient(t, server.URL)
		body, err := client.Get(context.Background(), "pending/1/capture-abc/page.html")
		require.NoError(t, err)
		defer func() { _ = body.Close() }()

		data, err := io.ReadAll(body)
		require.NoError(t, err)
		assert.Equal(t, "<html>hello</html>", string(data))

		assert.Equal(t, http.MethodGet, gotMethod)
		// Path-style: /<bucket>/<key>, confirming UsePathStyle actually
		// took effect rather than the SDK falling back to virtual-hosted
		// addressing (which would leave this server never receiving the
		// request at all, since httptest.Server only listens on one host).
		assert.Equal(t, "/test-bucket/pending/1/capture-abc/page.html", gotPath)
	})

	t.Run("returns an error on a non-2xx response", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`<?xml version="1.0" encoding="UTF-8"?>
<Error><Code>NoSuchKey</Code><Message>The specified key does not exist.</Message></Error>`))
		}))
		defer server.Close()

		client := newTestClient(t, server.URL)
		_, err := client.Get(context.Background(), "does-not-exist.html")
		require.Error(t, err)
	})
}

func TestClient_Delete(t *testing.T) {
	t.Run("sends a DELETE to the expected path-style URL", func(t *testing.T) {
		var gotMethod, gotPath string
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			gotMethod = r.Method
			gotPath = r.URL.Path
			w.WriteHeader(http.StatusNoContent)
		}))
		defer server.Close()

		client := newTestClient(t, server.URL)
		err := client.Delete(context.Background(), "pending/1/capture-abc/page.html")
		require.NoError(t, err)

		assert.Equal(t, http.MethodDelete, gotMethod)
		assert.Equal(t, "/test-bucket/pending/1/capture-abc/page.html", gotPath)
	})

	t.Run("returns an error on a non-2xx response", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`<?xml version="1.0" encoding="UTF-8"?>
<Error><Code>AccessDenied</Code><Message>Access Denied</Message></Error>`))
		}))
		defer server.Close()

		client := newTestClient(t, server.URL)
		err := client.Delete(context.Background(), "some/key.html")
		require.Error(t, err)
	})
}
