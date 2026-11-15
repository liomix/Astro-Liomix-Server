-- The operator audit trail.
--
-- actor_id is deliberately not a foreign key. The whole point of the trail is
-- to say who did something after the account is gone: a foreign key with
-- ON DELETE SET NULL would erase the only link, and one with RESTRICT would
-- make deleting an account impossible until its history aged out. actor_name is
-- the snapshot that keeps the row readable, and actor_id is kept for joins
-- while the account still exists.
--
-- detail is jsonb rather than text so an operator can query into it
-- (detail->>'status'), while the writer is free to evolve its shape per action.
CREATE TABLE IF NOT EXISTS audit_logs (
	id          text        PRIMARY KEY,
	actor_id    text,
	actor_name  text        NOT NULL DEFAULT '',
	action      text        NOT NULL,
	resource    text        NOT NULL,
	resource_id text        NOT NULL DEFAULT '',
	detail      jsonb       NOT NULL DEFAULT '{}'::jsonb,
	ip          text        NOT NULL DEFAULT '',
	user_agent  text        NOT NULL DEFAULT '',
	created_at  timestamptz NOT NULL,
	CONSTRAINT audit_logs_action_shape CHECK (action ~ '^[a-z][a-z0-9]*(\.[a-z0-9_]+)+$'),
	CONSTRAINT audit_logs_resource_shape CHECK (resource ~ '^[a-z][a-z0-9_]*$')
);

-- The listing is always newest first with the id breaking ties, which is also
-- what the cursor encodes. The two extra indexes serve the two filters the
-- endpoint exposes; without them a filtered read scans the whole trail.
CREATE INDEX IF NOT EXISTS audit_logs_created_idx
	ON audit_logs (created_at DESC, id DESC);
CREATE INDEX IF NOT EXISTS audit_logs_action_idx
	ON audit_logs (action, created_at DESC, id DESC);
CREATE INDEX IF NOT EXISTS audit_logs_actor_idx
	ON audit_logs (actor_id, created_at DESC, id DESC);

COMMENT ON TABLE audit_logs IS
	'Append-only record of privileged actions. actor_id is not a foreign key so the trail outlives the account it names.';
COMMENT ON COLUMN audit_logs.action IS
	'Dotted, stable action name, e.g. auth.login or comment.moderate. Clients filter on it.';
COMMENT ON COLUMN audit_logs.actor_name IS
	'Username at the time of the action, snapshotted so the row stays readable after a rename or deletion.';
