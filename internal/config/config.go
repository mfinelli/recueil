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

	// ArchiveDir is the root directory captures.html_path is stored
	// relative to.
	ArchiveDir string `mapstructure:"archive_dir"`

	// R2 credentials for the backend's own GET/DELETE access to capture
	// blobs; the same manually-provisioned R2 API token already used by
	// the Worker for presigned uploads (terraform/README.md), reused here
	// rather than requiring the operator to provision a second one.
	R2AccountID       string `mapstructure:"r2_account_id"`
	R2BucketName      string `mapstructure:"r2_bucket_name"`
	R2AccessKeyID     string `mapstructure:"r2_access_key_id"`
	R2AccessKeySecret string `mapstructure:"r2_access_key_secret"`

	// AgentWorkerPollIntervalSeconds and AgentLocalPollIntervalSeconds are
	// both plain ints (seconds), not time.Duration strings like "2m".
	//
	// AgentWorkerPollIntervalSeconds is how often `recueil agent` polls
	// the Cloudflare Worker: pulling pending captures (ingestion) and
	// pushing the D1 bookmark-list mirror sync. A separate,
	// longer-by-default schedule from AgentLocalPollIntervalSeconds --
	// the Worker is meant to run comfortably within Cloudflare's free
	// tier, and polling it as often as the purely-local Postgres jobs
	// below would burn through that budget for no real benefit (a
	// pending capture or bookmark change sitting an extra few minutes
	// before being picked up is a non-issue; the free tier's request
	// budget is the actual constraint worth respecting).
	AgentWorkerPollIntervalSeconds int `mapstructure:"agent_worker_poll_interval_seconds"`

	// AgentLocalPollIntervalSeconds is how often `recueil agent` runs the
	// jobs that only ever touch its own Postgres instance: the screenshot
	// job, the readability job, and the AI enrichment job. No
	// Worker/free-tier request budget applies to any of these, so this
	// can run much more often than AgentWorkerPollIntervalSeconds without
	// cost, which is the whole reason these are two separate schedules
	// rather than one shared one: a freshly-ingested capture shouldn't
	// have to wait for the (deliberately slow) Worker-facing interval
	// just because it happens to share a ticker with it.
	AgentLocalPollIntervalSeconds int `mapstructure:"agent_local_poll_interval_seconds"`

	// SidecarURL is where the agent connects to drive the shared
	// headless-Chrome sidecar via chromedp's RemoteAllocator -- an
	// http(s) base URL, not a raw ws:// one: chromedp's own detectURL
	// fetches /json/version itself and swaps in the real
	// webSocketDebuggerUrl, so this package never has to. Two real
	// deployments need two different values here, which is exactly
	// why this is operator config rather than a hardcoded constant:
	// "http://chromedp:9222" when both the agent and the sidecar run
	// inside the same compose network, or "http://127.0.0.1:9222" when
	// the agent binary runs directly on the operator's own machine
	// against the sidecar's published host port (compose.yaml's
	// documented local-dev shape -- see its own comment on why the
	// published port stays even though the all-in-docker deployment
	// doesn't need it). One connection, shared by both the screenshot
	// and readability jobs (internal/sidecar).
	SidecarURL string `mapstructure:"sidecar_url"`

	// SidecarRenderHost is the hostname the sidecar container should
	// use to reach *back* into the agent's own ephemeral per-job HTML
	// render server (internal/sidecar) -- separate from SidecarURL,
	// which is the opposite direction (agent -> sidecar). The render
	// server always binds every interface (0.0.0.0) internally; this
	// is only what hostname gets embedded in the URL handed to the
	// sidecar for it to fetch that HTML back. "chromedp"'s own compose
	// service name works when both run in the same compose network;
	// "host.docker.internal" (see compose.yaml's extra_hosts) is what
	// a sidecar-in-docker/agent-on-host local-dev setup needs instead,
	// since the agent process in that shape isn't reachable at any
	// compose service name at all.
	SidecarRenderHost string `mapstructure:"sidecar_render_host"`

	// ScreenshotWorkerConcurrency bounds how many tabs the screenshot
	// runner opens at once against the shared sidecar ("a small worker
	// pool (e.g. 2-3 concurrent tabs), appropriate for modest
	// self-hosted hardware").
	ScreenshotWorkerConcurrency int `mapstructure:"screenshot_worker_concurrency"`

	// ScreenshotMaxAttempts bounds the retry/backoff loop: once a
	// screenshot_jobs row has failed this many times, it's marked
	// 'failed' permanently rather than retried again.
	ScreenshotMaxAttempts int `mapstructure:"screenshot_max_attempts"`

	// ReadabilityWorkerConcurrency and ReadabilityMaxAttempts are the
	// readability job's own counterparts to the screenshot job's
	// above.
	ReadabilityWorkerConcurrency int `mapstructure:"readability_worker_concurrency"`
	ReadabilityMaxAttempts       int `mapstructure:"readability_max_attempts"`

	// AIBaseURL is the OpenAI-compatible API's base URL -- e.g.
	// "https://api.openai.com/v1", "http://ollama:11434/v1", or a
	// llama.cpp server's own address. A single path, not a separate
	// Ollama/OpenAI backend abstraction: Ollama, llama.cpp's own server,
	// and effectively every hosted provider besides Anthropic have all
	// standardized on the same /v1/chat/completions request/response
	// shape, so one configurable base URL (plus AIAPIKey/AIModel) covers
	// all of them.
	//
	// This is how AI enrichment gets toggled off entirely, rather
	// than a separate ai_enabled boolean: an empty AIBaseURL means
	// cmd/agent.go never constructs an *ai.Runner at all, which is
	// simpler than an explicit flag that could disagree with whether
	// the rest of the AI config is actually filled in.
	AIBaseURL string `mapstructure:"ai_base_url"`

	// AIAPIKey is sent as a bearer token. Many local runtimes (Ollama's
	// own OpenAI-compatible endpoint, in particular) don't validate it
	// at all, so any non-empty placeholder value works fine against
	// those.
	AIAPIKey string `mapstructure:"ai_api_key"`

	// AIModel is the model name sent on every request -- an arbitrary
	// string, since operators can point this at literally any model a
	// compatible server exposes, not just OpenAI's own named ones.
	AIModel string `mapstructure:"ai_model"`

	// AIWorkerConcurrency and AIMaxAttempts are this job's own
	// counterparts to the screenshot/readability jobs' own pairs.
	// AIWorkerConcurrency defaults more conservatively than those two:
	// hosted APIs often rate-limit, and many local single-GPU model
	// servers can't meaningfully parallelize inference against one
	// loaded model anyway.
	AIWorkerConcurrency int `mapstructure:"ai_worker_concurrency"`
	AIMaxAttempts       int `mapstructure:"ai_max_attempts"`

	// AIRequestTimeoutSeconds bounds a single chat completion call --
	// much longer than the sidecar jobs' fixed 60s renderTimeout, since
	// LLM completions (especially against local/smaller models) can
	// legitimately take several minutes.
	AIRequestTimeoutSeconds int `mapstructure:"ai_request_timeout_seconds"`

	// AIMaxInputChars bounds how much of a capture's reader_text gets
	// sent per chat completion call. There's no single right value here
	// -- raise it for a large-context hosted model (little real risk of
	// truncating even long-form articles), lower it for a constrained
	// local setup (Ollama's own *default* request context is often much
	// smaller than what the underlying model actually supports, unless
	// explicitly configured otherwise, and exceeding it fails the call
	// outright rather than just producing a slightly-truncated summary).
	// Defaults to internal/ai's own defaultMaxInputChars if unset (0).
	AIMaxInputChars int `mapstructure:"ai_max_input_chars"`
}

