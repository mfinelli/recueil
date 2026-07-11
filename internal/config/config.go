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

// Package config loads Recueil's backend configuration via Viper: an
// optional --config TOML file (see cmd/root.go), environment variables, and
// built-in defaults, in that precedence order (env overrides the file;
// flags, if any are ever added here, would override env). No other config
// formats (YAML, JSON, HCL, etc.) are supported, even though Viper itself
// can parse them (see cmd/root.go's initConfig for where that's enforced).
//
// Defaults live here, in init(), rather than in cmd/root.go (they need to
// apply regardless of which binary or test calls Load(), not only when
// cmd's own init() has run first).
package config

import (
	"fmt"

	"github.com/spf13/viper"
)

type Config struct {
	DatabaseURL         string `mapstructure:"database_url"`
	ListenAddr          string `mapstructure:"listen_addr"`
	WorkerURL           string `mapstructure:"worker_url"`
	WorkerServiceSecret string `mapstructure:"worker_service_secret"`
	SessionCookieSecure bool   `mapstructure:"session_cookie_secure"`

	// PairingTokenKey is a base64-encoded 32-byte AES-256 key, used to
	// reversibly encrypt/decrypt each account's pairing token for storage
	// in Postgres. Operator-generated (e.g. `openssl rand -base64 32`);
	PairingTokenKey string `mapstructure:"pairing_token_key"`

	CloudflareAccountID    string `mapstructure:"cloudflare_account_id"`
	CloudflareD1DatabaseID string `mapstructure:"cloudflare_d1_database_id"`
	CloudflareAPIToken     string `mapstructure:"cloudflare_api_token"`
}

func init() {
	viper.SetDefault("listen_addr", ":8080")
	viper.SetDefault("session_cookie_secure", true)
}

func Load() (Config, error) {
	var cfg Config
	if err := viper.Unmarshal(&cfg); err != nil {
		return cfg, fmt.Errorf("parsing config: %w", err)
	}

	var missing []string
	if cfg.DatabaseURL == "" {
		missing = append(missing, "database_url")
	}
	if cfg.WorkerURL == "" {
		missing = append(missing, "worker_url")
	}
	if cfg.WorkerServiceSecret == "" {
		missing = append(missing, "worker_service_secret")
	}
	if cfg.PairingTokenKey == "" {
		missing = append(missing, "pairing_token_key")
	}
	if cfg.CloudflareAccountID == "" {
		missing = append(missing, "cloudflare_account_id")
	}
	if cfg.CloudflareD1DatabaseID == "" {
		missing = append(missing, "cloudflare_d1_database_id")
	}
	if cfg.CloudflareAPIToken == "" {
		missing = append(missing, "cloudflare_api_token")
	}
	if len(missing) > 0 {
		return cfg, fmt.Errorf("missing required config values: %v",
			missing)
	}

	return cfg, nil
}
