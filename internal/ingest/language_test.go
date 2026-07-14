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

package ingest

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mfinelli/recueil/internal/dbtest"
)

func TestExtractLanguage(t *testing.T) {
	tests := []struct {
		name string
		html string
		want string
	}{
		{"simple english", `<html lang="en"><head></head></html>`, "en"},
		{"region subtag stripped", `<html lang="en-US">`, "en"},
		{"single quotes", `<html lang='fr'>`, "fr"},
		{"uppercase tag and attribute", `<HTML LANG="DE">`, "de"},
		{"other attributes present before lang", `<html class="no-js" lang="pt-BR" data-foo="bar">`, "pt"},
		{"whitespace around the equals sign", `<html lang = "ja">`, "ja"},
		{"no lang attribute at all", `<html><head></head></html>`, ""},
		{"empty document", ``, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractLanguage([]byte(tt.html))
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestIngester_resolveLanguageConfig(t *testing.T) {
	// Read-only against pg_ts_config (a system catalog, untouched by
	// dbtest.Reset's application-table truncation), so no Reset needed
	// here -- these tests don't modify anything.
	pool := dbtest.Setup(t)
	ing := &Ingester{pool: pool}
	ctx := context.Background()

	t.Run("no language tag falls back to simple", func(t *testing.T) {
		got, err := ing.resolveLanguageConfig(ctx, "")
		require.NoError(t, err)
		assert.Equal(t, "simple", got)
	})

	t.Run("an unmapped language tag falls back to simple", func(t *testing.T) {
		// Chinese has no snowball stemmer in Postgres (it needs
		// segmentation, not stemming), so postgresLanguageConfigs
		// deliberately has no entry for it.
		got, err := ing.resolveLanguageConfig(ctx, "zh")
		require.NoError(t, err)
		assert.Equal(t, "simple", got)
	})

	t.Run("a mapped, genuinely-available language resolves to its postgres config", func(t *testing.T) {
		got, err := ing.resolveLanguageConfig(ctx, "en")
		require.NoError(t, err)
		assert.Equal(t, "english", got)
	})

	t.Run("a full BCP 47 tag with a region subtag is not recognized directly", func(t *testing.T) {
		// resolveLanguageConfig itself doesn't strip region subtags --
		// that's extractLanguage's job, upstream of this function.
		// "en-US" has no entry in postgresLanguageConfigs (only "en"
		// does), so this correctly falls back to simple, confirming the
		// two functions' division of responsibility rather than silently
		// double-handling it in both places.
		got, err := ing.resolveLanguageConfig(ctx, "en-US")
		require.NoError(t, err)
		assert.Equal(t, "simple", got)
	})
}

func TestIngester_languageConfigExists(t *testing.T) {
	pool := dbtest.Setup(t)
	ing := &Ingester{pool: pool}
	ctx := context.Background()

	t.Run("a config that ships with every postgres installation", func(t *testing.T) {
		exists, err := ing.languageConfigExists(ctx, "english")
		require.NoError(t, err)
		assert.True(t, exists)
	})

	t.Run("a name that is not a real config", func(t *testing.T) {
		exists, err := ing.languageConfigExists(ctx, "not-a-real-config-name")
		require.NoError(t, err)
		assert.False(t, exists)
	})
}
