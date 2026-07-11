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
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
)

// PairingTokenPrefix mirrors the human-recognizable prefix convention used
// for every other token in this system (rcl_sess_, rcl_bootstrap_,
// rcl_live_).
const PairingTokenPrefix = "rcl_pair_"

// PairingKey is the AES-256 key (PAIRING_TOKEN_KEY in config) used to
// reversibly encrypt/decrypt the pairing token stored in Postgres. See
// DESIGN.md §5: this is a deliberate departure from how every other
// credential in this system is stored -- the pairing token is closer in
// kind to an API key than a password, and the dashboard needs to be able
// to redisplay it on demand.
//
// Operator-generated, once, and not intended to be rotated in the normal
// case: rotating it makes every already-encrypted pairing_token_enc value
// permanently undecryptable (equivalent to revoking every account's
// pairing token simultaneously).
type PairingKey [32]byte

var ErrInvalidPairingKey = errors.New("pairing token key must decode (base64) to exactly 32 bytes")

// ParsePairingKey decodes a base64-encoded 32-byte key, as loaded from
// config's pairing_token_key value.
func ParsePairingKey(encoded string) (PairingKey, error) {
	var key PairingKey
	raw, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return key, fmt.Errorf("decoding pairing token key: %w", err)
	}
	if len(raw) != 32 {
		return key, ErrInvalidPairingKey
	}
	copy(key[:], raw)
	return key, nil
}

// GeneratePairingToken returns a new random pairing token (raw, to encrypt
// for Postgres storage and hand back to the person) and its SHA-256 hex
// hash (to mirror to D1 for Worker-side verification at pairing time --
// see HashToken). Same entropy/shape as session and bearer tokens: a
// 32-byte CSPRNG value, hashed at rest wherever a one-way form suffices.
func GeneratePairingToken() (raw, hash string, err error) {
	buf := make([]byte, 32)
	if _, err = rand.Read(buf); err != nil {
		return "", "", err
	}
	raw = PairingTokenPrefix + base64.RawURLEncoding.EncodeToString(buf)
	hash = HashToken(raw)
	return raw, hash, nil
}

// EncryptPairingToken seals raw under key using AES-256-GCM, returning a
// base64-encoded nonce||ciphertext blob suitable for storing in
// users.pairing_token_enc.
func EncryptPairingToken(key PairingKey, raw string) (string, error) {
	gcm, err := newPairingGCM(key)
	if err != nil {
		return "", err
	}

	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}

	sealed := gcm.Seal(nonce, nonce, []byte(raw), nil)
	return base64.StdEncoding.EncodeToString(sealed), nil
}

var ErrPairingCiphertextTooShort = errors.New("pairing token ciphertext shorter than nonce")

// DecryptPairingToken reverses EncryptPairingToken.
func DecryptPairingToken(key PairingKey, enc string) (string, error) {
	sealed, err := base64.StdEncoding.DecodeString(enc)
	if err != nil {
		return "", err
	}

	gcm, err := newPairingGCM(key)
	if err != nil {
		return "", err
	}

	if len(sealed) < gcm.NonceSize() {
		return "", ErrPairingCiphertextTooShort
	}
	nonce, ciphertext := sealed[:gcm.NonceSize()], sealed[gcm.NonceSize():]

	plaintext, err := gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return "", err
	}
	return string(plaintext), nil
}

func newPairingGCM(key PairingKey) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}
