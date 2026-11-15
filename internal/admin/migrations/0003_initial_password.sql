-- The plaintext of an account's initial password, kept only so the console can
-- show it on the login page until someone changes it.
--
-- This is a deliberate trade-off, not an oversight. A password digest cannot be
-- reversed, so "show the initial password again" is impossible without keeping
-- a copy, and the console is being used as the place an operator looks for the
-- credential. Two limits keep the exposure small:
--
--   1. Only accounts that still hold their initial password have a value here;
--      a self-service change clears it.
--   2. The server only writes it, and only serves it, in development
--      (ADMIN_SHOW_INITIAL_CREDENTIALS / APP_ENV). Production accounts keep
--      NULL and the login page falls back to the generic hint.
--
-- It is still a plaintext credential at rest: a database dump while an account
-- is unclaimed exposes it, and the login page is unauthenticated. Change the
-- password on first sign-in and this column becomes NULL.
ALTER TABLE admin_accounts
	ADD COLUMN IF NOT EXISTS initial_password text;

COMMENT ON COLUMN admin_accounts.initial_password IS
	'Plaintext of the initial password, shown on the development console login page until the account changes it. NULL everywhere else; never returned by the JSON API.';
