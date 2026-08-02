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
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/mfinelli/recueil/internal/auth"
	"github.com/mfinelli/recueil/internal/db"
)

func registerTools(s *mcp.Server, q *db.Queries) {
	t := &tools{q: q}

	mcp.AddTool(s, &mcp.Tool{
		Name:        "search_archive",
		Description: "Full-text search over the saved pages' extracted content. Matches if any capture of a page contains the query terms.",
	}, t.searchArchive)

	mcp.AddTool(s, &mcp.Tool{
		Name:        "list_recent",
		Description: "List the most recently captured pages, without a search query.",
	}, t.listRecent)

	mcp.AddTool(s, &mcp.Tool{
		Name:        "list_tags",
		Description: "List every tag in the archive.",
	}, t.listTags)

	mcp.AddTool(s, &mcp.Tool{
		Name:        "list_pages_by_tag",
		Description: "List pages carrying a given tag, most recently captured first.",
	}, t.listPagesByTag)

	mcp.AddTool(s, &mcp.Tool{
		Name:        "list_collections",
		Description: "List every collection in the archive.",
	}, t.listCollections)

	mcp.AddTool(s, &mcp.Tool{
		Name:        "list_pages_by_collection",
		Description: "List pages in a given collection, most recently captured first.",
	}, t.listPagesByCollection)

	mcp.AddTool(s, &mcp.Tool{
		Name:        "get_page",
		Description: "Get a saved page's metadata (title, URL, notes, tags, collections) and one capture's actual extracted content, defaulting to the most recent capture.",
	}, t.getPage)
}

// tools holds the one dependency every handler needs. Not exported --
// registerTools is the only thing that constructs one, and every method on
// it is a ToolHandlerFor value passed straight to mcp.AddTool, never called
// directly.
type tools struct {
	q *db.Queries
}

// errResult builds the "tool ran, but the answer is a normal failure, not a
// bug" shape -- distinct from returning a Go error, which the SDK surfaces
// as a JSON-RPC protocol-level error instead of a result the caller can
// read and react to. Every "not found" / "doesn't belong to you" case in
// this file uses this, not a Go error.
func errResult(msg string) *mcp.CallToolResult {
	return &mcp.CallToolResult{
		IsError: true,
		Content: []mcp.Content{&mcp.TextContent{Text: msg}},
	}
}

// pageSummary is the shape shared by every list-of-pages tool
// (search_archive, list_recent, list_pages_by_tag, list_pages_by_collection).
type pageSummary struct {
	ID              int64  `json:"id"`
	URL             string `json:"url"`
	Title           string `json:"title,omitempty"`
	Notes           string `json:"notes,omitempty"`
	LatestCaptureAt string `json:"latest_capture_at"`
}

func pageSummaryFromRow(p *db.Page) pageSummary {
	return pageSummary{
		ID:              p.ID,
		URL:             p.NormalizedUrl,
		Title:           textOrEmpty(p.Title),
		Notes:           textOrEmpty(p.Notes),
		LatestCaptureAt: p.LatestCaptureAt.Time.Format(time.RFC3339),
	}
}

// SearchPages/ListPages carry an extra TotalCount column (the COUNT(*)
// OVER() window function), so sqlc generates distinct flat Row types for
// them rather than reusing db.Page -- same reasoning internal/httpapi's
// pageResponseFromSearchRow/pageResponseFromListRow exist for.
func pageSummaryFromSearchRow(p *db.SearchPagesRow) pageSummary {
	return pageSummary{
		ID:              p.ID,
		URL:             p.NormalizedUrl,
		Title:           textOrEmpty(p.Title),
		Notes:           textOrEmpty(p.Notes),
		LatestCaptureAt: p.LatestCaptureAt.Time.Format(time.RFC3339),
	}
}

func pageSummaryFromListRow(p *db.ListPagesRow) pageSummary {
	return pageSummary{
		ID:              p.ID,
		URL:             p.NormalizedUrl,
		Title:           textOrEmpty(p.Title),
		Notes:           textOrEmpty(p.Notes),
		LatestCaptureAt: p.LatestCaptureAt.Time.Format(time.RFC3339),
	}
}

type searchArchiveInput struct {
	Query string `json:"query" jsonschema:"the search terms"`
	Limit int    `json:"limit,omitempty" jsonschema:"max results to return, default 20, capped at 100"`
}

type searchArchiveOutput struct {
	Pages      []pageSummary `json:"pages"`
	TotalCount int64         `json:"total_count" jsonschema:"the total number of matches, which may exceed len(pages) if the result was capped by limit"`
}

func (t *tools) searchArchive(ctx context.Context, _ *mcp.CallToolRequest, in searchArchiveInput) (*mcp.CallToolResult, searchArchiveOutput, error) {
	user, _ := auth.UserFromContext(ctx)
	limit := clampLimit(in.Limit)

	rows, err := t.q.SearchPages(ctx, db.SearchPagesParams{
		UserID: user.ID, Query: in.Query, Limit: int32(limit), Offset: 0,
	})
	if err != nil {
		return nil, searchArchiveOutput{}, err
	}

	out := searchArchiveOutput{Pages: make([]pageSummary, len(rows))}
	for i := range rows {
		out.Pages[i] = pageSummaryFromSearchRow(&rows[i])
		out.TotalCount = rows[i].TotalCount
	}
	return nil, out, nil
}

