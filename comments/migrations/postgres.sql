-- One table, and it is the whole package: something somebody said, about
-- something the application owns, possibly in reply to something else somebody
-- said.
--
-- The row is conventional — created, soft-deleted, listed and filtered like
-- every other consumer row in this module — with two additions that are the
-- reason this package exists rather than being a shape each consumer writes
-- again. parent_id is what makes a comment a reply, and the target pair is what
-- makes a comment about something.
--
-- scope is whose data this row is: a reseller, a region, an account, or — as
-- the empty string — nobody. Every read filters on it. It has no default, and
-- that is deliberate: the empty string is a scope, tenancy.Global(), and a
-- column that supplies it for a write which did not name one hands the global
-- scope to whoever forgot the column. NOT NULL with nothing to fall back on
-- makes that write fail instead. See the tenancy package.
CREATE TABLE IF NOT EXISTS {{PREFIX}}comments (
    id              TEXT PRIMARY KEY,
    scope           TEXT NOT NULL,
    -- target_type and target_id are what the comment is about, as the
    -- application spells it — an entity kind and one of them. Neither is a
    -- foreign key and neither can be: the tables they name are the consumer's,
    -- in a schema this package has never seen. What stands in for referential
    -- integrity is the comments package's target catalog at the write and the
    -- consumer's sweep at the delete; see the package documentation, which owns
    -- the ruling on what a comment on a vanished target is.
    --
    -- Two columns rather than one composite key, because a key like
    -- "articles:1234" scopes by construction and cannot be indexed, filtered or
    -- enumerated as the two facts it is: "every comment about articles" is a
    -- question this shape answers and that one does not.
    target_type     TEXT NOT NULL,
    target_id       TEXT NOT NULL,
    -- parent_id is the comment this one replies to, and the empty string is a
    -- comment that replies to nothing — a root.
    --
    -- The empty string rather than NULL, because "the roots of this target" is
    -- the read a discussion opens with, and under NULL that predicate is
    -- `IS NULL`, which is statement text rather than a bound value. As the empty
    -- string it is an equality, so the root list and the reply list are one
    -- statement with a different argument rather than two statements that can
    -- drift apart.
    --
    -- Replies are one level deep. Nothing here enforces that — a self-referential
    -- CHECK cannot see another row — and the store does, by refusing a reply
    -- whose parent is itself a reply. See the comments package.
    parent_id       TEXT NOT NULL DEFAULT '',
    -- author is who said it — a user id in every deployment this module has, but
    -- a string here rather than a reference, because comments does not own the
    -- directory and a consumer storing its people somewhere else still takes
    -- comments. It is also the column an erasure keys on: the body below is free
    -- text a person wrote, which is personal data whoever they wrote about.
    author          TEXT NOT NULL,
    -- body is what the person actually said. Free text, unbounded, and the
    -- reason this table meets the dataprivacy seam: nothing can promise a
    -- sentence a user typed names nobody.
    body            TEXT NOT NULL,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    -- last_updated_at is when the body was last edited, and NULL for a comment
    -- nobody has revised. It is what a client renders an "edited" marker from:
    -- the only write that revises a live comment is the edit.
    last_updated_at TIMESTAMPTZ,
    archived_at     TIMESTAMPTZ
);

-- Serves both reads of a discussion: the target's roots, at parent_id = '', and
-- one root's replies, at parent_id = that root. They are one statement with a
-- different argument, so they are one index. Partial on the soft delete, so the
-- index tracks the live comments rather than every comment ever written.
CREATE INDEX IF NOT EXISTS {{PREFIX}}comments_target_idx
    ON {{PREFIX}}comments (scope, target_type, target_id, parent_id, id)
    WHERE archived_at IS NULL;

-- Serves the moderation read: everything anybody has said about one kind of
-- thing. It is the read an operator withdrawing a target type runs first, to see
-- what withdrawing it would strand.
--
-- It is its own index rather than a prefix of the one above, because that one
-- orders by id only after target_id and parent_id, and this read walks the
-- cursor across every target of the type.
CREATE INDEX IF NOT EXISTS {{PREFIX}}comments_target_type_idx
    ON {{PREFIX}}comments (scope, target_type, id)
    WHERE archived_at IS NULL;

-- Serves one person's own comments, and the subject access request that collects
-- them. The privacy path reads and deletes by exactly this key.
CREATE INDEX IF NOT EXISTS {{PREFIX}}comments_author_idx
    ON {{PREFIX}}comments (scope, author, id)
    WHERE archived_at IS NULL;
