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

package httpapi_test

import (
	"context"
	"fmt"
	"net/http"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mfinelli/recueil/internal/db"
	"github.com/mfinelli/recueil/internal/dbtest"
)

type filteredLinkList struct {
	linkListBody
	Tag *struct {
		ID   int64  `json:"id"`
		Name string `json:"name"`
		Slug string `json:"slug"`
	} `json:"tag"`
	Collection *struct {
		ID       int64  `json:"id"`
		Name     string `json:"name"`
		ParentID *int64 `json:"parent_id"`
	} `json:"collection"`
}

func linkIDs(l filteredLinkList) []int64 {
	var ids []int64
	for _, link := range l.Links {
		ids = append(ids, link.ID)
	}
	return ids
}

func TestListLinks_Filters(t *testing.T) {
	pool := dbtest.Setup(t)
	server, _ := newLinksTestServer(t, pool, true)
	q := db.New(pool)
	ctx := context.Background()

	user := dbtest.CreateUser(t, pool, "member")
	other := dbtest.CreateUser(t, pool, "member")
	cookie := sessionCookieFor(t, pool, &user)

	video := saveLink(t, pool, user.ID, "https://example.com/video", "Video")
	repo := saveLink(t, pool, user.ID, "https://example.com/repo", "Repo")
	both := saveLink(t, pool, user.ID, "https://example.com/both", "Both")
	plain := saveLink(t, pool, user.ID, "https://example.com/plain", "Plain")

	tagVideo, err := q.UpsertTag(ctx, db.UpsertTagParams{UserID: user.ID, Name: "Video", Slug: "video"})
	require.NoError(t, err)
	require.NoError(t, q.AddLinkTag(ctx, db.AddLinkTagParams{LinkID: video.ID, TagID: tagVideo.ID}))
	require.NoError(t, q.AddLinkTag(ctx, db.AddLinkTagParams{LinkID: both.ID, TagID: tagVideo.ID}))

	parent, err := q.CreateCollection(ctx, db.CreateCollectionParams{UserID: user.ID, Name: "Watch", Slug: "watch"})
	require.NoError(t, err)
	child, err := q.CreateCollection(ctx, db.CreateCollectionParams{
		UserID: user.ID, ParentID: pgtype.Int8{Int64: parent.ID, Valid: true}, Name: "Talks", Slug: "talks",
	})
	require.NoError(t, err)
	require.NoError(t, q.AddLinkToCollection(ctx, db.AddLinkToCollectionParams{LinkID: both.ID, CollectionID: parent.ID}))
	require.NoError(t, q.AddLinkToCollection(ctx, db.AddLinkToCollectionParams{LinkID: repo.ID, CollectionID: child.ID}))

	get := func(t *testing.T, query string) (int, filteredLinkList) {
		resp := requestWithCookie(t, server, http.MethodGet, "/api/links"+query, cookie)
		if resp.StatusCode != http.StatusOK {
			_ = resp.Body.Close()
			return resp.StatusCode, filteredLinkList{}
		}
		return resp.StatusCode, decodeBody[filteredLinkList](t, resp)
	}

	t.Run("?tag= lists only the links carrying that tag, and names it", func(t *testing.T) {
		status, got := get(t, "?tag=video")
		require.Equal(t, http.StatusOK, status)
		assert.ElementsMatch(t, []int64{video.ID, both.ID}, linkIDs(got))
		assert.Equal(t, int64(2), got.Total)
		require.NotNil(t, got.Tag)
		assert.Equal(t, "Video", got.Tag.Name)
		assert.Equal(t, "video", got.Tag.Slug)
		assert.Nil(t, got.Collection)
	})

	t.Run("?collection= lists a collection's own links, not its sub-collections'", func(t *testing.T) {
		status, got := get(t, fmt.Sprintf("?collection=%d", parent.ID))
		require.Equal(t, http.StatusOK, status)
		assert.Equal(t, []int64{both.ID}, linkIDs(got), "the child's link isn't in the parent")
		require.NotNil(t, got.Collection)
		assert.Equal(t, "Watch", got.Collection.Name)
		assert.Nil(t, got.Collection.ParentID)
		assert.Nil(t, got.Tag)

		status, got = get(t, fmt.Sprintf("?collection=%d", child.ID))
		require.Equal(t, http.StatusOK, status)
		assert.Equal(t, []int64{repo.ID}, linkIDs(got))
		require.NotNil(t, got.Collection)
		require.NotNil(t, got.Collection.ParentID)
		assert.Equal(t, parent.ID, *got.Collection.ParentID)
	})

	t.Run("a tag and a collection together both have to match", func(t *testing.T) {
		status, got := get(t, fmt.Sprintf("?tag=video&collection=%d", parent.ID))
		require.Equal(t, http.StatusOK, status)
		assert.Equal(t, []int64{both.ID}, linkIDs(got))
		assert.NotNil(t, got.Tag)
		assert.NotNil(t, got.Collection)
	})

	t.Run("a filter combines with a search and with pagination", func(t *testing.T) {
		status, got := get(t, "?tag=video&q=both")
		require.Equal(t, http.StatusOK, status)
		assert.Equal(t, []int64{both.ID}, linkIDs(got))
		assert.Equal(t, int64(1), got.Total)

		status, got = get(t, "?tag=video&q=nothing-matches")
		require.Equal(t, http.StatusOK, status)
		assert.Empty(t, got.Links)
		assert.NotNil(t, got.Tag, "still says what it was filtered by")

		status, got = get(t, "?tag=video&limit=1")
		require.Equal(t, http.StatusOK, status)
		assert.Len(t, got.Links, 1)
		assert.Equal(t, int64(2), got.Total, "total is the whole filtered set")
	})

	t.Run("a tag with no links, or no filter at all, behaves", func(t *testing.T) {
		_, err := q.UpsertTag(ctx, db.UpsertTagParams{UserID: user.ID, Name: "Empty", Slug: "empty"})
		require.NoError(t, err)
		status, got := get(t, "?tag=empty")
		require.Equal(t, http.StatusOK, status)
		assert.Empty(t, got.Links)
		require.NotNil(t, got.Tag)

		status, got = get(t, "")
		require.Equal(t, http.StatusOK, status)
		assert.ElementsMatch(t, []int64{video.ID, repo.ID, both.ID, plain.ID}, linkIDs(got))
		assert.Nil(t, got.Tag)
		assert.Nil(t, got.Collection)
	})

	t.Run("an unknown tag or collection, or another user's, is a 404; a bad id is a 400", func(t *testing.T) {
		theirTag, err := q.UpsertTag(ctx, db.UpsertTagParams{UserID: other.ID, Name: "Theirs", Slug: "theirs"})
		require.NoError(t, err)
		theirCollection, err := q.CreateCollection(ctx, db.CreateCollectionParams{UserID: other.ID, Name: "Theirs", Slug: "theirs"})
		require.NoError(t, err)
		_ = theirTag

		for query, want := range map[string]int{
			"?tag=no-such-tag":      http.StatusNotFound,
			"?tag=theirs":           http.StatusNotFound,
			"?collection=999999999": http.StatusNotFound,
			fmt.Sprintf("?collection=%d", theirCollection.ID): http.StatusNotFound,
			"?collection=abc": http.StatusBadRequest,
		} {
			status, _ := get(t, query)
			assert.Equal(t, want, status, query)
		}
	})

	t.Run("never shows another user's links", func(t *testing.T) {
		theirs := saveLink(t, pool, other.ID, "https://example.com/theirs", "Theirs")
		theirTag, err := q.UpsertTag(ctx, db.UpsertTagParams{UserID: other.ID, Name: "Video", Slug: "video"})
		require.NoError(t, err)
		require.NoError(t, q.AddLinkTag(ctx, db.AddLinkTagParams{LinkID: theirs.ID, TagID: theirTag.ID}))

		_, got := get(t, "?tag=video")
		assert.ElementsMatch(t, []int64{video.ID, both.ID}, linkIDs(got), "same slug, different user's tag")
	})
}

