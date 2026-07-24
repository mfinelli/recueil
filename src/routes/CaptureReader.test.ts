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
import { render, screen, cleanup } from "@testing-library/svelte";

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
import type { CaptureDetail } from "../lib/types";
import CaptureReader from "./CaptureReader.svelte";

const apiJSONMock = vi.mocked(apiJSON);

afterEach(() => {
  cleanup();
  apiJSONMock.mockReset();
});

const baseCapture: CaptureDetail = {
  id: 42,
  page_id: 7,
  source: "extension",
  raw_url: "https://example.com/article",
  title: "An example article",
  thumbnail_path: null,
  favicon_path: null,
  reader_text: "The full extracted body text of the article.",
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
});
