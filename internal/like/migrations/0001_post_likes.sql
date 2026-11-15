-- Likes, one row per reader per post.
--
-- A "post" is whatever the comment module calls a post slug: an article's slug
-- for articles, "dynamic-<id>" for dynamics. One table serves both because
-- nothing about a like depends on which kind of post it is — the same visitor
-- liking an article and a dynamic are two rows that happen to differ only in
-- their slug.
--
-- The primary key is the membership itself. (post_slug, visitor_id) forbids a
-- reader from counting twice no matter how many replicas receive the click, and
-- because it is a btree on those two columns in that order, "how many likes does
-- this post have" is answered by an index scan on the leading column. There is
-- no foreign key here — likes are keyed by an external identifier, not by a row
-- in another table of this schema — so there is no foreign-key index to add.
CREATE TABLE IF NOT EXISTS post_likes (
	post_slug  text        NOT NULL,
	visitor_id text        NOT NULL,
	created_at timestamptz NOT NULL DEFAULT now(),
	PRIMARY KEY (post_slug, visitor_id),
	CONSTRAINT post_likes_slug_length CHECK (char_length(post_slug) BETWEEN 1 AND 200),
	CONSTRAINT post_likes_visitor_length CHECK (char_length(visitor_id) BETWEEN 8 AND 128)
);
