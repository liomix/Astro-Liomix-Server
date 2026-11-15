-- Whether an account still uses the password it was created with.
--
-- The console shows a standing reminder until this flips to false, and there is
-- no way to derive it after the fact: updated_at also moves when a display name
-- changes, and comparing it with created_at would both miss a same-second edit
-- and accuse an account that never touched its password. A column states the
-- fact instead of guessing it.
--
-- Existing rows default to true: every account that exists when this migration
-- runs was created by the CLI or the bootstrap path, so its password is by
-- definition one nobody has changed through the console yet.
ALTER TABLE admin_accounts
	ADD COLUMN IF NOT EXISTS password_is_initial boolean NOT NULL DEFAULT true;

COMMENT ON COLUMN admin_accounts.password_is_initial IS
	'True until the account changes its own password. An administrator resetting someone else''s password sets it back to true, because the target still has to choose their own.';