type listRecentInput struct {
	Limit int `json:"limit,omitempty" jsonschema:"max results to return, default 20, capped at 100"`
}

type listRecentOutput struct {
	Pages      []pageSummary `json:"pages"`
	TotalCount int64         `json:"total_count" jsonschema:"the total number of pages in the archive, which may exceed len(pages) if the result was capped by limit"`
}

func (t *tools) listRecent(ctx context.Context, _ *mcp.CallToolRequest, in listRecentInput) (*mcp.CallToolResult, listRecentOutput, error) {
	user, _ := auth.UserFromContext(ctx)
	limit := clampLimit(in.Limit)

	rows, err := t.q.ListPages(ctx, db.ListPagesParams{
		UserID: user.ID, Limit: int32(limit), Offset: 0,
	})
	if err != nil {
		return nil, listRecentOutput{}, err
	}

	out := listRecentOutput{Pages: make([]pageSummary, len(rows))}
	for i := range rows {
		out.Pages[i] = pageSummaryFromListRow(&rows[i])
		out.TotalCount = rows[i].TotalCount
	}
	return nil, out, nil
}

type listTagsInput struct{}

type tagSummary struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
	Slug string `json:"slug"`
}

type listTagsOutput struct {
	Tags []tagSummary `json:"tags"`
}

func (t *tools) listTags(ctx context.Context, _ *mcp.CallToolRequest, _ listTagsInput) (*mcp.CallToolResult, listTagsOutput, error) {
	user, _ := auth.UserFromContext(ctx)

	rows, err := t.q.ListTags(ctx, user.ID)
	if err != nil {
		return nil, listTagsOutput{}, err
	}

	out := listTagsOutput{Tags: make([]tagSummary, len(rows))}
	for i, r := range rows {
		out.Tags[i] = tagSummary{ID: r.ID, Name: r.Name, Slug: r.Slug}
	}
	return nil, out, nil
}

type listPagesByTagInput struct {
	TagSlug string `json:"tag_slug" jsonschema:"the tag's slug, from list_tags"`
	Limit   int    `json:"limit,omitempty" jsonschema:"max results to return, default 20, capped at 100"`
}

type listPagesByTagOutput struct {
	Pages      []pageSummary `json:"pages"`
	TotalCount int           `json:"total_count" jsonschema:"the total number of pages carrying this tag, which may exceed len(pages) if the result was capped by limit"`
}

func (t *tools) listPagesByTag(ctx context.Context, _ *mcp.CallToolRequest, in listPagesByTagInput) (*mcp.CallToolResult, listPagesByTagOutput, error) {
	user, _ := auth.UserFromContext(ctx)

	tag, err := t.q.GetTagBySlug(ctx, db.GetTagBySlugParams{Slug: in.TagSlug, UserID: user.ID})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return errResult("no tag with that slug"), listPagesByTagOutput{}, nil
		}
		return nil, listPagesByTagOutput{}, err
	}

	rows, err := t.q.ListTagPages(ctx, tag.ID)
	if err != nil {
		return nil, listPagesByTagOutput{}, err
	}

	limit := clampLimit(in.Limit)
	out := listPagesByTagOutput{TotalCount: len(rows)}
	if limit < len(rows) {
		rows = rows[:limit]
	}
	out.Pages = make([]pageSummary, len(rows))
	for i := range rows {
		out.Pages[i] = pageSummaryFromRow(&rows[i])
	}
	return nil, out, nil
}

type listCollectionsInput struct{}

type collectionSummary struct {
	ID       int64  `json:"id"`
	Name     string `json:"name"`
	ParentID int64  `json:"parent_id,omitempty" jsonschema:"omitted for a top-level collection"`
}

type listCollectionsOutput struct {
	Collections []collectionSummary `json:"collections"`
}

func (t *tools) listCollections(ctx context.Context, _ *mcp.CallToolRequest, _ listCollectionsInput) (*mcp.CallToolResult, listCollectionsOutput, error) {
	user, _ := auth.UserFromContext(ctx)

	rows, err := t.q.ListCollectionsByUser(ctx, user.ID)
	if err != nil {
		return nil, listCollectionsOutput{}, err
	}

	out := listCollectionsOutput{Collections: make([]collectionSummary, len(rows))}
	for i := range rows {
		cs := collectionSummary{ID: rows[i].ID, Name: rows[i].Name}
		if rows[i].ParentID.Valid {
			cs.ParentID = rows[i].ParentID.Int64
		}
		out.Collections[i] = cs
	}
	return nil, out, nil
}

type listPagesByCollectionInput struct {
	CollectionID int64 `json:"collection_id" jsonschema:"the collection's id, from list_collections"`
	Limit        int   `json:"limit,omitempty" jsonschema:"max results to return, default 20, capped at 100"`
}

type listPagesByCollectionOutput struct {
	Pages      []pageSummary `json:"pages"`
	TotalCount int           `json:"total_count" jsonschema:"the total number of pages in this collection, which may exceed len(pages) if the result was capped by limit"`
}

