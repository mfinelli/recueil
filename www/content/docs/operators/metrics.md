+++
title = "Metrics"
weight = 6
template = "docs-page.html"

[extra]
audience = "operators"
dek = "What metrics are available, and what to alert on."
+++

`server` serves Prometheus metrics at `/metrics`, on the same listener as the
dashboard and the API. Point any Prometheus-compatible scraper at it —
Prometheus itself, VictoriaMetrics, or Grafana Alloy, etc.

## Who can reach /metrics

recueil doesn't authenticate `/metrics` or restrict who can read it: like most
Prometheus endpoints, access control is left to the operator, at the network or
the reverse proxy layer.

**What it exposes** is aggregate and operational: counts of users, pages,
captures and links, how much storage you use, background job states, how
recently the agent last succeeded, and the Go runtime and process figures for
`server` (memory, goroutines, start time, Go version, etc). No personal
information is exposed (i.e., URLs, titles, page content, or usernames). Each
request also runs a few cheap queries against Postgres. So, overall, it is
low-risk, but it does tell an outsider how big and busy your instance is and
when it restarted, and there's rarely a reason to publish that.

- **If `server` is only reachable on a private network or over a VPN**, then
  it's probably fine to just leave it as-is.
- **If it's reachable from the internet**, restrict `/metrics` at the reverse
  proxy. The example in
  [Deploying recueil](@/docs/operators/deploying-recueil.md#putting-a-reverse-proxy-in-front)
  forwards everything, `/metrics` included, so this is yours to add. Two ways,
  and either is fine; pick whichever suits your setup:

  - **Scrape `server` directly, and hide the route from the proxy.** If your
    scraper runs on the same machine (or can reach `server`'s port over a
    private network), scrape `127.0.0.1:8080/metrics` and block the route
    publicly:

    ```nginx
    location = /metrics {
      return 404;
    }
    ```

  - **Or allow only your scraper through the proxy:**

    ```nginx
    location = /metrics {
      allow 192.0.2.10; # your Prometheus
      deny all;
      proxy_set_header Host $host;
      proxy_pass http://127.0.0.1:8080;
    }
    ```

  Put the block in the `server { }` that listens on 443, next to
  `location / { }`.

(`/ping`, `/health` and `/info` are likewise unauthenticated; they only say
whether `server` is up and which version it is.)

## Scraping

```yaml
scrape_configs:
  - job_name: recueil
    scrape_interval: 30s
    static_configs:
      - targets: ["127.0.0.1:8080"]
```

Every scrape asks Postgres fresh, a handful of cheap counting queries, so there
is no cache to go stale and a 15–60 second interval costs nothing worth worrying
about. If one metric's query fails it is logged and skipped on its own, and the
rest of the scrape still succeeds. **Scraping doesn't touch the Cloudflare
Worker**, so it has no effect on your free-tier request budget.

## What is reported

Besides the standard `go_*` and `process_*` series for the `server` process
itself, recueil reports:

| Series                                   | Labels          | Meaning                                                       |
| ---------------------------------------- | --------------- | ------------------------------------------------------------- |
| `recueil_users_total`                    |                 | User accounts.                                                |
| `recueil_pages_total`                    |                 | Archived pages, system-wide.                                  |
| `recueil_captures_total`                 |                 | Captures (a page's version history), system-wide.             |
| `recueil_links_total`                    |                 | Saved links (bookmark-only entries), system-wide.             |
| `recueil_storage_bytes`                  | `kind`          | Bytes of local storage by kind, below.                        |
| `recueil_jobs_total`                     | `job`, `status` | Background jobs by type and state, below.                     |
| `recueil_job_oldest_pending_age_seconds` | `job`           | Age of the oldest job of that type still pending.             |
| `recueil_agent_last_success_seconds`     | `cycle`         | Seconds since `agent` last completed that cycle successfully. |

Because the page, capture and link counts rise as things arrive, `rate()` or
`increase()` over them is also a free picture of how fast they are arriving.

**`recueil_storage_bytes` kinds.** `html_compressed`, `favicon` (page favicons),
`screenshot` and `link_favicon` (the separate link favicon store, `links_dir`)
are what's on disk. `html_uncompressed` is what the stored HTML would be once
decompressed, **not** extra disk use, so leave it out when adding things up.

**`recueil_jobs_total` jobs and statuses.** The jobs are `screenshot`,
`readability`, `ai` and `wayback`, each with the statuses `pending`,
`processing`, `done` and `failed`. Every combination is always reported, as 0
when there are none, so a series doesn't disappear and reappear. `wayback` is
[Wayback Machine submissions](@/docs/operators/deploying-recueil.md#wayback-machine-submission-optional)
and works a little differently: there is one row per URL holding its latest
attempt, so its counts are URLs by state, not a running total of every
submission ever made, and it never has anything `processing` (a submission is
`pending` while the Internet Archive works on it). It reports zeros if you
haven't configured Wayback submission.

## What isn't tracked

The only metrics available are those that we can calculate from Postgres. The
real **queue** depth, meaning saves and captures waiting on the Cloudflare
Worker to be fetched, lives only in D1, and scraping the Worker on every tick
would spend the free tier for nothing, so it isn't reported. What you can see is
whether the `worker` cycle is succeeding (the age above) and whether pages,
captures and links are still arriving (their counts rising).

## Things worth alerting on

These assume the default agent intervals (`worker` every 30 minutes, `local`
every 5); scale them to yours (see the
[configuration reference](@/docs/operators/configuration-reference.md)).

```yaml
groups:
  - name: recueil
    rules:
      # The agent stopped finishing its Worker cycle, so saves and captures
      # aren't being ingested. Allow a few missed cycles.
      - alert: RecueilWorkerCycleStalled
        expr: recueil_agent_last_success_seconds{cycle="worker"} > 5400
        for: 10m

      # ...or its local cycle, so screenshots and the like have stopped.
      - alert: RecueilLocalCycleStalled
        expr: recueil_agent_last_success_seconds{cycle="local"} > 900
        for: 10m

      # A kind of job has been waiting a long time.
      - alert: RecueilJobBacklog
        expr: recueil_job_oldest_pending_age_seconds > 3600
        for: 15m

      # An agent that has never succeeded at all (the series is absent).
      - alert: RecueilAgentNeverRan
        expr: absent(recueil_agent_last_success_seconds{cycle="worker"})
        for: 1h
```
