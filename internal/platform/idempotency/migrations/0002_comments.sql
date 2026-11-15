-- Documentation only: the reasons behind this table, written into the
-- catalogue so `\d+ idempotency_keys` explains itself.
--
-- Kept in its own file because 0001 is already applied everywhere, and an
-- applied migration is never edited — that is what keeps "the schema the
-- repository describes" and "the schema the database has" the same thing.
COMMENT ON TABLE idempotency_keys IS
	'One row per (scope, key). The row is claimed before the work runs and completed after, so two requests arriving together cannot both execute: the primary key decides.';
COMMENT ON COLUMN idempotency_keys.scope IS
	'Which operation the key belongs to. A key is only unique within one scope: the same client-generated string posted to two endpoints must not collide.';
COMMENT ON COLUMN idempotency_keys.request_hash IS
	'Digest of the exact request body. The same key with a different body is a client bug, and it is answered with 409 rather than by replaying an answer to a question nobody asked.';
COMMENT ON COLUMN idempotency_keys.response IS
	'The recorded response body, replayed verbatim. Kept as bytea rather than jsonb: jsonb reorders keys and drops duplicates, and a replay must be the same bytes.';
COMMENT ON COLUMN idempotency_keys.created_at IS
	'When the key was claimed. Drives the retention sweep; rows past the window are deleted, and the key becomes usable again.';
