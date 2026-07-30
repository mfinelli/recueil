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

// This is not a general-purpose markdown library: page notes support
// exactly three constructs (bold, italic, simple lists), and a fixed,
// hand-rolled output vocabulary means every possible output is one of
// <strong>/<em>/<ul>/<li>/<p>/<br> wrapping escaped text -- there's no
// separate HTML-sanitization step to get right (or forget), unlike a
// general markdown parser, which would accept a much larger input
// grammar than notes need and require sanitizing its output separately.
// Store the raw source (pages.notes), render on read -- same "store
// source, derive presentation client-side" choice reader_text and
// ai_summary already made (see CaptureReader.svelte's own doc comment),
// just with actual formatting this time instead of plain text.

function escapeHtml(text: string): string {
  return text
    .replace(/&/g, "&amp;")
    .replace(/</g, "&lt;")
    .replace(/>/g, "&gt;");
}

// Bold first (**x**), so its own asterisks aren't later mistaken for
// italic markers. Asterisk-italic can sit directly against a word
// (*word*), matching common markdown convention; underscore-italic
// requires a non-word character (or start/end of string) on both
// outsides, so identifiers like `my_variable_name` are left alone --
function renderInline(text: string): string {
  let html = escapeHtml(text);
  html = html.replace(/\*\*(.+?)\*\*/g, "<strong>$1</strong>");
  html = html.replace(/\*(.+?)\*/g, "<em>$1</em>");
  html = html.replace(/(?<![\w])_(.+?)_(?![\w])/g, "<em>$1</em>");
  return html;
}

const LIST_ITEM = /^[-*]\s+(.*)$/;

// Blocks: consecutive "- "/"* " lines become one <ul>, blank lines
// separate paragraphs, everything else joins into a <p> with <br>
// between its own lines. No headings, links, code spans, or nested
// lists -- notes are a light annotation field, not a document format.
export function renderMarkdown(source: string): string {
  const trimmed = source.trim();
  if (trimmed === "") return "";

  const lines = trimmed.split("\n");
  let html = "";
  let inList = false;
  let paragraphLines: string[] = [];

  function flushParagraph() {
    if (paragraphLines.length > 0) {
      html += `<p>${paragraphLines.map(renderInline).join("<br>")}</p>`;
      paragraphLines = [];
    }
  }

  for (const line of lines) {
    const listMatch = line.match(LIST_ITEM);
    if (listMatch) {
      flushParagraph();
      if (!inList) {
        html += "<ul>";
        inList = true;
      }
      html += `<li>${renderInline(listMatch[1])}</li>`;
      continue;
    }
    if (inList) {
      html += "</ul>";
      inList = false;
    }
    if (line.trim() === "") {
      flushParagraph();
    } else {
      paragraphLines.push(line);
    }
  }
  if (inList) html += "</ul>";
  flushParagraph();

  return html;
}
