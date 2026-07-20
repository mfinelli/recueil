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

// Package ai is the async AI enrichment job: summarize a capture's
// Readability-extracted text and suggest topical tags for it, against a
// single OpenAI-compatible chat completions API. Built as a callable unit
// (RunOnce), same shape as internal/screenshot and internal/readability, with
// no scheduler of its own -- cmd/agent.go's ticker drives it.
//
// A row in ai_jobs is never created at ingest time the way
// screenshot_jobs/readability_jobs rows are -- it's created by
// internal/readability itself, in the same transaction as marking its
// own job done, since AI enrichment can only run once reader_text
// exists. That means ClaimDueAIJobs needs no readiness check of its
// own: a row's mere existence already implies the precondition is met.
//
// Summarization and tag generation are two independent chat completion
// calls, not one prompt asking for both. This costs more latency (LLM
// completions here are tolerated up to several minutes, per config's
// RequestTimeout) in exchange for each prompt staying simple, and for
// tag parsing not depending on the model reliably producing a specific
// combined structure. A failure in either call fails the whole
// attempt -- see processOne's own doc for why redoing the other call on
// retry is an accepted, low-stakes tradeoff rather than something worth
// partial-committing around.
//
// Response content is parsed as a plain comma-separated list, not JSON
// or any provider-specific structured-output feature -- support for
// those varies significantly across OpenAI-compatible servers,
// especially smaller local models served via Ollama/llama.cpp, and a
// comma-separated list is something every model handles reasonably
// well regardless.
package ai

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"

	"github.com/mfinelli/recueil/internal/db"
)

// defaultBatchLimit bounds how many jobs a single RunOnce call claims --
// same value and reasoning as internal/screenshot's own.
const defaultBatchLimit = 50

// claimStaleTimeout is how long a job can sit 'processing' before
// ClaimDueAIJobs treats it as abandoned and lets another claim reclaim
// it. Same value and reasoning as internal/screenshot's own.
const claimStaleTimeout = 15 * time.Minute

// baseBackoff and maxBackoff bound the exponential retry delay computed
// by backoff -- see its own doc. Same values as internal/screenshot's own.
const (
	baseBackoff = 30 * time.Second
	maxBackoff  = 30 * time.Minute
)

// defaultMaxInputChars is the fallback used when Params.MaxInputChars is
// unset, bounding how much of a capture's reader_text gets sent per call.
// There's no single right number here, which is why this is
// configurable (config's ai_max_input_chars) rather than a fixed
// constant: using the common ~4-characters-per-token rule of thumb,
// 24,000 characters is roughly 6k tokens, comfortably covering most
// real-world long-form articles/essays (many run 3,000-6,000+ words,
// i.e. 18,000-36,000+ characters) without truncation -- but an operator
// pointing this at a large-context hosted model (128k+ tokens) can raise
// it further, and one running a constrained local setup (Ollama's own
// *default* request context is often much smaller than what the
// underlying model actually supports, unless explicitly configured
// otherwise) can lower it, so a call doesn't simply fail outright
// instead of producing a slightly-truncated-but-successful summary.
const defaultMaxInputChars = 24000

const summarizeSystemPrompt = `You are a concise, factual summarizer of web articles. ` +
	`Write a 2-4 sentence summary of the article the user provides. ` +
	`Respond with only the summary itself -- no preamble, no headers, no quotation marks.`

const tagsSystemPrompt = `You suggest short topical tags for web articles. ` +
	`Given the article the user provides, respond with 3-6 short, lowercase, ` +
	`comma-separated tags describing its subject matter. ` +
	`Respond with only the comma-separated list -- no preamble, no numbering, no explanation.`

