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
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mfinelli/recueil/internal/auth"
	"github.com/mfinelli/recueil/internal/db"
	"github.com/mfinelli/recueil/internal/dbtest"
)

// ctxFor is this file's own shorthand for auth.NewContextForTesting --
// every tool method reads its user via auth.UserFromContext exactly like
// an internal/httpapi handler does, so every test needs one of these.
func ctxFor(user db.User) context.Context {
	return auth.NewContextForTesting(context.Background(), user)
}

func TestSearchArchive(t *testing.T) {
	pool := dbtest.Setup(t)
	q := db.New(pool)
	tl := &tools{q: q}

	t.Run("matches a page whose capture's reader_text contains the query", func(t *testing.T) {
		user := dbtest.CreateUser(t, pool, "member")
		page := dbtest.CreatePage(t, pool, user.ID, "https://example.com/search-1")
		capture := dbtest.CreateCapture(t, pool, page.ID)
		dbtest.SetCaptureReaderText(t, pool, capture.ID, "a treatise on the history of lighthouses")

		_, out, err := tl.searchArchive(ctxFor(user), nil, searchArchiveInput{Query: "lighthouses"})
		require.NoError(t, err)
		require.Len(t, out.Pages, 1)
		assert.Equal(t, page.ID, out.Pages[0].ID)
		assert.EqualValues(t, 1, out.TotalCount)
	})

	t.Run("never returns another user's pages", func(t *testing.T) {
		user := dbtest.CreateUser(t, pool, "member")
		other := dbtest.CreateUser(t, pool, "member")
		otherPage := dbtest.CreatePage(t, pool, other.ID, "https://example.com/search-2")
		otherCapture := dbtest.CreateCapture(t, pool, otherPage.ID)
		dbtest.SetCaptureReaderText(t, pool, otherCapture.ID, "a shared search term xyzzy")

		_, out, err := tl.searchArchive(ctxFor(user), nil, searchArchiveInput{Query: "xyzzy"})
		require.NoError(t, err)
		assert.Empty(t, out.Pages)
	})

	t.Run("a zero/unset limit falls back to the package default", func(t *testing.T) {
		user := dbtest.CreateUser(t, pool, "member")
		page := dbtest.CreatePage(t, pool, user.ID, "https://example.com/search-3")
		capture := dbtest.CreateCapture(t, pool, page.ID)
		dbtest.SetCaptureReaderText(t, pool, capture.ID, "quokkas are marsupials")

		_, out, err := tl.searchArchive(ctxFor(user), nil, searchArchiveInput{Query: "quokkas", Limit: 0})
		require.NoError(t, err)
		require.Len(t, out.Pages, 1)
	})
}

func TestListRecent(t *testing.T) {
	pool := dbtest.Setup(t)
	q := db.New(pool)
	tl := &tools{q: q}

	t.Run("lists only the calling user's pages, with a total count", func(t *testing.T) {
		user := dbtest.CreateUser(t, pool, "member")
		other := dbtest.CreateUser(t, pool, "member")
		dbtest.CreatePage(t, pool, user.ID, "https://example.com/recent-1")
		dbtest.CreatePage(t, pool, user.ID, "https://example.com/recent-2")
		dbtest.CreatePage(t, pool, other.ID, "https://example.com/recent-3")

		_, out, err := tl.listRecent(ctxFor(user), nil, listRecentInput{})
		require.NoError(t, err)
		assert.Len(t, out.Pages, 2)
		assert.EqualValues(t, 2, out.TotalCount)
	})

	t.Run("a limit above the cap is silently clamped, not rejected", func(t *testing.T) {
		user := dbtest.CreateUser(t, pool, "member")
		dbtest.CreatePage(t, pool, user.ID, "https://example.com/recent-4")

		_, out, err := tl.listRecent(ctxFor(user), nil, listRecentInput{Limit: 99999})
		require.NoError(t, err)
		assert.Len(t, out.Pages, 1)
	})
}

func TestListTags(t *testing.T) {
	pool := dbtest.Setup(t)
	q := db.New(pool)
	tl := &tools{q: q}

	user := dbtest.CreateUser(t, pool, "member")
	other := dbtest.CreateUser(t, pool, "member")
	_, err := q.UpsertTag(context.Background(), db.UpsertTagParams{UserID: user.ID, Name: "golang", Slug: "golang"})
	require.NoError(t, err)
	_, err = q.UpsertTag(context.Background(), db.UpsertTagParams{UserID: other.ID, Name: "not-yours", Slug: "not-yours"})
	require.NoError(t, err)

	_, out, err := tl.listTags(ctxFor(user), nil, listTagsInput{})
	require.NoError(t, err)
	require.Len(t, out.Tags, 1)
	assert.Equal(t, "golang", out.Tags[0].Name)
	assert.Equal(t, "golang", out.Tags[0].Slug)
}

