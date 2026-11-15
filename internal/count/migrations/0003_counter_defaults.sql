-- Third pass over the counter tables: server-side defaults, and the one column
-- the read model was missing.
--
-- 1. post_views had no created_at, so "when did this article first get read"
--    was unanswerable — the only timestamp was the last read. Every other table
--    in this database records when a row appeared; this one now does too.
--
--    Existing rows are backfilled from updated_at. That value is an upper bound,
--    not the real first read: for a row written before this migration, all we
--    know is that it was first read at or before its last read. Inventing
--    now() instead would have claimed every article was first read today.
--
--    No batching: this table holds one row per article, so the UPDATE touches
--    as many rows as there are posts. The batching rule (and its reason — long
--    transactions, WAL, held locks) applies to tables that grow with traffic.
ALTER TABLE post_views ADD COLUMN IF NOT EXISTS created_at timestamptz;
UPDATE post_views SET created_at = updated_at WHERE created_at IS NULL;
ALTER TABLE post_views ALTER COLUMN created_at SET NOT NULL;
ALTER TABLE post_views ALTER COLUMN created_at SET DEFAULT now();

-- 2. Defaults for the remaining timestamps, so a repair script or an import
--    does not have to invent one. The service still supplies its own values.
ALTER TABLE post_views   ALTER COLUMN updated_at  SET DEFAULT now();
ALTER TABLE site_visitors ALTER COLUMN first_seen SET DEFAULT now();

-- 3. Deviations that are deliberate, recorded where the next reader will look:
--    `\d+` answers them without searching a wiki.
--
--    Nothing here touches a table owned by another module: the order modules
--    migrate in is main's business, and a migration that assumes another
--    module's table already exists breaks the moment that order changes.
COMMENT ON COLUMN site_visitors.first_seen IS
	'When this browser was first counted. Named for what it means rather than created_at: the row exists only to remember that this visitor has been seen.';
COMMENT ON COLUMN site_counters.id IS
	'Singleton sentinel. The CHECK pins it to 1 so no second "the counter" row can appear; smallint is enough for a column that only ever holds that value.';
COMMENT ON COLUMN post_views.created_at IS
	'First recorded read. For rows written before this column existed, backfilled from updated_at (an upper bound on the real first read).';
