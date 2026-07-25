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

package slug

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestGenerate(t *testing.T) {
	t.Run("table", func(t *testing.T) {
		cases := []struct {
			name string
			in   string
			want string
		}{
			{"simple lowercase", "recipes", "recipes"},
			{"mixed case", "My Recipes", "my-recipes"},
			{"internal punctuation collapses to one hyphen", "C++", "c"},
			{"internal punctuation collapses to one hyphen mid-word", "rock & roll", "rock-roll"},
			{"accented latin transliterates", "café", "cafe"},
			{"multiple accents", "naïve résumé", "naive-resume"},
			{"leading and trailing junk trimmed", "  --Go!--  ", "go"},
			{"numeric-only name", "42", "42"},
			{"already-hyphenated stays stable", "js-notes", "js-notes"},
			{"doubled separators collapse", "a   b---c", "a-b-c"},
			{"pure emoji yields empty", "🎉🎉🎉", ""},
			{"pure CJK yields empty", "日本語", ""},
			{"mixed script keeps the latin part", "日本語 notes", "notes"},
			{"empty input yields empty", "", ""},
		}

		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				assert.Equal(t, tc.want, Generate(tc.in))
			})
		}
	})

	t.Run("truncates to MaxLength and doesn't leave a trailing hyphen", func(t *testing.T) {
		// 70 word-characters separated by nothing, so the raw truncation
		// point can't accidentally land on a hyphen by construction --
		// this case is exercising the length cap itself, not the
		// trailing-hyphen trim (that's the next case).
		in := strings.Repeat("a", 70)
		got := Generate(in)
		assert.Len(t, got, MaxLength)
		assert.Equal(t, strings.Repeat("a", MaxLength), got)
	})

	t.Run("truncation landing on a hyphen trims it", func(t *testing.T) {
		// 62 a's then a space then more content: the space becomes a
		// hyphen at position 63 (index 62), landing exactly at
		// MaxLength, so the naive slice would end in "-" before the
		// trim.
		in := strings.Repeat("a", 62) + " " + "bbbb"
		got := Generate(in)
		assert.LessOrEqual(t, len(got), MaxLength)
		assert.False(t, strings.HasSuffix(got, "-"))
		assert.Equal(t, strings.Repeat("a", 62), got)
	})
}

func TestValid(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want bool
	}{
		{"simple lowercase", "recipes", true},
		{"hyphenated", "my-recipes", true},
		{"numeric segment", "42", true},
		{"mixed alnum segments", "js-2026", true},
		{"empty", "", false},
		{"uppercase rejected", "Recipes", false},
		{"leading hyphen rejected", "-recipes", false},
		{"trailing hyphen rejected", "recipes-", false},
		{"doubled hyphen rejected", "my--recipes", false},
		{"space rejected", "my recipes", false},
		{"underscore rejected", "my_recipes", false},
		{"unicode rejected", "café", false},
		{"exactly max length accepted", strings.Repeat("a", MaxLength), true},
		{"over max length rejected", strings.Repeat("a", MaxLength+1), false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, Valid(tc.in))
		})
	}
}