func TestListPagesByTag(t *testing.T) {
	pool := dbtest.Setup(t)
	q := db.New(pool)
	tl := &tools{q: q}

	t.Run("lists pages carrying the tag", func(t *testing.T) {
		user := dbtest.CreateUser(t, pool, "member")
		page := dbtest.CreatePage(t, pool, user.ID, "https://example.com/tagged-1")
		tag, err := q.UpsertTag(context.Background(), db.UpsertTagParams{UserID: user.ID, Name: "reading", Slug: "reading"})
		require.NoError(t, err)
		require.NoError(t, q.AddPageTag(context.Background(), db.AddPageTagParams{
			PageID: page.ID, TagID: tag.ID, Source: "manual",
		}))

		_, out, err := tl.listPagesByTag(ctxFor(user), nil, listPagesByTagInput{TagSlug: "reading"})
		require.NoError(t, err)
		require.Len(t, out.Pages, 1)
		assert.Equal(t, page.ID, out.Pages[0].ID)
		assert.Equal(t, 1, out.TotalCount)
	})

	t.Run("an unknown slug is a tool-result error, not a Go error", func(t *testing.T) {
		user := dbtest.CreateUser(t, pool, "member")

		result, _, err := tl.listPagesByTag(ctxFor(user), nil, listPagesByTagInput{TagSlug: "does-not-exist"})
		require.NoError(t, err)
		require.NotNil(t, result)
		assert.True(t, result.IsError)
	})

	t.Run("can't list another user's tag by slug", func(t *testing.T) {
		user := dbtest.CreateUser(t, pool, "member")
		other := dbtest.CreateUser(t, pool, "member")
		_, err := q.UpsertTag(context.Background(), db.UpsertTagParams{UserID: other.ID, Name: "theirs", Slug: "theirs"})
		require.NoError(t, err)

		result, _, err := tl.listPagesByTag(ctxFor(user), nil, listPagesByTagInput{TagSlug: "theirs"})
		require.NoError(t, err)
		require.NotNil(t, result)
		assert.True(t, result.IsError)
	})
}

func TestListCollections(t *testing.T) {
	pool := dbtest.Setup(t)
	q := db.New(pool)
	tl := &tools{q: q}

	user := dbtest.CreateUser(t, pool, "member")
	other := dbtest.CreateUser(t, pool, "member")
	_, err := q.CreateCollection(context.Background(), db.CreateCollectionParams{
		UserID: user.ID, Name: "Recipes", Slug: "recipes",
	})
	require.NoError(t, err)
	_, err = q.CreateCollection(context.Background(), db.CreateCollectionParams{
		UserID: other.ID, Name: "Not yours", Slug: "not-yours",
	})
	require.NoError(t, err)

	_, out, err := tl.listCollections(ctxFor(user), nil, listCollectionsInput{})
	require.NoError(t, err)
	require.Len(t, out.Collections, 1)
	assert.Equal(t, "Recipes", out.Collections[0].Name)
	assert.Zero(t, out.Collections[0].ParentID, "a top-level collection has no parent")
}

func TestListPagesByCollection(t *testing.T) {
	pool := dbtest.Setup(t)
	q := db.New(pool)
	tl := &tools{q: q}

	t.Run("lists pages in the collection", func(t *testing.T) {
		user := dbtest.CreateUser(t, pool, "member")
		page := dbtest.CreatePage(t, pool, user.ID, "https://example.com/collected-1")
		collection, err := q.CreateCollection(context.Background(), db.CreateCollectionParams{
			UserID: user.ID, Name: "Reading list", Slug: "reading-list",
		})
		require.NoError(t, err)
		require.NoError(t, q.AddPageToCollection(context.Background(), db.AddPageToCollectionParams{
			PageID: page.ID, CollectionID: collection.ID,
		}))

		_, out, err := tl.listPagesByCollection(ctxFor(user), nil, listPagesByCollectionInput{CollectionID: collection.ID})
		require.NoError(t, err)
		require.Len(t, out.Pages, 1)
		assert.Equal(t, page.ID, out.Pages[0].ID)
	})

	t.Run("an unknown id is a tool-result error, not a Go error", func(t *testing.T) {
		user := dbtest.CreateUser(t, pool, "member")

		result, _, err := tl.listPagesByCollection(ctxFor(user), nil, listPagesByCollectionInput{CollectionID: 999999})
		require.NoError(t, err)
		require.NotNil(t, result)
		assert.True(t, result.IsError)
	})

	t.Run("can't list another user's collection by id", func(t *testing.T) {
		user := dbtest.CreateUser(t, pool, "member")
		other := dbtest.CreateUser(t, pool, "member")
		collection, err := q.CreateCollection(context.Background(), db.CreateCollectionParams{
			UserID: other.ID, Name: "Theirs", Slug: "theirs",
		})
		require.NoError(t, err)

		result, _, err := tl.listPagesByCollection(ctxFor(user), nil, listPagesByCollectionInput{CollectionID: collection.ID})
		require.NoError(t, err)
		require.NotNil(t, result)
		assert.True(t, result.IsError)
	})
}

