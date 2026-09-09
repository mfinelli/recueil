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

// open is a $bindable prop, but there's no wrapping harness component here
// since Svelte 5's $bindable still behaves as ordinary local reactive state
// within the component itself even with no bind: and this is from a parent, so
// asserting the modal disappears from the DOM after Cancel/Escape/a
// successful submit is enough; a real bind: wiring is exercised by
// Library.test.ts instead (that's genuinely Library's own behavior to
// verify, not this component's).
import { describe, it, expect, vi, afterEach } from "vitest";
import { render, screen, fireEvent, cleanup } from "@testing-library/svelte";

vi.mock("svelte-spa-router", () => ({ push: vi.fn() }));

vi.mock("../lib/api", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../lib/api")>();
  return { ...actual, apiJSON: vi.fn(), uploadManualCapture: vi.fn() };
});

import { push } from "svelte-spa-router";
import { apiJSON, uploadManualCapture, ApiError } from "../lib/api";
import ManualUploadModal from "./ManualUploadModal.svelte";

const apiJSONMock = vi.mocked(apiJSON);
const uploadMock = vi.mocked(uploadManualCapture);
const pushMock = vi.mocked(push);

function htmlFile(name = "capture.html") {
  return new File(["<html></html>"], name, { type: "text/html" });
}

afterEach(() => {
  cleanup();
  apiJSONMock.mockReset();
  uploadMock.mockReset();
  pushMock.mockReset();
});

