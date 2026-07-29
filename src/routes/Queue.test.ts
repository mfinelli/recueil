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
// `manual_retry` and left in the list -- while a job retry updates it in
// place to "pending". Where more than one "Retry" button exists on screen
// at once, tests scope into the specific <li> via `within()` rather than
// indexing into getAllByRole, since which sections are populated (and
// therefore how many "Retry" buttons exist) varies test to test.
//
// Relative-time fixtures ("2 minutes ago") are built from the real
// Date.now() at test-run time rather than fake timers, since the values
// involved are always small (seconds/minutes) and this avoids any
// interaction with the component's own 15-minute auto-refresh interval.
// Fake timers are used in exactly one test, specifically to verify that
// interval fires.
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
  vi.useRealTimers();
});

function minutesAgo(n: number): string {
  return new Date(Date.now() - n * 60 * 1000).toISOString();
}

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
  it("shows every status, not just failed, each with its own badge", async () => {
    mockLoad({
      items: [
        failedItem,
        {
          ...failedItem,
          id: "q2",
          status: "pending",
          created_at: minutesAgo(2),
          url: "https://example.com/pending",
        },
        {
          ...failedItem,
          id: "q4",
          status: "claimed",
          claimed_at: minutesAgo(1),
          url: "https://example.com/claimed",
        },
        {
          ...failedItem,
          id: "q3",
          status: "captured",
          claimed_at: minutesAgo(3),
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
            claimed_at: minutesAgo(1),
            url: "https://example.com/processing",
          },
          {
            ...readabilityJob,
            id: 7,
            status: "done",
            completed_at: minutesAgo(5),
            url: "https://example.com/done",
          },
        ],
      },
    });
    render(Queue);

    for (const url of [
      "https://example.com/failed-capture",
      "https://example.com/pending",
      "https://example.com/claimed",
      "https://example.com/captured",
      "https://example.com/failed-job",
      "https://example.com/processing",
      "https://example.com/done",
    ]) {
      expect(await screen.findByText(url)).toBeTruthy();
    }

    expect(screen.getAllByText("Pending")).toHaveLength(1);
    expect(screen.getByText("Claimed")).toBeTruthy();
    expect(screen.getByText("Captured")).toBeTruthy();
    expect(screen.getAllByText("Failed")).toHaveLength(2);
    expect(screen.getByText("Processing")).toBeTruthy();
    expect(screen.getByText("Done")).toBeTruthy();
  });

  it("shows a summary count per status category, combining queue items and all three job kinds", async () => {
    mockLoad({
      items: [
        {
          ...failedItem,
          id: "q1",
          status: "pending",
          url: "https://example.com/pending",
        },
        {
          ...failedItem,
          id: "q2",
          status: "claimed",
          claimed_at: minutesAgo(1),
          url: "https://example.com/claimed",
        },
      ],
      jobs: {
        ...emptyJobs,
        screenshot_jobs: [{ ...readabilityJob, id: 1, status: "failed" }],
        ai_jobs: [{ ...readabilityJob, id: 2, status: "failed" }],
      },
    });
    render(Queue);

    await screen.findByText("pending");
    // Both "pending" (1: the pending item) and "in progress" (1: the
    // claimed item) show the same count text ("1"), so each needs
    // scoping to its own stat rather than a page-wide exact-text match.
    const pendingStat = screen.getByText("pending").closest(".stat");
    expect(within(pendingStat as HTMLElement).getByText("1")).toBeTruthy();
    const stats = screen.getByText("in progress").closest(".stat");
    expect(within(stats as HTMLElement).getByText("1")).toBeTruthy();
    const failedStat = screen.getByText("failed").closest(".stat");
    expect(within(failedStat as HTMLElement).getByText("2")).toBeTruthy();
    // Nothing "done" in this fixture -- that stat shouldn't render at all.
    expect(screen.queryByText("completed recently")).toBeNull();
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

  it("shows placeholders when there is nothing at all", async () => {
    mockLoad({ items: [], jobs: emptyJobs });
    render(Queue);

    expect(
      await screen.findByText("Nothing in the queue right now."),
    ).toBeTruthy();
    // One "Nothing to show." per job section (screenshots, article
    // extraction, AI summaries).
    expect(screen.getAllByText("Nothing to show.")).toHaveLength(3);
    // No summary row at all when everything's empty.
    expect(screen.queryByText("pending")).toBeNull();
  });

  it("shows the API's own error message when loading items fails", async () => {
    mockLoad({ itemsError: new ApiError(500, "queue store unavailable") });
    render(Queue);

    expect(await screen.findByText("queue store unavailable")).toBeTruthy();
  });

  it("falls back to a generic error for a non-ApiError jobs load failure", async () => {
    mockLoad({ jobsError: new Error("network error") });
    render(Queue);

    expect(await screen.findByText("failed to load jobs")).toBeTruthy();
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

  it("shows relative time for a pending queue item, an in-progress one, and a recently-captured one", async () => {
    mockLoad({
      items: [
        {
          ...failedItem,
          id: "q1",
          status: "pending",
          created_at: minutesAgo(2),
          url: "https://example.com/pending",
        },
        {
          ...failedItem,
          id: "q2",
          status: "claimed",
          claimed_at: minutesAgo(1),
          url: "https://example.com/claimed",
        },
        {
          ...failedItem,
          id: "q3",
          status: "captured",
          claimed_at: minutesAgo(5),
          url: "https://example.com/captured",
        },
      ],
    });
    render(Queue);

    expect(await screen.findByText(/^Added /)).toBeTruthy();
    expect(screen.getByText(/^Claimed /)).toBeTruthy();
    expect(screen.getByText(/^Captured /)).toBeTruthy();
  });

  it("shows relative time for a processing job and a recently-done one", async () => {
    mockLoad({
      jobs: {
        ...emptyJobs,
        screenshot_jobs: [
          {
            ...readabilityJob,
            id: 1,
            status: "processing",
            claimed_at: minutesAgo(1),
            url: "https://example.com/processing",
          },
          {
            ...readabilityJob,
            id: 2,
            status: "done",
            completed_at: minutesAgo(4),
            url: "https://example.com/done",
          },
        ],
      },
    });
    render(Queue);

    expect(await screen.findByText(/^Started /)).toBeTruthy();
    expect(screen.getByText(/^Completed /)).toBeTruthy();
  });

  it("only shows a Retry button for failed rows, not pending/active/done ones", async () => {
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
          status: "claimed",
          claimed_at: minutesAgo(1),
          url: "https://example.com/claimed",
        },
      ],
    });
    render(Queue);

    await screen.findByText("https://example.com/failed-capture");
    // Exactly one Retry button on the whole page -- the failed item's.
    expect(screen.getAllByRole("button", { name: "Retry" })).toHaveLength(1);
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

  it("retries a failed job, updating it in place to pending rather than removing it", async () => {
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
    // Still shown -- now as Pending, not removed -- since this screen
    // shows more than just failed rows now.
    expect(screen.getByText("https://example.com/failed-job")).toBeTruthy();
    expect(within(row).getByText("Pending")).toBeTruthy();
    expect(within(row).queryByRole("button", { name: "Retry" })).toBeNull();
    expect(within(row).queryByText("could not parse article body")).toBeNull();
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
    expect(within(row).getByText("Failed")).toBeTruthy();
  });

  it("refetches both lists when the refresh button is clicked", async () => {
    mockLoad({ items: [failedItem] });
    render(Queue);

    await screen.findByText("https://example.com/failed-capture");
    const callsBefore = apiJSONMock.mock.calls.length;

    await fireEvent.click(screen.getByRole("button", { name: "Refresh" }));

    expect(apiJSONMock.mock.calls.length).toBe(callsBefore + 2);
    expect(await screen.findByText(/^Updated /)).toBeTruthy();
  });

  it("auto-refreshes on a 15-minute interval, not more often", async () => {
    vi.useFakeTimers();
    mockLoad({ items: [] });
    render(Queue);

    // Initial load only.
    await vi.waitFor(() => expect(apiJSONMock.mock.calls.length).toBe(2));

    await vi.advanceTimersByTimeAsync(14 * 60 * 1000);
    expect(apiJSONMock.mock.calls.length).toBe(2);

    await vi.advanceTimersByTimeAsync(1 * 60 * 1000 + 1000);
    expect(apiJSONMock.mock.calls.length).toBe(4);
  });
});
