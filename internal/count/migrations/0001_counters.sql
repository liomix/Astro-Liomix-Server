-- Traffic counters.
--
-- site_counters is a single row, enforced by the CHECK: a counter table with
-- several rows would let two paths disagree about what "the" total is.
-- site_visitors is the dedupe set behind the distinct-visitor figure; it grows
-- with traffic, which is the price of counting people rather than page loads.
CREATE TABLE IF NOT EXISTS site_counters (
	id         smallint    PRIMARY KEY,
	views      bigint      NOT NULL DEFAULT 0,
	visitors   bigint      NOT NULL DEFAULT 0,
	updated_at timestamptz NOT NULL DEFAULT now(),
	CONSTRAINT site_counters_single_row CHECK (id = 1)
);

-- Seed the row so every later write is an UPDATE … RETURNING rather than an
-- insert-or-update that has to decide whether this is the first visit ever.
INSERT INTO site_counters (id) VALUES (1) ON CONFLICT (id) DO NOTHING;

CREATE TABLE IF NOT EXISTS site_visitors (
	visitor_id text        PRIMARY KEY,
	first_seen timestamptz NOT NULL
);

-- One row per post instead of one per read: the table stays proportional to
-- the number of articles, not to traffic.
CREATE TABLE IF NOT EXISTS post_views (
	slug       text        PRIMARY KEY,
	views      bigint      NOT NULL DEFAULT 0,
	updated_at timestamptz NOT NULL
);
