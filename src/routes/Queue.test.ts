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

// Same AppHeader/apiJSON path-dispatching mock approach as Devices.test.ts
// (loadItems() and loadJobs() also fire in parallel from one $effect).
// Queue items and jobs are two genuinely different retry models (see this
// file's own top comment): a queue item retry is optimistic -- flagged
// `manual_retry` and left in the list -- while a job retry means it's no
// longer 'failed' at all, so it's just removed from its list. Both are
// covered below since the difference is the whole point of the screen.
// Where more than one "Retry" button exists on screen at once, tests
// scope into the specific <li> via `within()` rather than indexing into
// getAllByRole, since which sections are populated (and therefore how
// many "Retry" buttons exist) varies test to test.
import { describe, it, expect, vi, afterEach } from "vitest";
import {
  render,
  screen,
  fireEvent,
  cleanup,
  within,
} from "@testing-library/svelte";

vi.mock("svelte-spa-router", async (importOriginal) => {
  const actual = await importOriginal<typeof import("svelte-spa-router")>();
  return { ...actual, push: vi.fn() };
});

vi.stubGlobal(
  "fetch",
  vi.fn().mockResolvedValue(new Response("{}", { status: 200 })),
);

vi.mock("../lib/api", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../lib/api")>();
  return { ...actual, apiJSON: vi.fn() };
});

import { apiJSON, ApiError } from "../lib/api";
import type { Job, JobsResponse, QueueItem } from "../lib/types";
import Queue from "./Queue.svelte";

const apiJSONMock = vi.mocked(apiJSON);

afterEach(() => {
  cleanup();
  apiJSONMock.mockReset();
});

const failedItem: QueueItem = {
  id: "q1",
  url: "https://example.com/failed-capture",
  status: "failed",
  manual_retry: false,
  claimed_at: null,
  created_at: "2026-05-01T12:00:00Z",
};

const readabilityJob: Job = {
  id: 5,
  page_id: 10,
  url: "https://example.com/failed-job",
  title: null,
  status: "failed",
  attempts: 2,
  error: "could not parse article body",
  claimed_at: null,
  completed_at: "2026-05-02T08:00:00Z",
};

const emptyJobs: JobsResponse = {
  screenshot_jobs: [],
  readability_jobs: [],
  ai_jobs: [],
};

type LoadOptions = {
  items?: QueueItem[];
  itemsError?: unknown;
  jobs?: JobsResponse;
  jobsError?: unknown;
};

function mockLoad({
  items = [],
  itemsError,
  jobs = emptyJobs,
  jobsError,
}: LoadOptions = {}) {
  apiJSONMock.mockImplementation((path: string) => {
    if (path === "/queue-items") {
      if (itemsError) return Promise.reject(itemsError);
      return Promise.resolve({ items });
    }
    if (path === "/jobs") {
      if (jobsError) return Promise.reject(jobsError);
      return Promise.resolve(jobs);
    }
    throw new Error(`unexpected apiJSON call: ${path}`);
  });
}