func init() {
	viper.SetDefault("listen_addr", ":8080")
	viper.SetDefault("session_cookie_secure", true)
	viper.SetDefault("agent_worker_poll_interval_seconds", 1800)
	viper.SetDefault("agent_local_poll_interval_seconds", 300)
	viper.SetDefault("sidecar_url", "http://127.0.0.1:9222")
	viper.SetDefault("sidecar_render_host", "127.0.0.1")
	viper.SetDefault("screenshot_worker_concurrency", 3)
	viper.SetDefault("screenshot_max_attempts", 3)
	viper.SetDefault("readability_worker_concurrency", 3)
	viper.SetDefault("readability_max_attempts", 3)
	// ai_base_url has no default: empty is what disables AI enrichment
	// entirely.
	viper.SetDefault("ai_worker_concurrency", 1)
	viper.SetDefault("ai_max_attempts", 3)
	viper.SetDefault("ai_request_timeout_seconds", 300)
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
	if cfg.ArchiveDir == "" {
		missing = append(missing, "archive_dir")
	}
	if cfg.R2AccountID == "" {
		missing = append(missing, "r2_account_id")
	}
	if cfg.R2BucketName == "" {
		missing = append(missing, "r2_bucket_name")
	}
	if cfg.R2AccessKeyID == "" {
		missing = append(missing, "r2_access_key_id")
	}
	if cfg.R2AccessKeySecret == "" {
		missing = append(missing, "r2_access_key_secret")
	}
	if len(missing) > 0 {
		return cfg, fmt.Errorf("missing required config values: %v",
			missing)
	}

	return cfg, nil
}
