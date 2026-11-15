-- Administrator accounts and their sign-in sessions.
--
-- Two tables rather than one with a "current token" column: an operator may be
-- signed in on a laptop and a phone, and signing out of one must not sign out
-- the other. Sessions are rows so "sign out everywhere" is one UPDATE and
-- "who is signed in" is one SELECT.
CREATE TABLE IF NOT EXISTS admin_accounts (
	id            text        PRIMARY KEY,
	username      text        NOT NULL,
	display_name  text        NOT NULL,
	password_hash text        NOT NULL,
	status        text        NOT NULL DEFAULT 'active',
	created_at    timestamptz NOT NULL DEFAULT now(),
	updated_at    timestamptz NOT NULL DEFAULT now(),
	last_login_at timestamptz,
	CONSTRAINT admin_accounts_status_valid CHECK (status IN ('active', 'disabled')),
	-- Usernames are stored folded to lowercase, which is what makes the unique
	-- index case-insensitive. The shape check is the same rule the Go validator
	-- applies; a script or a psql session gets it too.
	CONSTRAINT admin_accounts_username_shape CHECK (username ~ '^[a-z0-9][a-z0-9._-]{2,39}$'),
	CONSTRAINT admin_accounts_display_name_length CHECK (char_length(btrim(display_name)) BETWEEN 1 AND 80),
	-- The digest carries its own scheme, cost and salt, so the cost can be
	-- raised later without a migration: old rows keep verifying as written.
	CONSTRAINT admin_accounts_password_hash_shape CHECK (
		password_hash ~ '^pbkdf2-sha256\$[0-9]{1,8}\$[A-Za-z0-9+/]{8,64}\$[A-Za-z0-9+/]{16,128}$'
	)
);

-- Case-insensitivity is already guaranteed by the shape check above; the unique
-- index is what turns a duplicate into a constraint violation instead of a
-- second account nobody can tell apart at the login form.
CREATE UNIQUE INDEX IF NOT EXISTS admin_accounts_username_key ON admin_accounts (username);

-- The account listing pages newest first with the id breaking ties, matching
-- the cursor the API hands out.
CREATE INDEX IF NOT EXISTS admin_accounts_created_idx
	ON admin_accounts (created_at DESC, id DESC);

CREATE TABLE IF NOT EXISTS admin_sessions (
	id           text        PRIMARY KEY,
	account_id   text        NOT NULL REFERENCES admin_accounts (id) ON DELETE CASCADE,
	token_hash   text        NOT NULL,
	created_at   timestamptz NOT NULL DEFAULT now(),
	last_seen_at timestamptz NOT NULL DEFAULT now(),
	expires_at   timestamptz NOT NULL,
	revoked_at   timestamptz,
	ip           text        NOT NULL DEFAULT '',
	user_agent   text        NOT NULL DEFAULT '',
	CONSTRAINT admin_sessions_token_hash_shape CHECK (token_hash ~ '^[0-9a-f]{64}$'),
	CONSTRAINT admin_sessions_expiry_after_created CHECK (expires_at > created_at),
	CONSTRAINT admin_sessions_user_agent_length CHECK (char_length(user_agent) <= 512),
	CONSTRAINT admin_sessions_ip_length CHECK (char_length(ip) <= 64)
);

-- Authentication is one lookup on the token digest, so this index is the hot
-- path: every authenticated request goes through it.
CREATE UNIQUE INDEX IF NOT EXISTS admin_sessions_token_hash_key ON admin_sessions (token_hash);

-- "Sessions of this account, newest first" and the foreign-key cascade both
-- need this; PostgreSQL does not index foreign keys on its own.
CREATE INDEX IF NOT EXISTS admin_sessions_account_idx
	ON admin_sessions (account_id, created_at DESC, id DESC);

-- The prune at startup deletes expired rows; without this index it scans.
CREATE INDEX IF NOT EXISTS admin_sessions_expiry_idx ON admin_sessions (expires_at);

COMMENT ON TABLE admin_accounts IS
	'Operator identities. Single role on purpose: every account may moderate comments and manage accounts, and the last active one cannot be disabled or deleted.';
COMMENT ON COLUMN admin_accounts.password_hash IS
	'PBKDF2-HMAC-SHA256 digest encoded as scheme$cost$salt$key, so the cost can be raised without a migration.';
COMMENT ON COLUMN admin_accounts.last_login_at IS
	'NULL means the account has never signed in; that is different from an account created long ago and used immediately.';
COMMENT ON TABLE admin_sessions IS
	'Sign-in sessions. Only the SHA-256 digest of the bearer token is stored, so a database dump cannot be replayed.';
COMMENT ON COLUMN admin_sessions.expires_at IS
	'Absolute expiry, not sliding: an active attacker must not be able to keep a stolen token alive forever.';
COMMENT ON COLUMN admin_sessions.revoked_at IS
	'Set on sign-out. Rows are kept until they expire so the audit trail can still name the session that performed an action.';
