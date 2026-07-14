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

// newTestClearURLs builds a ClearURLs against the real embedded ruleset,
// not a synthetic fixture -- so a change in this package's parsing/matching
// logic that breaks real providers is caught here, not only in production.
func newTestClearURLs(t *testing.T) *urlnorm.ClearURLs {
	t.Helper()
	c, err := urlnorm.NewClearURLs()
	require.NoError(t, err, "the entire real vendored ruleset must parse and every pattern must compile")
	return c
}

func TestNewClearURLs_LoadsRealRuleset(t *testing.T) {
	// This is deliberately its own test, not folded into
	// newTestClearURLs's require.NoError above: it's the one test in this
	// file that specifically validates the *whole* embedded ruleset --
	// every one of its 200+ providers' patterns -- compiles cleanly
	// against regexp2, which was the entire reason regexp2 was chosen
	// over stdlib RE2 in the first place. A failure here means the
	// vendored snapshot itself has a pattern regexp2 can't handle, not a
	// bug in a specific test case below.
	_ = newTestClearURLs(t)
}

func TestClearURLs_Normalize(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "strips a globalRules tracking param (utm_source) from an arbitrary, otherwise-unrecognized site",
			input: "https://some-random-blog.example/post?utm_source=newsletter&id=42",
			want:  "https://some-random-blog.example/post?id=42",
		},
		{
			name:  "strips multiple globalRules tracking params at once",
			input: "https://some-random-blog.example/post?utm_source=a&utm_medium=b&utm_campaign=c&id=42",
			want:  "https://some-random-blog.example/post?id=42",
		},
		{
			name:  "strips Google's own 'ved' search-tracking param",
			input: "https://www.google.com/search?q=recueil&ved=abc123",
			want:  "https://www.google.com/search?q=recueil",
		},
		{
			name:  "strips Amazon's referral 'tag' query param",
			input: "https://www.amazon.com/dp/EXAMPLE?tag=someaffiliate-20",
			want:  "https://www.amazon.com/dp/EXAMPLE",
		},
		{
			name:  "strips Amazon's '/ref=...' path suffix via its rawRules entry",
			input: "https://www.amazon.com/dp/EXAMPLE/ref=sxin_0_pb",
			want:  "https://www.amazon.com/dp/EXAMPLE",
		},
		{
			name:  "leaves an unrelated, non-tracking query param alone",
			input: "https://some-random-blog.example/search?q=hello+world",
			want:  "https://some-random-blog.example/search?q=hello+world",
		},
		{
			name:  "unwraps a Google redirect-wrapper URL to its real destination",
			input: "https://www.google.com/url?q=https://destination.example/page&sa=D",
			want:  "https://destination.example/page",
		},
	}

	c := newTestClearURLs(t)
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := c.Normalize(context.Background(), tt.input)
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestClearURLs_Normalize_FieldRulesAreAnchored(t *testing.T) {
	c := newTestClearURLs(t)

	// "ved" is a real Google rule; "vedette" is NOT -- if field-rule
	// matching weren't correctly anchored (^rule$), a naive substring or
	// prefix match could incorrectly strip "vedette" too, which would be
	// a real, silent data-loss bug (deleting a query param the site
	// actually needs). This test exists specifically to catch that class
	// of regression, not just to confirm the happy path above.
	got, err := c.Normalize(context.Background(), "https://www.google.com/search?q=x&vedette=keep-me")
	require.NoError(t, err)
	assert.Contains(t, got, "vedette=keep-me")
}

func TestClearURLs_Normalize_Determinism(t *testing.T) {
	c := newTestClearURLs(t)
	input := "https://www.amazon.com/dp/EXAMPLE/ref=sxin_0_pb?tag=someaffiliate-20&utm_source=x"

	first, err := c.Normalize(context.Background(), input)
	require.NoError(t, err)
	second, err := c.Normalize(context.Background(), input)
	require.NoError(t, err)

	assert.Equal(t, first, second)
}

func TestClearURLs_Normalize_IsIdempotent(t *testing.T) {
	c := newTestClearURLs(t)
	input := "https://www.amazon.com/dp/EXAMPLE/ref=sxin_0_pb?tag=someaffiliate-20&utm_source=x"

	once, err := c.Normalize(context.Background(), input)
	require.NoError(t, err)
	twice, err := c.Normalize(context.Background(), once)
	require.NoError(t, err)

	assert.Equal(t, once, twice, "normalizing an already-normalized URL must not change it further")
}
