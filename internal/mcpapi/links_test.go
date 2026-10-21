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
	"fmt"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mfinelli/recueil/internal/db"
	"github.com/mfinelli/recueil/internal/dbtest"
)

// saveLink adds a link for userID the way ingestion does (an upsert), with
// title as given ("" for none). The raw URL carries a fragment so a test can
// tell raw from normalized.
func saveLink(t *testing.T, q *db.Queries, userID int64, normalizedURL, title string) db.Link {
	t.Helper()
	link, err := q.UpsertLink(context.Background(), db.UpsertLinkParams{
		UserID: userID, RawUrl: normalizedURL + "#raw", NormalizedUrl: normalizedURL,
		Title: pgtype.Text{String: title, Valid: title != ""}, Source: "extension",
	})
	require.NoError(t, err)
	return link
}

func linkIDs(out searchLinksOutput) []int64 {
	ids := make([]int64, len(out.Links))
	for i, l := range out.Links {
		ids[i] = l.ID
	}
	return ids
}

func TestSearchLinks(t *testing.T) {
	pool := dbtest.Setup(t)
	q := db.New(pool)
	tl := &tools{q: q}
	ctx := context.Background()

	t.Run("lists the most recently saved links when there's no query, with a total", func(t *testing.T) {
		user := dbtest.CreateUser(t, pool, "member")
		other := dbtest.CreateUser(t, pool, "member")
		a := saveLink(t, q, user.ID, "https://example.com/list-a", "A")
		b := saveLink(t, q, user.ID, "https://example.com/list-b", "B")
		saveLink(t, q, other.ID, "https://example.com/list-c", "Not mine")

		res, out, err := tl.searchLinks(ctxFor(&user), nil, searchLinksInput{})
		require.NoError(t, err)
		assert.Nil(t, res)
		assert.Equal(t, []int64{b.ID, a.ID}, linkIDs(out), "newest first")
		assert.EqualValues(t, 2, out.TotalCount)
		assert.Equal(t, "https://example.com/list-b#raw", out.Links[0].URL, "the URL as saved, not normalized")
		assert.NotEmpty(t, out.Links[0].SavedAt)
	})

	t.Run("an empty result is an empty list, not null", func(t *testing.T) {
		user := dbtest.CreateUser(t, pool, "member")
		_, out, err := tl.searchLinks(ctxFor(&user), nil, searchLinksInput{})
		require.NoError(t, err)
		assert.NotNil(t, out.Links)
		assert.Empty(t, out.Links)
	})

	t.Run("matches the title, the URL and the notes, case-insensitively", func(t *testing.T) {
		user := dbtest.CreateUser(t, pool, "member")
		byTitle := saveLink(t, q, user.ID, "https://example.com/one", "Rust Ownership Explained")
		byURL := saveLink(t, q, user.ID, "https://github.com/rust-lang/rust", "A repo")
		byNotes := saveLink(t, q, user.ID, "https://example.com/three", "Three")
		_, err := q.SetLinkNotes(ctx, db.SetLinkNotesParams{
			Notes: pgtype.Text{String: "a talk about rust async", Valid: true}, ID: byNotes.ID, UserID: user.ID,
		})
		require.NoError(t, err)
		saveLink(t, q, user.ID, "https://example.com/four", "Cooking pasta")

		_, out, err := tl.searchLinks(ctxFor(&user), nil, searchLinksInput{Query: "  RUST  "})
		require.NoError(t, err)
		assert.ElementsMatch(t, []int64{byTitle.ID, byURL.ID, byNotes.ID}, linkIDs(out))
		assert.EqualValues(t, 3, out.TotalCount)
	})

	t.Run("LIKE's wildcards in the query match literally", func(t *testing.T) {
		user := dbtest.CreateUser(t, pool, "member")
		percent := saveLink(t, q, user.ID, "https://example.com/pct", "100% organic")
		underscore := saveLink(t, q, user.ID, "https://example.com/us", "snake_case names")
		saveLink(t, q, user.ID, "https://example.com/plain", "Plain")

		_, out, err := tl.searchLinks(ctxFor(&user), nil, searchLinksInput{Query: "100%"})
		require.NoError(t, err)
		assert.Equal(t, []int64{percent.ID}, linkIDs(out))

		_, out, err = tl.searchLinks(ctxFor(&user), nil, searchLinksInput{Query: "snake_case"})
		require.NoError(t, err)
		assert.Equal(t, []int64{underscore.ID}, linkIDs(out))

		_, out, err = tl.searchLinks(ctxFor(&user), nil, searchLinksInput{Query: "sna_e"})
		require.NoError(t, err)
		assert.Empty(t, out.Links, "_ is not a one-character wildcard")
	})

	t.Run("never returns another user's links", func(t *testing.T) {
		user := dbtest.CreateUser(t, pool, "member")
		other := dbtest.CreateUser(t, pool, "member")
		saveLink(t, q, other.ID, "https://example.com/theirs", "A shared term xyzzy")

		_, out, err := tl.searchLinks(ctxFor(&user), nil, searchLinksInput{Query: "xyzzy"})
		require.NoError(t, err)
		assert.Empty(t, out.Links)
	})

	t.Run("says when a link's URL is also an archived page", func(t *testing.T) {
		user := dbtest.CreateUser(t, pool, "member")
		page := dbtest.CreatePage(t, pool, user.ID, "https://example.com/both")
		both := saveLink(t, q, user.ID, "https://example.com/both", "Both")
		linkOnly := saveLink(t, q, user.ID, "https://example.com/only", "Only")

		_, out, err := tl.searchLinks(ctxFor(&user), nil, searchLinksInput{})
		require.NoError(t, err)
		byID := map[int64]linkSummary{}
		for _, l := range out.Links {
			byID[l.ID] = l
		}
		assert.Equal(t, page.ID, byID[both.ID].PageID)
		assert.Zero(t, byID[linkOnly.ID].PageID)

		// And the same on the search path.
		_, out, err = tl.searchLinks(ctxFor(&user), nil, searchLinksInput{Query: "both"})
		require.NoError(t, err)
		require.Len(t, out.Links, 1)
		assert.Equal(t, page.ID, out.Links[0].PageID)
	})

	t.Run("a limit caps the list but not the total; unset takes the default; too large is clamped", func(t *testing.T) {
		user := dbtest.CreateUser(t, pool, "member")
		for i := range 3 {
			saveLink(t, q, user.ID, fmt.Sprintf("https://example.com/many-%d", i), "Many")
		}

		_, out, err := tl.searchLinks(ctxFor(&user), nil, searchLinksInput{Limit: 2})
		require.NoError(t, err)
		assert.Len(t, out.Links, 2)
		assert.EqualValues(t, 3, out.TotalCount)

		_, out, err = tl.searchLinks(ctxFor(&user), nil, searchLinksInput{Limit: 0})
		require.NoError(t, err)
		assert.Len(t, out.Links, 3)

		_, out, err = tl.searchLinks(ctxFor(&user), nil, searchLinksInput{Limit: 99999})
		require.NoError(t, err)
		assert.Len(t, out.Links, 3)
	})

	t.Run("narrows to a tag, and combines with a query", func(t *testing.T) {
		user := dbtest.CreateUser(t, pool, "member")
		video := saveLink(t, q, user.ID, "https://example.com/video", "Rust talk")
		repo := saveLink(t, q, user.ID, "https://example.com/repo", "Rust repo")
		saveLink(t, q, user.ID, "https://example.com/other", "Rust other")
		tag, err := q.UpsertTag(ctx, db.UpsertTagParams{UserID: user.ID, Name: "Watch", Slug: "watch"})
		require.NoError(t, err)
		require.NoError(t, q.AddLinkTag(ctx, db.AddLinkTagParams{LinkID: video.ID, TagID: tag.ID, UserID: video.UserID}))
		require.NoError(t, q.AddLinkTag(ctx, db.AddLinkTagParams{LinkID: repo.ID, TagID: tag.ID, UserID: repo.UserID}))

		_, out, err := tl.searchLinks(ctxFor(&user), nil, searchLinksInput{TagSlug: "watch"})
		require.NoError(t, err)
		assert.ElementsMatch(t, []int64{video.ID, repo.ID}, linkIDs(out))

		_, out, err = tl.searchLinks(ctxFor(&user), nil, searchLinksInput{TagSlug: "watch", Query: "talk"})
		require.NoError(t, err)
		assert.Equal(t, []int64{video.ID}, linkIDs(out))
	})

	t.Run("narrows to a collection's own links, not its sub-collections'", func(t *testing.T) {
		user := dbtest.CreateUser(t, pool, "member")
		inParent := saveLink(t, q, user.ID, "https://example.com/in-parent", "In parent")
		inChild := saveLink(t, q, user.ID, "https://example.com/in-child", "In child")
		parent, err := q.CreateCollection(ctx, db.CreateCollectionParams{UserID: user.ID, Name: "Parent", Slug: "parent"})
		require.NoError(t, err)
		child, err := q.CreateCollection(ctx, db.CreateCollectionParams{
			UserID: user.ID, ParentID: pgtype.Int8{Int64: parent.ID, Valid: true}, Name: "Child", Slug: "child",
		})
		require.NoError(t, err)
		require.NoError(t, q.AddLinkToCollection(ctx, db.AddLinkToCollectionParams{LinkID: inParent.ID, CollectionID: parent.ID, UserID: inParent.UserID}))
		require.NoError(t, q.AddLinkToCollection(ctx, db.AddLinkToCollectionParams{LinkID: inChild.ID, CollectionID: child.ID, UserID: inChild.UserID}))

		_, out, err := tl.searchLinks(ctxFor(&user), nil, searchLinksInput{CollectionID: parent.ID})
		require.NoError(t, err)
		assert.Equal(t, []int64{inParent.ID}, linkIDs(out))
	})

	t.Run("an unknown tag or collection, or another user's, is a normal error result", func(t *testing.T) {
		user := dbtest.CreateUser(t, pool, "member")
		other := dbtest.CreateUser(t, pool, "member")
		_, err := q.UpsertTag(ctx, db.UpsertTagParams{UserID: other.ID, Name: "Theirs", Slug: "theirs"})
		require.NoError(t, err)
		theirs, err := q.CreateCollection(ctx, db.CreateCollectionParams{UserID: other.ID, Name: "Theirs", Slug: "theirs"})
		require.NoError(t, err)

		for name, in := range map[string]searchLinksInput{
			"an unknown tag":            {TagSlug: "no-such-tag"},
			"another user's tag":        {TagSlug: "theirs"},
			"an unknown collection":     {CollectionID: 999999999},
			"another user's collection": {CollectionID: theirs.ID},
		} {
			res, _, err := tl.searchLinks(ctxFor(&user), nil, in)
			require.NoError(t, err, name)
			require.NotNil(t, res, name)
			assert.True(t, res.IsError, name)
		}
	})
}

