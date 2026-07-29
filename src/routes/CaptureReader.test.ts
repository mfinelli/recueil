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
//
// Three concurrent GETs now (capture, capture-config, text-search-configs),
// same URL-dispatching mockLoad()/render helper pattern PageDetail.test.ts
// already uses for its own three-way Promise.allSettled load.
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
import type { CaptureDetail, CaptureConfig } from "../lib/types";
import CaptureReader from "./CaptureReader.svelte";

const apiJSONMock = vi.mocked(apiJSON);
const pushMock = vi.mocked(push);
const confirmMock = vi.fn();
const writeTextMock = vi.fn().mockResolvedValue(undefined);

vi.stubGlobal("confirm", confirmMock);
Object.defineProperty(navigator, "clipboard", {
  value: { writeText: writeTextMock },
  configurable: true,
});

afterEach(() => {
  cleanup();
  apiJSONMock.mockReset();
  pushMock.mockClear();
  confirmMock.mockReset();
  writeTextMock.mockClear();
  localStorage.clear();
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
  content_hash: "a1b2c3d4e5f6a7b8c9d0e1f2a3b4c5d6",
  ai_summary: null,
  ai_model: null,
  language: "en",
  html_compressed_size_bytes: 1024,
  html_uncompressed_size_bytes: 4096,
  captured_at: "2026-05-01T12:00:00Z",
  created_at: "2026-05-01T12:00:00Z",
  updated_at: "2026-05-01T12:00:00Z",
};

// null/null: matches baseCapture's own null readability_version/ai_model
// exactly, which would actually *hide* both regenerate buttons under the
// real showReadabilityRegenerate/showSummaryRegenerate equality check
// (null === null). Individual tests override this whenever they need a
// button visible.
const upToDateConfig: CaptureConfig = {
  readability_version: null,
  ai_model: null,
};

interface LoadOptions {
  id?: string;
  capture?: CaptureDetail;
  captureError?: Error;
  captureConfig?: CaptureConfig;
  captureConfigError?: Error;
  languages?: string[];
  languagesError?: Error;
}

function mockLoad({
  id = "42",
  capture = baseCapture,
  captureError,
  captureConfig = upToDateConfig,
  captureConfigError,
  languages = [],
  languagesError,
}: LoadOptions = {}) {
  apiJSONMock.mockImplementation(
    (path: string, options?: { method?: string }) => {
      const method = options?.method ?? "GET";
      if (path === `/captures/${id}` && method === "GET") {
        return captureError
          ? Promise.reject(captureError)
          : Promise.resolve(capture);
      }
      if (path === "/capture-config" && method === "GET") {
        return captureConfigError
          ? Promise.reject(captureConfigError)
          : Promise.resolve(captureConfig);
      }
      if (path === "/text-search-configs" && method === "GET") {
        return languagesError
          ? Promise.reject(languagesError)
          : Promise.resolve({ languages });
      }
      throw new Error(`unexpected apiJSON call: ${method} ${path}`);
    },
  );
}

function renderReader(overrides: LoadOptions = {}) {
  mockLoad(overrides);
  return render(CaptureReader, { params: { id: overrides.id ?? "42" } });
}

describe("CaptureReader", () => {
  it("shows a loading state, then fetches the capture by the id from params", () => {
    renderReader();

    expect(screen.getByText("Loading…")).toBeTruthy();
    expect(apiJSONMock).toHaveBeenCalledWith("/captures/42");
  });

  it("shows the API's own error message when the fetch fails with ApiError", async () => {
    renderReader({
      id: "999",
      captureError: new ApiError(404, "capture not found"),
    });

    expect(await screen.findByText("capture not found")).toBeTruthy();
  });

  it("falls back to a generic error message for a non-ApiError failure", async () => {
    renderReader({ captureError: new Error("network error") });

    expect(await screen.findByText("failed to load capture")).toBeTruthy();
  });

  it("renders the title, favicon, byline, and extracted reader text on success", async () => {
    const { container } = renderReader({
      capture: { ...baseCapture, favicon_path: "/some/path.png" },
    });

    expect(
      await screen.findByRole("heading", { name: "An example article" }),
    ).toBeTruthy();

    expect(screen.getByText(/^Captured /)).toBeTruthy();
    expect(screen.getByText(/via browser extension$/)).toBeTruthy();

    const rawUrlLink = screen.getByRole("link", {
      name: "https://example.com/article",
    });
    expect(rawUrlLink).toHaveProperty("href", "https://example.com/article");

    const archivedLink = screen.getByRole("link", {
      name: "View archived page",
    });
    expect(archivedLink).toHaveProperty(
      "href",
      "http://localhost:3000/api/captures/42/html",
    );

    const backLink = screen.getByRole("link", { name: "Back to page" });
    expect(backLink).toHaveProperty("hash", "#/pages/7");

    const favicon = container.querySelector<HTMLImageElement>(".favicon");
    expect(favicon?.src).toContain("/api/captures/42/favicon");

    expect(
      screen.getByText("The full extracted body text of the article."),
    ).toBeTruthy();
  });

  it("calls out a manual_upload capture's source specifically", async () => {
    renderReader({ capture: { ...baseCapture, source: "manual_upload" } });

    expect(await screen.findByText(/via manual upload$/)).toBeTruthy();
  });

  it("shows no favicon at all (no placeholder) when favicon_path is null", async () => {
    const { container } = renderReader();
    await screen.findByRole("heading", { name: "An example article" });

    expect(container.querySelector(".favicon")).toBeNull();
  });

  it("hides the favicon on a broken image, again with no placeholder", async () => {
    const { container } = renderReader({
      capture: { ...baseCapture, favicon_path: "/some/path.png" },
    });
    await screen.findByRole("heading", { name: "An example article" });

    const favicon = container.querySelector<HTMLImageElement>(".favicon");
    await fireEvent.error(favicon as HTMLImageElement);

    expect(container.querySelector(".favicon")).toBeNull();
  });

  it("falls back to the raw URL as the heading when there's no title", async () => {
    renderReader({ capture: { ...baseCapture, title: null } });

    expect(
      await screen.findByRole("heading", {
        name: "https://example.com/article",
      }),
    ).toBeTruthy();
  });

  it("shows a placeholder message instead of reader text when there is none", async () => {
    renderReader({ capture: { ...baseCapture, reader_text: null } });

    expect(
      await screen.findByText("No extracted text for this capture yet."),
    ).toBeTruthy();
  });

  describe("reader font toggle", () => {
    it("defaults to sans and switches to serif, persisting the choice", async () => {
      const { container } = renderReader();
      await screen.findByRole("heading", { name: "An example article" });

      const readerText = () => container.querySelector(".reader-text");
      expect(readerText()?.classList.contains("serif")).toBe(false);

      await fireEvent.click(screen.getByRole("button", { name: "Serif" }));
      expect(readerText()?.classList.contains("serif")).toBe(true);
      expect(localStorage.getItem("recueil:capture-reader-font")).toBe("serif");

      await fireEvent.click(screen.getByRole("button", { name: "Sans" }));
      expect(readerText()?.classList.contains("serif")).toBe(false);
    });

    it("loads the persisted font preference on mount", async () => {
      localStorage.setItem("recueil:capture-reader-font", "serif");
      const { container } = renderReader();
      await screen.findByRole("heading", { name: "An example article" });

      expect(
        container.querySelector(".reader-text")?.classList.contains("serif"),
      ).toBe(true);
    });
  });

  describe("AI summary", () => {
    it("renders the summary, model, and a regenerate button when the model differs from capture-config's current one", async () => {
      renderReader({
        capture: {
          ...baseCapture,
          ai_summary: "A concise AI-generated summary.",
          ai_model: "gpt-4o-mini",
        },
        captureConfig: { readability_version: null, ai_model: "gpt-5" },
      });

      expect(
        await screen.findByText("A concise AI-generated summary."),
      ).toBeTruthy();
      expect(screen.getByText("gpt-4o-mini")).toBeTruthy();
      expect(
        screen.getByRole("button", { name: "Regenerate summary" }),
      ).toBeTruthy();
    });

    it("hides the regenerate-summary button once the model already matches capture-config's current one", async () => {
      renderReader({
        capture: {
          ...baseCapture,
          ai_summary: "A concise AI-generated summary.",
          ai_model: "gpt-4o-mini",
        },
        captureConfig: { readability_version: null, ai_model: "gpt-4o-mini" },
      });

      await screen.findByText("A concise AI-generated summary.");
      expect(
        screen.queryByRole("button", { name: "Regenerate summary" }),
      ).toBeNull();
    });

    it("shows the regenerate-summary button when capture-config fails to load (fails open)", async () => {
      renderReader({
        capture: {
          ...baseCapture,
          ai_summary: "A concise AI-generated summary.",
          ai_model: "gpt-4o-mini",
        },
        captureConfigError: new Error("network error"),
      });

      await screen.findByText("A concise AI-generated summary.");
      expect(
        await screen.findByRole("button", { name: "Regenerate summary" }),
      ).toBeTruthy();
    });

    it("renders no summary block at all when there's no AI summary yet", async () => {
      renderReader();
      await screen.findByRole("heading", { name: "An example article" });

      expect(
        screen.queryByRole("button", { name: "Regenerate summary" }),
      ).toBeNull();
    });
  });

  describe("readability version footer", () => {
    it("shows the version and a regenerate button when it differs from capture-config's current one", async () => {
      renderReader({
        capture: { ...baseCapture, readability_version: "2.1.0" },
        captureConfig: { readability_version: "2.3.1", ai_model: null },
      });

      expect(await screen.findByText(/2\.1\.0/)).toBeTruthy();
      expect(
        screen.getByRole("button", { name: "Regenerate extracted text" }),
      ).toBeTruthy();
    });

    it("hides the regenerate button once the version already matches capture-config's current one", async () => {
      renderReader({
        capture: { ...baseCapture, readability_version: "2.3.1" },
        captureConfig: { readability_version: "2.3.1", ai_model: null },
      });

      await screen.findByRole("heading", { name: "An example article" });
      expect(
        screen.queryByRole("button", { name: "Regenerate extracted text" }),
      ).toBeNull();
    });

    it("shows the regenerate button when capture-config fails to load (fails open)", async () => {
      renderReader({ captureConfigError: new Error("network error") });

      expect(
        await screen.findByRole("button", {
          name: "Regenerate extracted text",
        }),
      ).toBeTruthy();
    });

    it("shows an em dash for a capture with no readability_version yet, still offering regenerate", async () => {
      renderReader({
        capture: { ...baseCapture, readability_version: null },
        captureConfig: { readability_version: "2.3.1", ai_model: null },
      });

      expect(await screen.findByText(/—/)).toBeTruthy();
      expect(
        screen.getByRole("button", { name: "Regenerate extracted text" }),
      ).toBeTruthy();
    });
  });

  describe("language picker", () => {
    it("updates a capture's language on success", async () => {
      renderReader({ languages: ["en", "fr", "de"] });
      const select = await screen.findByRole("combobox", { name: "Language" });

      apiJSONMock.mockResolvedValueOnce(undefined);
      await fireEvent.change(select, { target: { value: "fr" } });

      expect(apiJSONMock).toHaveBeenCalledWith("/captures/42/language", {
        method: "PATCH",
        body: { language: "fr" },
      });
      expect(select).toHaveProperty("value", "fr");
    });

    it("shows an error when the language update fails, but doesn't correct the select's now-stale value", async () => {
      renderReader({ languages: ["en", "fr", "de"] });
      const select = await screen.findByRole("combobox", { name: "Language" });

      apiJSONMock.mockRejectedValueOnce(new ApiError(500, "update rejected"));
      await fireEvent.change(select, { target: { value: "fr" } });

      expect(await screen.findByText("update rejected")).toBeTruthy();
      expect(select).toHaveProperty("value", "fr");
    });

    it("labels each language option in the dashboard's own locale, not the raw pg_ts_config name", async () => {
      renderReader({ languages: ["english", "french", "german"] });
      const select = await screen.findByRole("combobox", { name: "Language" });

      const labels = Array.from(select.querySelectorAll("option")).map(
        (o) => o.textContent,
      );
      expect(labels).toEqual(["English", "French", "German"]);
    });

    it('relabels "simple" as "Other" rather than translating it as a language', async () => {
      renderReader({ languages: ["english", "simple"] });
      const select = await screen.findByRole("combobox", { name: "Language" });

      const labels = Array.from(select.querySelectorAll("option")).map(
        (o) => o.textContent,
      );
      expect(labels).toEqual(["English", "Other"]);
    });

    it("renders no language picker at all when there are no configured languages", async () => {
      renderReader({ languages: [] });
      await screen.findByRole("heading", { name: "An example article" });

      expect(screen.queryByRole("combobox", { name: "Language" })).toBeNull();
    });
  });

  describe("details box", () => {
    it("shows archive size and its sha256 (truncated, with a copy button)", async () => {
      renderReader();
      await screen.findByRole("heading", { name: "An example article" });

      expect(screen.getByText(/1\.0 KB \(4\.0 KB uncompressed\)/)).toBeTruthy();
      expect(
        screen.getByText(`${baseCapture.content_hash.slice(0, 12)}…`),
      ).toBeTruthy();

      await fireEvent.click(
        screen.getByRole("button", {
          name: `Copy ${"Archive sha256"}`,
        }),
      );
      expect(writeTextMock).toHaveBeenCalledWith(baseCapture.content_hash);
      expect(await screen.findByText("Copied!")).toBeTruthy();
    });

    it("omits thumbnail/favicon rows entirely when a capture has neither", async () => {
      renderReader();
      await screen.findByRole("heading", { name: "An example article" });

      expect(screen.queryByText("Thumbnail")).toBeNull();
      expect(screen.queryByText("Favicon")).toBeNull();
    });

    it("shows thumbnail size/hash with its own copy button when present", async () => {
      renderReader({
        capture: {
          ...baseCapture,
          thumbnail_path: "/some/thumb.png",
          thumbnail_size_bytes: 86016,
          thumbnail_hash: "f1e2d3c4b5a6f7e8d9c0b1a2f3e4d5c6",
        },
      });
      await screen.findByRole("heading", { name: "An example article" });

      expect(screen.getByText("84.0 KB")).toBeTruthy();
      await fireEvent.click(
        screen.getByRole("button", { name: "Copy Thumbnail sha256" }),
      );
      expect(writeTextMock).toHaveBeenCalledWith(
        "f1e2d3c4b5a6f7e8d9c0b1a2f3e4d5c6",
      );
    });

    it("shows favicon size/hash with its own copy button when present", async () => {
      renderReader({
        capture: {
          ...baseCapture,
          favicon_path: "/some/favicon.png",
          favicon_size_bytes: 2048,
          favicon_hash: "0011223344556677889900112233445",
        },
      });
      await screen.findByRole("heading", { name: "An example article" });

      expect(screen.getByText("2.0 KB")).toBeTruthy();
      await fireEvent.click(
        screen.getByRole("button", { name: "Copy Favicon sha256" }),
      );
      expect(writeTextMock).toHaveBeenCalledWith(
        "0011223344556677889900112233445",
      );
    });
  });

  it("deletes the capture after confirmation and navigates back to the library", async () => {
    renderReader();
    await screen.findByRole("heading", { name: "An example article" });

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
    renderReader();
    await screen.findByRole("heading", { name: "An example article" });

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
    renderReader();
    await screen.findByRole("heading", { name: "An example article" });

    confirmMock.mockReturnValue(true);
    apiJSONMock.mockRejectedValueOnce(new ApiError(500, "delete failed"));
    await fireEvent.click(
      screen.getByRole("button", { name: "Delete capture" }),
    );

    expect(await screen.findByText("delete failed")).toBeTruthy();
    expect(pushMock).not.toHaveBeenCalled();
  });

  it("queues a summary regeneration, showing a transient confirmation and disabling the button", async () => {
    renderReader({
      capture: {
        ...baseCapture,
        ai_summary: "A concise AI-generated summary.",
        ai_model: "gpt-4o-mini",
      },
      captureConfig: { readability_version: null, ai_model: "gpt-5" },
    });
    await screen.findByText("A concise AI-generated summary.");

    apiJSONMock.mockResolvedValueOnce(undefined);
    const button = screen.getByRole("button", { name: "Regenerate summary" });
    await fireEvent.click(button);

    expect(apiJSONMock).toHaveBeenCalledWith(
      "/captures/42/regenerate-summary",
      { method: "POST" },
    );
    const queuedButton = await screen.findByRole("button", {
      name: "Queued!",
    });
    expect(queuedButton).toHaveProperty("disabled", true);
  });

  it("shows the API's own error message when queuing a summary regeneration fails", async () => {
    renderReader({
      capture: {
        ...baseCapture,
        ai_summary: "A concise AI-generated summary.",
        ai_model: "gpt-4o-mini",
      },
      captureConfig: { readability_version: null, ai_model: "gpt-5" },
    });
    await screen.findByText("A concise AI-generated summary.");

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

  it("queues a readability regeneration, showing a transient confirmation and disabling the button", async () => {
    renderReader({
      capture: { ...baseCapture, readability_version: "2.1.0" },
      captureConfig: { readability_version: "2.3.1", ai_model: null },
    });
    await screen.findByRole("heading", { name: "An example article" });

    apiJSONMock.mockResolvedValueOnce(undefined);
    const button = screen.getByRole("button", {
      name: "Regenerate extracted text",
    });
    await fireEvent.click(button);

    expect(apiJSONMock).toHaveBeenCalledWith(
      "/captures/42/regenerate-readability",
      { method: "POST" },
    );
    const queuedButton = await screen.findByRole("button", {
      name: "Queued!",
    });
    expect(queuedButton).toHaveProperty("disabled", true);
  });

  it("shows the API's own error message when queuing a readability regeneration fails", async () => {
    renderReader({
      capture: { ...baseCapture, readability_version: "2.1.0" },
      captureConfig: { readability_version: "2.3.1", ai_model: null },
    });
    await screen.findByRole("heading", { name: "An example article" });

    apiJSONMock.mockRejectedValueOnce(new ApiError(500, "queue unavailable"));
    await fireEvent.click(
      screen.getByRole("button", { name: "Regenerate extracted text" }),
    );

    expect(await screen.findByText("queue unavailable")).toBeTruthy();
  });
});
