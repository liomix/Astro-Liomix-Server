-- Second pass over the counter tables: the constraints that keep a counter
-- table from accumulating nonsense.
--
-- The repository only ever adds to these rows, so every value below is an
-- invariant the code already maintains. Writing them down means a repair
-- script, a restore or a future adapter cannot quietly break the figure the
-- footer shows.
ALTER TABLE site_counters
	ADD CONSTRAINT site_counters_views_non_negative CHECK (views >= 0),
	ADD CONSTRAINT site_counters_visitors_non_negative CHECK (visitors >= 0),
	-- A visitor row is written only together with the view that discovered it,
	-- so distinct visitors can never outnumber page views.
	ADD CONSTRAINT site_counters_visitors_within_views CHECK (visitors <= views);

-- An empty visitor id is how a caller says "do not deduplicate me"; it must
-- never reach the dedupe table, or the second anonymous visit would be
-- mistaken for a repeat visitor.
ALTER TABLE site_visitors
	ADD CONSTRAINT site_visitors_id_not_blank CHECK (visitor_id <> '');

ALTER TABLE post_views
	ADD CONSTRAINT post_views_slug_length CHECK (char_length(slug) BETWEEN 1 AND 200),
	ADD CONSTRAINT post_views_views_non_negative CHECK (views >= 0);
