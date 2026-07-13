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
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mfinelli/recueil/internal/urlnorm"
)

// appendStep is a trivial Step for composition tests: it appends a fixed
// suffix to whatever URL string it receives, so test assertions can read
// the accumulated suffixes off the final output to confirm both ordering
// and that each step really did receive the previous step's output (not,
// say, the pipeline's original input every time).
func appendStep(suffix string) urlnorm.StepFunc {
	return func(_ context.Context, rawURL string) (string, error) {
		return rawURL + suffix, nil
	}
}

func TestPipeline_Normalize(t *testing.T) {
	t.Run("empty pipeline returns input unchanged", func(t *testing.T) {
		p := urlnorm.NewPipeline()
		got, err := p.Normalize(context.Background(), "https://example.com")
		require.NoError(t, err)
		assert.Equal(t, "https://example.com", got)
	})

	t.Run("single step applies once", func(t *testing.T) {
		p := urlnorm.NewPipeline(appendStep("-a"))
		got, err := p.Normalize(context.Background(), "start")
		require.NoError(t, err)
		assert.Equal(t, "start-a", got)
	})

	t.Run("multiple steps run in order, each fed the previous step's output", func(t *testing.T) {
		p := urlnorm.NewPipeline(appendStep("-a"), appendStep("-b"), appendStep("-c"))
		got, err := p.Normalize(context.Background(), "start")
		require.NoError(t, err)
		assert.Equal(t, "start-a-b-c", got)
	})

	t.Run("a failing step stops the pipeline and its error is wrapped with position", func(t *testing.T) {
		boom := errors.New("boom")
		failingStep := urlnorm.StepFunc(func(_ context.Context, _ string) (string, error) {
			return "", boom
		})
		neverCalled := urlnorm.StepFunc(func(_ context.Context, rawURL string) (string, error) {
			t.Fatal("step after a failing step must not run")
			return rawURL, nil
		})

		p := urlnorm.NewPipeline(appendStep("-a"), failingStep, neverCalled)
		_, err := p.Normalize(context.Background(), "start")

		require.Error(t, err)
		assert.ErrorIs(t, err, boom)
		assert.Contains(t, err.Error(), "step 1")
	})
}
