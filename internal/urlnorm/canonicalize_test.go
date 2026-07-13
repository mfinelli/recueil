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

package urlnorm_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mfinelli/recueil/internal/urlnorm"
)

func TestCanonicalize_Normalize(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "lowercases the host",
			input: "https://ExAmPlE.COM/Path",
			want:  "https://example.com/Path",
		},
		{
			name:  "strips the default https port",
			input: "https://example.com:443/page",
			want:  "https://example.com/page",
		},
		{
			name:  "strips the default http port",
			input: "http://example.com:80/page",
			want:  "http://example.com/page",
		},
		{
			name:  "keeps a non-default port",
			input: "https://example.com:8443/page",
			want:  "https://example.com:8443/page",
		},
		{
			name:  "keeps the default port number if it doesn't match the scheme (edge case, not stripped)",
			input: "http://example.com:443/page",
			want:  "http://example.com:443/page",
		},
		{
			name:  "handles an IPv6 host with a non-default port correctly (brackets preserved)",
			input: "http://[::1]:8080/page",
			want:  "http://[::1]:8080/page",
		},
		{
			name:  "handles an IPv6 host with the default port (brackets dropped along with the port)",
			input: "http://[::1]:80/page",
			want:  "http://[::1]/page",
		},
		{
			name:  "lowercases the scheme",
			input: "HTTPS://example.com/page",
			want:  "https://example.com/page",
		},
		{
			name:  "lowercases an uppercase scheme AND correctly still strips the now-matching default port",
			input: "HTTPS://example.com:443/page",
			want:  "https://example.com/page",
		},
		{
			name:  "removes anchor tags",
			input: "https://example.com/page#section-2",
			want:  "https://example.com/page",
		},
		{
			name:  "sorts query parameters alphabetically",
			input: "https://example.com/page?zeta=1&alpha=2&mid=3",
			want:  "https://example.com/page?alpha=2&mid=3&zeta=1",
		},
		{
			name:  "strips a trailing slash from a non-root path",
			input: "https://example.com/page/",
			want:  "https://example.com/page",
		},
		{
			name:  "collapses a bare root slash the same as no path at all",
			input: "https://example.com/",
			want:  "https://example.com",
		},
		{
			name:  "combines all canonicalizations together",
			input: "HTTPS://Example.COM:443/Page/?b=2&a=1#frag",
			want:  "https://example.com/Page?a=1&b=2",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := urlnorm.Canonicalize{}
			got, err := c.Normalize(context.Background(), tt.input)
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}

	t.Run("is idempotent (canonicalizing an already-canonical URL is a no-op)", func(t *testing.T) {
		c := urlnorm.Canonicalize{}
		once, err := c.Normalize(context.Background(), "https://example.com/page?a=1&b=2")
		require.NoError(t, err)
		twice, err := c.Normalize(context.Background(), once)
		require.NoError(t, err)
		assert.Equal(t, once, twice)
	})

	t.Run("rejects an unparseable URL", func(t *testing.T) {
		c := urlnorm.Canonicalize{}
		_, err := c.Normalize(context.Background(), "://not-a-valid-url")
		require.Error(t, err)
	})
}
