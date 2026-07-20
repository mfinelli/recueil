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

package screenshot

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// TestBackoff lives in an internal (package screenshot, not
// screenshot_test) file specifically because backoff is unexported --
// per this repo's own testing conventions, that's the one case worth
// breaking from the external-test-package default.
func TestBackoff(t *testing.T) {
	tests := []struct {
		name     string
		attempts int32
		want     time.Duration
	}{
		{name: "first attempt", attempts: 1, want: 30 * time.Second},
		{name: "second attempt doubles", attempts: 2, want: 60 * time.Second},
		{name: "third attempt doubles again", attempts: 3, want: 120 * time.Second},
		{name: "eventually caps at maxBackoff", attempts: 8, want: 30 * time.Minute},
		{name: "a pathologically large attempts count still caps", attempts: 1000, want: 30 * time.Minute},
		{name: "zero is treated the same as one", attempts: 0, want: 30 * time.Second},
		{name: "negative is treated the same as one", attempts: -5, want: 30 * time.Second},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, backoff(tt.attempts))
		})
	}
}
