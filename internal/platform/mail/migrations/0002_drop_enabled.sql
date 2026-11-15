-- The console no longer has a master "mail is on" switch: the two notices are
-- the switches, and a send happens whenever the account is configured and the
-- notice that triggered it is on. A flag that says "mail is enabled" while both
-- notices are off was a second source of truth for the same question.
ALTER TABLE mail_settings DROP COLUMN IF EXISTS enabled;

-- Both notices now start on. A fresh install still sends nothing — it has no
-- SMTP account — but the page no longer asks the operator to find and flip two
-- switches before their blog can tell them a comment arrived.
ALTER TABLE mail_settings ALTER COLUMN reply_notify SET DEFAULT true;
ALTER TABLE mail_settings ALTER COLUMN admin_notify SET DEFAULT true;

-- One-time: the existing row was configured back when both switches defaulted
-- to off, so "default on" has to be applied to it explicitly.
UPDATE mail_settings SET reply_notify = true, admin_notify = true, updated_at = now();
