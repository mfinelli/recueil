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

import { describe, it, expect, vi, afterEach } from "vitest";
import { render, screen, fireEvent, cleanup } from "@testing-library/svelte";

vi.mock("../lib/api", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../lib/api")>();
  return { ...actual, apiJSON: vi.fn() };
});

import { apiJSON, ApiError } from "../lib/api";
import { session } from "../lib/session.svelte";
import type { WaybackState } from "../lib/types";
import WaybackBlock from "./WaybackBlock.svelte";

const apiJSONMock = vi.mocked(apiJSON);

afterEach(() => {
  cleanup();
  apiJSONMock.mockReset();
  session.waybackEnabled = false;
});

const URL = "https://example.com/article?id=7";
const SUBMIT_PATH = "/links/42/wayback";
const CALENDAR = `https://web.archive.org/web/*/${URL}`;

const pendingState: WaybackState = {
  status: "pending",
  submitted_at: "2026-10-12T15:41:00Z",
  last_submitted_at: null,
  error: null,
};
const succeededState: WaybackState = {
  status: "succeeded",
  submitted_at: "2026-10-12T15:41:00Z",
  last_submitted_at: "2026-10-12T15:41:00Z",
  error: null,
};
const failedState: WaybackState = {
  status: "failed",
  submitted_at: "2026-10-12T15:41:00Z",
  last_submitted_at: null,
  error: "Couldn't resolve host.",
};

function show(
  wayback: WaybackState | null,
  { enabled = true, onchange = vi.fn() } = {},
) {
  session.waybackEnabled = enabled;
  render(WaybackBlock, {
    url: URL,
    wayback,
    submitPath: SUBMIT_PATH,
    onchange,
  });
  return onchange;
}

const button = () => screen.getByRole("button");

function formatted(iso: string) {
  return new Date(iso).toLocaleString(undefined, {
    year: "numeric",
    month: "short",
    day: "numeric",
    hour: "numeric",
    minute: "2-digit",
  });
}

