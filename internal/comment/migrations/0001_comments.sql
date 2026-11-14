-- Comments, one row per reader response.
--
-- parent_id carries the thread: the foreign key is declared ON DELETE CASCADE,
-- so removing a comment removes its replies at every depth without the service
-- walking the tree. The schema also forbids a comment from being its own parent,
-- which no client can produce but a bad import could.
CREATE TABLE IF NOT EXISTS comments (
	id                text        PRIMARY KEY,
	post_slug         text        NOT NULL,
	parent_id         text        REFERENCES comments (id) ON DELETE CASCADE,
	author_name       text        NOT NULL,
	author_email      text        NOT NULL,
	author_website    text        NOT NULL DEFAULT '',
	body              text        NOT NULL,
	status            text        NOT NULL,
	delete_token_hash text        NOT NULL DEFAULT '',
	created_at        timestamptz NOT NULL,
	updated_at        timestamptz NOT NULL,
	CONSTRAINT comments_status_valid CHECK (status IN ('pending', 'approved', 'rejected', 'spam')),
	CONSTRAINT comments_not_own_parent CHECK (parent_id IS NULL OR parent_id <> id)
);

-- Thread listing: filter by post, newest first, paginated. The trailing id
-- breaks ties between comments written in the same microsecond, which keeps a
-- page boundary from repeating or skipping a row.
CREATE INDEX IF NOT EXISTS comments_thread_idx
	ON comments (post_slug, created_at DESC, id DESC);

-- Batch counts, which filter on status as well as post.
CREATE INDEX IF NOT EXISTS comments_counts_idx
	ON comments (post_slug, status);