func TestGetLink(t *testing.T) {
	pool := dbtest.Setup(t)
	q := db.New(pool)
	tl := &tools{q: q}
	ctx := context.Background()

	t.Run("returns the metadata, tags and collections by name, and the page it's also archived as", func(t *testing.T) {
		user := dbtest.CreateUser(t, pool, "member")
		page := dbtest.CreatePage(t, pool, user.ID, "https://example.com/detail")
		link := saveLink(t, q, user.ID, "https://example.com/detail", "Detail")
		_, err := q.SetLinkNotes(ctx, db.SetLinkNotesParams{
			Notes: pgtype.Text{String: "why I saved it", Valid: true}, ID: link.ID, UserID: user.ID,
		})
		require.NoError(t, err)
		tag, err := q.UpsertTag(ctx, db.UpsertTagParams{UserID: user.ID, Name: "Video", Slug: "video"})
		require.NoError(t, err)
		col, err := q.CreateCollection(ctx, db.CreateCollectionParams{UserID: user.ID, Name: "Watch later", Slug: "watch-later"})
		require.NoError(t, err)
		require.NoError(t, q.AddLinkTag(ctx, db.AddLinkTagParams{LinkID: link.ID, TagID: tag.ID, UserID: link.UserID}))
		require.NoError(t, q.AddLinkToCollection(ctx, db.AddLinkToCollectionParams{LinkID: link.ID, CollectionID: col.ID, UserID: link.UserID}))

		res, out, err := tl.getLink(ctxFor(&user), nil, getLinkInput{LinkID: link.ID})
		require.NoError(t, err)
		assert.Nil(t, res)
		assert.Equal(t, link.ID, out.ID)
		assert.Equal(t, "https://example.com/detail#raw", out.URL)
		assert.Equal(t, "Detail", out.Title)
		assert.Equal(t, "why I saved it", out.Notes)
		assert.Equal(t, []string{"Video"}, out.Tags)
		assert.Equal(t, []string{"Watch later"}, out.Collections)
		assert.Equal(t, page.ID, out.PageID)
		assert.NotEmpty(t, out.SavedAt)
	})

	t.Run("a link with nothing attached and no matching page has none of them", func(t *testing.T) {
		user := dbtest.CreateUser(t, pool, "member")
		link := saveLink(t, q, user.ID, "https://example.com/bare", "")

		_, out, err := tl.getLink(ctxFor(&user), nil, getLinkInput{LinkID: link.ID})
		require.NoError(t, err)
		assert.Empty(t, out.Title)
		assert.Empty(t, out.Notes)
		assert.Empty(t, out.Tags)
		assert.Empty(t, out.Collections)
		assert.Zero(t, out.PageID)
	})

	t.Run("another user's page with the same URL is not the matching page", func(t *testing.T) {
		user := dbtest.CreateUser(t, pool, "member")
		other := dbtest.CreateUser(t, pool, "member")
		dbtest.CreatePage(t, pool, other.ID, "https://example.com/theirs-page")
		link := saveLink(t, q, user.ID, "https://example.com/theirs-page", "Mine")

		_, out, err := tl.getLink(ctxFor(&user), nil, getLinkInput{LinkID: link.ID})
		require.NoError(t, err)
		assert.Zero(t, out.PageID)
	})

	t.Run("another user's link, or none at all, is a normal error result", func(t *testing.T) {
		owner := dbtest.CreateUser(t, pool, "member")
		other := dbtest.CreateUser(t, pool, "member")
		link := saveLink(t, q, owner.ID, "https://example.com/private", "Private")

		for _, id := range []int64{link.ID, 999999999} {
			res, _, err := tl.getLink(ctxFor(&other), nil, getLinkInput{LinkID: id})
			require.NoError(t, err)
			require.NotNil(t, res)
			assert.True(t, res.IsError)
			assert.Contains(t, res.Content[0].(*mcp.TextContent).Text, "no link with that id")
		}
	})
}

