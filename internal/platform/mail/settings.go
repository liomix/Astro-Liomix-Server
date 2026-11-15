package mail

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net"
	netmail "net/mail"
	"strings"

	"github.com/liomix/astro-liomix-server/internal/platform/apperr"
	"github.com/liomix/astro-liomix-server/internal/platform/postgres"
)

// TLS modes. They are the values the console's dropdown offers and the ones the
// column's CHECK constraint accepts.
const (
	TLSAuto     = "auto"
	TLSStartTLS = "starttls"
	TLSImplicit = "implicit"
	TLSNone     = "none"
)

// ErrNotConfigured is what a send returns while the server has no host or
// sender. Callers treat it as "nothing to do" — a blog that has not set up mail
// is not an outage.
var ErrNotConfigured = errors.New("mail: smtp is not configured")

// Settings is the complete outbound-mail configuration: where to connect, who
// it comes from, who hears about a new comment, and which notices are on.
//
// It is a value, not a live object. Everything reads it fresh from storage
// before it matters, which is what makes a change on the page take effect on
// the next message instead of the next restart.
type Settings struct {
	Host string
	Port int
	// TLS is one of TLSAuto, TLSStartTLS, TLSImplicit, TLSNone.
	TLS      string
	Username string
	// Password is the SMTP password or app-specific token. It is write-only as
	// far as the console is concerned, but the sender obviously needs it.
	Password string

	// From is the envelope sender, FromName the display name.
	From     string
	FromName string

	// AdminTo receives the "new comment" notice. Several addresses are allowed
	// because a blog often outlives one person.
	AdminTo []string

	// ReplyNotify mails the author a comment replies to.
	ReplyNotify bool
	// AdminNotify mails AdminTo on every comment that gets approved.
	AdminNotify bool

	// TemplateAdmin and TemplateReply are whole HTML documents the operator
	// pasted into the console. Empty means the template compiled into the
	// binary, which is also what a fresh install sends.
	TemplateAdmin string
	TemplateReply string
}

// DefaultSettings is what a fresh install starts from: a conventional
// submission port, no credentials, and both notices on.
//
// The notices default to on because they are the reason the page exists; what
// stops a fresh install from mailing anybody is that there is no SMTP account
// yet, which the page says out loud instead of hiding behind a master switch.
func DefaultSettings() Settings {
	return Settings{Port: 587, TLS: TLSAuto, ReplyNotify: true, AdminNotify: true}
}

// SMTP projects the connection half of the settings for the sender.
func (s Settings) SMTP() SMTPConfig {
	return SMTPConfig{
		Host:     s.Host,
		Port:     s.Port,
		TLS:      s.TLS,
		Username: s.Username,
		Password: s.Password,
		From:     s.From,
		FromName: s.FromName,
	}
}

// Configured reports whether the connection and the sender are filled in, which
// is the one condition a send needs. There is no separate "mail is on" flag:
// turning the two notices off is how you stop mail without losing the account.
func (s Settings) Configured() bool { return s.Host != "" && s.From != "" }

// Validate reports a value that cannot even be stored: an impossible port, an
// unknown mode, an address that is not an address.
//
// It deliberately says nothing about whether the settings are *complete*. An
// operator fills the form in one sitting while an app password is still on its
// way, and refusing the half-filled save would throw their work away and make
// the page look broken. Completeness is a question about sending, not about
// saving; the console answers it with MailSettings.Issue.
func (s Settings) Validate() error {
	switch s.TLS {
	case TLSAuto, TLSStartTLS, TLSImplicit, TLSNone:
	default:
		return &apperr.ValidationError{Field: "tls", Code: apperr.FieldInvalid, Message: "must be one of auto, starttls, implicit, none"}
	}
	if s.Port <= 0 || s.Port > 65535 {
		return &apperr.ValidationError{Field: "port", Code: apperr.FieldOutOfRange, Message: "must be between 1 and 65535"}
	}
	if host := strings.TrimSpace(s.Host); host != "" {
		if net.ParseIP(host) == nil && !validHostname(host) {
			return &apperr.ValidationError{Field: "host", Code: apperr.FieldInvalid, Message: "is not a host name"}
		}
	}
	if s.From != "" {
		if err := checkAddress(s.From); err != nil {
			return &apperr.ValidationError{Field: "from", Code: apperr.FieldInvalid, Message: err.Error()}
		}
	}
	for _, to := range s.AdminTo {
		if err := checkAddress(to); err != nil {
			return &apperr.ValidationError{Field: "adminTo", Code: apperr.FieldInvalid, Message: err.Error()}
		}
	}
	return nil
}

// ParseRecipients splits the admin-recipient field however it was typed:
// commas, semicolons, spaces or newlines, with duplicates removed.
func ParseRecipients(raw string) []string {
	fields := strings.FieldsFunc(raw, func(r rune) bool {
		return r == ',' || r == ';' || r == ' ' || r == '\t' || r == '\n' || r == '\r'
	})
	out := make([]string, 0, len(fields))
	seen := make(map[string]struct{}, len(fields))
	for _, field := range fields {
		field = strings.TrimSpace(field)
		if field == "" {
			continue
		}
		if _, duplicate := seen[strings.ToLower(field)]; duplicate {
			continue
		}
		seen[strings.ToLower(field)] = struct{}{}
		out = append(out, field)
	}
	return out
}

// joinRecipients is the storage form of AdminTo.
func joinRecipients(list []string) string { return strings.Join(list, ", ") }

