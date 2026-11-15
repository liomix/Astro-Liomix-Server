-- The moderation queue reads across posts: "everything pending, newest first"
-- is the operator's first screen, not "the pending comments of one article".
--
-- comments_thread_status_idx starts with post_slug, so it cannot serve a query
-- that does not name a post: PostgreSQL will sequentially scan and sort the
-- table. This index carries the filter and the sort order of the queue, the
-- same way the thread index carries them for one post.
CREATE INDEX IF NOT EXISTS comments_status_created_idx
	ON comments (status, created_at DESC, id DESC);

COMMENT ON INDEX comments_status_created_idx IS
	'Serves the cross-post moderation queue: WHERE status = $1 ORDER BY created_at DESC, id DESC.';
