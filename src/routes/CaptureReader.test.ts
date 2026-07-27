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

// Same AppHeader/apiJSON mocking approach as Settings.test.ts. `params` is
// svelte-spa-router's own prop convention for a route component -- passed
// directly as this component's props, same shape a real <Router> would
// supply from the URL. formatDateTime() goes through
// `new Date(...).toLocaleString(undefined, ...)`, which resolves to
// whatever locale this machine/CI runner defaults to -- byline assertions
// below only check for the "Captured " prefix plus year, not the full
// formatted string, so they don't depend on which locale that turns out
// to be.
import { describe, it, expect, vi, afterEach } from "vitest";
import { render, screen, fireEvent, cleanup } from "@testing-library/svelte";

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

import { push } from "svelte-spa-router";
import { apiJSON, ApiError } from "../lib/api";
import type { CaptureDetail } from "../lib/types";
import CaptureReader from "./CaptureReader.svelte";

const apiJSONMock = vi.mocked(apiJSON);
const pushMock = vi.mocked(push);
const confirmMock = vi.fn();
vi.stubGlobal("confirm", confirmMock);

afterEach(() => {
  cleanup();
  apiJSONMock.mockReset();
  pushMock.mockClear();
  confirmMock.mockReset();
});

const baseCapture: CaptureDetail = {
  id: 42,
  page_id: 7,
  source: "extension",
  raw_url: "https://example.com/article",
  title: "An example article",
  thumbnail_path: null,
  thumbnail_size_bytes: null,
  thumbnail_hash: null,
  favicon_path: null,
  favicon_size_bytes: null,
  favicon_hash: null,
  reader_text: "The full extracted body text of the article.",
  readability_version: null,
  content_hash: "test-content-hash",
  ai_summary: null,
  ai_model: null,
  language: "en",
  html_compressed_size_bytes: 1024,
  html_uncompressed_size_bytes: 4096,
  captured_at: "2026-05-01T12:00:00Z",
  created_at: "2026-05-01T12:00:00Z",
  updated_at: "2026-05-01T12:00:00Z",
};

