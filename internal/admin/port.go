package admin

import (
	"context"
	"time"

	"github.com/liomix/astro-liomix-server/internal/platform/audit"
)

// Repository is the storage port required by Service. It is declared here, at
// the point of use, so implementations (PostgreSQL now, a remote API later)
// stay free of this package's internals beyond the entities themselves.
type Repository interface {
	// CreateAccount stores a new account. It returns an error wrapping
	// apperr.ErrConflict when the username is already taken.
	CreateAccount(ctx context.Context, a *Account) error
	// GetAccount returns the account with the given ID, or an error wrapping
	// apperr.ErrNotFound.
	GetAccount(ctx context.Context, accountID string) (*Account, error)
	// GetAccountByUsername returns the account with the given normalized
	// username. Usernames are unique case-insensitively.
	GetAccountByUsername(ctx context.Context, username string) (*Account, error)
	// ListAccounts returns the accounts matching q, newest first.
	ListAccounts(ctx context.Context, q AccountQuery) ([]*Account, error)
	// UpdateAccount stores every mutable field of a. The caller has already
	// validated it and decided what changed.
	UpdateAccount(ctx context.Context, a *Account) error
	// UpdateLastLogin records a successful sign-in without writing the other
	// columns: a full update here would race with a concurrent password change
	// and could resurrect the old digest.
	UpdateLastLogin(ctx context.Context, accountID string, at time.Time) error
	// DeleteAccount removes an account. Its sessions go with it.
	DeleteAccount(ctx context.Context, accountID string) error
	// Settings returns every stored console preference as a key/value map.
	// Missing keys are simply absent; the caller supplies defaults.
	Settings(ctx context.Context) (map[string]string, error)
	// PutSettings stores the given preferences, overwriting existing keys.
	PutSettings(ctx context.Context, values map[string]string) error
	// CountAccounts reports how many accounts exist, optionally in one status.
	// An empty status counts every account.
	//
	// It exists so the service can refuse to remove the last active
	// administrator: a count via List would page, and the answer would be
	// wrong exactly when it matters.
	CountAccounts(ctx context.Context, status AccountStatus) (int, error)

	// CreateSession stores a new session. It returns an error wrapping
	// apperr.ErrConflict when the token digest already exists.
	CreateSession(ctx context.Context, s *Session) error
	// GetSessionByTokenHash returns the session a presented token belongs to,
	// or an error wrapping apperr.ErrNotFound.
	GetSessionByTokenHash(ctx context.Context, tokenHash string) (*Session, error)
	// GetSession returns one session by ID.
	GetSession(ctx context.Context, sessionID string) (*Session, error)
	// TouchSession records that the session was used at the given time. It
	// does not extend the expiry: the lifetime is absolute so a stolen token
	// cannot be kept alive forever by an active attacker.
	TouchSession(ctx context.Context, sessionID string, at time.Time) error
	// RevokeSession marks one session as signed out. It returns an error
	// wrapping apperr.ErrNotFound when the session does not exist or is
	// already revoked.
	RevokeSession(ctx context.Context, sessionID string, at time.Time) error
	// RevokeAccountSessions marks every live session of an account as signed
	// out. Used when a password changes.
	RevokeAccountSessions(ctx context.Context, accountID string, at time.Time) error
	// ListSessions returns the live sessions of an account at the given time,
	// newest first.
	ListSessions(ctx context.Context, accountID string, at time.Time) ([]*Session, error)
	// DeleteExpiredSessions removes sessions that expired before the given
	// time and reports how many went away. Revoked rows are kept until they
	// expire so the audit trail can still name them.
	DeleteExpiredSessions(ctx context.Context, before time.Time) (int64, error)
}

