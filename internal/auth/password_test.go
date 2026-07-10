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
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/bcrypt"
)

func TestHashPassword(t *testing.T) {
	password := "correct horse battery staple"

	hash, err := HashPassword(password)
	require.NoError(t, err)

	t.Run("produces a hash usable by VerifyPassword", func(t *testing.T) {
		assert.True(t, VerifyPassword(hash, password))
		assert.False(t, VerifyPassword(hash, "wrong password"))
	})

	t.Run("hash is not the plaintext password", func(t *testing.T) {
		assert.NotEqual(t, password, hash)
	})

	t.Run("encodes the configured cost factor", func(t *testing.T) {
		cost, err := bcrypt.Cost([]byte(hash))
		require.NoError(t, err)
		assert.Equal(t, bcryptCost, cost)
	})

	t.Run("two hashes of the same password differ (random salt)", func(t *testing.T) {
		hash2, err := HashPassword(password)
		require.NoError(t, err)
		assert.NotEqual(t, hash, hash2)
		assert.True(t, VerifyPassword(hash2, password), "a differently-salted hash must still verify")
	})

	t.Run("72 bytes is accepted", func(t *testing.T) {
		p := strings.Repeat("a", 72)
		h, err := HashPassword(p)
		require.NoError(t, err)
		assert.True(t, VerifyPassword(h, p))
	})

	t.Run("73 bytes is rejected, not silently truncated", func(t *testing.T) {
		p := strings.Repeat("a", 73)
		_, err := HashPassword(p)
		assert.Error(t, err)
	})
}

func TestVerifyPassword(t *testing.T) {
	password := "correct horse battery staple"
	hash, err := HashPassword(password)
	require.NoError(t, err)

	tests := []struct {
		name     string
		hash     string
		password string
		want     bool
	}{
		{name: "correct password", hash: hash, password: password, want: true},
		{name: "wrong password", hash: hash, password: "incorrect", want: false},
		{name: "malformed hash (not bcrypt at all)", hash: "not-a-real-hash", password: password, want: false},
		{name: "empty hash", hash: "", password: password, want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, VerifyPassword(tt.hash, tt.password))
		})
	}
}