describe("Queue", () => {
  it("filters out non-failed items and jobs (bridge behavior until this screen's own broader-status UI lands)", async () => {
    mockLoad({
      items: [
        failedItem,
        {
          ...failedItem,
          id: "q2",
          status: "pending",
          url: "https://example.com/pending",
        },
        {
          ...failedItem,
          id: "q3",
          status: "captured",
          url: "https://example.com/captured",
        },
      ],
      jobs: {
        ...emptyJobs,
        readability_jobs: [
          readabilityJob,
          {
            ...readabilityJob,
            id: 6,
            status: "processing",
            url: "https://example.com/processing",
          },
          {
            ...readabilityJob,
            id: 7,
            status: "done",
            url: "https://example.com/done",
          },
        ],
      },
    });
    render(Queue);

    expect(
      await screen.findByText("https://example.com/failed-capture"),
    ).toBeTruthy();
    expect(screen.getByText("https://example.com/failed-job")).toBeTruthy();

    expect(screen.queryByText("https://example.com/pending")).toBeNull();
    expect(screen.queryByText("https://example.com/captured")).toBeNull();
    expect(screen.queryByText("https://example.com/processing")).toBeNull();
    expect(screen.queryByText("https://example.com/done")).toBeNull();
  });

  it("shows loading states, then failed items and failed jobs", async () => {
    mockLoad({
      items: [failedItem],
      jobs: { ...emptyJobs, readability_jobs: [readabilityJob] },
    });
    render(Queue);

    expect(screen.getAllByText("Loading…").length).toBeGreaterThan(0);

    expect(
      await screen.findByText("https://example.com/failed-capture"),
    ).toBeTruthy();
    expect(screen.getByText("https://example.com/failed-job")).toBeTruthy();
    expect(screen.getByText(/2 attempts/)).toBeTruthy();
    expect(screen.getByText("could not parse article body")).toBeTruthy();
  });

  it("shows placeholders when there are no failed items or jobs", async () => {
    mockLoad({ items: [], jobs: emptyJobs });
    render(Queue);

    expect(await screen.findByText("No failed items.")).toBeTruthy();
    // One "Nothing failed." per job section (screenshots, article
    // extraction, AI summaries).
    expect(screen.getAllByText("Nothing failed.")).toHaveLength(3);
  });

  it("shows the API's own error message when loading items fails", async () => {
    mockLoad({ itemsError: new ApiError(500, "queue store unavailable") });
    render(Queue);

    expect(await screen.findByText("queue store unavailable")).toBeTruthy();
  });

  it("falls back to a generic error for a non-ApiError jobs load failure", async () => {
    mockLoad({ jobsError: new Error("network error") });
    render(Queue);

    expect(await screen.findByText("failed to load failed jobs")).toBeTruthy();
  });

  it("shows singular attempt phrasing for a single attempt", async () => {
    mockLoad({
      jobs: {
        ...emptyJobs,
        readability_jobs: [{ ...readabilityJob, attempts: 1 }],
      },
    });
    render(Queue);

    expect(await screen.findByText(/1 attempt\b/)).toBeTruthy();
  });

  it("retries a failed queue item, flagging it as pending rather than removing it", async () => {
    mockLoad({ items: [failedItem] });
    render(Queue);

    const row = (
      await screen.findByText("https://example.com/failed-capture")
    ).closest("li") as HTMLElement;
    apiJSONMock.mockResolvedValueOnce(undefined);
    await fireEvent.click(within(row).getByRole("button", { name: "Retry" }));

    expect(apiJSONMock).toHaveBeenCalledWith("/queue-items/q1/retry", {
      method: "POST",
    });
    expect(within(row).getByText("retry pending")).toBeTruthy();
    const retryButton = within(row).getByRole("button", {
      name: "Retry queued",
    });
    expect(retryButton).toHaveProperty("disabled", true);
    // Still present, not removed -- a queue item retry is a flag, not a
    // resolution (see this file's own top comment).
    expect(screen.getByText("https://example.com/failed-capture")).toBeTruthy();
  });

  it("shows an action error and leaves the item retryable when the item retry call fails", async () => {
    mockLoad({ items: [failedItem] });
    render(Queue);

    const row = (
      await screen.findByText("https://example.com/failed-capture")
    ).closest("li") as HTMLElement;
    apiJSONMock.mockRejectedValueOnce(new ApiError(500, "retry rejected"));
    await fireEvent.click(within(row).getByRole("button", { name: "Retry" }));

    expect(await screen.findByText("retry rejected")).toBeTruthy();
    expect(within(row).queryByText("retry pending")).toBeNull();
    expect(within(row).getByRole("button", { name: "Retry" })).toHaveProperty(
      "disabled",
      false,
    );
  });

  it("retries a failed job, removing it from its list entirely on success", async () => {
    mockLoad({ jobs: { ...emptyJobs, readability_jobs: [readabilityJob] } });
    render(Queue);

    const row = (
      await screen.findByText("https://example.com/failed-job")
    ).closest("li") as HTMLElement;
    apiJSONMock.mockResolvedValueOnce(undefined);
    await fireEvent.click(within(row).getByRole("button", { name: "Retry" }));

    expect(apiJSONMock).toHaveBeenCalledWith("/jobs/readability/5/retry", {
      method: "POST",
    });
    expect(screen.queryByText("https://example.com/failed-job")).toBeNull();
    // The Article extraction section's own list is now empty -- scope
    // into that specific section rather than a page-wide text match,
    // since all three job sections read "Nothing failed." at this point.
    const articleSection = screen
      .getByText("Article extraction")
      .closest(".job-section") as HTMLElement;
    expect(within(articleSection).getByText("Nothing failed.")).toBeTruthy();
  });

  it("shows an action error and leaves the job in its list when the job retry call fails", async () => {
    mockLoad({ jobs: { ...emptyJobs, readability_jobs: [readabilityJob] } });
    render(Queue);

    const row = (
      await screen.findByText("https://example.com/failed-job")
    ).closest("li") as HTMLElement;
    apiJSONMock.mockRejectedValueOnce(new ApiError(500, "retry rejected"));
    await fireEvent.click(within(row).getByRole("button", { name: "Retry" }));

    expect(await screen.findByText("retry rejected")).toBeTruthy();
    expect(screen.getByText("https://example.com/failed-job")).toBeTruthy();
  });
});
