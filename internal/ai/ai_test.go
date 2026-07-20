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

// These tests run against real Postgres (internal/dbtest, same as every
// other DB-touching package) but a *fake* OpenAI-compatible HTTP server,
// not a real LLM -- a deliberate departure from internal/screenshot's and
// internal/readability's "no mocks, real sidecar" convention, and worth
// explaining why: a real local model would make these tests slow, heavy
// (a real model download/load), and -- critically -- non-deterministic,
// so they could only ever assert "got some non-empty text back," far
// weaker than what's actually worth testing here. What this package
// actually owns and could have bugs in is the request/response handling,
// retry bookkeeping, and tag parsing -- all of which a fake server
// exercises precisely, without depending on any model's actual output
// quality (which isn't this package's concern, and isn't deterministic
// enough to assert on anyway).
package ai_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mfinelli/recueil/internal/ai"
	"github.com/mfinelli/recueil/internal/db"
	"github.com/mfinelli/recueil/internal/dbtest"
)

const testFailureTrigger = "TRIGGER_FAILURE"

// fakeOpenAIServer implements just enough of the chat completions
// response shape for the openai-go client to parse successfully. It
// inspects the incoming request's system prompt to decide whether this
// is a summarize or a tag-generation call (internal/ai's two prompts are
// distinct enough to tell apart by a simple substring check), and
// returns an HTTP error instead whenever the user message contains
// testFailureTrigger, letting tests deterministically simulate a failed
// call without needing a second server or base URL.
func fakeOpenAIServer(t *testing.T) *httptest.Server {
	t.Helper()

	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		body, err := io.ReadAll(req.Body)
		require.NoError(t, err)

		var parsed struct {
			Messages []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
		}
		require.NoError(t, json.Unmarshal(body, &parsed))

		var systemPrompt, userContent string
		for _, m := range parsed.Messages {
			switch m.Role {
			case "system", "developer":
				systemPrompt = m.Content
			case "user":
				userContent = m.Content
			}
		}

		if strings.Contains(userContent, testFailureTrigger) {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"error": {"message": "simulated failure"}}`))
			return
		}

		var content string
		if strings.Contains(systemPrompt, "tags") {
			content = "testing, fake server, chat completions"
		} else {
			content = "This is a fake summary of the article for testing purposes."
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id":      "chatcmpl-test",
			"object":  "chat.completion",
			"created": 0,
			"model":   "test-model",
			"choices": []map[string]any{
				{
					"index":         0,
					"finish_reason": "stop",
					"message":       map[string]any{"role": "assistant", "content": content},
				},
			},
		})
	}))
}

func newRunner(t *testing.T, pool *pgxpool.Pool, serverURL string, maxAttempts int) *ai.Runner {
	t.Helper()

	r, err := ai.New(&ai.Params{
		Pool:           pool,
		Queries:        db.New(pool),
		BaseURL:        serverURL,
		APIKey:         "test-key",
		Model:          "test-model",
		Concurrency:    2,
		MaxAttempts:    maxAttempts,
		RequestTimeout: 10 * time.Second,
		Logger:         slog.Default(),
	})
	require.NoError(t, err)

	return r
}

// newDueAIJob inserts a page/capture, sets reader_text directly (standing
// in for a real readability job having already succeeded), and explicitly
// calls CreateAIJob -- mirroring exactly what internal/readability's own
// commitDone does on success, an application-level step, not a database
// trigger.
func newDueAIJob(t *testing.T, pool *pgxpool.Pool, readerText string) (db.Capture, db.AiJob) {
	t.Helper()
	ctx := context.Background()
	q := db.New(pool)

	user := dbtest.CreateUser(t, pool, "member")

	page, err := q.UpsertPage(ctx, db.UpsertPageParams{
		UserID:          user.ID,
		NormalizedUrl:   "https://example.com/" + uuid.NewString(),
		Title:           pgtype.Text{String: "AI Test Article", Valid: true},
		LatestCaptureAt: pgtype.Timestamptz{Time: time.Now(), Valid: true},
	})
	require.NoError(t, err)

	inserted, err := q.InsertCaptureIdempotent(ctx, db.InsertCaptureIdempotentParams{
		PageID:                    page.ID,
		SourceCaptureID:           pgtype.Text{String: uuid.NewString(), Valid: true},
		Source:                    "extension",
		RawUrl:                    "https://example.com/test",
		Title:                     pgtype.Text{String: "AI Test Article", Valid: true},
		HtmlPath:                  "irrelevant/for/this/test.html.zst",
		HtmlCompressedSizeBytes:   1,
		HtmlUncompressedSizeBytes: 1,
		ContentHash:               uuid.NewString(),
		CapturedAt:                pgtype.Timestamptz{Time: time.Now(), Valid: true},
		Language:                  "english",
	})
	require.NoError(t, err)
	require.True(t, inserted.Inserted, "expected a genuinely new capture")

	require.NoError(t, q.SetCaptureReadability(ctx, db.SetCaptureReadabilityParams{
		ID:                 inserted.ID,
		ReaderText:         pgtype.Text{String: readerText, Valid: true},
		ReaderTextHash:     pgtype.Text{Valid: false},
		ReadabilityVersion: pgtype.Text{Valid: false},
	}))
	require.NoError(t, q.CreateAIJob(ctx, inserted.ID))

	job, err := q.GetAIJobByCaptureID(ctx, inserted.ID)
	require.NoError(t, err)
	assert.Equal(t, "pending", job.Status)

	capture, err := q.GetCaptureByID(ctx, inserted.ID)
	require.NoError(t, err)

	return capture, job
}

func TestRunner_RunOnce_EnrichesDueJob(t *testing.T) {
	pool := dbtest.Setup(t)
	dbtest.Reset(t, pool)
	q := db.New(pool)

	server := fakeOpenAIServer(t)
	defer server.Close()

	capture, _ := newDueAIJob(t, pool, "A long article about gardening and composting techniques.")

	r := newRunner(t, pool, server.URL, 3)

	require.NoError(t, r.RunOnce(context.Background()))

	job, err := q.GetAIJobByCaptureID(context.Background(), capture.ID)
	require.NoError(t, err)
	assert.Equal(t, "done", job.Status)
	assert.True(t, job.CompletedAt.Valid)
	assert.False(t, job.Error.Valid)

	updated, err := q.GetCaptureByID(context.Background(), capture.ID)
	require.NoError(t, err)
	require.True(t, updated.AiSummary.Valid)
	assert.Contains(t, updated.AiSummary.String, "fake summary")
	require.True(t, updated.AiModel.Valid)
	assert.Equal(t, "test-model", updated.AiModel.String)

	tags, err := q.ListPageTags(context.Background(), capture.PageID)
	require.NoError(t, err)
	require.Len(t, tags, 3)
	for _, tag := range tags {
		assert.Equal(t, "ai", tag.Source)
	}
}

func TestRunner_RunOnce_TagCollidingWithManualTagIsANoOp(t *testing.T) {
	pool := dbtest.Setup(t)
	dbtest.Reset(t, pool)
	q := db.New(pool)

	server := fakeOpenAIServer(t)
	defer server.Close()

	capture, _ := newDueAIJob(t, pool, "An article about testing.")

	// Manually pre-tag this page with one of the exact tags the fake
	// server will suggest -- confirms AddPageTag's ON CONFLICT DO
	// NOTHING rather than erroring the whole job.
	tag, err := q.UpsertTag(context.Background(), db.UpsertTagParams{
		UserID: mustGetPageUserID(t, q, capture.PageID),
		Name:   "testing",
	})
	require.NoError(t, err)
	require.NoError(t, q.AddPageTag(context.Background(), db.AddPageTagParams{
		PageID: capture.PageID, TagID: tag.ID, Source: "manual",
	}))

	r := newRunner(t, pool, server.URL, 3)
	require.NoError(t, r.RunOnce(context.Background()))

	job, err := q.GetAIJobByCaptureID(context.Background(), capture.ID)
	require.NoError(t, err)
	assert.Equal(t, "done", job.Status, "the AI job should still succeed despite the tag collision")

	tags, err := q.ListPageTags(context.Background(), capture.PageID)
	require.NoError(t, err)
	for _, tg := range tags {
		if tg.Name == "testing" {
			assert.Equal(t, "manual", tg.Source, "the pre-existing manual tag should win, not get overwritten")
		}
	}
}

func mustGetPageUserID(t *testing.T, q *db.Queries, pageID int64) int64 {
	t.Helper()
	page, err := q.GetPageByID(context.Background(), pageID)
	require.NoError(t, err)
	return page.UserID
}

func TestRunner_RunOnce_ReclaimsStaleProcessingJob(t *testing.T) {
	pool := dbtest.Setup(t)
	dbtest.Reset(t, pool)
	q := db.New(pool)

	server := fakeOpenAIServer(t)
	defer server.Close()

	capture, job := newDueAIJob(t, pool, "An article about reclaiming stale jobs.")

	_, err := pool.Exec(context.Background(),
		"UPDATE ai_jobs SET status = 'processing', claimed_at = NOW() - INTERVAL '20 minutes' WHERE id = $1",
		job.ID)
	require.NoError(t, err)

	r := newRunner(t, pool, server.URL, 3)
	require.NoError(t, r.RunOnce(context.Background()))

	reclaimed, err := q.GetAIJobByCaptureID(context.Background(), capture.ID)
	require.NoError(t, err)
	assert.Equal(t, "done", reclaimed.Status, "a stale 'processing' job should be reclaimed and completed, not left stuck")
}

func TestRunner_RunOnce_OneFailureDoesNotBlockTheRestOfTheBatch(t *testing.T) {
	pool := dbtest.Setup(t)
	dbtest.Reset(t, pool)
	q := db.New(pool)

	server := fakeOpenAIServer(t)
	defer server.Close()

	goodCapture, _ := newDueAIJob(t, pool, "A perfectly normal article.")
	brokenCapture, _ := newDueAIJob(t, pool, testFailureTrigger+" this article always fails.")

	r := newRunner(t, pool, server.URL, 3)
	require.NoError(t, r.RunOnce(context.Background()))

	goodJob, err := q.GetAIJobByCaptureID(context.Background(), goodCapture.ID)
	require.NoError(t, err)
	assert.Equal(t, "done", goodJob.Status)

	brokenJob, err := q.GetAIJobByCaptureID(context.Background(), brokenCapture.ID)
	require.NoError(t, err)
	assert.Equal(t, "pending", brokenJob.Status, "should be scheduled for retry, not blocking the batch")
	assert.Equal(t, int32(1), brokenJob.Attempts)
	assert.True(t, brokenJob.Error.Valid)
}

func TestRunner_RunOnce_FailsPermanentlyAfterMaxAttempts(t *testing.T) {
	pool := dbtest.Setup(t)
	dbtest.Reset(t, pool)
	q := db.New(pool)

	server := fakeOpenAIServer(t)
	defer server.Close()

	capture, _ := newDueAIJob(t, pool, testFailureTrigger+" this article always fails.")

	r := newRunner(t, pool, server.URL, 1)
	require.NoError(t, r.RunOnce(context.Background()))

	job, err := q.GetAIJobByCaptureID(context.Background(), capture.ID)
	require.NoError(t, err)
	assert.Equal(t, "failed", job.Status)
	assert.Equal(t, int32(1), job.Attempts)
	assert.True(t, job.Error.Valid)
	assert.True(t, job.CompletedAt.Valid)
}

func TestRunner_RunOnce_NoDueJobsIsANoOp(t *testing.T) {
	pool := dbtest.Setup(t)
	dbtest.Reset(t, pool)

	server := fakeOpenAIServer(t)
	defer server.Close()

	r := newRunner(t, pool, server.URL, 3)
	assert.NoError(t, r.RunOnce(context.Background()))
}
