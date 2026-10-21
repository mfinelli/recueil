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

// Mocks the way ManualUploadModal's own tests do: apiJSON replaced, push
// captured (a successful add navigates away), and the real FileReader driving
// the favicon read.
import { describe, it, expect, vi, afterEach } from "vitest";
import { render, screen, fireEvent, cleanup } from "@testing-library/svelte";

vi.mock("svelte-spa-router", async (importOriginal) => {
  const actual = await importOriginal<typeof import("svelte-spa-router")>();
  return { ...actual, push: vi.fn() };
});

vi.mock("../lib/api", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../lib/api")>();
  return { ...actual, apiJSON: vi.fn() };
});

import { push } from "svelte-spa-router";
import { apiJSON, ApiError } from "../lib/api";
import AddLinkModal from "./AddLinkModal.svelte";

const apiJSONMock = vi.mocked(apiJSON);
const pushMock = vi.mocked(push);

afterEach(() => {
  cleanup();
  apiJSONMock.mockReset();
  pushMock.mockClear();
});

function open() {
  return render(AddLinkModal, { open: true });
}

const urlInput = () => screen.getByLabelText("URL") as HTMLInputElement;
const titleInput = () =>
  screen.getByLabelText("Title (optional)") as HTMLInputElement;
const faviconInput = () =>
  screen.getByLabelText("Favicon (optional)") as HTMLInputElement;
const submit = () => screen.getByRole("button", { name: "Add link" });

async function pickFavicon(file: File) {
  await fireEvent.change(faviconInput(), { target: { files: [file] } });
}

