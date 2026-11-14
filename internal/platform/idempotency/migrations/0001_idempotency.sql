-- Idempotency keys: one row per (caller scope, key) that has been seen.
--
-- The row is claimed before the work runs and completed after, so two requests
-- arriving at the same instant cannot both execute: the primary key decides
-- which one wins. status_code and response stay null while the first request is
-- in flight, which is how the second one learns to back off instead of
-- duplicating the write.
CREATE TABLE IF NOT EXISTS idempotency_keys (
	scope        text        NOT NULL,
	key          text        NOT NULL,
	request_hash text        NOT NULL,
	status_code  integer,
	response     bytea,
	created_at   timestamptz NOT NULL DEFAULT now(),
	PRIMARY KEY (scope, key),
	CONSTRAINT idempotency_keys_scope_present CHECK (scope <> ''),
	CONSTRAINT idempotency_keys_key_length CHECK (char_length(key) BETWEEN 8 AND 200),
	CONSTRAINT idempotency_keys_hash_present CHECK (request_hash <> ''),
	CONSTRAINT idempotency_keys_status_valid CHECK (status_code IS NULL OR status_code BETWEEN 100 AND 599),
	-- Both completed together or neither: a stored status with no body would
	-- replay an empty 201, and a body with no status would replay nothing.
	CONSTRAINT idempotency_keys_response_paired CHECK ((status_code IS NULL) = (response IS NULL))
);

-- Expiry is time-based, so the index supports the prune scan rather than a
-- lookup by key (the primary key already covers that).
CREATE INDEX IF NOT EXISTS idempotency_keys_created_at_idx
	ON idempotency_keys (created_at);
