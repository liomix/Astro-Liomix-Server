-- Whether the site owner wrote this comment, decided once at creation.
--
-- It cannot be derived from the author email at read time: the email is a field
-- anybody can type, so computing the badge from it let anyone who knew the
-- owner's address publish as the owner. The flag is set when the comment is
-- created, after the owner key has been checked, and is never recomputed.
ALTER TABLE comments ADD COLUMN IF NOT EXISTS author_is_owner boolean NOT NULL DEFAULT false;

-- Comments written before the flag existed belong to whoever owned that address
-- at the time; backfill them once from the deployment's owner list, e.g.:
--   UPDATE comments SET author_is_owner = true WHERE lower(author_email) = 'owner@example.com';