func (t *tools) listPagesByCollection(ctx context.Context, _ *mcp.CallToolRequest, in listPagesByCollectionInput) (*mcp.CallToolResult, listPagesByCollectionOutput, error) {
	user, _ := auth.UserFromContext(ctx)

	collection, err := t.q.GetCollectionByID(ctx, db.GetCollectionByIDParams{ID: in.CollectionID, UserID: user.ID})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return errResult("no collection with that id"), listPagesByCollectionOutput{}, nil
		}
		return nil, listPagesByCollectionOutput{}, err
	}

	rows, err := t.q.ListCollectionPages(ctx, collection.ID)
	if err != nil {
		return nil, listPagesByCollectionOutput{}, err
	}

	limit := clampLimit(in.Limit)
	out := listPagesByCollectionOutput{TotalCount: len(rows)}
	if limit < len(rows) {
		rows = rows[:limit]
	}
	out.Pages = make([]pageSummary, len(rows))
	for i := range rows {
		out.Pages[i] = pageSummaryFromRow(&rows[i])
	}
	return nil, out, nil
}

type getPageInput struct {
	PageID    int64 `json:"page_id" jsonschema:"the page's id, from search_archive/list_recent/etc."`
	CaptureID int64 `json:"capture_id,omitempty" jsonschema:"a specific capture's id, from a prior get_page call's other_captures; defaults to the latest capture"`
}

type captureRef struct {
	ID         int64  `json:"id"`
	CapturedAt string `json:"captured_at"`
}

type getPageOutput struct {
	ID            int64        `json:"id"`
	URL           string       `json:"url"`
	Title         string       `json:"title,omitempty"`
	Notes         string       `json:"notes,omitempty"`
	Tags          []string     `json:"tags,omitempty"`
	Collections   []string     `json:"collections,omitempty"`
	CaptureID     int64        `json:"capture_id" jsonschema:"which capture the content below is from"`
	CapturedAt    string       `json:"captured_at"`
	Content       string       `json:"content,omitempty" jsonschema:"the extracted readable text of this capture"`
	Summary       string       `json:"summary,omitempty" jsonschema:"an AI-generated summary, if one was produced for this capture"`
	OtherCaptures []captureRef `json:"other_captures,omitempty" jsonschema:"this page's other captures, if any -- pass one of these ids as capture_id to fetch its content instead"`
}

func (t *tools) getPage(ctx context.Context, _ *mcp.CallToolRequest, in getPageInput) (*mcp.CallToolResult, getPageOutput, error) {
	user, _ := auth.UserFromContext(ctx)

	page, err := t.q.GetPageByIDForUser(ctx, db.GetPageByIDForUserParams{ID: in.PageID, UserID: user.ID})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return errResult("no page with that id"), getPageOutput{}, nil
		}
		return nil, getPageOutput{}, err
	}

	captures, err := t.q.ListCapturesByPage(ctx, page.ID)
	if err != nil {
		return nil, getPageOutput{}, err
	}
	if len(captures) == 0 {
		return errResult("this page has no captures"), getPageOutput{}, nil
	}

	// Resolve which capture's content to return: the one named by
	// capture_id, checked against this page's own captures (not just this
	// user's, via GetCaptureByIDForUser alone) so a caller can't pair this
	// page's metadata with another of its own pages' content. Falls back
	// to captures[0], already the latest since ListCapturesByPage orders
	// captured_at DESC.
	capture := captures[0]
	if in.CaptureID != 0 {
		found := false
		for i := range captures {
			if captures[i].ID == in.CaptureID {
				capture = captures[i]
				found = true
				break
			}
		}
		if !found {
			return errResult("that capture doesn't belong to this page"), getPageOutput{}, nil
		}
	}

	tagRows, err := t.q.ListPageTags(ctx, page.ID)
	if err != nil {
		return nil, getPageOutput{}, err
	}
	tags := make([]string, len(tagRows))
	for i, r := range tagRows {
		tags[i] = r.Name
	}

	collectionRows, err := t.q.ListPageCollections(ctx, page.ID)
	if err != nil {
		return nil, getPageOutput{}, err
	}
	collections := make([]string, len(collectionRows))
	for i, r := range collectionRows {
		collections[i] = r.Name
	}

	var others []captureRef
	for i := range captures {
		if captures[i].ID == capture.ID {
			continue
		}
		others = append(others, captureRef{ID: captures[i].ID, CapturedAt: captures[i].CapturedAt.Time.Format(time.RFC3339)})
	}

	return nil, getPageOutput{
		ID:            page.ID,
		URL:           page.NormalizedUrl,
		Title:         textOrEmpty(page.Title),
		Notes:         textOrEmpty(page.Notes),
		Tags:          tags,
		Collections:   collections,
		CaptureID:     capture.ID,
		CapturedAt:    capture.CapturedAt.Time.Format(time.RFC3339),
		Content:       textOrEmpty(capture.ReaderText),
		Summary:       textOrEmpty(capture.AiSummary),
		OtherCaptures: others,
	}, nil
}
