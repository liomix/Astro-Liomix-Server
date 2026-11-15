-- The whole outbound-mail configuration in one row.
--
-- The console shows every field of it as a single form and saves it with one
-- statement, so a column-per-setting table would buy nothing but joins. id is
-- pinned to 1 by a CHECK: "the settings" cannot fork into two rows, and an
-- INSERT that forgets to name a row cannot create a second one.
--
-- The SMTP password is stored as written. It has to be replayed to the mail
-- server on every send, so encrypting it here would need a key the process
-- already holds — a key sitting in the same environment as the database
-- password protects nothing. What matters instead: the page never echoes the
-- stored value back, and the audit trail records only that it changed.
CREATE TABLE IF NOT EXISTS mail_settings (
	id           smallint    PRIMARY KEY DEFAULT 1,
	enabled      boolean     NOT NULL DEFAULT false,
	host         text        NOT NULL DEFAULT '',
	port         integer     NOT NULL DEFAULT 587,
	tls          text        NOT NULL DEFAULT 'auto',
	username     text        NOT NULL DEFAULT '',
	password     text        NOT NULL DEFAULT '',
	sender_mail  text        NOT NULL DEFAULT '',
	sender_name  text        NOT NULL DEFAULT '',
	admin_mail   text        NOT NULL DEFAULT '',
	reply_notify boolean     NOT NULL DEFAULT false,
	admin_notify boolean     NOT NULL DEFAULT false,
	updated_at   timestamptz NOT NULL DEFAULT now(),
	CONSTRAINT mail_settings_singleton CHECK (id = 1),
	CONSTRAINT mail_settings_port_range CHECK (port BETWEEN 1 AND 65535),
	CONSTRAINT mail_settings_tls_mode CHECK (tls IN ('auto', 'starttls', 'implicit', 'none'))
);

COMMENT ON TABLE mail_settings IS
	'Single-row outbound mail configuration, editable from the console.';
COMMENT ON COLUMN mail_settings.admin_mail IS
	'Comma-separated recipients of the "new comment" notice. Empty disables that notice.';
