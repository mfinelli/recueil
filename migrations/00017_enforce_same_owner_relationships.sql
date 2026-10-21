-- recueil: self-hosted webpage bookmarker and archiver
-- Copyright © 2026 Mario Finelli
--
-- This program is free software: you can redistribute it and/or modify
-- it under the terms of the GNU Affero General Public License as published by
-- the Free Software Foundation, either version 3 of the License, or
-- (at your option) any later version.
--
-- This program is distributed in the hope that it will be useful,
-- but WITHOUT ANY WARRANTY; without even the implied warranty of
-- MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
-- GNU Affero General Public License for more details.
--
-- You should have received a copy of the GNU Affero General Public License
-- along with this program. If not, see <https://www.gnu.org/licenses/>.

-- Every relationship between two user-owned rows now has to join rows owned by the
-- SAME user, enforced by Postgres rather than only by the handlers' ownership checks.
-- Each relation table gets a user_id and its foreign keys become composite:
-- (page_id, user_id) references pages(id, user_id), and so on, so a row can
-- only reference parents with its own owner, from any writer. The same goes
-- for a collection's parent.
--
-- The relation tables have no data of their own to move: user_id is backfilled from
-- the parent row, and every existing row already satisfies the rule (the handlers
-- never allowed otherwise), which the preflight below verifies, failing loudly with
-- the offending count rather than guessing how to repair anything.

-- +goose Up
-- Fail with a count if any existing row violates the rule, before changing anything.
-- +goose StatementBegin
DO $$
DECLARE
  n bigint;
BEGIN
  SELECT count(*) INTO n FROM page_tags r
    JOIN pages p ON p.id = r.page_id JOIN tags t ON t.id = r.tag_id
    WHERE p.user_id <> t.user_id;
  IF n > 0 THEN
    RAISE EXCEPTION '% rows in page_tags join a page and a tag owned by different users; fix or delete them before upgrading', n;
  END IF;

  SELECT count(*) INTO n FROM page_collections r
    JOIN pages p ON p.id = r.page_id JOIN collections c ON c.id = r.collection_id
    WHERE p.user_id <> c.user_id;
  IF n > 0 THEN
    RAISE EXCEPTION '% rows in page_collections join a page and a collection owned by different users; fix or delete them before upgrading', n;
  END IF;

  SELECT count(*) INTO n FROM page_relations r
    JOIN pages a ON a.id = r.page_id_a JOIN pages b ON b.id = r.page_id_b
    WHERE a.user_id <> b.user_id;
  IF n > 0 THEN
    RAISE EXCEPTION '% rows in page_relations join two pages owned by different users; fix or delete them before upgrading', n;
  END IF;

  SELECT count(*) INTO n FROM link_tags r
    JOIN links l ON l.id = r.link_id JOIN tags t ON t.id = r.tag_id
    WHERE l.user_id <> t.user_id;
  IF n > 0 THEN
    RAISE EXCEPTION '% rows in link_tags join a link and a tag owned by different users; fix or delete them before upgrading', n;
  END IF;

  SELECT count(*) INTO n FROM link_collections r
    JOIN links l ON l.id = r.link_id JOIN collections c ON c.id = r.collection_id
    WHERE l.user_id <> c.user_id;
  IF n > 0 THEN
    RAISE EXCEPTION '% rows in link_collections join a link and a collection owned by different users; fix or delete them before upgrading', n;
  END IF;

  SELECT count(*) INTO n FROM link_relations r
    JOIN links a ON a.id = r.link_id_a JOIN links b ON b.id = r.link_id_b
    WHERE a.user_id <> b.user_id;
  IF n > 0 THEN
    RAISE EXCEPTION '% rows in link_relations join two links owned by different users; fix or delete them before upgrading', n;
  END IF;

  SELECT count(*) INTO n FROM page_link_relations r
    JOIN pages p ON p.id = r.page_id JOIN links l ON l.id = r.link_id
    WHERE p.user_id <> l.user_id;
  IF n > 0 THEN
    RAISE EXCEPTION '% rows in page_link_relations join a page and a link owned by different users; fix or delete them before upgrading', n;
  END IF;

  SELECT count(*) INTO n FROM collections c
    JOIN collections parent ON parent.id = c.parent_id
    WHERE c.user_id <> parent.user_id;
  IF n > 0 THEN
    RAISE EXCEPTION '% collections have a parent owned by a different user; fix or delete them before upgrading', n;
  END IF;
