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

package dbtest_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mfinelli/recueil/internal/db"
	"github.com/mfinelli/recueil/internal/dbtest"
)

// Every relationship between two user-owned rows has to join rows owned by the same
// user, enforced by composite foreign keys (migration 00017).
// These write straight through the queries, bypassing the handlers' ownership checks,
// which is the point: Postgres refuses a mismatched pair whatever the caller does.

// requireOwnershipViolation asserts err is a foreign key violation on a constraint whose
// name starts with prefix.
func requireOwnershipViolation(t *testing.T, err error, prefix string) {
	t.Helper()
	require.Error(t, err)
	var pgErr *pgconn.PgError
	require.True(t, errors.As(err, &pgErr), "not a Postgres error: %v", err)
	assert.Equal(t, "23503", pgErr.Code, "expected a foreign key violation: %v", err)
	assert.True(t, strings.HasPrefix(pgErr.ConstraintName, prefix), "constraint %q", pgErr.ConstraintName)
}

type world struct {
	q                    *db.Queries
	ctx                  context.Context
	a, b                 db.User
	pageA, pageA2, pageB db.Page
	linkA, linkA2, linkB db.Link
	tagA, tagB           db.Tag
	collA, collB         db.Collection
	pool                 *pgxpool.Pool
}

func newWorld(t *testing.T) *world {
	t.Helper()
	pool := dbtest.Setup(t)
	dbtest.Reset(t, pool)
	q := db.New(pool)
	ctx := context.Background()
	w := &world{q: q, ctx: ctx, pool: pool}
	w.a = dbtest.CreateUser(t, pool, "member")
	w.b = dbtest.CreateUser(t, pool, "member")
	w.pageA = dbtest.CreatePage(t, pool, w.a.ID, "https://example.com/a1")
	w.pageA2 = dbtest.CreatePage(t, pool, w.a.ID, "https://example.com/a2")
	w.pageB = dbtest.CreatePage(t, pool, w.b.ID, "https://example.com/b1")
	w.linkA = dbtest.CreateLink(t, pool, w.a.ID, "https://example.com/la1")
	w.linkA2 = dbtest.CreateLink(t, pool, w.a.ID, "https://example.com/la2")
	w.linkB = dbtest.CreateLink(t, pool, w.b.ID, "https://example.com/lb1")
	var err error
	w.tagA, err = q.UpsertTag(ctx, db.UpsertTagParams{UserID: w.a.ID, Name: "Shared", Slug: "shared"})
	require.NoError(t, err)
	w.tagB, err = q.UpsertTag(ctx, db.UpsertTagParams{UserID: w.b.ID, Name: "Shared", Slug: "shared"})
	require.NoError(t, err)
	w.collA, err = q.CreateCollection(ctx, db.CreateCollectionParams{UserID: w.a.ID, Name: "Shelf", Slug: "shelf"})
	require.NoError(t, err)
	w.collB, err = q.CreateCollection(ctx, db.CreateCollectionParams{UserID: w.b.ID, Name: "Shelf", Slug: "shelf"})
	require.NoError(t, err)
	return w
}