func TestEscapeLike(t *testing.T) {
	assert.Equal(t, `100\%`, escapeLike("100%"))
	assert.Equal(t, `a\_b`, escapeLike("a_b"))
	assert.Equal(t, `c:\\dir`, escapeLike(`c:\dir`))
	assert.Equal(t, "plain", escapeLike("plain"))
}

// Which tools exist depends on the links flag: with it off a client never sees
// the link tools, and the existing tools are unaffected either way. Registered
// against a real server and listed over an in-memory client, which also proves
// every tool's input and output schema is valid (AddTool panics otherwise).
func TestNewServer_RegistersLinkToolsOnlyWithLinksOn(t *testing.T) {
	pool := dbtest.Setup(t)
	q := db.New(pool)

	listTools := func(t *testing.T, enableLinks bool) []string {
		t.Helper()
		ctx := context.Background()
		server := newServer(q, "test", enableLinks)
		serverTransport, clientTransport := mcp.NewInMemoryTransports()
		_, err := server.Connect(ctx, serverTransport, nil)
		require.NoError(t, err)
		client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, nil)
		session, err := client.Connect(ctx, clientTransport, nil)
		require.NoError(t, err)
		t.Cleanup(func() { _ = session.Close() })

		res, err := session.ListTools(ctx, nil)
		require.NoError(t, err)
		var names []string
		for _, tool := range res.Tools {
			names = append(names, tool.Name)
		}
		return names
	}

	existing := []string{
		"search_archive", "list_recent", "list_tags", "list_pages_by_tag",
		"list_collections", "list_pages_by_collection", "get_page",
	}

	t.Run("with links on, adds search_links and get_link", func(t *testing.T) {
		names := listTools(t, true)
		assert.Subset(t, names, existing)
		assert.Contains(t, names, "search_links")
		assert.Contains(t, names, "get_link")
		assert.Len(t, names, len(existing)+2)
	})

	t.Run("with links off, has only the existing tools", func(t *testing.T) {
		names := listTools(t, false)
		assert.ElementsMatch(t, existing, names)
		for _, name := range names {
			assert.False(t, strings.Contains(name, "link"), name)
		}
	})
}