describe("ManualUploadModal", () => {
  it("renders nothing when closed", () => {
    apiJSONMock.mockResolvedValue({
      readability_version: null,
      ai_model: null,
      manual_upload_max_bytes: 104857600,
    });
    render(ManualUploadModal, { open: false });
    expect(screen.queryByRole("dialog")).toBeNull();
  });

  it("renders the form when open, and fetches the current size limit", async () => {
    apiJSONMock.mockResolvedValue({
      readability_version: null,
      ai_model: null,
      manual_upload_max_bytes: 104857600,
    });
    render(ManualUploadModal, { open: true });

    expect(screen.getByRole("dialog")).toBeTruthy();
    expect(apiJSONMock).toHaveBeenCalledWith("/capture-config");
    expect(await screen.findByText(/Max 100.0 MB/)).toBeTruthy();
  });

  it("closes on Cancel", async () => {
    apiJSONMock.mockResolvedValue({
      readability_version: null,
      ai_model: null,
      manual_upload_max_bytes: 104857600,
    });
    render(ManualUploadModal, { open: true });

    await fireEvent.click(screen.getByRole("button", { name: "Cancel" }));
    expect(screen.queryByRole("dialog")).toBeNull();
  });

  it("closes on the header close button", async () => {
    apiJSONMock.mockResolvedValue({
      readability_version: null,
      ai_model: null,
      manual_upload_max_bytes: 104857600,
    });
    render(ManualUploadModal, { open: true });

    await fireEvent.click(screen.getByRole("button", { name: "Close" }));
    expect(screen.queryByRole("dialog")).toBeNull();
  });

  it("closes on Escape", async () => {
    apiJSONMock.mockResolvedValue({
      readability_version: null,
      ai_model: null,
      manual_upload_max_bytes: 104857600,
    });
    render(ManualUploadModal, { open: true });

    await fireEvent.keyDown(window, { key: "Escape" });
    expect(screen.queryByRole("dialog")).toBeNull();
  });

  it("shows an error and does not submit when the url is empty", async () => {
    apiJSONMock.mockResolvedValue({
      readability_version: null,
      ai_model: null,
      manual_upload_max_bytes: 104857600,
    });
    render(ManualUploadModal, { open: true });

    await fireEvent.click(screen.getByRole("button", { name: "Upload" }));

    expect(await screen.findByText("URL is required.")).toBeTruthy();
    expect(uploadMock).not.toHaveBeenCalled();
  });

  it("shows an error and does not submit when no html file is chosen", async () => {
    apiJSONMock.mockResolvedValue({
      readability_version: null,
      ai_model: null,
      manual_upload_max_bytes: 104857600,
    });
    render(ManualUploadModal, { open: true });

    await fireEvent.input(screen.getByLabelText("URL"), {
      target: { value: "https://example.com/article" },
    });
    await fireEvent.click(screen.getByRole("button", { name: "Upload" }));

    expect(await screen.findByText("An HTML file is required.")).toBeTruthy();
    expect(uploadMock).not.toHaveBeenCalled();
  });

  it("shows an error, without submitting, when the chosen file exceeds the configured limit", async () => {
    apiJSONMock.mockResolvedValue({
      readability_version: null,
      ai_model: null,
      manual_upload_max_bytes: 10,
    });
    render(ManualUploadModal, { open: true });

    await screen.findByText(/Max 10 B\./);

    await fireEvent.input(screen.getByLabelText("URL"), {
      target: { value: "https://example.com/article" },
    });
    const input = screen.getByLabelText("Captured HTML file", {
      selector: "input",
    }) as HTMLInputElement;
    await fireEvent.change(input, {
      target: { files: [htmlFile()] },
    });
    await fireEvent.click(screen.getByRole("button", { name: "Upload" }));

    expect(
      await screen.findByText(/This upload is larger than the 10 B limit/),
    ).toBeTruthy();
    expect(uploadMock).not.toHaveBeenCalled();
  });

  it("submits url + html file, then navigates to the new page", async () => {
    apiJSONMock.mockResolvedValue({
      readability_version: null,
      ai_model: null,
      manual_upload_max_bytes: 104857600,
    });
    uploadMock.mockResolvedValue({ page_id: 42, capture_id: 7 });
    render(ManualUploadModal, { open: true });

    await fireEvent.input(screen.getByLabelText("URL"), {
      target: { value: "https://example.com/article" },
    });
    const file = htmlFile();
    const input = screen.getByLabelText("Captured HTML file", {
      selector: "input",
    }) as HTMLInputElement;
    await fireEvent.change(input, { target: { files: [file] } });

    await fireEvent.click(screen.getByRole("button", { name: "Upload" }));

    expect(uploadMock).toHaveBeenCalledWith(
      "https://example.com/article",
      file,
      null,
    );
    await vi.waitFor(() => {
      expect(pushMock).toHaveBeenCalledWith("/pages/42");
    });
    expect(screen.queryByRole("dialog")).toBeNull();
  });

  it("displays the backend's own error message on a failed upload, and stays open", async () => {
    apiJSONMock.mockResolvedValue({
      readability_version: null,
      ai_model: null,
      manual_upload_max_bytes: 104857600,
    });
    uploadMock.mockRejectedValue(new ApiError(400, "url is required"));
    render(ManualUploadModal, { open: true });

    await fireEvent.input(screen.getByLabelText("URL"), {
      target: { value: "https://example.com/article" },
    });
    const input = screen.getByLabelText("Captured HTML file", {
      selector: "input",
    }) as HTMLInputElement;
    await fireEvent.change(input, { target: { files: [htmlFile()] } });
    await fireEvent.click(screen.getByRole("button", { name: "Upload" }));

    expect(await screen.findByText("url is required")).toBeTruthy();
    expect(screen.getByRole("dialog")).toBeTruthy();
    expect(pushMock).not.toHaveBeenCalled();
  });

  it("lets a chosen html file be removed and re-chosen", async () => {
    apiJSONMock.mockResolvedValue({
      readability_version: null,
      ai_model: null,
      manual_upload_max_bytes: 104857600,
    });
    render(ManualUploadModal, { open: true });

    const input = screen.getByLabelText("Captured HTML file", {
      selector: "input",
    }) as HTMLInputElement;
    await fireEvent.change(input, { target: { files: [htmlFile()] } });
    expect(screen.getByText("capture.html")).toBeTruthy();

    await fireEvent.click(screen.getByRole("button", { name: "Remove file" }));
    expect(screen.queryByText("capture.html")).toBeNull();
    expect(input.value).toBe("");
  });
});