END
$$;
-- +goose StatementEnd

-- What a composite foreign key references has to be unique, so each parent table
-- gets a unique constraint on (id, user_id). id is already unique on its own, so this
-- adds no restriction, only the index the foreign keys need.
ALTER TABLE pages ADD CONSTRAINT pages_id_user_id_key UNIQUE (id, user_id);
ALTER TABLE tags ADD CONSTRAINT tags_id_user_id_key UNIQUE (id, user_id);
ALTER TABLE collections ADD CONSTRAINT collections_id_user_id_key UNIQUE (id, user_id);
ALTER TABLE links ADD CONSTRAINT links_id_user_id_key UNIQUE (id, user_id);

-- page_tags
ALTER TABLE page_tags ADD COLUMN user_id BIGINT;
UPDATE page_tags SET user_id = pages.user_id FROM pages WHERE pages.id = page_tags.page_id;
ALTER TABLE page_tags ALTER COLUMN user_id SET NOT NULL;
ALTER TABLE page_tags DROP CONSTRAINT page_tags_page_id_fkey;
ALTER TABLE page_tags DROP CONSTRAINT page_tags_tag_id_fkey;
ALTER TABLE page_tags ADD CONSTRAINT page_tags_page_id_user_id_fkey FOREIGN KEY (page_id, user_id)
  REFERENCES pages(id, user_id) ON DELETE CASCADE;
ALTER TABLE page_tags ADD CONSTRAINT page_tags_tag_id_user_id_fkey FOREIGN KEY (tag_id, user_id)
  REFERENCES tags(id, user_id) ON DELETE CASCADE;

-- page_collections
ALTER TABLE page_collections ADD COLUMN user_id BIGINT;
UPDATE page_collections SET user_id = pages.user_id FROM pages WHERE pages.id = page_collections.page_id;
ALTER TABLE page_collections ALTER COLUMN user_id SET NOT NULL;
ALTER TABLE page_collections DROP CONSTRAINT page_collections_page_id_fkey;
ALTER TABLE page_collections DROP CONSTRAINT page_collections_collection_id_fkey;
ALTER TABLE page_collections ADD CONSTRAINT page_collections_page_id_user_id_fkey FOREIGN KEY (page_id, user_id)
  REFERENCES pages(id, user_id) ON DELETE CASCADE;
ALTER TABLE page_collections ADD CONSTRAINT page_collections_collection_id_user_id_fkey FOREIGN KEY (collection_id, user_id)
  REFERENCES collections(id, user_id) ON DELETE CASCADE;

-- page_relations
ALTER TABLE page_relations ADD COLUMN user_id BIGINT;
UPDATE page_relations SET user_id = pages.user_id FROM pages WHERE pages.id = page_relations.page_id_a;
ALTER TABLE page_relations ALTER COLUMN user_id SET NOT NULL;
ALTER TABLE page_relations DROP CONSTRAINT page_relations_page_id_a_fkey;
ALTER TABLE page_relations DROP CONSTRAINT page_relations_page_id_b_fkey;
ALTER TABLE page_relations ADD CONSTRAINT page_relations_page_id_a_user_id_fkey FOREIGN KEY (page_id_a, user_id)
  REFERENCES pages(id, user_id) ON DELETE CASCADE;
ALTER TABLE page_relations ADD CONSTRAINT page_relations_page_id_b_user_id_fkey FOREIGN KEY (page_id_b, user_id)
  REFERENCES pages(id, user_id) ON DELETE CASCADE;

-- link_tags
ALTER TABLE link_tags ADD COLUMN user_id BIGINT;
UPDATE link_tags SET user_id = links.user_id FROM links WHERE links.id = link_tags.link_id;
ALTER TABLE link_tags ALTER COLUMN user_id SET NOT NULL;
ALTER TABLE link_tags DROP CONSTRAINT link_tags_link_id_fkey;
ALTER TABLE link_tags DROP CONSTRAINT link_tags_tag_id_fkey;
ALTER TABLE link_tags ADD CONSTRAINT link_tags_link_id_user_id_fkey FOREIGN KEY (link_id, user_id)
  REFERENCES links(id, user_id) ON DELETE CASCADE;
