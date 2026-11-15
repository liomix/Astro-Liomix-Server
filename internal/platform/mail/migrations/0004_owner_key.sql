-- The secret that proves "this is the site owner" when they comment.
--
-- It lives here because the console's mail page is where the owner's own
-- addresses already are; the value is one shared secret, not a per-session
-- credential. Empty means "not configured": the owner is then recognised by
-- address alone, which is the behaviour a deployment has before it sets one.
ALTER TABLE mail_settings ADD COLUMN IF NOT EXISTS owner_key text NOT NULL DEFAULT '';
