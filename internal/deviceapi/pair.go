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

// Package deviceapi is what a paired device (today: the CLI; the shape
// generalizes if other Go-side device code ever needs it) does against
// the Worker's public, device-facing endpoints -- distinct from
// internal/mirror (backend-to-D1 push) and internal/ingest.WorkerClient
// (service-secret-gated backend polling): both of those authenticate as
// the backend itself, never as a device.
//
// Pair and Client are deliberately separate, not one unified type: POST
// /pair is unauthenticated by nature (it's how a device obtains a bearer
// token in the first place, so it can't require one), while everything
// else here requires a token already in hand. Forcing both into one
// Client would mean either a Client usable before it has real
// credentials, or a separate constructor path for pairing anyway --
// no simpler than just keeping them apart.
package deviceapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// PairResult is exactly what a successful POST /pair returns.
type PairResult struct {
	Token      string
	DeviceID   int64
	DeviceName string
}

// Pair exchanges a pairing token for a device bearer token. deviceType is
// accepted as a parameter (rather than hardcoded to "cli") so this
// function stays reusable if other Go-side device code ever needs it, per
// the package doc above -- today's only caller (`recueil auth`) always
// passes "cli".
func Pair(ctx context.Context, workerURL, pairingToken, deviceName, deviceType string) (*PairResult, error) {
	body, err := json.Marshal(struct {
		PairingToken string `json:"pairing_token"`
		DeviceName   string `json:"device_name"`
		DeviceType   string `json:"device_type"`
	}{
		PairingToken: pairingToken,
		DeviceName:   deviceName,
		DeviceType:   deviceType,
	})
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		strings.TrimRight(workerURL, "/")+"/pair", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", userAgent)

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("deviceapi: pairing request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusCreated {
		return nil, fmt.Errorf("deviceapi: pairing failed: status %d", resp.StatusCode)
	}

	var parsed struct {
		Token      string `json:"token"`
		DeviceID   int64  `json:"device_id"`
		DeviceName string `json:"device_name"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return nil, fmt.Errorf("deviceapi: decoding pairing response: %w", err)
	}

	return &PairResult{
		Token:      parsed.Token,
		DeviceID:   parsed.DeviceID,
		DeviceName: parsed.DeviceName,
	}, nil
}