// Params are Runner's dependencies, all required except Logger.
type Params struct {
	Pool    *pgxpool.Pool
	Queries *db.Queries

	// BaseURL is the OpenAI-compatible API's base URL (config's
	// ai_base_url) -- e.g. "https://api.openai.com/v1",
	// "http://ollama:11434/v1", or a llama.cpp server's own
	// `/v1`-prefixed address. Whether this Runner gets constructed at
	// all is gated on this being non-empty; see cmd/agent.go.
	BaseURL string

	// APIKey is sent as a bearer token the same way any OpenAI-compatible
	// client would send it. Many local runtimes (Ollama's own
	// OpenAI-compatible endpoint, in particular) don't validate it at
	// all, so any non-empty placeholder value works fine against those.
	APIKey string

	// Model is the model name passed on every request -- an arbitrary
	// string, not one of the SDK's own named constants, since operators
	// can point this at literally any model a compatible server exposes.
	Model string

	// Concurrency bounds how many requests are in flight against the
	// API at once (config's ai_worker_concurrency). Values below 1 are
	// treated as 1. More conservative by default than the
	// screenshot/readability jobs' own concurrency: hosted APIs often
	// rate-limit, and many local single-GPU model servers can't
	// meaningfully parallelize inference against one loaded model
	// anyway.
	Concurrency int

	// MaxAttempts bounds the retry/backoff loop (config's
	// ai_max_attempts). Values below 1 are treated as 1.
	MaxAttempts int

	// RequestTimeout bounds a single chat completion call. Defaults to
	// 5 minutes if zero or negative; much longer than the sidecar jobs'
	// 60s, since LLM completions (especially against local/smaller
	// models) can legitimately take a while.
	RequestTimeout time.Duration

	// MaxInputChars bounds how much of a capture's reader_text gets sent
	// per call (config's ai_max_input_chars). Defaults to
	// defaultMaxInputChars if zero or negative -- see its own doc for
	// the tradeoff involved in choosing a value.
	MaxInputChars int

	// Logger defaults to slog.Default() if nil.
	Logger *slog.Logger
}

// Runner has no OS resources of its own to close: the openai-go client
// is just a thin HTTP client wrapper, safe to keep and reuse for the
// Runner's whole lifetime without any explicit teardown.
type Runner struct {
	pool           *pgxpool.Pool
	queries        *db.Queries
	client         openai.Client
	model          string
	concurrency    int
	maxAttempts    int
	requestTimeout time.Duration
	maxInputChars  int
	logger         *slog.Logger
}

// New validates Params, constructs the OpenAI-compatible client, and
// returns a Runner.
func New(p *Params) (*Runner, error) {
	logger := p.Logger
	if logger == nil {
		logger = slog.Default()
	}
	concurrency := p.Concurrency
	if concurrency < 1 {
		concurrency = 1
	}
	maxAttempts := p.MaxAttempts
	if maxAttempts < 1 {
		maxAttempts = 1
	}
	requestTimeout := p.RequestTimeout
	if requestTimeout <= 0 {
		requestTimeout = 5 * time.Minute
	}
	maxInputChars := p.MaxInputChars
	if maxInputChars <= 0 {
		maxInputChars = defaultMaxInputChars
	}

	client := openai.NewClient(
		option.WithBaseURL(p.BaseURL),
		option.WithAPIKey(p.APIKey),
	)

	return &Runner{
		pool:           p.Pool,
		queries:        p.Queries,
		client:         client,
		model:          p.Model,
		concurrency:    concurrency,
		maxAttempts:    maxAttempts,
		requestTimeout: requestTimeout,
		maxInputChars:  maxInputChars,
		logger:         logger,
	}, nil
}

// RunOnce claims one bounded batch of due ai_jobs rows and processes
// them with a Concurrency-bounded worker pool. See
// internal/screenshot.Runner.RunOnce's own doc -- identical shape and
// reasoning, just against a different table.
func (r *Runner) RunOnce(ctx context.Context) error {
	jobs, err := r.queries.ClaimDueAIJobs(ctx, db.ClaimDueAIJobsParams{
		StaleBefore: pgtype.Timestamptz{Time: time.Now().Add(-claimStaleTimeout), Valid: true},
		RowLimit:    defaultBatchLimit,
	})
	if err != nil {
		return fmt.Errorf("ai: claiming due jobs: %w", err)
	}
	if len(jobs) == 0 {
		return nil
	}

	sem := make(chan struct{}, r.concurrency)
	var wg sync.WaitGroup
	for _, job := range jobs {
		wg.Add(1)
		sem <- struct{}{}
		go func(job db.ClaimDueAIJobsRow) {
			defer wg.Done()
			defer func() { <-sem }()
			if err := r.processOne(ctx, job); err != nil {
				r.logger.ErrorContext(ctx, "ai: failed to process job",
					"job_id", job.JobID, "capture_id", job.CaptureID, "error", err)
			}
		}(job)
	}
	wg.Wait()

	return nil
}

