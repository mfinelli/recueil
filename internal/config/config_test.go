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

package config

import (
	"testing"

	"github.com/spf13/viper"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func allRequiredSet() {
	viper.Set("database_url", "postgres://x")
	viper.Set("worker_url", "https://worker.example")
	viper.Set("worker_service_secret", "secret")
	viper.Set("pairing_token_key", "dGVzdC1wYWlyaW5nLXRva2VuLWtleS0zMi1ieXRlcyE=")
	viper.Set("cloudflare_account_id", "acct")
	viper.Set("cloudflare_d1_database_id", "db")
	viper.Set("cloudflare_api_token", "token")
	viper.Set("archive_dir", "/var/lib/recueil/archive")
	viper.Set("r2_account_id", "r2acct")
	viper.Set("r2_bucket_name", "recueil-captures")
	viper.Set("r2_access_key_id", "r2key")
	viper.Set("r2_access_key_secret", "r2secret")
}

func TestLoad(t *testing.T) {
	tests := []struct {
		name        string
		setup       func()
		wantErr     bool
		errContains string
		check       func(t *testing.T, cfg Config)
	}{
		{
			name: "all required fields present, unmarshal maps correctly",
			setup: func() {
				allRequiredSet()
			},
			check: func(t *testing.T, cfg Config) {
				assert.Equal(t, "postgres://x", cfg.DatabaseURL)
				assert.Equal(t, "https://worker.example", cfg.WorkerURL)
				assert.Equal(t, "secret", cfg.WorkerServiceSecret)
				assert.Equal(t, "dGVzdC1wYWlyaW5nLXRva2VuLWtleS0zMi1ieXRlcyE=", cfg.PairingTokenKey)
				assert.Equal(t, "acct", cfg.CloudflareAccountID)
				assert.Equal(t, "db", cfg.CloudflareD1DatabaseID)
				assert.Equal(t, "token", cfg.CloudflareAPIToken)
				assert.Equal(t, "/var/lib/recueil/archive", cfg.ArchiveDir)
				assert.Equal(t, "r2acct", cfg.R2AccountID)
				assert.Equal(t, "recueil-captures", cfg.R2BucketName)
				assert.Equal(t, "r2key", cfg.R2AccessKeyID)
				assert.Equal(t, "r2secret", cfg.R2AccessKeySecret)
			},
		},
		{
			name: "defaults apply without cmd/root.go ever running",
			setup: func() {
				allRequiredSet()
			},
			check: func(t *testing.T, cfg Config) {
				assert.Equal(t, ":8080", cfg.ListenAddr)
				assert.True(t, cfg.SessionCookieSecure)
				assert.Equal(t, 1800, cfg.AgentWorkerPollIntervalSeconds)
				assert.Equal(t, 300, cfg.AgentLocalPollIntervalSeconds)
				assert.Equal(t, "http://127.0.0.1:9222", cfg.SidecarURL)
				assert.Equal(t, "127.0.0.1", cfg.SidecarRenderHost)
				assert.Equal(t, 3, cfg.ReadabilityWorkerConcurrency)
				assert.Equal(t, 3, cfg.ReadabilityMaxAttempts)
				assert.Equal(t, "", cfg.AIBaseURL)
				assert.Equal(t, 1, cfg.AIWorkerConcurrency)
				assert.Equal(t, 3, cfg.AIMaxAttempts)
				assert.Equal(t, 300, cfg.AIRequestTimeoutSeconds)
				assert.Equal(t, 0, cfg.AIMaxInputChars, "no default here -- internal/ai applies its own fallback for zero")
				assert.Equal(t, 3, cfg.ScreenshotWorkerConcurrency)
				assert.Equal(t, 3, cfg.ScreenshotMaxAttempts)
			},
		},
		{
			name: "explicit values override defaults",
			setup: func() {
				allRequiredSet()
				viper.Set("listen_addr", ":9090")
				viper.Set("session_cookie_secure", false)
				viper.Set("agent_worker_poll_interval_seconds", 600)
				viper.Set("agent_local_poll_interval_seconds", 10)
			},
			check: func(t *testing.T, cfg Config) {
				assert.Equal(t, ":9090", cfg.ListenAddr)
				assert.False(t, cfg.SessionCookieSecure)
				assert.Equal(t, 600, cfg.AgentWorkerPollIntervalSeconds)
				assert.Equal(t, 10, cfg.AgentLocalPollIntervalSeconds)
			},
		},
		{
			name:        "missing all required fields lists every one",
			setup:       func() {},
			wantErr:     true,
			errContains: "database_url worker_url worker_service_secret pairing_token_key cloudflare_account_id cloudflare_d1_database_id cloudflare_api_token archive_dir r2_account_id r2_bucket_name r2_access_key_id r2_access_key_secret",
		},
		{
			name: "missing a single required field is still caught",
			setup: func() {
				allRequiredSet()
				viper.Set("worker_service_secret", "")
			},
			wantErr:     true,
			errContains: "worker_service_secret",
		},
		{
			name: "missing pairing_token_key is caught too",
			setup: func() {
				allRequiredSet()
				viper.Set("pairing_token_key", "")
			},
			wantErr:     true,
			errContains: "pairing_token_key",
		},
		{
			name: "missing archive_dir is caught too",
			setup: func() {
				allRequiredSet()
				viper.Set("archive_dir", "")
			},
			wantErr:     true,
			errContains: "archive_dir",
		},
		{
			name: "missing an r2 credential is caught too",
			setup: func() {
				allRequiredSet()
				viper.Set("r2_access_key_secret", "")
			},
			wantErr:     true,
			errContains: "r2_access_key_secret",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Load() reads Viper's package-level global state rather than
			// anything passed in, so each case needs a clean slate; without
			// this, an earlier case's viper.Set calls would leak into the
			// next one.
			viper.Reset()
			viper.SetDefault("listen_addr", ":8080")
			viper.SetDefault("session_cookie_secure", true)
			viper.SetDefault("agent_worker_poll_interval_seconds", 1800)
			viper.SetDefault("agent_local_poll_interval_seconds", 300)
			viper.SetDefault("sidecar_url", "http://127.0.0.1:9222")
			viper.SetDefault("sidecar_render_host", "127.0.0.1")
			viper.SetDefault("readability_worker_concurrency", 3)
			viper.SetDefault("readability_max_attempts", 3)
			viper.SetDefault("ai_worker_concurrency", 1)
			viper.SetDefault("ai_max_attempts", 3)
			viper.SetDefault("ai_request_timeout_seconds", 300)
			viper.SetDefault("screenshot_worker_concurrency", 3)
			viper.SetDefault("screenshot_max_attempts", 3)
			tt.setup()

			cfg, err := Load()

			if tt.wantErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.errContains)
				return
			}
			require.NoError(t, err)
			tt.check(t, cfg)
		})
	}
}