func TestTagAndCollectionPages_LinkCount(t *testing.T) {
	pool := dbtest.Setup(t)
	q := db.New(pool)
	ctx := context.Background()

	type tagPages struct {
		LinkCount int64 `json:"link_count"`
		Pages     []any `json:"pages"`
	}
	type collectionPages struct {
		LinkCount int64 `json:"link_count"`
		Pages     []any `json:"pages"`
	}

	setup := func(t *testing.T) (db.User, db.Tag, db.Collection, db.Collection) {
		user := dbtest.CreateUser(t, pool, "member")
		tag, err := q.UpsertTag(ctx, db.UpsertTagParams{UserID: user.ID, Name: "Video", Slug: "video"})
		require.NoError(t, err)
		parent, err := q.CreateCollection(ctx, db.CreateCollectionParams{UserID: user.ID, Name: "Watch", Slug: "watch"})
		require.NoError(t, err)
		child, err := q.CreateCollection(ctx, db.CreateCollectionParams{
			UserID: user.ID, ParentID: pgtype.Int8{Int64: parent.ID, Valid: true}, Name: "Talks", Slug: "talks",
		})
		require.NoError(t, err)
		for i, url := range []string{"https://example.com/1", "https://example.com/2", "https://example.com/3"} {
			link := saveLink(t, pool, user.ID, url, fmt.Sprintf("Link %d", i))
			require.NoError(t, q.AddLinkTag(ctx, db.AddLinkTagParams{LinkID: link.ID, TagID: tag.ID}))
			target := parent
			if i == 2 {
				target = child
			}
			require.NoError(t, q.AddLinkToCollection(ctx, db.AddLinkToCollectionParams{LinkID: link.ID, CollectionID: target.ID}))
		}
		return user, tag, parent, child
	}

	t.Run("a tag's page list says how many links carry it, even with no pages", func(t *testing.T) {
		server, _ := newLinksTestServer(t, pool, true)
		user, _, _, _ := setup(t)
		resp := requestWithCookie(t, server, http.MethodGet, "/api/tags/video/pages", sessionCookieFor(t, pool, &user))
		require.Equal(t, http.StatusOK, resp.StatusCode)
		got := decodeBody[tagPages](t, resp)
		assert.Equal(t, int64(3), got.LinkCount)
		assert.Empty(t, got.Pages, "a tag used only by links has no pages")
	})

	t.Run("a collection's page list counts only its direct links", func(t *testing.T) {
		server, _ := newLinksTestServer(t, pool, true)
		user, _, parent, child := setup(t)
		cookie := sessionCookieFor(t, pool, &user)

		resp := requestWithCookie(t, server, http.MethodGet, fmt.Sprintf("/api/collections/%d/pages", parent.ID), cookie)
		require.Equal(t, http.StatusOK, resp.StatusCode)
		assert.Equal(t, int64(2), decodeBody[collectionPages](t, resp).LinkCount, "the sub-collection's link isn't counted")

		resp = requestWithCookie(t, server, http.MethodGet, fmt.Sprintf("/api/collections/%d/pages", child.ID), cookie)
		assert.Equal(t, int64(1), decodeBody[collectionPages](t, resp).LinkCount)
	})

	t.Run("is 0 with nothing attached", func(t *testing.T) {
		server, _ := newLinksTestServer(t, pool, true)
		user := dbtest.CreateUser(t, pool, "member")
		_, err := q.UpsertTag(ctx, db.UpsertTagParams{UserID: user.ID, Name: "Bare", Slug: "bare"})
		require.NoError(t, err)
		resp := requestWithCookie(t, server, http.MethodGet, "/api/tags/bare/pages", sessionCookieFor(t, pool, &user))
		assert.Equal(t, int64(0), decodeBody[tagPages](t, resp).LinkCount)
	})

	t.Run("is always 0 with links off, even if link rows exist", func(t *testing.T) {
		server, _ := newLinksTestServer(t, pool, false)
		user, _, parent, _ := setup(t)
		cookie := sessionCookieFor(t, pool, &user)

		resp := requestWithCookie(t, server, http.MethodGet, "/api/tags/video/pages", cookie)
		assert.Equal(t, int64(0), decodeBody[tagPages](t, resp).LinkCount)
		resp = requestWithCookie(t, server, http.MethodGet, fmt.Sprintf("/api/collections/%d/pages", parent.ID), cookie)
		assert.Equal(t, int64(0), decodeBody[collectionPages](t, resp).LinkCount)
	})

	t.Run("deleting a tag or collection leaves the links, just not in it", func(t *testing.T) {
		server, _ := newLinksTestServer(t, pool, true)
		user, tag, _, _ := setup(t)
		cookie := sessionCookieFor(t, pool, &user)

		resp := requestWithCookie(t, server, http.MethodDelete, fmt.Sprintf("/api/tags/%d", tag.ID), cookie)
		require.Equal(t, http.StatusNoContent, resp.StatusCode)

		list := decodeBody[linkListBody](t, requestWithCookie(t, server, http.MethodGet, "/api/links", cookie))
		assert.Equal(t, int64(3), list.Total, "the links themselves are untouched")
	})
}
