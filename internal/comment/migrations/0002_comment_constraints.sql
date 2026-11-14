-- Second pass over the comments table: the indexes and constraints the first
-- file left out.
--
-- 1. parent_id had no index. PostgreSQL indexes primary keys and unique
--    constraints, never foreign keys, so every ON DELETE CASCADE had to scan the
--    whole table to find the replies of the row being deleted. Comment volumes
--    are small enough that nobody noticed; the index is what keeps a delete
--    proportional to the size of one thread instead of the size of the table.
CREATE INDEX IF NOT EXISTS comments_parent_idx ON comments (parent_id);

-- 2. Thread pages and batch counts were served by two indexes:
--    (post_slug, created_at DESC, id DESC) for the listing and
--    (post_slug, status) for the counts. The listing almost always filters on
--    status too — only approved comments are public — so one composite that
--    carries both filters and the sort order replaces the narrow one: a page is
--    an ordered index scan from the first matching row, and a count is answered
--    from the same index without touching the heap.
DROP INDEX IF EXISTS comments_counts_idx;
CREATE INDEX IF NOT EXISTS comments_thread_status_idx
	ON comments (post_slug, status, created_at DESC, id DESC);

-- 3. "No delete credential" used to be stored as an empty string, which is a
--    value, not an absence: the column claimed every comment had a digest and
--    left the reader to interpret ''. NULL says what is meant, and lets the
--    CHECK below describe a real digest instead of exempting a sentinel.
UPDATE comments SET delete_token_hash = NULL WHERE delete_token_hash = '';
ALTER TABLE comments ALTER COLUMN delete_token_hash DROP NOT NULL;
ALTER TABLE comments ALTER COLUMN delete_token_hash DROP DEFAULT;
ALTER TABLE comments ADD CONSTRAINT comments_delete_token_hash_digest
	CHECK (delete_token_hash IS NULL OR delete_token_hash ~ '^[0-9a-f]{64}$');

-- 4. The service validates every field before writing, but a check constraint
--    is the copy of those rules that no other writer — a repair script, a
--    restore, a future import — can skip. The bounds mirror MaxSlugLength,
--    MaxNameLength, MaxEmailLength, MaxURLLength and MaxBodyLength, counted in
--    characters as the Go code counts runes.
ALTER TABLE comments
	ADD CONSTRAINT comments_post_slug_length CHECK (char_length(post_slug) BETWEEN 1 AND 200),
	ADD CONSTRAINT comments_author_name_length CHECK (char_length(author_name) BETWEEN 1 AND 80),
	ADD CONSTRAINT comments_author_email_length CHECK (char_length(author_email) <= 254),
	ADD CONSTRAINT comments_author_website_length CHECK (char_length(author_website) <= 200),
	ADD CONSTRAINT comments_body_not_blank CHECK (btrim(body) <> ''),
	ADD CONSTRAINT comments_body_length CHECK (char_length(body) <= 2000);
