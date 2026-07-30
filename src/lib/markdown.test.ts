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

import { describe, it, expect } from "vitest";
import { renderMarkdown } from "./markdown";

describe("renderMarkdown", () => {
  it("returns an empty string for empty or whitespace-only input", () => {
    expect(renderMarkdown("")).toBe("");
    expect(renderMarkdown("   \n  \n  ")).toBe("");
  });

  it("wraps a plain line in a paragraph", () => {
    expect(renderMarkdown("just some text")).toBe("<p>just some text</p>");
  });

  it("renders bold", () => {
    expect(renderMarkdown("this is **bold** text")).toBe(
      "<p>this is <strong>bold</strong> text</p>",
    );
  });

  it("renders asterisk italic, including directly against a word", () => {
    expect(renderMarkdown("this is *italic* text")).toBe(
      "<p>this is <em>italic</em> text</p>",
    );
    expect(renderMarkdown("*word*-adjacent")).toBe(
      "<p><em>word</em>-adjacent</p>",
    );
  });

  it("renders underscore italic", () => {
    expect(renderMarkdown("this is _italic_ text")).toBe(
      "<p>this is <em>italic</em> text</p>",
    );
  });

  it("does not treat underscores inside an identifier as italic", () => {
    expect(renderMarkdown("check my_variable_name works")).toBe(
      "<p>check my_variable_name works</p>",
    );
  });

  it("does not confuse bold's own asterisks with italic", () => {
    expect(renderMarkdown("**bold** and *italic*")).toBe(
      "<p><strong>bold</strong> and <em>italic</em></p>",
    );
  });

  it("renders a simple list with either - or * markers", () => {
    expect(renderMarkdown("- one\n- two\n* three")).toBe(
      "<ul><li>one</li><li>two</li><li>three</li></ul>",
    );
  });

  it("applies inline formatting inside list items", () => {
    expect(renderMarkdown("- **bold** item\n- _italic_ item")).toBe(
      "<ul><li><strong>bold</strong> item</li><li><em>italic</em> item</li></ul>",
    );
  });

  it("does not treat a bare asterisk-italic line as a list item", () => {
    // No space after the leading "*", so this isn't a list marker --
    // it's the start of an italic span, same as standard markdown's
    // own disambiguation.
    expect(renderMarkdown("*not a list*")).toBe("<p><em>not a list</em></p>");
  });

  it("separates paragraphs on a blank line and joins same-paragraph lines with <br>", () => {
    expect(renderMarkdown("line one\nline two\n\nsecond paragraph")).toBe(
      "<p>line one<br>line two</p><p>second paragraph</p>",
    );
  });

  it("closes a list when a blank line or paragraph text follows it", () => {
    expect(renderMarkdown("- one\n- two\n\nafter the list")).toBe(
      "<ul><li>one</li><li>two</li></ul><p>after the list</p>",
    );
    expect(renderMarkdown("- one\nafter the list")).toBe(
      "<ul><li>one</li></ul><p>after the list</p>",
    );
  });

  it("escapes HTML-significant characters instead of interpreting them", () => {
    expect(renderMarkdown("<script>alert(1)</script> & friends")).toBe(
      "<p>&lt;script&gt;alert(1)&lt;/script&gt; &amp; friends</p>",
    );
  });

  it("leaves an unmatched marker as literal text", () => {
    expect(renderMarkdown("half of *this stays literal")).toBe(
      "<p>half of *this stays literal</p>",
    );
  });

  it("trims leading/trailing whitespace on the whole input", () => {
    expect(renderMarkdown("  \n  hello  \n  ")).toBe("<p>hello</p>");
  });
});