describe("CaptureReader", () => {
  // Loads baseCapture (or an override) and waits for the heading before
  // returning -- every delete/regenerate test below needs the loaded
  // state first, so this avoids repeating that same
  // render+mockResolvedValueOnce+findByRole dance in each one.
  async function renderLoaded(overrides: Partial<CaptureDetail> = {}) {
    apiJSONMock.mockResolvedValueOnce({ ...baseCapture, ...overrides });
    render(CaptureReader, { params: { id: "42" } });
    await screen.findByRole("heading", {
      name: overrides.title ?? baseCapture.title ?? "",
    });
  }

  it("shows a loading state, then fetches by the id from params", () => {
    apiJSONMock.mockResolvedValueOnce(baseCapture);
    render(CaptureReader, { params: { id: "42" } });

    expect(screen.getByText("Loading…")).toBeTruthy();
    expect(apiJSONMock).toHaveBeenCalledWith("/captures/42");
  });

  it("shows the API's own error message when the fetch fails with ApiError", async () => {
    apiJSONMock.mockRejectedValueOnce(new ApiError(404, "capture not found"));
    render(CaptureReader, { params: { id: "999" } });

    expect(await screen.findByText("capture not found")).toBeTruthy();
  });

  it("falls back to a generic error message for a non-ApiError failure", async () => {
    apiJSONMock.mockRejectedValueOnce(new Error("network error"));
    render(CaptureReader, { params: { id: "42" } });

    expect(await screen.findByText("failed to load capture")).toBeTruthy();
  });

  it("renders the title, byline links, and extracted reader text on success", async () => {
    apiJSONMock.mockResolvedValueOnce(baseCapture);
    render(CaptureReader, { params: { id: "42" } });

    expect(
      await screen.findByRole("heading", { name: "An example article" }),
    ).toBeTruthy();
    expect(screen.getByText(/^Captured /)).toBeTruthy();

    const originalUrlLink = screen.getByRole("link", {
      name: "Original URL",
    });
    expect(originalUrlLink).toHaveProperty(
      "href",
      "https://example.com/article",
    );

    const archivedLink = screen.getByRole("link", {
      name: "View archived page",
    });
    expect(archivedLink).toHaveProperty(
      "href",
      "http://localhost:3000/api/captures/42/html",
    );

    const backLink = screen.getByRole("link", { name: "← Back to page" });
    expect(backLink.getAttribute("href")).toBe("#/pages/7");

    expect(
      screen.getByText("The full extracted body text of the article."),
    ).toBeTruthy();
  });

  it("falls back to the raw URL as the heading when there's no title", async () => {
    apiJSONMock.mockResolvedValueOnce({ ...baseCapture, title: null });
    render(CaptureReader, { params: { id: "42" } });

    expect(
      await screen.findByRole("heading", {
        name: "https://example.com/article",
      }),
    ).toBeTruthy();
  });

  it("shows a placeholder message instead of reader text when there is none", async () => {
    apiJSONMock.mockResolvedValueOnce({ ...baseCapture, reader_text: null });
    render(CaptureReader, { params: { id: "42" } });

    expect(
      await screen.findByText("No extracted text for this capture yet."),
    ).toBeTruthy();
  });

  it("renders the AI summary when present", async () => {
    apiJSONMock.mockResolvedValueOnce({
      ...baseCapture,
      ai_summary: "A concise AI-generated summary.",
    });
    render(CaptureReader, { params: { id: "42" } });

    expect(
      await screen.findByText("A concise AI-generated summary."),
    ).toBeTruthy();
  });

  it("deletes the capture after confirmation and navigates back to the library", async () => {
    await renderLoaded();

    confirmMock.mockReturnValue(true);
    apiJSONMock.mockResolvedValueOnce(undefined);
    await fireEvent.click(
      screen.getByRole("button", { name: "Delete capture" }),
    );

    expect(confirmMock).toHaveBeenCalledWith(
      "Delete this capture? This can't be undone from the dashboard.",
    );
    expect(apiJSONMock).toHaveBeenCalledWith("/captures/42", {
      method: "DELETE",
    });
    await vi.waitFor(() => {
      expect(pushMock).toHaveBeenCalledWith("/");
    });
  });

  it("doesn't delete the capture when the confirmation is declined", async () => {
    await renderLoaded();

    confirmMock.mockReturnValue(false);
    await fireEvent.click(
      screen.getByRole("button", { name: "Delete capture" }),
    );

    expect(apiJSONMock).not.toHaveBeenCalledWith(
      "/captures/42",
      expect.objectContaining({ method: "DELETE" }),
    );
    expect(pushMock).not.toHaveBeenCalled();
  });

  it("shows the API's own error message when delete fails, without navigating away", async () => {
    await renderLoaded();

    confirmMock.mockReturnValue(true);
    apiJSONMock.mockRejectedValueOnce(new ApiError(500, "delete failed"));
    await fireEvent.click(
      screen.getByRole("button", { name: "Delete capture" }),
    );

    expect(await screen.findByText("delete failed")).toBeTruthy();
    expect(pushMock).not.toHaveBeenCalled();
  });

  it("queues a summary regeneration, showing a transient confirmation", async () => {
    await renderLoaded();

    apiJSONMock.mockResolvedValueOnce(undefined);
    await fireEvent.click(
      screen.getByRole("button", { name: "Regenerate summary" }),
    );

    expect(apiJSONMock).toHaveBeenCalledWith(
      "/captures/42/regenerate-summary",
      { method: "POST" },
    );
    expect(await screen.findByRole("button", { name: "Queued!" })).toBeTruthy();
  });

  it("shows the API's own error message when queuing a summary regeneration fails", async () => {
    await renderLoaded();

    apiJSONMock.mockRejectedValueOnce(
      new ApiError(404, "no AI job to regenerate for this capture"),
    );
    await fireEvent.click(
      screen.getByRole("button", { name: "Regenerate summary" }),
    );

    expect(
      await screen.findByText("no AI job to regenerate for this capture"),
    ).toBeTruthy();
  });

  it("queues a readability regeneration, showing a transient confirmation", async () => {
    await renderLoaded();

    apiJSONMock.mockResolvedValueOnce(undefined);
    await fireEvent.click(
      screen.getByRole("button", { name: "Regenerate extracted text" }),
    );

    expect(apiJSONMock).toHaveBeenCalledWith(
      "/captures/42/regenerate-readability",
      { method: "POST" },
    );
    expect(await screen.findByRole("button", { name: "Queued!" })).toBeTruthy();
  });

  it("shows the API's own error message when queuing a readability regeneration fails", async () => {
    await renderLoaded();

    apiJSONMock.mockRejectedValueOnce(new ApiError(500, "queue unavailable"));
    await fireEvent.click(
      screen.getByRole("button", { name: "Regenerate extracted text" }),
    );

    expect(await screen.findByText("queue unavailable")).toBeTruthy();
  });
});
