-- Fourth pass: comments.body holds the source a reader typed, not HTML we rendered
-- for our own stylesheet.
--
-- What was there before: the composer turned every inline emoji into
--   <img class="tg-emoji-inline" src="/assets/emoji/huoying/hy-1.webp" alt="摸头">
-- and posted that string as the body. Three consequences, all of them real:
--
--   1. The column recorded a rendering, not a fact. Move the assets, rename the
--      directory or put them behind a CDN, and every published comment keeps
--      pointing at the old path — the data cannot be re-rendered, because the
--      URL was baked into the text.
--   2. The length limit is on the body, so markup eats the author's budget:
--      77 characters per emoji instead of 15. Four emoji cost 308 of 2000.
--   3. It made the field "HTML that happens to be trusted". The body comes from
--      user input; anything that renders it is a stored-XSS sink one sanitizer
--      mistake away. Storing source keeps the field text, and text is only
--      dangerous when a renderer opts in — which is exactly where the whitelist
--      already lives.
--
-- The new shape: an emoji is the shortcode `:huoying/hy-1:`. The client resolves
-- it against the same config that draws the picker, so the address lives in one
-- place and the stored data survives it changing.
--
-- Deliberately no CHECK constraint here. `body` is Markdown source, and "does it
-- contain a tag" cannot be answered by the database: someone writing about HTML
-- in a code sample is typing a legitimate `<img>`. The guard belongs on the one
-- writer that used to produce this markup, and that is where it now sits.

UPDATE comments
SET body = regexp_replace(
		body,
		'<img[^>]*\ssrc="/assets/emoji/([^/"]+)/([^/".]+)\.[a-z0-9]+"[^>]*>',
		':\1/\2:',
		'gi'
	)
WHERE body ~ '<img[^>]*src="/assets/emoji/';

-- The column comment is the only place a future reader will look before writing
-- a new producer of this field.
COMMENT ON COLUMN comments.body IS
	'Markdown source as the author wrote it (text, line breaks, inline emoji as shortcodes like `:huoying/hy-1:`). Never rendered HTML: rendering is the reader''s job, and a stored <img> would pin this row to one asset path and spend the length budget on markup.';
