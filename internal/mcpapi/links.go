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

package mcpapi

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/mfinelli/recueil/internal/auth"
	"github.com/mfinelli/recueil/internal/db"
)

// The link tools: read-only access to a user's links which are bookmark-only
// entries for things that aren't archived. Registered only when the links
// feature is on (registerLinkTools is called from newServer when enableLinks
// is set), so with it off a client never sees them.
//
// A link has no body text, so there is nothing here like get_page's content: a
// link's value to a model is its title, URL, notes, tags and collections, and,
// when the same URL is also an archived page, that page's id, which is how a
// model gets from "I saved this" to its full text (get_page).
func registerLinkTools(s *mcp.Server, q *db.Queries) {
	t := &tools{q: q}

	mcp.AddTool(s, &mcp.Tool{
		Name: "search_links",
		Description: "Search the saved links: bookmark-only entries for things that aren't archived " +
			"(videos, repositories, apps), so a link has a title, URL, notes and tags but no extracted " +
			"content. Matches the query, case-insensitively, against title, URL and notes; with no query, " +
			"lists the most recently saved. Can be narrowed to a tag or a collection. If a link's URL is " +
			"also an archived page, its page_id says so: pass it to get_page for the page's full text.",
	}, t.searchLinks)

	mcp.AddTool(s, &mcp.Tool{
		Name: "get_link",
		Description: "Get a saved link's metadata (title, URL, notes, tags, collections). A link has no " +
			"content of its own; if the same URL is also archived as a page, page_id is that page's id: " +
			"pass it to get_page for its full text.",
	}, t.getLink)
}

// linkSummary is the shape shared by search_links' results. URL is the URL as
// it was saved (what to open), not the normalized one pages report.
type linkSummary struct {
	ID      int64  `json:"id"`
	URL     string `json:"url" jsonschema:"the URL as it was saved"`
	Title   string `json:"title,omitempty"`
	Notes   string `json:"notes,omitempty"`
	SavedAt string `json:"saved_at"`
	PageID  int64  `json:"page_id,omitempty" jsonschema:"the id of the archived page with this same URL, if there is one; pass it to get_page for the page's full text"`
}

func linkSummaryFromListRow(l *db.ListLinksRow) linkSummary {
	return linkSummary{
		ID: l.ID, URL: l.RawUrl, Title: textOrEmpty(l.Title), Notes: textOrEmpty(l.Notes),
		SavedAt: l.CreatedAt.Time.Format(time.RFC3339), PageID: int8OrZero(l.MatchingPageID),
	}
}

func linkSummaryFromSearchRow(l *db.SearchLinksRow) linkSummary {
	return linkSummary{
		ID: l.ID, URL: l.RawUrl, Title: textOrEmpty(l.Title), Notes: textOrEmpty(l.Notes),
		SavedAt: l.CreatedAt.Time.Format(time.RFC3339), PageID: int8OrZero(l.MatchingPageID),
	}
}

// int8OrZero is this package's own copy of the same nil-coalescing helper
// textOrEmpty is for text: 0 (omitted by omitempty) for NULL.
func int8OrZero(v pgtype.Int8) int64 {
	if !v.Valid {
		return 0
	}
	return v.Int64
}

// escapeLike escapes the characters that mean something to LIKE/ILIKE (the
// escape character itself, %, and _) with a backslash, Postgres's default, so a
// search for "50%" matches literally rather than as a wildcard. This package's
// own copy of internal/httpapi's unexported helper, for the same reason as
// textOrEmpty above.
func escapeLike(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}

type searchLinksInput struct {
	Query        string `json:"query,omitempty" jsonschema:"text to look for in the links' titles, URLs and notes; omit to list the most recently saved links"`
	TagSlug      string `json:"tag_slug,omitempty" jsonschema:"only links carrying this tag, by slug (from list_tags)"`
	CollectionID int64  `json:"collection_id,omitempty" jsonschema:"only links directly in this collection (from list_collections), not its sub-collections"`
	Limit        int    `json:"limit,omitempty" jsonschema:"max results to return, default 20, capped at 100"`
}

type searchLinksOutput struct {
	Links      []linkSummary `json:"links"`
	TotalCount int64         `json:"total_count" jsonschema:"the total number of matching links, which may exceed len(links) if the result was capped by limit"`
}