// processOne summarizes and tags one capture, then commits both -- or,
// on any failure, hands off to handleFailure. A failure generating tags
// discards an already-successful summary from the same attempt rather
// than partially committing it: the whole attempt retries together next
// time, recomputing the summary again too. That's accepted, deliberate
// waste, not an oversight -- this is optional, low-stakes enrichment
// with a small bounded MaxAttempts, and the alternative (partial
// commit + a "resume from where it left off" state machine) is real
// complexity this doesn't need at that scale.
func (r *Runner) processOne(ctx context.Context, job db.ClaimDueAIJobsRow) error {
	if !job.ReaderText.Valid || job.ReaderText.String == "" {
		// Shouldn't happen -- CreateAIJob only ever runs once
		// reader_text is set, and nothing between retry attempts could
		// ever populate it (only internal/readability's own success does
		// that, and that already happened once for this row to exist
		// at all). Routing this through the ordinary retry-eligible
		// handleFailure would just retry something structurally incapable
		// of ever succeeding -- fail permanently and immediately instead,
		// since this is a genuine invariant violation, not a transient error.
		return r.failPermanently(ctx, job, errors.New("reader_text is empty"))
	}

	text := job.ReaderText.String
	if len(text) > r.maxInputChars {
		text = text[:r.maxInputChars]
	}

	summary, err := r.summarize(ctx, text)
	if err != nil {
		return r.handleFailure(ctx, job, fmt.Errorf("summarizing: %w", err))
	}

	tags, err := r.generateTags(ctx, text)
	if err != nil {
		return r.handleFailure(ctx, job, fmt.Errorf("generating tags: %w", err))
	}

	if err := r.commitDone(ctx, job, summary, tags); err != nil {
		return r.handleFailure(ctx, job, fmt.Errorf("committing ai enrichment: %w", err))
	}

	r.logger.InfoContext(ctx, "ai: enriched", "capture_id", job.CaptureID, "tags", len(tags))
	return nil
}

// summarize asks for a short summary of text and returns it, trimmed.
func (r *Runner) summarize(ctx context.Context, text string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, r.requestTimeout)
	defer cancel()

	completion, err := r.client.Chat.Completions.New(ctx, openai.ChatCompletionNewParams{
		Model: openai.ChatModel(r.model),
		Messages: []openai.ChatCompletionMessageParamUnion{
			openai.SystemMessage(summarizeSystemPrompt),
			openai.UserMessage(text),
		},
	})
	if err != nil {
		return "", err
	}
	if len(completion.Choices) == 0 {
		return "", errors.New("no choices returned")
	}

	summary := strings.TrimSpace(completion.Choices[0].Message.Content)
	if summary == "" {
		return "", errors.New("model returned an empty summary")
	}
	return summary, nil
}

// generateTags asks for topical tags for text and returns them parsed.
func (r *Runner) generateTags(ctx context.Context, text string) ([]string, error) {
	ctx, cancel := context.WithTimeout(ctx, r.requestTimeout)
	defer cancel()

	completion, err := r.client.Chat.Completions.New(ctx, openai.ChatCompletionNewParams{
		Model: openai.ChatModel(r.model),
		Messages: []openai.ChatCompletionMessageParamUnion{
			openai.SystemMessage(tagsSystemPrompt),
			openai.UserMessage(text),
		},
	})
	if err != nil {
		return nil, err
	}
	if len(completion.Choices) == 0 {
		return nil, errors.New("no choices returned")
	}

	return parseTags(completion.Choices[0].Message.Content), nil
}

