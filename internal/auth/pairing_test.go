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
	"crypto/rand"
	"encoding/base64"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func encodeRawForTest(raw []byte) string {
	return base64.StdEncoding.EncodeToString(raw)
}

func testKey(t *testing.T) PairingKey {
	t.Helper()
	var key PairingKey
	_, err := rand.Read(key[:])
	require.NoError(t, err)
	return key
}

func TestParsePairingKey(t *testing.T) {
	t.Run("valid 32-byte base64 key decodes", func(t *testing.T) {
		want := testKey(t)
		encoded := encodeKeyForTest(want)

		got, err := ParsePairingKey(encoded)
		require.NoError(t, err)
		assert.Equal(t, want, got)
	})

	t.Run("invalid base64 is rejected", func(t *testing.T) {
		_, err := ParsePairingKey("not valid base64!!!")
		assert.Error(t, err)
	})

	t.Run("wrong length (too short) is rejected", func(t *testing.T) {
		_, err := ParsePairingKey(encodeRawForTest([]byte("too-short")))
		assert.ErrorIs(t, err, ErrInvalidPairingKey)
	})

	t.Run("wrong length (too long) is rejected", func(t *testing.T) {
		_, err := ParsePairingKey(encodeRawForTest(make([]byte, 64)))
		assert.ErrorIs(t, err, ErrInvalidPairingKey)
	})
}

func TestGeneratePairingToken(t *testing.T) {
	raw, hash, err := GeneratePairingToken()
	require.NoError(t, err)

	assert.True(t, strings.HasPrefix(raw, PairingTokenPrefix))
	assert.Equal(t, HashToken(raw), hash, "returned hash must match HashToken(raw)")
	assert.Len(t, hash, 64, "SHA-256 hex-encoded should be 64 characters")

	raw2, hash2, err := GeneratePairingToken()
	require.NoError(t, err)
	assert.NotEqual(t, raw, raw2, "two tokens should never collide")
	assert.NotEqual(t, hash, hash2)
}

func TestEncryptDecryptPairingToken(t *testing.T) {
	key := testKey(t)
	raw, _, err := GeneratePairingToken()
	require.NoError(t, err)

	t.Run("round-trips correctly", func(t *testing.T) {
		enc, err := EncryptPairingToken(key, raw)
		require.NoError(t, err)
		assert.NotEqual(t, raw, enc, "ciphertext must not equal the plaintext")

		got, err := DecryptPairingToken(key, enc)
		require.NoError(t, err)
		assert.Equal(t, raw, got)
	})

	t.Run("two encryptions of the same token differ (random nonce)", func(t *testing.T) {
		enc1, err := EncryptPairingToken(key, raw)
		require.NoError(t, err)
		enc2, err := EncryptPairingToken(key, raw)
		require.NoError(t, err)
		assert.NotEqual(t, enc1, enc2)

		got1, err := DecryptPairingToken(key, enc1)
		require.NoError(t, err)
		got2, err := DecryptPairingToken(key, enc2)
		require.NoError(t, err)
		assert.Equal(t, raw, got1)
		assert.Equal(t, raw, got2)
	})

	t.Run("decrypting with the wrong key fails", func(t *testing.T) {
		enc, err := EncryptPairingToken(key, raw)
		require.NoError(t, err)

		wrongKey := testKey(t)
		_, err = DecryptPairingToken(wrongKey, enc)
		assert.Error(t, err)
	})

	t.Run("decrypting malformed (non-base64) ciphertext fails", func(t *testing.T) {
		_, err := DecryptPairingToken(key, "not valid base64!!!")
		assert.Error(t, err)
	})

	t.Run("decrypting a too-short blob fails", func(t *testing.T) {
		_, err := DecryptPairingToken(key, encodeRawForTest([]byte("short")))
		assert.ErrorIs(t, err, ErrPairingCiphertextTooShort)
	})

	t.Run("decrypting tampered ciphertext fails (authenticated encryption)", func(t *testing.T) {
		enc, err := EncryptPairingToken(key, raw)
		require.NoError(t, err)

		tampered := []byte(enc)
		// Flip a byte past the base64 header to corrupt the sealed payload
		// without producing an invalid base64 string.
		tampered[len(tampered)-1] ^= 0x01
		_, err = DecryptPairingToken(key, string(tampered))
		assert.Error(t, err)
	})
}

func encodeKeyForTest(key PairingKey) string {
	return encodeRawForTest(key[:])
}
