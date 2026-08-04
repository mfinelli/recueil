+++
title = "Configuration Reference"
weight = 2
template = "docs-page.html"

[extra]
audience = "operators"
dek = "All of recueil's possible configuration settings."
+++

Both `recueil server` and `recueil agent` read the same settings, from either a
TOML file passed via `-c`/`--config`, or environment variables (where each
setting's name is uppercased — e.g., `database_url` becomes `DATABASE_URL`).
Both can be used together; when a setting is present in both, the environment
variable wins. See [Deploying recueil](@/docs/operators/deploying-recueil.md)
for how they get supplied in practice — environment variables in the Docker
Compose example there, or a TOML file mounted into the container instead.

## Required

Both commands refuse to start if any of these are missing — there's no default
for any of them:

| Setting                     | What it is                                                                                                          |
| --------------------------- | ------------------------------------------------------------------------------------------------------------------- |
| `database_url`              | Postgres connection string.                                                                                         |
| `worker_url`                | Your Cloudflare Worker's public URL (a Terraform output).                                                           |
| `worker_service_secret`     | Backend↔Worker shared secret (a Terraform output).                                                                  |
| `pairing_token_key`         | Base64-encoded 32-byte AES-256 key used to encrypt stored pairing tokens — generate with `openssl rand -base64 32`. |
| `cloudflare_account_id`     | Your Cloudflare account ID.                                                                                         |
| `cloudflare_d1_database_id` | A Terraform output.                                                                                                 |
| `cloudflare_api_token`      | The same Cloudflare API token used for the Terraform provider config.                                               |
| `archive_dir`               | Root directory captures are stored under.                                                                           |
| `r2_account_id`             | Same value as `cloudflare_account_id`.                                                                              |
| `r2_bucket_name`            | A Terraform output.                                                                                                 |
| `r2_access_key_id`          | The manually-provisioned [R2 API credential](@/docs/operators/deploying-recueil.md#r2-api-credentials).             |
| `r2_access_key_secret`      | The other half of the above credential.                                                                             |

## Server

| Setting                    | Default | What it does                                                                                                                                                           |
| -------------------------- | ------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `listen_addr`              | `:8080` | Address the HTTP server binds to.                                                                                                                                      |
| `session_cookie_secure`    | `true`  | Sets the `Secure` flag on session cookies. Only turn this off for a plain-HTTP setup with no TLS at all, which isn't recommended.                                      |
| `enable_open_registration` | `false` | Lets anyone who can reach the dashboard create their own account without an invite. The bootstrap flow and `recueil user create` both work regardless of this setting. |

## Agent scheduling

| Setting                              | Default       | What it does                                                                                               |
| ------------------------------------ | ------------- | ---------------------------------------------------------------------------------------------------------- |
| `agent_worker_poll_interval_seconds` | 1800 (30 min) | How often the agent checks the Cloudflare Worker for pending captures and pushes the bookmark-mirror sync. |
| `agent_local_poll_interval_seconds`  | 300 (5 min)   | How often the agent runs its own local jobs (screenshot, readability, AI enrichment)                       |

These are intentionally different schedules: the Worker poll is slower by
default to stay comfortably within Cloudflare's free tier, while the local jobs
have no such budget to respect and can run more often.

## Sidecar (headless Chrome)

| Setting               | Default                 | What it does                                                                                                                                                                                                                                                                            |
| --------------------- | ----------------------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `sidecar_url`         | `http://127.0.0.1:9222` | Where the agent reaches the headless-Chrome sidecar. The default only fits running the agent directly on the host against a locally-published sidecar — the documented Docker Compose deployment uses `http://chromedp:9222` instead, since both run on the same Compose network there. |
| `sidecar_render_host` | `127.0.0.1`             | The hostname the sidecar uses to reach back into the agent's per-job render server. `agent` in the Compose deployment, matching its service name.                                                                                                                                       |

## Screenshot and readability jobs

| Setting                          | Default | What it does                                                        |
| -------------------------------- | ------- | ------------------------------------------------------------------- |
| `screenshot_worker_concurrency`  | 3       | How many tabs render screenshots at once.                           |
| `screenshot_max_attempts`        | 3       | Retries before a capture's screenshot is marked permanently failed. |
| `readability_worker_concurrency` | 3       | Same idea, for extracting readable text.                            |
| `readability_max_attempts`       | 3       | Same idea, for extracting readable text.                            |

## AI enrichment

Entirely optional — leaving `ai_base_url` unset disables it, and nothing else in
this group matters until it's set.

| Setting                      | Default              | What it does                                                                                                                                                                                                                                                      |
| ---------------------------- | -------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `ai_base_url`                | _(unset)_            | Base URL of an OpenAI-compatible chat completions API — a hosted provider, an Ollama server's OpenAI-compatible endpoint, or a llama.cpp server.                                                                                                                  |
| `ai_api_key`                 | _(unset)_            | Sent as a bearer token. Many local runtimes (Ollama included) don't check it at all — any non-empty placeholder works against those.                                                                                                                              |
| `ai_model`                   | _(unset)_            | Model name sent with every request — any string a compatible server recognizes, not just an official OpenAI model name.                                                                                                                                           |
| `ai_worker_concurrency`      | 1                    | Lower than the other jobs by default: hosted APIs often rate-limit, and most local single-GPU setups can't meaningfully parallelize one loaded model anyway.                                                                                                      |
| `ai_max_attempts`            | 3                    | Same idea as the other jobs.                                                                                                                                                                                                                                      |
| `ai_request_timeout_seconds` | 300                  | Much longer than the sidecar jobs' timeout — completions against smaller or local models can legitimately take minutes.                                                                                                                                           |
| `ai_max_input_chars`         | _(internal default)_ | Caps how much of a capture's extracted text gets sent per request. Raise it for a large-context hosted model; lower it for a constrained local one, since exceeding a model's real context window fails the call outright rather than just truncating gracefully. |
