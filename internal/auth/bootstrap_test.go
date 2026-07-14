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

package auth

import (
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewBootstrapTokenHolder(t *testing.T) {
	holder, raw, err := NewBootstrapTokenHolder()
	require.NoError(t, err)
	require.NotNil(t, holder)

	assert.True(t, strings.HasPrefix(raw, "rcl_bootstrap_"))
	assert.Equal(t, raw, holder.token)
	assert.WithinDuration(t, time.Now().Add(bootstrapTokenTTL), holder.expiresAt, time.Second)
	assert.False(t, holder.consumed)

	_, raw2, err := NewBootstrapTokenHolder()
	require.NoError(t, err)
	assert.NotEqual(t, raw, raw2, "two holders should never produce the same token")
}

func TestUse(t *testing.T) {
	sentinelFnErr := errors.New("boom")

	tests := []struct {
		name         string
		mangleToken  func(actual string) string
		expired      bool
		fnErr        error
		wantFnCalled bool
		wantErrIs    error // checked via errors.Is when set
		wantConsumed bool
	}{
		{
			name:         "correct token, fn succeeds: consumes and returns nil",
			mangleToken:  func(actual string) string { return actual },
			wantFnCalled: true,
			wantConsumed: true,
		},
		{
			name:         "wrong token: rejected, fn never runs, stays unconsumed",
			mangleToken:  func(actual string) string { return actual + "-wrong" },
			wantFnCalled: false,
			wantErrIs:    ErrInvalidBootstrapToken,
			wantConsumed: false,
		},
		{
			name:         "expired token: rejected even though the value matches",
			mangleToken:  func(actual string) string { return actual },
			expired:      true,
			wantFnCalled: false,
			wantErrIs:    ErrInvalidBootstrapToken,
			wantConsumed: false,
		},
		{
			name:         "fn fails: token NOT consumed, error passed through unwrapped",
			mangleToken:  func(actual string) string { return actual },
			fnErr:        sentinelFnErr,
			wantFnCalled: true,
			wantErrIs:    sentinelFnErr,
			wantConsumed: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			holder, raw, err := NewBootstrapTokenHolder()
			require.NoError(t, err)
			if tt.expired {
				holder.expiresAt = time.Now().Add(-time.Minute)
			}

			var fnCalled bool
			err = holder.Use(tt.mangleToken(raw), func() error {
				fnCalled = true
				return tt.fnErr
			})

			assert.Equal(t, tt.wantFnCalled, fnCalled)
			assert.Equal(t, tt.wantConsumed, holder.consumed)
			if tt.wantErrIs != nil {
				assert.ErrorIs(t, err, tt.wantErrIs)
			} else {
				assert.NoError(t, err)
			}
		})
	}

	// Doesn't fit the table above: needs two sequential calls on the same
	// holder. This is the exact scenario the httpapi smoke test caught
	// earlier (setup succeeding, then a retry with the same token failing).
	t.Run("reusing an already-consumed token is rejected", func(t *testing.T) {
		holder, raw, err := NewBootstrapTokenHolder()
		require.NoError(t, err)

		require.NoError(t, holder.Use(raw, func() error { return nil }))

		var secondCallRan bool
		err = holder.Use(raw, func() error {
			secondCallRan = true
			return nil
		})
		assert.ErrorIs(t, err, ErrInvalidBootstrapToken)
		assert.False(t, secondCallRan, "fn must not run again for a consumed token")
	})

	// The mutex exists specifically so concurrent setup requests can't both
	// create an admin
	t.Run("concurrent Use calls with the correct token: exactly one succeeds", func(t *testing.T) {
		holder, raw, err := NewBootstrapTokenHolder()
		require.NoError(t, err)

		const goroutines = 50
		var successes int64
		var wg sync.WaitGroup
		wg.Add(goroutines)
		for range goroutines {
			go func() {
				defer wg.Done()
				_ = holder.Use(raw, func() error {
					atomic.AddInt64(&successes, 1)
					return nil
				})
			}()
		}
		wg.Wait()

		assert.Equal(t, int64(1), successes, "fn must run exactly once even under concurrent Use calls")
	})
}