describe("WaybackBlock", () => {
  describe("never submitted", () => {
    it("says so, links to the calendar of existing snapshots, and offers to submit", () => {
      show(null);

      expect(
        screen.getByRole("heading", { name: "Internet Archive" }),
      ).toBeTruthy();
      expect(
        screen.getByText("Not submitted to the Wayback Machine."),
      ).toBeTruthy();
      const check = screen.getByRole("link", {
        name: "Check for existing snapshots",
      });
      expect(check.getAttribute("href")).toBe(CALENDAR);
      expect(check.getAttribute("target")).toBe("_blank");
      expect(check.getAttribute("rel")).toBe("noreferrer");
      expect((button() as HTMLButtonElement).disabled).toBe(false);
      expect(button().textContent).toContain("Submit to Wayback Machine");
    });

    it("keeps the privacy line, which says the URL is shared and the result public", () => {
      show(null);
      const line = screen.getByText(
        /fetches this URL itself, as a logged-out visitor/,
      );
      expect(line.textContent).toContain("shares the URL with a third party");
      expect(line.textContent).toContain("will be public");
    });
  });

  describe("without a configured key pair", () => {
    it("offers only the calendar link: no button, and no privacy line for a submission that can't happen", () => {
      show(null, { enabled: false });

      expect(screen.queryByRole("button")).toBeNull();
      expect(screen.queryByText(/logged-out visitor/)).toBeNull();
      expect(
        screen.getByRole("link", { name: "Check for existing snapshots" }),
      ).toBeTruthy();
    });

    it("still shows what was recorded before the keys were removed", () => {
      show(succeededState, { enabled: false });
      expect(screen.getByText("Submitted to the Wayback Machine")).toBeTruthy();
      expect(screen.getByRole("link", { name: "View snapshots" })).toBeTruthy();
      expect(screen.queryByRole("button")).toBeNull();
    });
  });

  describe("pending", () => {
    it("says it's in progress and to reload, with the button off", () => {
      show(pendingState);

      expect(
        screen.getByText("Submitting to the Wayback Machine…"),
      ).toBeTruthy();
      expect(
        screen.getByText(
          "This can take a few minutes. Reload the page to see the result.",
        ),
      ).toBeTruthy();
      expect((button() as HTMLButtonElement).disabled).toBe(true);
      expect(button().textContent).toContain("Submitting…");
    });

    it("has no last-submitted line when nothing has succeeded, and links to check for snapshots", () => {
      show(pendingState);
      expect(screen.queryByText(/Last submitted/)).toBeNull();
      expect(
        screen.getByRole("link", { name: "Check for existing snapshots" }),
      ).toBeTruthy();
    });

    it("on a re-submission, still shows the earlier success and the link to view snapshots", () => {
      show({ ...pendingState, last_submitted_at: "2026-09-01T10:00:00Z" });
      expect(
        screen.getByText(`Last submitted ${formatted("2026-09-01T10:00:00Z")}`),
      ).toBeTruthy();
      expect(screen.getByRole("link", { name: "View snapshots" })).toBeTruthy();
    });
  });

  describe("succeeded", () => {
    it("shows when it was last submitted, links to view the snapshots, and offers to submit again", () => {
      show(succeededState);

      expect(screen.getByText("Submitted to the Wayback Machine")).toBeTruthy();
      expect(
        screen.getByText(
          `Last submitted ${formatted(succeededState.last_submitted_at!)}`,
        ),
      ).toBeTruthy();
      const view = screen.getByRole("link", { name: "View snapshots" });
      expect(view.getAttribute("href")).toBe(CALENDAR);
      expect(view.getAttribute("target")).toBe("_blank");
      expect(button().textContent).toContain("Submit again");
      expect((button() as HTMLButtonElement).disabled).toBe(false);
    });

    it("stores and shows no snapshot URL: only the calendar for the submitted URL", () => {
      show(succeededState);
      const hrefs = screen
        .getAllByRole("link")
        .map((a) => a.getAttribute("href"));
      expect(hrefs).toEqual([CALENDAR]);
    });
  });

  describe("failed", () => {
    it("shows archive.org's reason and offers to try again, linking to check for snapshots", () => {
      show(failedState);

      expect(
        screen.getByText(
          "The last attempt to submit this to the Wayback Machine failed.",
        ),
      ).toBeTruthy();
      expect(screen.getByText("Couldn't resolve host.")).toBeTruthy();
      expect(button().textContent).toContain("Try again");
      expect((button() as HTMLButtonElement).disabled).toBe(false);
      expect(
        screen.getByRole("link", { name: "Check for existing snapshots" }),
      ).toBeTruthy();
    });

    it("keeps an earlier success visible beside the failure", () => {
      show({ ...failedState, last_submitted_at: "2026-09-01T10:00:00Z" });

      expect(screen.getByText("Couldn't resolve host.")).toBeTruthy();
      expect(
        screen.getByText(`Last submitted ${formatted("2026-09-01T10:00:00Z")}`),
      ).toBeTruthy();
      expect(screen.getByRole("link", { name: "View snapshots" })).toBeTruthy();
    });

    it("copes with a failure that has no message", () => {
      show({ ...failedState, error: null });
      expect(
        screen.getByText(
          "The last attempt to submit this to the Wayback Machine failed.",
        ),
      ).toBeTruthy();
    });
  });

  describe("submitting", () => {
    it("posts to the item's own route and hands the pending state it gets back to the screen", async () => {
      apiJSONMock.mockResolvedValueOnce(pendingState);
      const onchange = show(null);

      await fireEvent.click(button());

      expect(apiJSONMock).toHaveBeenCalledWith(SUBMIT_PATH, { method: "POST" });
      await vi.waitFor(() =>
        expect(onchange).toHaveBeenCalledWith(pendingState),
      );
    });

    it("shows 'Submitting…' with the button off while the request is in flight", async () => {
      let resolve: (v: WaybackState) => void = () => {};
      apiJSONMock.mockReturnValueOnce(new Promise((r) => (resolve = r)));
      show(null);

      await fireEvent.click(button());

      expect(button().textContent).toContain("Submitting…");
      expect((button() as HTMLButtonElement).disabled).toBe(true);
      resolve(pendingState);
      await vi.waitFor(() =>
        expect((button() as HTMLButtonElement).disabled).toBe(false),
      );
    });

    it("shows archive.org's own message when the server relays a refusal, and leaves the button usable", async () => {
      apiJSONMock.mockRejectedValueOnce(
        new ApiError(502, "This URL has been captured too many times today."),
      );
      const onchange = show(null);

      await fireEvent.click(button());

      expect(
        await screen.findByText(
          "This URL has been captured too many times today.",
        ),
      ).toBeTruthy();
      expect(onchange).not.toHaveBeenCalled();
      expect((button() as HTMLButtonElement).disabled).toBe(false);
    });

    it("shows the server's message for a submission already in progress", async () => {
      apiJSONMock.mockRejectedValueOnce(
        new ApiError(409, "a submission for this URL is already in progress"),
      );
      show(null);
      await fireEvent.click(button());
      expect(
        await screen.findByText(
          "a submission for this URL is already in progress",
        ),
      ).toBeTruthy();
    });

    it("falls back to a generic message for a non-API failure", async () => {
      apiJSONMock.mockRejectedValueOnce(new Error("network down"));
      show(null);
      await fireEvent.click(button());
      expect(
        await screen.findByText("failed to submit to the Wayback Machine"),
      ).toBeTruthy();
    });

    it("clears an earlier error when it's tried again", async () => {
      apiJSONMock.mockRejectedValueOnce(new ApiError(502, "no luck"));
      apiJSONMock.mockResolvedValueOnce(pendingState);
      show(null);
      await fireEvent.click(button());
      await screen.findByText("no luck");

      await fireEvent.click(button());

      await vi.waitFor(() => expect(screen.queryByText("no luck")).toBeNull());
    });
  });

  it("builds the calendar link from the URL exactly as given, query and all", () => {
    show(null);
    expect(
      screen
        .getByRole("link", { name: "Check for existing snapshots" })
        .getAttribute("href"),
    ).toBe("https://web.archive.org/web/*/https://example.com/article?id=7");
  });
});