// parseTags splits a comma-separated tag response into trimmed,
// lowercased, deduplicated, non-empty tag names -- see the package doc
// for why this stays deliberately lenient rather than requiring any
// structured-output feature.
func parseTags(response string) []string {
	seen := make(map[string]struct{})
	var tags []string

	for _, part := range strings.Split(response, ",") {
		tag := strings.ToLower(strings.TrimSpace(part))
		tag = strings.Trim(tag, ".\"'")
		if tag == "" {
			continue
		}
		if _, ok := seen[tag]; ok {
			continue
		}
		seen[tag] = struct{}{}
		tags = append(tags, tag)
	}

	return tags
}

// commitDone records the capture's ai_summary/ai_model, upserts and
// attaches every tag (source 'ai'), and marks the job done -- all in one
// transaction, same "either everything lands or nothing does" shape as
// internal/screenshot.Runner.commitDone. AddPageTag's own ON CONFLICT DO
// NOTHING means a tag colliding with one the user already applied manually is
// a silent no-op here, never an error.
func (r *Runner) commitDone(ctx context.Context, job db.ClaimDueAIJobsRow, summary string, tags []string) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("beginning transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	qtx := r.queries.WithTx(tx)

	if err := qtx.SetCaptureAI(ctx, db.SetCaptureAIParams{
		ID:        job.CaptureID,
		AiSummary: pgtype.Text{String: summary, Valid: true},
		AiModel:   pgtype.Text{String: r.model, Valid: r.model != ""},
	}); err != nil {
		return fmt.Errorf("setting capture ai summary: %w", err)
	}

	for _, name := range tags {
		tag, err := qtx.UpsertTag(ctx, db.UpsertTagParams{UserID: job.UserID, Name: name})
		if err != nil {
			return fmt.Errorf("upserting tag %q: %w", name, err)
		}
		if err := qtx.AddPageTag(ctx, db.AddPageTagParams{
			PageID: job.PageID,
			TagID:  tag.ID,
			Source: "ai",
		}); err != nil {
			return fmt.Errorf("adding page tag %q: %w", name, err)
		}
	}

	if err := qtx.MarkAIJobDone(ctx, job.JobID); err != nil {
		return fmt.Errorf("marking ai job done: %w", err)
	}

	return tx.Commit(ctx)
}

// handleFailure records a failed attempt: either scheduled for retry, or,
// once MaxAttempts is exhausted, marked 'failed' permanently. Identical
// shape to internal/screenshot.Runner.handleFailure.
func (r *Runner) handleFailure(ctx context.Context, job db.ClaimDueAIJobsRow, cause error) error {
	attempts := job.Attempts + 1

	if attempts >= int32(r.maxAttempts) {
		return r.failPermanently(ctx, job, cause)
	}

	if err := r.queries.RetryAIJob(ctx, db.RetryAIJobParams{
		ID:            job.JobID,
		Attempts:      attempts,
		NextAttemptAt: pgtype.Timestamptz{Time: time.Now().Add(backoff(attempts)), Valid: true},
		Error:         pgtype.Text{String: cause.Error(), Valid: true},
	}); err != nil {
		return fmt.Errorf("%w (also failed scheduling retry: %v)", cause, err)
	}
	return cause
}

// failPermanently marks the job 'failed' immediately, with no further
// retry -- used both by handleFailure once MaxAttempts is exhausted, and
// directly by processOne for the empty-reader_text case, which is
// structurally incapable of ever succeeding on a later attempt (see its
// own comment).
func (r *Runner) failPermanently(ctx context.Context, job db.ClaimDueAIJobsRow, cause error) error {
	attempts := job.Attempts + 1
	if err := r.queries.FailAIJob(ctx, db.FailAIJobParams{
		ID:       job.JobID,
		Attempts: attempts,
		Error:    pgtype.Text{String: cause.Error(), Valid: true},
	}); err != nil {
		return fmt.Errorf("%w (also failed marking job failed: %v)", cause, err)
	}
	return cause
}

// backoff is identical to internal/screenshot.
func backoff(attempts int32) time.Duration {
	if attempts < 1 {
		attempts = 1
	}
	shift := attempts - 1
	if shift > 10 {
		return maxBackoff
	}
	d := baseBackoff * time.Duration(int64(1)<<uint(shift))
	if d > maxBackoff {
		return maxBackoff
	}
	return d
}