func TestSameOwnerRelationships(t *testing.T) {
	w := newWorld(t)

	t.Run("page_tags", func(t *testing.T) {
		add := func(pageID, tagID, owner int64) error {
			return w.q.AddPageTag(w.ctx, db.AddPageTagParams{PageID: pageID, TagID: tagID, Source: "manual", UserID: owner})
		}
		assert.NoError(t, add(w.pageA.ID, w.tagA.ID, w.a.ID), "same owner")
		requireOwnershipViolation(t, add(w.pageA.ID, w.tagB.ID, w.a.ID), "page_tags_tag_id_user_id_fkey")
		requireOwnershipViolation(t, add(w.pageA.ID, w.tagB.ID, w.b.ID), "page_tags_page_id_user_id_fkey")
		// A user_id that owns neither side is refused too. (A different page, since a
		// pair that already exists is skipped by ON CONFLICT before any key is checked.)
		requireOwnershipViolation(t, add(w.pageA2.ID, w.tagA.ID, w.b.ID), "page_tags_")
	})

	t.Run("page_collections", func(t *testing.T) {
		add := func(pageID, collID, owner int64) error {
			return w.q.AddPageToCollection(w.ctx, db.AddPageToCollectionParams{PageID: pageID, CollectionID: collID, UserID: owner})
		}
		assert.NoError(t, add(w.pageA.ID, w.collA.ID, w.a.ID), "same owner")
		requireOwnershipViolation(t, add(w.pageA.ID, w.collB.ID, w.a.ID), "page_collections_collection_id_user_id_fkey")
		requireOwnershipViolation(t, add(w.pageA.ID, w.collB.ID, w.b.ID), "page_collections_page_id_user_id_fkey")
	})

	t.Run("page_relations", func(t *testing.T) {
		add := func(x, y, owner int64) error {
			return w.q.AddPageRelation(w.ctx, db.AddPageRelationParams{PageA: x, PageB: y, UserID: owner})
		}
		assert.NoError(t, add(w.pageA.ID, w.pageA2.ID, w.a.ID), "same owner")
		// Whichever of the two ends is stored as page_id_a, one of them is the other
		// user's, so one of the composite keys refuses it, with either user's id.
		requireOwnershipViolation(t, add(w.pageA.ID, w.pageB.ID, w.a.ID), "page_relations_page_id_")
		requireOwnershipViolation(t, add(w.pageB.ID, w.pageA.ID, w.b.ID), "page_relations_page_id_")
	})

	t.Run("link_tags", func(t *testing.T) {
		add := func(linkID, tagID, owner int64) error {
			return w.q.AddLinkTag(w.ctx, db.AddLinkTagParams{LinkID: linkID, TagID: tagID, UserID: owner})
		}
		assert.NoError(t, add(w.linkA.ID, w.tagA.ID, w.a.ID), "same owner")
		requireOwnershipViolation(t, add(w.linkA.ID, w.tagB.ID, w.a.ID), "link_tags_tag_id_user_id_fkey")
		requireOwnershipViolation(t, add(w.linkA.ID, w.tagB.ID, w.b.ID), "link_tags_link_id_user_id_fkey")
	})

	t.Run("link_collections", func(t *testing.T) {
		add := func(linkID, collID, owner int64) error {
			return w.q.AddLinkToCollection(w.ctx, db.AddLinkToCollectionParams{LinkID: linkID, CollectionID: collID, UserID: owner})
		}
		assert.NoError(t, add(w.linkA.ID, w.collA.ID, w.a.ID), "same owner")
		requireOwnershipViolation(t, add(w.linkA.ID, w.collB.ID, w.a.ID), "link_collections_collection_id_user_id_fkey")
		requireOwnershipViolation(t, add(w.linkA.ID, w.collB.ID, w.b.ID), "link_collections_link_id_user_id_fkey")
	})

	t.Run("link_relations", func(t *testing.T) {
		add := func(x, y, owner int64) error {
			return w.q.AddLinkRelation(w.ctx, db.AddLinkRelationParams{LinkA: x, LinkB: y, UserID: owner})
		}
		assert.NoError(t, add(w.linkA.ID, w.linkA2.ID, w.a.ID), "same owner")
		requireOwnershipViolation(t, add(w.linkA.ID, w.linkB.ID, w.a.ID), "link_relations_link_id_")
		requireOwnershipViolation(t, add(w.linkB.ID, w.linkA.ID, w.b.ID), "link_relations_link_id_")
	})

	t.Run("page_link_relations", func(t *testing.T) {
		add := func(pageID, linkID, owner int64) error {
			return w.q.AddPageLinkRelation(w.ctx, db.AddPageLinkRelationParams{PageID: pageID, LinkID: linkID, UserID: owner})
		}
		assert.NoError(t, add(w.pageA.ID, w.linkA.ID, w.a.ID), "same owner")
		requireOwnershipViolation(t, add(w.pageA.ID, w.linkB.ID, w.a.ID), "page_link_relations_link_id_user_id_fkey")
		requireOwnershipViolation(t, add(w.pageA.ID, w.linkB.ID, w.b.ID), "page_link_relations_page_id_user_id_fkey")
	})

	t.Run("a collection's parent", func(t *testing.T) {
		create := func(owner int64, parent pgtype.Int8, slug string) error {
			_, err := w.q.CreateCollection(w.ctx, db.CreateCollectionParams{UserID: owner, ParentID: parent, Name: slug, Slug: slug})
			return err
		}
		assert.NoError(t, create(w.a.ID, pgtype.Int8{}, "top-level"), "a NULL parent isn't checked")
		assert.NoError(t, create(w.a.ID, pgtype.Int8{Int64: w.collA.ID, Valid: true}, "child"), "the same user's parent")
		requireOwnershipViolation(t, create(w.a.ID, pgtype.Int8{Int64: w.collB.ID, Valid: true}, "stolen"), "collections_parent_id_user_id_fkey")
	})

	t.Run("a refused pair leaves nothing behind", func(t *testing.T) {
		var n int
		require.NoError(t, w.pool.QueryRow(w.ctx, "SELECT COUNT(*) FROM page_tags WHERE tag_id = $1", w.tagB.ID).Scan(&n))
		assert.Zero(t, n)
	})
}