func TestGetPage(t *testing.T) {
	pool := dbtest.Setup(t)
	q := db.New(pool)
	tl := &tools{q: q}

	t.Run("returns metadata, tags, collections, and the latest capture's content by default", func(t *testing.T) {
		user := dbtest.CreateUser(t, pool, "member")
		page := dbtest.CreatePage(t, pool, user.ID, "https://example.com/getpage-1")
		capture := dbtest.CreateCapture(t, pool, page.ID)
		dbtest.SetCaptureReaderText(t, pool, capture.ID, "the content of the page")

		tag, err := q.UpsertTag(context.Background(), db.UpsertTagParams{UserID: user.ID, Name: "golang", Slug: "golang"})
		require.NoError(t, err)
		require.NoError(t, q.AddPageTag(context.Background(), db.AddPageTagParams{
			PageID: page.ID, TagID: tag.ID, Source: "manual",
		}))
		collection, err := q.CreateCollection(context.Background(), db.CreateCollectionParams{
			UserID: user.ID, Name: "Reading list", Slug: "reading-list",
		})
		require.NoError(t, err)
		require.NoError(t, q.AddPageToCollection(context.Background(), db.AddPageToCollectionParams{
			PageID: page.ID, CollectionID: collection.ID,
		}))

		_, out, err := tl.getPage(ctxFor(user), nil, getPageInput{PageID: page.ID})
		require.NoError(t, err)
		assert.Equal(t, page.ID, out.ID)
		assert.Equal(t, capture.ID, out.CaptureID)
		assert.Equal(t, "the content of the page", out.Content)
		assert.Equal(t, []string{"golang"}, out.Tags)
		assert.Equal(t, []string{"Reading list"}, out.Collections)
		assert.Empty(t, out.OtherCaptures)
	})

	t.Run("an explicit capture_id selects that capture's content", func(t *testing.T) {
		user := dbtest.CreateUser(t, pool, "member")
		page := dbtest.CreatePage(t, pool, user.ID, "https://example.com/getpage-2")
		older := dbtest.CreateCapture(t, pool, page.ID)
		dbtest.SetCaptureReaderText(t, pool, older.ID, "older content")
		newer := dbtest.CreateCapture(t, pool, page.ID)
		dbtest.SetCaptureReaderText(t, pool, newer.ID, "newer content")

		_, out, err := tl.getPage(ctxFor(user), nil, getPageInput{PageID: page.ID, CaptureID: older.ID})
		require.NoError(t, err)
		assert.Equal(t, older.ID, out.CaptureID)
		assert.Equal(t, "older content", out.Content)
		require.Len(t, out.OtherCaptures, 1)
		assert.Equal(t, newer.ID, out.OtherCaptures[0].ID)
	})

	t.Run("a capture_id belonging to a different page of the same user is rejected", func(t *testing.T) {
		user := dbtest.CreateUser(t, pool, "member")
		pageA := dbtest.CreatePage(t, pool, user.ID, "https://example.com/getpage-3a")
		pageB := dbtest.CreatePage(t, pool, user.ID, "https://example.com/getpage-3b")
		captureB := dbtest.CreateCapture(t, pool, pageB.ID)

		result, _, err := tl.getPage(ctxFor(user), nil, getPageInput{PageID: pageA.ID, CaptureID: captureB.ID})
		require.NoError(t, err)
		require.NotNil(t, result)
		assert.True(t, result.IsError)
	})

	t.Run("an unknown page id is a tool-result error, not a Go error", func(t *testing.T) {
		user := dbtest.CreateUser(t, pool, "member")

		result, _, err := tl.getPage(ctxFor(user), nil, getPageInput{PageID: 999999})
		require.NoError(t, err)
		require.NotNil(t, result)
		assert.True(t, result.IsError)
	})

	t.Run("can't get another user's page by id", func(t *testing.T) {
		user := dbtest.CreateUser(t, pool, "member")
		other := dbtest.CreateUser(t, pool, "member")
		theirPage := dbtest.CreatePage(t, pool, other.ID, "https://example.com/getpage-4")

		result, _, err := tl.getPage(ctxFor(user), nil, getPageInput{PageID: theirPage.ID})
		require.NoError(t, err)
		require.NotNil(t, result)
		assert.True(t, result.IsError)
	})
}

// Sanity check that clampLimit's behavior matches every test above that
// relies on it implicitly (default-on-zero, capped-not-rejected).
func TestClampLimit(t *testing.T) {
	assert.Equal(t, defaultLimit, clampLimit(0))
	assert.Equal(t, defaultLimit, clampLimit(-5))
	assert.Equal(t, 10, clampLimit(10))
	assert.Equal(t, maxLimit, clampLimit(maxLimit+1))
}
