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

// Package urlnorm computes captures.normalized_url / pages.normalized_url
// via a pipeline of independent normalization Steps, rather than a single
// hardcoded function. Today the pipeline is ClearURLs (tracking-parameter
// stripping and redirect-wrapper unwrapping) followed by Recueil's own
// additional canonicalization (host casing, default ports, fragment,
// query-param ordering, trailing slash). Deliberately structured this way so
// a future third-party library, or a hand-rolled Recueil-specific ruleset,
// can be added as one more Step without any existing step needing to know
// about it.
package urlnorm

import (
	"context"
	"fmt"
)

// Step is a single normalization pass: it transforms a URL string and
// returns the (possibly unchanged) result. Steps are pure functions of
// their own input -- no shared state, no assumptions about what ran
// before or after them beyond "my output is the next step's input" --
// which is what makes them freely composable in a Pipeline.
//
// context.Context is threaded through even though no current Step
// performs I/O (ClearURLs and Canonicalize are both pure in-memory
// string/regex work): every other package in this codebase that does
// meaningful work takes a context, and a future Step (e.g. one that calls
// out to an external URL-unshortening service) plausibly would too. Adding
// it now avoids a breaking signature change later for that one hypothetical
// step, at no real cost to the steps that don't need it.
type Step interface {
	Normalize(ctx context.Context, rawURL string) (string, error)
}

// StepFunc adapts a plain function to the Step interface -- the same
// adapter pattern as http.HandlerFunc, for a Step simple enough not to
// need its own named type.
type StepFunc func(ctx context.Context, rawURL string) (string, error)

func (f StepFunc) Normalize(ctx context.Context, rawURL string) (string, error) {
	return f(ctx, rawURL)
}

// Pipeline runs a sequence of Steps in order, feeding each step's output
// into the next step's input.
type Pipeline struct {
	steps []Step
}

// NewPipeline builds a Pipeline from the given Steps, run in the order
// supplied. A zero-Step pipeline is valid and just returns its input
// unchanged -- useful in tests that don't care about normalization.
func NewPipeline(steps ...Step) *Pipeline {
	return &Pipeline{steps: steps}
}

// Normalize runs rawURL through every Step in order, returning the final
// result. If any Step errors, the pipeline stops immediately and returns
// that error wrapped with its position -- a step failing on unexpected
// input (a malformed URL, say) should be loud, not silently skipped.
func (p *Pipeline) Normalize(ctx context.Context, rawURL string) (string, error) {
	current := rawURL
	for i, step := range p.steps {
		next, err := step.Normalize(ctx, current)
		if err != nil {
			return "", fmt.Errorf("urlnorm: pipeline step %d: %w", i, err)
		}
		current = next
	}
	return current, nil
}