// The composite keys keep the cascades the plain ones had: deleting either end removes
// the relation, and deleting a collection still removes its children.
func TestSameOwnerRelationships_CascadesAreUnchanged(t *testing.T) {
	w := newWorld(t)
	count := func(t *testing.T, table, where string, args ...any) int {
		t.Helper()
		var n int
		require.NoError(t, w.pool.QueryRow(w.ctx, "SELECT COUNT(*) FROM "+table+" WHERE "+where, args...).Scan(&n))
		return n
	}
	exec := func(sql string, args ...any) {
		t.Helper()
		_, err := w.pool.Exec(w.ctx, sql, args...)
		require.NoError(t, err)
	}

	require.NoError(t, w.q.AddPageTag(w.ctx, db.AddPageTagParams{PageID: w.pageA.ID, TagID: w.tagA.ID, Source: "manual", UserID: w.a.ID}))
	require.NoError(t, w.q.AddPageToCollection(w.ctx, db.AddPageToCollectionParams{PageID: w.pageA.ID, CollectionID: w.collA.ID, UserID: w.a.ID}))
	require.NoError(t, w.q.AddPageRelation(w.ctx, db.AddPageRelationParams{PageA: w.pageA.ID, PageB: w.pageA2.ID, UserID: w.a.ID}))
	require.NoError(t, w.q.AddLinkTag(w.ctx, db.AddLinkTagParams{LinkID: w.linkA.ID, TagID: w.tagA.ID, UserID: w.a.ID}))
	require.NoError(t, w.q.AddLinkToCollection(w.ctx, db.AddLinkToCollectionParams{LinkID: w.linkA.ID, CollectionID: w.collA.ID, UserID: w.a.ID}))
	require.NoError(t, w.q.AddLinkRelation(w.ctx, db.AddLinkRelationParams{LinkA: w.linkA.ID, LinkB: w.linkA2.ID, UserID: w.a.ID}))
	require.NoError(t, w.q.AddPageLinkRelation(w.ctx, db.AddPageLinkRelationParams{PageID: w.pageA.ID, LinkID: w.linkA.ID, UserID: w.a.ID}))
	child, err := w.q.CreateCollection(w.ctx, db.CreateCollectionParams{
		UserID: w.a.ID, ParentID: pgtype.Int8{Int64: w.collA.ID, Valid: true}, Name: "Child", Slug: "child",
	})
	require.NoError(t, err)

	t.Run("deleting a page removes its relations", func(t *testing.T) {
		exec("DELETE FROM pages WHERE id = $1", w.pageA2.ID)
		assert.Zero(t, count(t, "page_relations", "page_id_a = $1 OR page_id_b = $1", w.pageA2.ID))
		assert.Equal(t, 1, count(t, "page_tags", "page_id = $1", w.pageA.ID), "others are untouched")
	})

	t.Run("deleting a tag removes it from pages and links", func(t *testing.T) {
		exec("DELETE FROM tags WHERE id = $1", w.tagA.ID)
		assert.Zero(t, count(t, "page_tags", "tag_id = $1", w.tagA.ID))
		assert.Zero(t, count(t, "link_tags", "tag_id = $1", w.tagA.ID))
	})

	t.Run("deleting a link removes its relations", func(t *testing.T) {
		exec("DELETE FROM links WHERE id = $1", w.linkA2.ID)
		assert.Zero(t, count(t, "link_relations", "link_id_a = $1 OR link_id_b = $1", w.linkA2.ID))
		assert.Equal(t, 1, count(t, "page_link_relations", "link_id = $1", w.linkA.ID), "others are untouched")
	})

	t.Run("deleting a collection removes its members and its child collections", func(t *testing.T) {
		exec("DELETE FROM collections WHERE id = $1", w.collA.ID)
		assert.Zero(t, count(t, "page_collections", "collection_id = $1", w.collA.ID))
		assert.Zero(t, count(t, "link_collections", "collection_id = $1", w.collA.ID))
		assert.Zero(t, count(t, "collections", "id = $1", child.ID), "the child went with its parent")
	})

	t.Run("deleting a user removes everything of theirs", func(t *testing.T) {
		exec("DELETE FROM users WHERE id = $1", w.a.ID)
		assert.Zero(t, count(t, "page_link_relations", "true"))
	})
}