describe("AddLinkModal", () => {
  it("renders nothing while closed", () => {
    render(AddLinkModal, { open: false });
    expect(screen.queryByRole("dialog")).toBeNull();
  });

  it("explains that recueil never visits the page, and asks for a URL, a title and a favicon", () => {
    open();
    expect(screen.getByRole("dialog")).toBeTruthy();
    expect(screen.getByRole("heading", { name: "Add a link" })).toBeTruthy();
    expect(
      screen.getByText(
        "Recueil never visits the page itself, so add a title and an icon here if you want them.",
      ),
    ).toBeTruthy();
    expect(urlInput()).toBeTruthy();
    expect(titleInput().placeholder).toBe("Shown instead of the URL");
    expect(screen.getByText(".svg, .png, or .ico")).toBeTruthy();
  });

  it("accepts only icon file types", () => {
    open();
    expect(faviconInput().accept).toBe(".svg,.png,.ico");
  });

  it("requires a URL, without calling the API", async () => {
    open();
    await fireEvent.click(submit());
    expect(await screen.findByText("URL is required.")).toBeTruthy();
    expect(apiJSONMock).not.toHaveBeenCalled();
  });

  it("adds a link from just a URL, then goes to its screen", async () => {
    apiJSONMock.mockResolvedValueOnce({ id: 9 });
    open();
    await fireEvent.input(urlInput(), {
      target: { value: "  https://example.com/page  " },
    });
    await fireEvent.click(submit());

    expect(apiJSONMock).toHaveBeenCalledWith("/links", {
      method: "POST",
      body: { url: "https://example.com/page" },
    });
    await vi.waitFor(() => expect(pushMock).toHaveBeenCalledWith("/links/9"));
  });

  it("sends a trimmed title, and leaves a blank one out", async () => {
    apiJSONMock.mockResolvedValue({ id: 9 });
    open();
    await fireEvent.input(urlInput(), {
      target: { value: "https://a.example" },
    });
    await fireEvent.input(titleInput(), { target: { value: "  Jellyfin  " } });
    await fireEvent.click(submit());
    expect(apiJSONMock).toHaveBeenLastCalledWith("/links", {
      method: "POST",
      body: { url: "https://a.example", title: "Jellyfin" },
    });

    cleanup();
    open();
    await fireEvent.input(urlInput(), {
      target: { value: "https://b.example" },
    });
    await fireEvent.input(titleInput(), { target: { value: "   " } });
    await fireEvent.click(submit());
    expect(apiJSONMock).toHaveBeenLastCalledWith("/links", {
      method: "POST",
      body: { url: "https://b.example" },
    });
  });

  it("sends a chosen favicon as its extension and base64 bytes", async () => {
    apiJSONMock.mockResolvedValueOnce({ id: 9 });
    open();
    await fireEvent.input(urlInput(), {
      target: { value: "https://example.com" },
    });
    await pickFavicon(
      new File(["icon bytes"], "Site.PNG", { type: "image/png" }),
    );
    expect(screen.getByText("Site.PNG")).toBeTruthy();
    await fireEvent.click(submit());

    await vi.waitFor(() =>
      expect(apiJSONMock).toHaveBeenCalledWith("/links", {
        method: "POST",
        body: {
          url: "https://example.com",
          favicon_ext: "png",
          favicon_data: btoa("icon bytes"),
        },
      }),
    );
  });

  it("removes a chosen favicon again", async () => {
    open();
    await pickFavicon(new File(["x"], "icon.ico"));
    expect(screen.getByText("icon.ico")).toBeTruthy();

    await fireEvent.click(screen.getByRole("button", { name: "Remove file" }));
    expect(screen.queryByText("icon.ico")).toBeNull();
    expect(screen.getByText("Choose a file…")).toBeTruthy();
  });

  it("refuses an obviously too-large favicon without sending anything", async () => {
    open();
    await fireEvent.input(urlInput(), {
      target: { value: "https://example.com" },
    });
    await pickFavicon(new File([new Uint8Array(512 * 1024 + 1)], "huge.png"));
    await fireEvent.click(submit());

    expect(
      await screen.findByText(/larger than the 512\.0 KB limit/),
    ).toBeTruthy();
    expect(apiJSONMock).not.toHaveBeenCalled();
  });

  it("shows the server's own error, and stays open", async () => {
    // A syntactically valid URL, so the browser's own type=url validation lets
    // the submit through; the server can still refuse it.
    apiJSONMock.mockRejectedValueOnce(new ApiError(400, "invalid url"));
    open();
    await fireEvent.input(urlInput(), {
      target: { value: "https://nope.invalid" },
    });
    await fireEvent.click(submit());

    expect(await screen.findByText("invalid url")).toBeTruthy();
    expect(screen.getByRole("dialog")).toBeTruthy();
    expect(pushMock).not.toHaveBeenCalled();
  });

  it("falls back to a generic message for a non-API failure", async () => {
    apiJSONMock.mockRejectedValueOnce(new Error("network down"));
    open();
    await fireEvent.input(urlInput(), {
      target: { value: "https://example.com" },
    });
    await fireEvent.click(submit());
    expect(
      await screen.findByText("Something went wrong. Please try again."),
    ).toBeTruthy();
  });

  it("offers to open the existing link when the URL is already saved", async () => {
    apiJSONMock.mockRejectedValueOnce(
      new ApiError(409, "this URL is already saved as a link", {
        error: "this URL is already saved as a link",
        link_id: 33,
      }),
    );
    open();
    await fireEvent.input(urlInput(), {
      target: { value: "https://example.com" },
    });
    await fireEvent.click(submit());

    expect(
      await screen.findByText(/This URL is already saved as a link\./),
    ).toBeTruthy();
    const openIt = screen.getByRole("link", { name: "Open it" });
    expect(openIt.getAttribute("href")).toBe("#/links/33");
    expect(pushMock).not.toHaveBeenCalled();
  });

  it("closes, and clears the form, when the existing link is opened", async () => {
    apiJSONMock.mockRejectedValueOnce(
      new ApiError(409, "already", { link_id: 33 }),
    );
    const { component } = render(AddLinkModal, { open: true });
    void component;
    await fireEvent.input(urlInput(), {
      target: { value: "https://example.com" },
    });
    await fireEvent.click(submit());
    await fireEvent.click(await screen.findByRole("link", { name: "Open it" }));

    expect(screen.queryByRole("dialog")).toBeNull();
  });

  it("treats a 409 with no link id as an ordinary error", async () => {
    apiJSONMock.mockRejectedValueOnce(new ApiError(409, "conflict"));
    open();
    await fireEvent.input(urlInput(), {
      target: { value: "https://example.com" },
    });
    await fireEvent.click(submit());
    expect(await screen.findByText("conflict")).toBeTruthy();
    expect(screen.queryByRole("link", { name: "Open it" })).toBeNull();
  });

  it("closes on Cancel, the close button and Escape, discarding what was typed", async () => {
    const { unmount } = open();
    await fireEvent.input(urlInput(), {
      target: { value: "https://example.com" },
    });
    await fireEvent.click(screen.getByRole("button", { name: "Cancel" }));
    expect(screen.queryByRole("dialog")).toBeNull();
    unmount();

    open();
    await fireEvent.click(screen.getByRole("button", { name: "Close" }));
    expect(screen.queryByRole("dialog")).toBeNull();
    cleanup();

    open();
    await fireEvent.keyDown(window, { key: "Escape" });
    expect(screen.queryByRole("dialog")).toBeNull();
  });

  it("disables the form while a request is in flight", async () => {
    let resolve: (v: unknown) => void = () => {};
    apiJSONMock.mockReturnValueOnce(new Promise((r) => (resolve = r)));
    open();
    await fireEvent.input(urlInput(), {
      target: { value: "https://example.com" },
    });
    await fireEvent.click(submit());

    expect(
      (await screen.findByRole("button", {
        name: "Adding…",
      })) as HTMLButtonElement,
    ).toHaveProperty("disabled", true);
    expect(urlInput().disabled).toBe(true);
    resolve({ id: 1 });
  });
});