ALTER TABLE link_tags ADD CONSTRAINT link_tags_tag_id_user_id_fkey FOREIGN KEY (tag_id, user_id)
  REFERENCES tags(id, user_id) ON DELETE CASCADE;

-- link_collections
ALTER TABLE link_collections ADD COLUMN user_id BIGINT;
UPDATE link_collections SET user_id = links.user_id FROM links WHERE links.id = link_collections.link_id;
ALTER TABLE link_collections ALTER COLUMN user_id SET NOT NULL;
ALTER TABLE link_collections DROP CONSTRAINT link_collections_link_id_fkey;
ALTER TABLE link_collections DROP CONSTRAINT link_collections_collection_id_fkey;
ALTER TABLE link_collections ADD CONSTRAINT link_collections_link_id_user_id_fkey FOREIGN KEY (link_id, user_id)
  REFERENCES links(id, user_id) ON DELETE CASCADE;
ALTER TABLE link_collections ADD CONSTRAINT link_collections_collection_id_user_id_fkey FOREIGN KEY (collection_id, user_id)
  REFERENCES collections(id, user_id) ON DELETE CASCADE;

-- link_relations
ALTER TABLE link_relations ADD COLUMN user_id BIGINT;
UPDATE link_relations SET user_id = links.user_id FROM links WHERE links.id = link_relations.link_id_a;
ALTER TABLE link_relations ALTER COLUMN user_id SET NOT NULL;
ALTER TABLE link_relations DROP CONSTRAINT link_relations_link_id_a_fkey;
ALTER TABLE link_relations DROP CONSTRAINT link_relations_link_id_b_fkey;
ALTER TABLE link_relations ADD CONSTRAINT link_relations_link_id_a_user_id_fkey FOREIGN KEY (link_id_a, user_id)
  REFERENCES links(id, user_id) ON DELETE CASCADE;
ALTER TABLE link_relations ADD CONSTRAINT link_relations_link_id_b_user_id_fkey FOREIGN KEY (link_id_b, user_id)
  REFERENCES links(id, user_id) ON DELETE CASCADE;

-- page_link_relations
ALTER TABLE page_link_relations ADD COLUMN user_id BIGINT;
UPDATE page_link_relations SET user_id = pages.user_id FROM pages WHERE pages.id = page_link_relations.page_id;
ALTER TABLE page_link_relations ALTER COLUMN user_id SET NOT NULL;
ALTER TABLE page_link_relations DROP CONSTRAINT page_link_relations_page_id_fkey;
ALTER TABLE page_link_relations DROP CONSTRAINT page_link_relations_link_id_fkey;
ALTER TABLE page_link_relations ADD CONSTRAINT page_link_relations_page_id_user_id_fkey FOREIGN KEY (page_id, user_id)
  REFERENCES pages(id, user_id) ON DELETE CASCADE;
ALTER TABLE page_link_relations ADD CONSTRAINT page_link_relations_link_id_user_id_fkey FOREIGN KEY (link_id, user_id)
  REFERENCES links(id, user_id) ON DELETE CASCADE;

-- A collection's parent has to be the same user's. A NULL parent_id (a top-level
-- collection) is not checked, which is what we want.
ALTER TABLE collections DROP CONSTRAINT collections_parent_id_fkey;
ALTER TABLE collections ADD CONSTRAINT collections_parent_id_user_id_fkey
  FOREIGN KEY (parent_id, user_id) REFERENCES collections(id, user_id) ON DELETE CASCADE;

-- +goose Down
ALTER TABLE collections DROP CONSTRAINT collections_parent_id_user_id_fkey;
ALTER TABLE collections ADD CONSTRAINT collections_parent_id_fkey
  FOREIGN KEY (parent_id) REFERENCES collections(id) ON DELETE CASCADE;

ALTER TABLE page_link_relations DROP CONSTRAINT page_link_relations_page_id_user_id_fkey;
ALTER TABLE page_link_relations DROP CONSTRAINT page_link_relations_link_id_user_id_fkey;
ALTER TABLE page_link_relations ADD CONSTRAINT page_link_relations_page_id_fkey FOREIGN KEY (page_id)
  REFERENCES pages(id) ON DELETE CASCADE;
ALTER TABLE page_link_relations ADD CONSTRAINT page_link_relations_link_id_fkey FOREIGN KEY (link_id)
  REFERENCES links(id) ON DELETE CASCADE;