// AuditStore is the console's access to the operator trail: newest-first pages,
// plus removing rows that should never have been written.
//
// It is a separate port from audit.Recorder because the two are configured
// separately: a deployment can record without exposing a reader, and a test can
// assert what was recorded without a second implementation of a query language.
// *audit.Store implements both.
type AuditStore interface {
	List(ctx context.Context, q audit.Query) ([]*audit.Entry, string, error)
	// Delete removes the named entries and reports how many rows went away.
	// An id that no longer exists is not an error: a page rendered before
	// someone else's purge is a normal thing to submit.
	Delete(ctx context.Context, ids []string) (int, error)
}

// ModerationComment is one comment as the console shows it. It is a read model:
// the console never stores comments, it only lists and decides on them.
type ModerationComment struct {
	ID          string
	PostSlug    string
	AuthorName  string
	AuthorEmail string
	Body        string
	Status      string
	CreatedAt   time.Time
}

// ModerationQuery narrows the moderation listing.
type ModerationQuery struct {
	// Status is the moderation state to list: pending, approved, rejected or
	// spam. The console defaults to pending.
	Status string
	// Keyword narrows the listing to comments whose author name, email, body
	// or post slug contains it. Empty means no narrowing.
	Keyword string
	Limit   int
	// Cursor continues after the page it names. Empty starts at the newest.
	// It is opaque to this package: the adapter that owns the comment module
	// encodes and decodes it.
	Cursor string
}

// ModerationPage is one page of the moderation listing.
type ModerationPage struct {
	Items []ModerationComment
	// NextCursor is empty on the last page.
	NextCursor string
}

// ModerationQueue is what the console needs from the comment module.
//
// The interface lives here, at the point of use, so the admin module never
// imports comment: cmd/server adapts the comment service to it. That keeps the
// dependency direction "cmd → both modules", which is the only place allowed to
// know about more than one of them.
type ModerationQueue interface {
	List(ctx context.Context, q ModerationQuery) (ModerationPage, error)
	// Counts returns how many comments sit in each moderation state, honoring
	// the keyword filter and ignoring the status one — it answers the question
	// "what is in the queue", which is what the tabs display. States with no
	// comment are present with a zero.
	Counts(ctx context.Context, q ModerationQuery) (map[string]int, error)
	// CountSince returns how many comments arrived at or after since, in any
	// state. The dashboard uses it for "today".
	CountSince(ctx context.Context, since time.Time) (int, error)
	// SetStatus validates and stores a new moderation state.
	SetStatus(ctx context.Context, commentID, status string) (ModerationComment, error)
	// BulkSetStatus applies one state to a selection and reports how many rows
	// changed. Rows deleted since the page rendered are skipped.
	BulkSetStatus(ctx context.Context, commentIDs []string, status string) (int, error)
	// BulkDelete removes a selection, each with its replies.
	BulkDelete(ctx context.Context, commentIDs []string) (int, error)
	// Delete removes a comment and every reply beneath it.
	Delete(ctx context.Context, commentID string) error
}

// MailSettings is the outbound-mail configuration as the console edits it.
//
// Password is write-only. The page is never handed the stored value, so an
// empty Password on the way in means "keep what is stored"; HasPassword says
// whether anything is stored, which is all the page needs to know.
type MailSettings struct {
	Host        string
	Port        int
	TLS         string
	Username    string
	Password    string
	HasPassword bool
	From        string
	FromName    string
	AdminTo     []string
	ReplyNotify bool
	AdminNotify bool
	// TemplateAdmin and TemplateReply are whole HTML documents pasted into the
	// page. Empty means the template compiled into the binary.
	TemplateAdmin string
	TemplateReply string
	// OwnerKey is the secret that lets a comment from an owner address wear the
	// owner badge.
	OwnerKey string
}

// MailService is the mail subsystem as the console sees it: one settings row to
// read and write, and one way to prove the settings work.
type MailService interface {
	Settings(ctx context.Context) (MailSettings, error)
	// SaveSettings validates and stores the settings. An error wraps
	// apperr.ValidationError when the form itself is wrong.
	SaveSettings(ctx context.Context, in MailSettings) error
	// SendTest delivers one message before answering, so the operator learns
	// whether it worked instead of waiting for a comment.
	SendTest(ctx context.Context, to string) error
}
