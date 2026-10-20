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

// Package wayback submits URLs to the Internet Archive's Wayback Machine on a
// user's request and records how each submission went.
//
// It uses the Save Page Now 2 (SPN2) API, documented at
// https://archive.org/details/spn-2-public-api-page-docs-2025-10-22 (the shapes
// of the requests and responses below, the limits, and the hour its job statuses
// are kept for all come from there). A capture request returns at once with
// a job id and archive.org does the capturing itself, so recueil is never doing
// the work while it runs; what it needs later is only to learn the outcome, by
// asking about the job. Submission is on demand only, never automatic, and the
// archive fetches the URL as a logged-out visitor: it sees what a stranger would,
// not what the user saw in their own browser.
package wayback

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// DefaultBaseURL is where SPN2 lives.
const DefaultBaseURL = "https://web.archive.org"

// userAgent identifies recueil to archive.org, with the project's repository so
// that someone at the Internet Archive who sees these requests knows where to
// come with questions.
const userAgent = "recueil/1.0 (+https://github.com/mfinelli/recueil)"

// JobState is where a capture job stands, as archive.org reports it.
type JobState string

const (
	StatePending JobState = "pending"
	StateSuccess JobState = "success"
	StateError   JobState = "error"
)

// Job is the outcome SPN2 reports for a capture job.
type Job struct {
	State JobState
	// Timestamp is when the capture was made (success only; zero if not
	// reported or unparseable).
	Timestamp time.Time
	// OriginalURL is the final URL captured, after any redirects (success only).
	OriginalURL string
	// Message is archive.org's own explanation of a failure (error only).
	Message string
}

// APIError is a failure reported by archive.org itself: a refused capture
// request (a rate limit, a URL it won't capture), or an error status on the
// status route. Message is its own wording where it gave any, for showing to the
// user.
type APIError struct {
	StatusCode int
	Message    string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("archive.org: %s (HTTP %d)", e.Message, e.StatusCode)
}

// Client talks to SPN2 with one archive.org account's IA-S3 keys.
type Client struct {
	baseURL    string
	accessKey  string
	secretKey  string
	httpClient *http.Client
}

// NewClient returns a client for the real archive.org.
func NewClient(accessKey, secretKey string) *Client {
	return NewClientWithBaseURL(DefaultBaseURL, accessKey, secretKey, &http.Client{Timeout: 30 * time.Second})
}

// NewClientWithBaseURL is NewClient against another base URL and HTTP client,
// for tests.
func NewClientWithBaseURL(baseURL, accessKey, secretKey string, httpClient *http.Client) *Client {
	return &Client{baseURL: strings.TrimRight(baseURL, "/"), accessKey: accessKey, secretKey: secretKey, httpClient: httpClient}
}

func (c *Client) do(ctx context.Context, method, path string, form url.Values) (*http.Response, error) {
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "LOW "+c.accessKey+":"+c.secretKey)
	req.Header.Set("User-Agent", userAgent)
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	return c.httpClient.Do(req)
}

// apiErrorFrom turns a non-2xx response into an *APIError, taking archive.org's
// own message from a JSON body if it sent one.
func apiErrorFrom(resp *http.Response) *APIError {
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	var parsed struct {
		Message   string `json:"message"`
		StatusExt string `json:"status_ext"`
	}
	msg := ""
	if json.Unmarshal(raw, &parsed) == nil {
		msg = parsed.Message
		if msg == "" {
			msg = parsed.StatusExt
		}
	}
	if msg == "" {
		msg = strings.TrimSpace(string(raw))
	}
	if msg == "" {
		msg = http.StatusText(resp.StatusCode)
	}
	return &APIError{StatusCode: resp.StatusCode, Message: msg}
}

// Save asks archive.org to capture pageURL and returns the job id to ask about
// later. skip_first_archive is set because nothing here wants to know whether
// this is the URL's first capture, and leaving it out only makes captures slower.
// archive.org may answer with a recent capture instead of making a new one (its
// own default is to skip one made within the last hour); that still comes back as
// an ordinary job.
func (c *Client) Save(ctx context.Context, pageURL string) (string, error) {
	resp, err := c.do(ctx, http.MethodPost, "/save", url.Values{
		"url":                {pageURL},
		"skip_first_archive": {"1"},
	})
	if err != nil {
		return "", fmt.Errorf("wayback: requesting a capture: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", apiErrorFrom(resp)
	}
	var parsed struct {
		JobID   string `json:"job_id"`
		Message string `json:"message"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return "", fmt.Errorf("wayback: reading the capture response: %w", err)
	}
	if parsed.JobID == "" {
		msg := parsed.Message
		if msg == "" {
			msg = "no job id in the response"
		}
		return "", &APIError{StatusCode: resp.StatusCode, Message: msg}
	}
	return parsed.JobID, nil
}

// Status asks how a capture job is going. archive.org only keeps a job's status
// for a limited time (about an hour), so a job that is asked about too late is
// an error, not a pending one.
func (c *Client) Status(ctx context.Context, jobID string) (Job, error) {
	resp, err := c.do(ctx, http.MethodGet, "/save/status/"+url.PathEscape(jobID), nil)
	if err != nil {
		return Job{}, fmt.Errorf("wayback: checking job %s: %w", jobID, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return Job{}, apiErrorFrom(resp)
	}
	var parsed struct {
		Status      string `json:"status"`
		Timestamp   string `json:"timestamp"`
		OriginalURL string `json:"original_url"`
		Message     string `json:"message"`
		StatusExt   string `json:"status_ext"`
		Exception   string `json:"exception"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return Job{}, fmt.Errorf("wayback: reading the status response: %w", err)
	}

	switch parsed.Status {
	case "success":
		job := Job{State: StateSuccess, OriginalURL: parsed.OriginalURL}
		if t, err := time.Parse("20060102150405", parsed.Timestamp); err == nil {
			job.Timestamp = t.UTC()
		}
		return job, nil
	case "error":
		msg := parsed.Message
		if msg == "" {
			msg = parsed.StatusExt
		}
		if msg == "" {
			msg = parsed.Exception
		}
		if msg == "" {
			msg = "the capture failed"
		}
		return Job{State: StateError, Message: msg}, nil
	default:
		// "pending", or anything unrecognized: not a result yet. The caller
		// bounds how long it waits, so an unknown status can't hang forever.
		return Job{State: StatePending}, nil
	}
}