ALTER TABLE page_link_relations DROP COLUMN user_id;

ALTER TABLE link_relations DROP CONSTRAINT link_relations_link_id_a_user_id_fkey;
ALTER TABLE link_relations DROP CONSTRAINT link_relations_link_id_b_user_id_fkey;
ALTER TABLE link_relations ADD CONSTRAINT link_relations_link_id_a_fkey FOREIGN KEY (link_id_a)
  REFERENCES links(id) ON DELETE CASCADE;
ALTER TABLE link_relations ADD CONSTRAINT link_relations_link_id_b_fkey FOREIGN KEY (link_id_b)
  REFERENCES links(id) ON DELETE CASCADE;
ALTER TABLE link_relations DROP COLUMN user_id;

ALTER TABLE link_collections DROP CONSTRAINT link_collections_link_id_user_id_fkey;
ALTER TABLE link_collections DROP CONSTRAINT link_collections_collection_id_user_id_fkey;
ALTER TABLE link_collections ADD CONSTRAINT link_collections_link_id_fkey FOREIGN KEY (link_id)
  REFERENCES links(id) ON DELETE CASCADE;
ALTER TABLE link_collections ADD CONSTRAINT link_collections_collection_id_fkey FOREIGN KEY (collection_id)
  REFERENCES collections(id) ON DELETE CASCADE;
ALTER TABLE link_collections DROP COLUMN user_id;

ALTER TABLE link_tags DROP CONSTRAINT link_tags_link_id_user_id_fkey;
ALTER TABLE link_tags DROP CONSTRAINT link_tags_tag_id_user_id_fkey;
ALTER TABLE link_tags ADD CONSTRAINT link_tags_link_id_fkey FOREIGN KEY (link_id)
  REFERENCES links(id) ON DELETE CASCADE;
ALTER TABLE link_tags ADD CONSTRAINT link_tags_tag_id_fkey FOREIGN KEY (tag_id)
  REFERENCES tags(id) ON DELETE CASCADE;
ALTER TABLE link_tags DROP COLUMN user_id;

ALTER TABLE page_relations DROP CONSTRAINT page_relations_page_id_a_user_id_fkey;
ALTER TABLE page_relations DROP CONSTRAINT page_relations_page_id_b_user_id_fkey;
ALTER TABLE page_relations ADD CONSTRAINT page_relations_page_id_a_fkey FOREIGN KEY (page_id_a)
  REFERENCES pages(id) ON DELETE CASCADE;
ALTER TABLE page_relations ADD CONSTRAINT page_relations_page_id_b_fkey FOREIGN KEY (page_id_b)
  REFERENCES pages(id) ON DELETE CASCADE;
ALTER TABLE page_relations DROP COLUMN user_id;

ALTER TABLE page_collections DROP CONSTRAINT page_collections_page_id_user_id_fkey;
ALTER TABLE page_collections DROP CONSTRAINT page_collections_collection_id_user_id_fkey;
ALTER TABLE page_collections ADD CONSTRAINT page_collections_page_id_fkey FOREIGN KEY (page_id)
  REFERENCES pages(id) ON DELETE CASCADE;
ALTER TABLE page_collections ADD CONSTRAINT page_collections_collection_id_fkey FOREIGN KEY (collection_id)
  REFERENCES collections(id) ON DELETE CASCADE;
ALTER TABLE page_collections DROP COLUMN user_id;

ALTER TABLE page_tags DROP CONSTRAINT page_tags_page_id_user_id_fkey;
ALTER TABLE page_tags DROP CONSTRAINT page_tags_tag_id_user_id_fkey;
ALTER TABLE page_tags ADD CONSTRAINT page_tags_page_id_fkey FOREIGN KEY (page_id)
  REFERENCES pages(id) ON DELETE CASCADE;
ALTER TABLE page_tags ADD CONSTRAINT page_tags_tag_id_fkey FOREIGN KEY (tag_id)
  REFERENCES tags(id) ON DELETE CASCADE;
ALTER TABLE page_tags DROP COLUMN user_id;

ALTER TABLE links DROP CONSTRAINT links_id_user_id_key;
ALTER TABLE collections DROP CONSTRAINT collections_id_user_id_key;
ALTER TABLE tags DROP CONSTRAINT tags_id_user_id_key;
ALTER TABLE pages DROP CONSTRAINT pages_id_user_id_key;