func checkAddress(addr string) error {
	parsed, err := netmail.ParseAddress(addr)
	if err != nil || parsed.Address != addr {
		return fmt.Errorf("%q is not an email address", addr)
	}
	return nil
}

func validHostname(host string) bool {
	if host == "" || len(host) > 253 {
		return false
	}
	for _, label := range strings.Split(host, ".") {
		if label == "" || len(label) > 63 {
			return false
		}
		for _, r := range label {
			if r == '-' || r == '_' || r >= '0' && r <= '9' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' {
				continue
			}
			return false
		}
	}
	return true
}

// Store keeps the settings row in PostgreSQL. There is exactly one row.
type Store struct {
	db postgres.Executor
}

// NewStore returns a store reading and writing through db, which is the pool in
// production and a rolled-back transaction in tests.
func NewStore(db postgres.Executor) *Store { return &Store{db: db} }

// Load returns the stored settings and whether the row exists yet.
func (s *Store) Load(ctx context.Context) (Settings, bool, error) {
	var (
		out     Settings
		adminTo string
	)
	err := s.db.QueryRowContext(ctx, `
		SELECT host, port, tls, username, password,
			sender_mail, sender_name, admin_mail, reply_notify, admin_notify,
			template_admin, template_reply
		FROM mail_settings WHERE id = 1`).Scan(
		&out.Host,
		&out.Port,
		&out.TLS,
		&out.Username,
		&out.Password,
		&out.From,
		&out.FromName,
		&adminTo,
		&out.ReplyNotify,
		&out.AdminNotify,
		&out.TemplateAdmin,
		&out.TemplateReply,
	)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return DefaultSettings(), false, nil
	case err != nil:
		return DefaultSettings(), false, fmt.Errorf("mail: load settings: %w", err)
	}
	out.AdminTo = ParseRecipients(adminTo)
	return out, true, nil
}

// Save writes the settings, creating the row on first use.
func (s *Store) Save(ctx context.Context, in Settings) error {
	if err := in.Validate(); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO mail_settings (
			id, host, port, tls, username, password,
			sender_mail, sender_name, admin_mail, reply_notify, admin_notify,
			template_admin, template_reply, updated_at
		)
		VALUES (1, $1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, now())
		ON CONFLICT (id) DO UPDATE SET
			host = EXCLUDED.host,
			port = EXCLUDED.port,
			tls = EXCLUDED.tls,
			username = EXCLUDED.username,
			password = EXCLUDED.password,
			sender_mail = EXCLUDED.sender_mail,
			sender_name = EXCLUDED.sender_name,
			admin_mail = EXCLUDED.admin_mail,
			reply_notify = EXCLUDED.reply_notify,
			admin_notify = EXCLUDED.admin_notify,
			template_admin = EXCLUDED.template_admin,
			template_reply = EXCLUDED.template_reply,
			updated_at = now()`,
		strings.TrimSpace(in.Host),
		in.Port,
		in.TLS,
		in.Username,
		in.Password,
		strings.TrimSpace(in.From),
		strings.TrimSpace(in.FromName),
		joinRecipients(in.AdminTo),
		in.ReplyNotify,
		in.AdminNotify,
		in.TemplateAdmin,
		in.TemplateReply,
	)
	if err != nil {
		return fmt.Errorf("mail: save settings: %w", err)
	}
	return nil
}

// Ensure writes seed unless a row already exists, and reports whether it wrote.
//
// It is how a deployment that configured mail through the environment arrives
// in the new world: the values are imported once, then the console owns them.
// A row that already exists is never overwritten — the page is the source of
// truth from that point on.
func (s *Store) Ensure(ctx context.Context, seed Settings) (bool, error) {
	if err := seed.Validate(); err != nil {
		return false, err
	}
	result, err := s.db.ExecContext(ctx, `
		INSERT INTO mail_settings (
			id, host, port, tls, username, password,
			sender_mail, sender_name, admin_mail, reply_notify, admin_notify,
			template_admin, template_reply, updated_at
		)
		VALUES (1, $1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, now())
		ON CONFLICT (id) DO NOTHING`,
		strings.TrimSpace(seed.Host),
		seed.Port,
		seed.TLS,
		seed.Username,
		seed.Password,
		strings.TrimSpace(seed.From),
		strings.TrimSpace(seed.FromName),
		joinRecipients(seed.AdminTo),
		seed.ReplyNotify,
		seed.AdminNotify,
		seed.TemplateAdmin,
		seed.TemplateReply,
	)
	if err != nil {
		return false, fmt.Errorf("mail: seed settings: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("mail: seed settings: %w", err)
	}
	return affected > 0, nil
}

// SettingsSender sends through whatever the settings row holds at the moment of
// the attempt.
//
// A sender captured at startup would freeze the configuration until a restart,
// which is exactly the behaviour the editable page exists to remove. Building
// one per message costs a couple of string checks and no I/O.
type SettingsSender struct {
	store *Store
}

// NewSettingsSender returns a sender backed by store.
func NewSettingsSender(store *Store) SettingsSender { return SettingsSender{store: store} }

// Send implements Sender.
func (s SettingsSender) Send(ctx context.Context, m Message) error {
	settings, found, err := s.store.Load(ctx)
	if err != nil {
		return err
	}
	if !found || !settings.Configured() {
		return ErrNotConfigured
	}
	sender, err := NewSMTPSender(settings.SMTP())
	if err != nil {
		return err
	}
	return sender.Send(ctx, m)
}