func (t *tools) searchLinks(ctx context.Context, _ *mcp.CallToolRequest, in searchLinksInput) (*mcp.CallToolResult, searchLinksOutput, error) {
	user, _ := auth.UserFromContext(ctx)
	limit := clampLimit(in.Limit)

	// Each filter is checked against the caller's own tags and collections
	// first, the same ownership-check-then-use shape list_pages_by_tag and
	// list_pages_by_collection have.
	var tagID, collectionID pgtype.Int8
	if in.TagSlug != "" {
		tag, err := t.q.GetTagBySlug(ctx, db.GetTagBySlugParams{Slug: in.TagSlug, UserID: user.ID})
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return errResult("no tag with that slug"), searchLinksOutput{}, nil
			}
			return nil, searchLinksOutput{}, err
		}
		tagID = pgtype.Int8{Int64: tag.ID, Valid: true}
	}
	if in.CollectionID != 0 {
		collection, err := t.q.GetCollectionByID(ctx, db.GetCollectionByIDParams{ID: in.CollectionID, UserID: user.ID})
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return errResult("no collection with that id"), searchLinksOutput{}, nil
			}
			return nil, searchLinksOutput{}, err
		}
		collectionID = pgtype.Int8{Int64: collection.ID, Valid: true}
	}

	out := searchLinksOutput{Links: []linkSummary{}}

	if query := strings.TrimSpace(in.Query); query != "" {
		rows, err := t.q.SearchLinks(ctx, db.SearchLinksParams{
			UserID:  user.ID,
			Pattern: pgtype.Text{String: "%" + escapeLike(query) + "%", Valid: true},
			TagID:   tagID, CollectionID: collectionID,
			Limit: int32(limit), Offset: 0,
		})
		if err != nil {
			return nil, searchLinksOutput{}, err
		}
		for i := range rows {
			out.Links = append(out.Links, linkSummaryFromSearchRow(&rows[i]))
			out.TotalCount = rows[i].TotalCount
		}
		return nil, out, nil
	}

	rows, err := t.q.ListLinks(ctx, db.ListLinksParams{
		UserID: user.ID, TagID: tagID, CollectionID: collectionID,
		Limit: int32(limit), Offset: 0,
	})
	if err != nil {
		return nil, searchLinksOutput{}, err
	}
	for i := range rows {
		out.Links = append(out.Links, linkSummaryFromListRow(&rows[i]))
		out.TotalCount = rows[i].TotalCount
	}
	return nil, out, nil
}

type getLinkInput struct {
	LinkID int64 `json:"link_id" jsonschema:"the link's id, from search_links"`
}

type getLinkOutput struct {
	ID          int64    `json:"id"`
	URL         string   `json:"url" jsonschema:"the URL as it was saved"`
	Title       string   `json:"title,omitempty"`
	Notes       string   `json:"notes,omitempty"`
	Tags        []string `json:"tags,omitempty"`
	Collections []string `json:"collections,omitempty"`
	SavedAt     string   `json:"saved_at"`
	PageID      int64    `json:"page_id,omitempty" jsonschema:"the id of the archived page with this same URL, if there is one; pass it to get_page for the page's full text"`
}

func (t *tools) getLink(ctx context.Context, _ *mcp.CallToolRequest, in getLinkInput) (*mcp.CallToolResult, getLinkOutput, error) {
	user, _ := auth.UserFromContext(ctx)

	link, err := t.q.GetLinkByIDForUser(ctx, db.GetLinkByIDForUserParams{ID: in.LinkID, UserID: user.ID})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return errResult("no link with that id"), getLinkOutput{}, nil
		}
		return nil, getLinkOutput{}, err
	}

	tagRows, err := t.q.ListLinkTags(ctx, link.ID)
	if err != nil {
		return nil, getLinkOutput{}, err
	}
	tags := make([]string, len(tagRows))
	for i, r := range tagRows {
		tags[i] = r.Name
	}

	collectionRows, err := t.q.ListLinkCollections(ctx, link.ID)
	if err != nil {
		return nil, getLinkOutput{}, err
	}
	collections := make([]string, len(collectionRows))
	for i, r := range collectionRows {
		collections[i] = r.Name
	}

	var pageID int64
	page, err := t.q.GetPageRefByNormalizedURL(ctx, db.GetPageRefByNormalizedURLParams{
		UserID: user.ID, NormalizedUrl: link.NormalizedUrl,
	})
	switch {
	case err == nil:
		pageID = page.ID
	case !errors.Is(err, pgx.ErrNoRows):
		return nil, getLinkOutput{}, err
	}

	return nil, getLinkOutput{
		ID:          link.ID,
		URL:         link.RawUrl,
		Title:       textOrEmpty(link.Title),
		Notes:       textOrEmpty(link.Notes),
		Tags:        tags,
		Collections: collections,
		SavedAt:     link.CreatedAt.Time.Format(time.RFC3339),
		PageID:      pageID,
	}, nil
}
