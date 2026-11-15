-- Custom HTML bodies for the two notifications.
--
-- Empty means "use the template compiled into the binary". The console shows
-- them as two textareas, and an operator pastes a whole document — table-based
-- layout, inline styles, a header image encoded as data: — because that is what
-- mail clients actually render.
--
-- They are text, not jsonb: the value is a document, and the only operations on
-- it are read and write whole.
ALTER TABLE mail_settings ADD COLUMN IF NOT EXISTS template_admin text NOT NULL DEFAULT '';
ALTER TABLE mail_settings ADD COLUMN IF NOT EXISTS template_reply text NOT NULL DEFAULT '';

COMMENT ON COLUMN mail_settings.template_admin IS
	'Full HTML body for the "new comment" notice. Empty uses the built-in template.';
COMMENT ON COLUMN mail_settings.template_reply IS
	'Full HTML body for the "someone replied to you" notice. Empty uses the built-in template.';
