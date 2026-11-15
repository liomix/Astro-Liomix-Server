package admin

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/liomix/astro-liomix-server/internal/platform/apperr"
	"github.com/liomix/astro-liomix-server/internal/platform/audit"
	"github.com/liomix/astro-liomix-server/internal/platform/cursor"
	"github.com/liomix/astro-liomix-server/internal/platform/id"
)

// Session lifetime bounds and defaults.
const (
	// defaultSessionTTL is how long a sign-in lasts. A working week is the
	// longest an operator can be away without noticing a stolen laptop, and a
	// week of not signing in twice is worth the risk on a blog console.
	defaultSessionTTL = 7 * 24 * time.Hour
	// minSessionTTL keeps a deployment from configuring sessions that expire
	// faster than an operator can use them.
	minSessionTTL = 15 * time.Minute
	// maxSessionTTL caps the damage a lost token can do.
	maxSessionTTL = 30 * 24 * time.Hour
	// sessionTouchInterval throttles the last-seen write. Updating it on every
	// request would turn each authenticated read into a write, and the
	// difference between one minute and five is not worth that.
	sessionTouchInterval = 5 * time.Minute
)

// Account listing bounds.
const (
	defaultAccountPageSize = 50
	maxAccountPageSize     = 200
	// maxSessionsListed bounds the sessions returned for one account. Sessions
	// are a handful per person; the cap is a guard against a leaked token used
	// to open thousands, not a pagination scheme.
	maxSessionsListed = 100
)

// Audit action names. They are dotted and stable: a filter in the console, an
// alert rule or a saved query all match on these strings.
const (
	actionLogin          = "auth.login"
	actionSettingsUpdate = "settings.mail"
	actionLogout         = "auth.logout"
	actionAccountCreate  = "account.create"
	actionAccountUpdate  = "account.update"
	actionAccountDelete  = "account.delete"
	actionPasswordChange = "account.password_change"
	actionSessionRevoke  = "session.revoke"
	actionBootstrap      = "account.bootstrap"
	// actionAuditDelete records the removal of trail rows. It is written after
	// the delete, so the trail stays a record of what happened rather than
	// silently getting shorter.
	actionAuditDelete = "audit.delete"
)

// maxAuditDeleteIDs bounds one purge, matching the comment queue's batch limit:
// a selection larger than this is a mistake or an attempt to empty the table
// through a form, and both deserve no more than a refusal.
const maxAuditDeleteIDs = 100

// Service holds the administrator use cases.
type Service struct {
	repo       Repository
	auditor    audit.Recorder
	auditStore AuditStore
	hasher     passwordHasher
	ids        id.Generator
	now        func() time.Time
	sessionTTL time.Duration
	logger     *slog.Logger
	// storePlaintext keeps the initial password so the development console can
	// show it. Off in production: a plaintext credential at rest is a risk that
	// only local convenience justifies.
	storePlaintext bool
}

// Option configures a Service. Options are applied in order, so later ones win.
type Option func(*Service)

// WithLogger sets the structured logger. A nil logger keeps slog.Default().
func WithLogger(l *slog.Logger) Option {
	return func(s *Service) {
		if l != nil {
			s.logger = l
		}
	}
}

// WithClock replaces the time source, mainly in tests.
func WithClock(now func() time.Time) Option {
	return func(s *Service) {
		if now != nil {
			s.now = now
		}
	}
}

// WithIDGenerator replaces the random identifier source, mainly in tests.
func WithIDGenerator(g id.Generator) Option {
	return func(s *Service) {
		if g != nil {
			s.ids = g
		}
	}
}

// WithAuditor records privileged actions. Without one the service works and
// silently keeps no trail, which is the right default for unit tests.
func WithAuditor(r audit.Recorder) Option {
	return func(s *Service) {
		if r != nil {
			s.auditor = r
		}
	}
}

// WithAuditStore exposes the trail through the service, so the console can
// read what WithAuditor recorded and clear rows that should not be there. A
// store passed as both is the usual wiring.
func WithAuditStore(r AuditStore) Option {
	return func(s *Service) {
		if r != nil {
			s.auditStore = r
		}
	}
}

// WithSessionTTL sets how long a sign-in lasts. Values outside the accepted
// range are clamped rather than rejected: a typo should not take the console
// down, and the log line below says what happened.
func WithSessionTTL(ttl time.Duration) Option {
	return func(s *Service) {
		switch {
		case ttl <= 0:
			return
		case ttl < minSessionTTL:
			s.sessionTTL = minSessionTTL
		case ttl > maxSessionTTL:
			s.sessionTTL = maxSessionTTL
		default:
			s.sessionTTL = ttl
		}
	}
}

// WithPlaintextInitialPassword controls whether the plaintext of an initial
// password is kept for the console to display. Defaults to true; cmd/server
// turns it off outside development.
func WithPlaintextInitialPassword(keep bool) Option {
	return func(s *Service) {
		s.storePlaintext = keep
	}
}

// WithPasswordCost sets the PBKDF2 iteration count. It exists for tests: the
// production value deliberately costs a few hundred milliseconds per login.
func WithPasswordCost(iterations int) Option {
	return func(s *Service) {
		if iterations > 0 {
			s.hasher.cost = iterations
		}
	}
}

// NewService wires an admin service. It fails fast on a missing repository
// rather than panicking on the first request.
func NewService(repo Repository, opts ...Option) (*Service, error) {
	if repo == nil {
		return nil, errors.New("admin: repository is required")
	}
	s := &Service{
		repo:           repo,
		hasher:         defaultHasher,
		ids:            id.RandomGenerator{},
		now:            time.Now,
		sessionTTL:     defaultSessionTTL,
		logger:         slog.Default(),
		storePlaintext: true,
	}
	for _, opt := range opts {
		opt(s)
	}
	return s, nil
}

// LoginInput is a sign-in attempt. IP and UserAgent are recorded on the session
// and in the audit trail; they are not part of authentication.
type LoginInput struct {
	Username  string
	Password  string
	IP        string
	UserAgent string
}

// Login verifies a password and opens a session.
//
// Every failure returns apperr.ErrUnauthorized with no detail, whether the
// account is unknown, the password is wrong or the account is disabled.
// Distinguishing them would let anyone enumerate valid usernames — and a
// disabled account is not the caller's business until they prove they own it.
//
// The successful return values are the account, the stored session and the
// plaintext token. The token is shown to the client once and never stored.
func (s *Service) Login(ctx context.Context, in LoginInput) (*Account, *Session, string, error) {
	username := NormalizeUsername(in.Username)
	if username == "" || in.Password == "" {
		return nil, nil, "", apperr.ErrUnauthorized
	}

	account, err := s.repo.GetAccountByUsername(ctx, username)
	if err != nil {
		if errors.Is(err, apperr.ErrNotFound) {
			// Derive against a hash nobody knows, so an unknown username costs
			// what a wrong password costs. Without this the response time
			// enumerates accounts even though the body does not.
			_, _ = s.hasher.Verify(dummyPasswordHash, in.Password)
			s.logger.WarnContext(ctx, "login rejected", slog.String("username", username), slog.String("reason", "unknown account"))
			return nil, nil, "", apperr.ErrUnauthorized
		}
		return nil, nil, "", fmt.Errorf("login %q: %w", username, err)
	}

	matched, err := s.hasher.Verify(account.PasswordHash, in.Password)
	if err != nil {
		// A digest this package cannot read is a corrupted row or a hand
		// edit, not a wrong password. Log it as the server fault it is and
		// still answer with the generic rejection.
		s.logger.ErrorContext(ctx, "verify password", slog.String("account_id", account.ID), slog.Any("error", err))
		return nil, nil, "", apperr.ErrUnauthorized
	}
	if !matched {
		s.logger.WarnContext(ctx, "login rejected", slog.String("username", username), slog.String("reason", "wrong password"))
		return nil, nil, "", apperr.ErrUnauthorized
	}
	if !account.Active() {
		s.logger.WarnContext(ctx, "login rejected", slog.String("username", username), slog.String("reason", "account disabled"))
		return nil, nil, "", apperr.ErrUnauthorized
	}

	token, err := newSessionToken()
	if err != nil {
		return nil, nil, "", err
	}
	now := s.timestamp()
	session := &Session{
		ID:         s.ids.New(),
		AccountID:  account.ID,
		TokenHash:  hashSessionToken(token),
		CreatedAt:  now,
		LastSeenAt: now,
		ExpiresAt:  now.Add(s.sessionTTL),
		IP:         in.IP,
		UserAgent:  in.UserAgent,
	}
	if err := s.repo.CreateSession(ctx, session); err != nil {
		return nil, nil, "", fmt.Errorf("open session for %q: %w", account.ID, err)
	}

	// Booking the sign-in must not fail the sign-in: the session is already
	// valid, and turning a successful login into a 500 because a convenience
	// column could not be written would be a worse trade.
	if err := s.repo.UpdateLastLogin(ctx, account.ID, now); err != nil {
		s.logger.WarnContext(ctx, "record last login", slog.String("account_id", account.ID), slog.Any("error", err))
	} else {
		account.LastLoginAt = now
	}

	actor := audit.Actor{ID: account.ID, Name: account.Username, IP: in.IP, UserAgent: in.UserAgent}
	s.record(ctx, actor, actionLogin, "session", session.ID, map[string]any{"username": account.Username})
	s.logger.InfoContext(ctx, "admin signed in", slog.String("account_id", account.ID), slog.String("session_id", session.ID))

	return account.Clone(), session.Clone(), token, nil
}

// Authenticate resolves a bearer token to its account and session.
//
// It checks the session's state on every call rather than trusting a
// self-contained token: revocation and expiry are the reason sessions are rows.
// A session that is expired, revoked, or belongs to a disabled account is
// reported as unauthorized in all three cases.
func (s *Service) Authenticate(ctx context.Context, token string) (*Account, *Session, error) {
	if token == "" {
		return nil, nil, apperr.ErrUnauthorized
	}
	session, err := s.repo.GetSessionByTokenHash(ctx, hashSessionToken(token))
	if err != nil {
		if errors.Is(err, apperr.ErrNotFound) {
			return nil, nil, apperr.ErrUnauthorized
		}
		return nil, nil, fmt.Errorf("authenticate session: %w", err)
	}

	now := s.timestamp()
	if !session.Active(now) {
		return nil, nil, apperr.ErrUnauthorized
	}

	account, err := s.repo.GetAccount(ctx, session.AccountID)
	if err != nil {
		if errors.Is(err, apperr.ErrNotFound) {
			return nil, nil, apperr.ErrUnauthorized
		}
		return nil, nil, fmt.Errorf("authenticate session: %w", err)
	}
	if !account.Active() {
		return nil, nil, apperr.ErrUnauthorized
	}

	if now.Sub(session.LastSeenAt) >= sessionTouchInterval {
		if err := s.repo.TouchSession(ctx, session.ID, now); err != nil {
			// The request is still legitimate; the bookkeeping is not.
			s.logger.WarnContext(ctx, "touch session", slog.String("session_id", session.ID), slog.Any("error", err))
		} else {
			session.LastSeenAt = now
		}
	}
	return account, session, nil
}

// Logout revokes one session. Revoking twice reports not-found, which tells the
// client its second call changed nothing.
func (s *Service) Logout(ctx context.Context, sessionID string, actor audit.Actor) error {
	if sessionID == "" {
		return &apperr.ValidationError{Field: "session", Code: apperr.FieldRequired, Message: "is required"}
	}
	if err := s.repo.RevokeSession(ctx, sessionID, s.timestamp()); err != nil {
		return fmt.Errorf("logout: %w", err)
	}
	s.record(ctx, actor, actionLogout, "session", sessionID, nil)
	return nil
}

// CreateAccountInput is the data an administrator submits for a new account.
// The client never chooses IDs, status or timestamps.
type CreateAccountInput struct {
	Username    string
	DisplayName string
	Password    string
}

// CreateAccount validates and stores a new active account.
func (s *Service) CreateAccount(ctx context.Context, in CreateAccountInput, actor audit.Actor) (*Account, error) {
	account, err := s.createAccount(ctx, in)
	if err != nil {
		return nil, err
	}
	s.record(ctx, actor, actionAccountCreate, "account", account.ID, map[string]any{
		"username": account.Username,
		"status":   string(account.Status),
	})
	return account, nil
}

// createAccount is the part both CreateAccount and Bootstrap share. It records
// nothing: the caller knows which audit action applies.
func (s *Service) createAccount(ctx context.Context, in CreateAccountInput) (*Account, error) {
	username := NormalizeUsername(in.Username)
	displayName := strings.TrimSpace(in.DisplayName)
	if displayName == "" {
		// An account created from a script has no display name; using the
		// username keeps the field non-empty rather than showing a blank row.
		displayName = username
	}

	var problems apperr.ValidationList
	if err := ValidateUsername(username); err != nil {
		problems = append(problems, apperr.Fields(err)...)
	}
	if err := validateDisplayName(displayName); err != nil {
		problems = append(problems, apperr.Fields(err)...)
	}
	if err := ValidatePassword("password", username, in.Password); err != nil {
		problems = append(problems, apperr.Fields(err)...)
	}
	if err := problems.Err(); err != nil {
		return nil, err
	}

	digest, err := s.hasher.Hash(in.Password)
	if err != nil {
		return nil, err
	}
	now := s.timestamp()
	account := &Account{
		ID:           s.ids.New(),
		Username:     username,
		DisplayName:  displayName,
		PasswordHash: digest,
		Status:       StatusActive,
		// The password was chosen by whoever ran the create command, not by the
		// person who will use the account. The console keeps reminding until
		// they change it.
		PasswordInitial: true,
		CreatedAt:       now,
		UpdatedAt:       now,
	}
	if s.storePlaintext {
		// Kept only so the development login page can show the credential a new
		// operator has to type. Cleared on the first self-service change.
		account.InitialPassword = in.Password
	}
	if err := s.repo.CreateAccount(ctx, account); err != nil {
		if errors.Is(err, apperr.ErrConflict) {
			return nil, &apperr.DetailError{
				Category: apperr.ErrConflict,
				Message:  "that username is already taken",
			}
		}
		return nil, fmt.Errorf("create account: %w", err)
	}
	return account.Clone(), nil
}

// ListAccounts returns one page of accounts, newest first, plus the cursor that
// continues after it.
func (s *Service) ListAccounts(ctx context.Context, q AccountQuery) ([]*Account, string, error) {
	if err := q.Validate(); err != nil {
		return nil, "", fmt.Errorf("list accounts: %w", err)
	}
	limit := q.Limit
	switch {
	case limit <= 0:
		limit = defaultAccountPageSize
	case limit > maxAccountPageSize:
		limit = maxAccountPageSize
	}

	page := q
	page.Limit = limit + 1

	accounts, err := s.repo.ListAccounts(ctx, page)
	if err != nil {
		return nil, "", fmt.Errorf("list accounts: %w", err)
	}
	if len(accounts) <= limit {
		return accounts, "", nil
	}
	accounts = accounts[:limit]
	last := accounts[len(accounts)-1]
	return accounts, cursor.Encode(cursor.Cursor{CreatedAt: last.CreatedAt, ID: last.ID}), nil
}

// GetAccount returns one account by ID.
func (s *Service) GetAccount(ctx context.Context, accountID string) (*Account, error) {
	account, err := s.repo.GetAccount(ctx, accountID)
	if err != nil {
		return nil, fmt.Errorf("get account %q: %w", accountID, err)
	}
	return account, nil
}

// InitialCredential returns an account that still holds its initial password
// and whose plaintext is available.
//
// preferredUsername wins when it matches, which is how the development default
// administrator keeps being shown after the operator adds a second account. With
// no match it answers only when exactly one account qualifies: the login page is
// unauthenticated, and picking one of several credentials would be a guess
// rather than a hint.
func (s *Service) InitialCredential(ctx context.Context, preferredUsername string) (username, password string, ok bool, err error) {
	accounts, _, err := s.ListAccounts(ctx, AccountQuery{Limit: maxAccountPageSize})
	if err != nil {
		return "", "", false, fmt.Errorf("initial credential: %w", err)
	}

	preferred := NormalizeUsername(preferredUsername)
	matches := make([]*Account, 0, 2)
	for _, account := range accounts {
		if !account.PasswordInitial || account.InitialPassword == "" {
			continue
		}
		if preferred != "" && account.Username == preferred {
			return account.Username, account.InitialPassword, true, nil
		}
		matches = append(matches, account)
	}
	if len(matches) != 1 {
		return "", "", false, nil
	}
	return matches[0].Username, matches[0].InitialPassword, true, nil
}

// HasAccounts reports whether any account exists, in any state.
//
// It exists for the console's first-run hint: a login form that always answers
// "wrong password" on an empty database tells a new operator to keep trying
// instead of telling them to create the account.
func (s *Service) HasAccounts(ctx context.Context) (bool, error) {
	count, err := s.repo.CountAccounts(ctx, "")
	if err != nil {
		return false, fmt.Errorf("count accounts: %w", err)
	}
	return count > 0, nil
}

// GetAccountByUsername returns one account by its username.
//
// The username is folded first, so a command-line tool or an import can pass
// whatever casing the operator typed without knowing the storage convention.
func (s *Service) GetAccountByUsername(ctx context.Context, username string) (*Account, error) {
	normalized := NormalizeUsername(username)
	if normalized == "" {
		return nil, &apperr.ValidationError{Field: "username", Code: apperr.FieldRequired, Message: "is required"}
	}
	account, err := s.repo.GetAccountByUsername(ctx, normalized)
	if err != nil {
		return nil, fmt.Errorf("get account %q: %w", normalized, err)
	}
	return account, nil
}

// UpdateAccountInput carries the fields an update may change. A nil pointer
// means "leave it alone", which is what lets PATCH send only what changed.
type UpdateAccountInput struct {
	DisplayName *string
	Status      *AccountStatus
}

// UpdateAccount changes the mutable fields of an account.
//
// Two states are refused because they lock the operator out of their own
// console: disabling the account making the request, and disabling the last
// active administrator. Both are reported as conflicts with a detail sentence,
// so the console can show why the switch flipped back.
func (s *Service) UpdateAccount(ctx context.Context, accountID string, in UpdateAccountInput, actor audit.Actor) (*Account, error) {
	if in.DisplayName == nil && in.Status == nil {
		return nil, &apperr.ValidationError{Field: "body", Code: apperr.FieldRequired, Message: "must name at least one field to change"}
	}

	account, err := s.repo.GetAccount(ctx, accountID)
	if err != nil {
		return nil, fmt.Errorf("update account %q: %w", accountID, err)
	}

	changes := map[string]any{}
	if in.DisplayName != nil {
		displayName := strings.TrimSpace(*in.DisplayName)
		if err := validateDisplayName(displayName); err != nil {
			return nil, err
		}
		if displayName != account.DisplayName {
			changes["displayName"] = displayName
			account.DisplayName = displayName
		}
	}
	if in.Status != nil {
		status := *in.Status
		if !status.Valid() {
			return nil, &apperr.ValidationError{Field: "status", Code: apperr.FieldInvalid, Message: "must be one of " + statusList}
		}
		if status != account.Status {
			if err := s.guardDisable(ctx, account, status, actor); err != nil {
				return nil, err
			}
			changes["status"] = string(status)
			account.Status = status
		}
	}

	if len(changes) == 0 {
		// A PATCH that changes nothing is not an error, but it must not write
		// an updated_at either: the row would claim an edit that never happened.
		return account.Clone(), nil
	}

	account.UpdatedAt = s.timestamp()
	if err := s.repo.UpdateAccount(ctx, account); err != nil {
		return nil, fmt.Errorf("update account %q: %w", accountID, err)
	}
	s.record(ctx, actor, actionAccountUpdate, "account", account.ID, changes)
	return account.Clone(), nil
}

// guardDisable refuses the two ways an operator can lock everyone out.
func (s *Service) guardDisable(ctx context.Context, account *Account, status AccountStatus, actor audit.Actor) error {
	if status != StatusDisabled || !account.Active() {
		return nil
	}
	if account.ID == actor.ID {
		return &apperr.DetailError{
			Category: apperr.ErrConflict,
			Message:  "you cannot disable the account you are signed in with",
		}
	}
	active, err := s.repo.CountAccounts(ctx, StatusActive)
	if err != nil {
		return fmt.Errorf("count active accounts: %w", err)
	}
	if active <= 1 {
		return &apperr.DetailError{
			Category: apperr.ErrConflict,
			Message:  "the last active administrator cannot be disabled",
		}
	}
	return nil
}

// DeleteAccount removes an account and every session it opened.
//
// Deleting the last account is refused even when it is disabled: the console
// would have nobody left to sign in as, and the only way back would be a
// restart with the bootstrap variables set. Deleting oneself is refused for the
// same reason, one step earlier.
func (s *Service) DeleteAccount(ctx context.Context, accountID string, actor audit.Actor) error {
	account, err := s.repo.GetAccount(ctx, accountID)
	if err != nil {
		return fmt.Errorf("delete account %q: %w", accountID, err)
	}
	if account.ID == actor.ID {
		return &apperr.DetailError{
			Category: apperr.ErrConflict,
			Message:  "you cannot delete the account you are signed in with",
		}
	}
	total, err := s.repo.CountAccounts(ctx, "")
	if err != nil {
		return fmt.Errorf("count accounts: %w", err)
	}
	if total <= 1 {
		return &apperr.DetailError{
			Category: apperr.ErrConflict,
			Message:  "the last administrator account cannot be deleted",
		}
	}

	if err := s.repo.DeleteAccount(ctx, accountID); err != nil {
		return fmt.Errorf("delete account %q: %w", accountID, err)
	}
	s.record(ctx, actor, actionAccountDelete, "account", account.ID, map[string]any{"username": account.Username})
	return nil
}

// ChangePasswordInput is a password change or reset.
type ChangePasswordInput struct {
	// CurrentPassword is required when an account changes its own password and
	// ignored when an administrator resets someone else's.
	CurrentPassword string
	NewPassword     string
}

// ChangePassword sets a new password.
//
// Self-service requires the current password, so a stolen session cannot lock
// the owner out. Every session of the target is revoked afterwards — including
// the caller's when they changed their own — because a password change is the
// moment to assume the old credential leaked.
func (s *Service) ChangePassword(ctx context.Context, accountID string, in ChangePasswordInput, actor audit.Actor) error {
	account, err := s.repo.GetAccount(ctx, accountID)
	if err != nil {
		return fmt.Errorf("change password for %q: %w", accountID, err)
	}

	self := account.ID == actor.ID
	if self {
		if in.CurrentPassword == "" {
			return &apperr.ValidationError{Field: "currentPassword", Code: apperr.FieldRequired, Message: "is required"}
		}
		matched, err := s.hasher.Verify(account.PasswordHash, in.CurrentPassword)
		if err != nil {
			s.logger.ErrorContext(ctx, "verify current password", slog.String("account_id", account.ID), slog.Any("error", err))
			return fmt.Errorf("change password for %q: %w", accountID, apperr.ErrUnauthorized)
		}
		if !matched {
			// A wrong current password is a field problem, not a failed
			// session: the client is already authenticated, and a 401 here
			// would make the console throw the user out instead of marking
			// one input red.
			return &apperr.ValidationError{Field: "currentPassword", Code: apperr.FieldInvalid, Message: "is incorrect"}
		}
	}

	if err := ValidatePassword("newPassword", account.Username, in.NewPassword); err != nil {
		return err
	}
	if in.CurrentPassword != "" && in.NewPassword == in.CurrentPassword {
		return &apperr.ValidationError{Field: "newPassword", Code: apperr.FieldInvalid, Message: "must differ from the current password"}
	}

	digest, err := s.hasher.Hash(in.NewPassword)
	if err != nil {
		return err
	}
	now := s.timestamp()
	account.PasswordHash = digest
	// A self-service change clears the reminder and the plaintext copy. An
	// administrator resetting someone else's password arms both again, because
	// the target still has to pick a password only they know.
	account.PasswordInitial = !self
	switch {
	case self:
		account.InitialPassword = ""
	case s.storePlaintext:
		account.InitialPassword = in.NewPassword
	default:
		account.InitialPassword = ""
	}
	account.UpdatedAt = now
	if err := s.repo.UpdateAccount(ctx, account); err != nil {
		return fmt.Errorf("change password for %q: %w", accountID, err)
	}
	if err := s.repo.RevokeAccountSessions(ctx, account.ID, now); err != nil {
		return fmt.Errorf("revoke sessions for %q: %w", accountID, err)
	}

	s.record(ctx, actor, actionPasswordChange, "account", account.ID, map[string]any{
		"username": account.Username,
		"self":     self,
	})
	return nil
}

// ListSessions returns the live sessions of an account, newest first.
func (s *Service) ListSessions(ctx context.Context, accountID string) ([]*Session, error) {
	if _, err := s.repo.GetAccount(ctx, accountID); err != nil {
		return nil, fmt.Errorf("list sessions for %q: %w", accountID, err)
	}
	sessions, err := s.repo.ListSessions(ctx, accountID, s.timestamp())
	if err != nil {
		return nil, fmt.Errorf("list sessions for %q: %w", accountID, err)
	}
	if len(sessions) > maxSessionsListed {
		sessions = sessions[:maxSessionsListed]
	}
	return sessions, nil
}

// RevokeSession signs one session out on behalf of an administrator.
//
// A session that belongs to another account is reported as missing rather than
// forbidden: the ID is a random string, and telling a caller it exists would
// confirm a guess it cannot use.
func (s *Service) RevokeSession(ctx context.Context, accountID, sessionID string, actor audit.Actor) error {
	session, err := s.repo.GetSession(ctx, sessionID)
	if err != nil {
		return fmt.Errorf("revoke session %q: %w", sessionID, err)
	}
	if session.AccountID != accountID {
		return fmt.Errorf("revoke session %q: %w", sessionID, apperr.ErrNotFound)
	}
	if err := s.repo.RevokeSession(ctx, sessionID, s.timestamp()); err != nil {
		return fmt.Errorf("revoke session %q: %w", sessionID, err)
	}
	s.record(ctx, actor, actionSessionRevoke, "session", sessionID, map[string]any{"accountId": accountID})
	return nil
}

// Bootstrap creates the first administrator account if the table is empty.
//
// It is idempotent by construction: once any account exists it does nothing and
// says so. That is what makes it safe to leave the bootstrap variables set
// across restarts — and why they must be removed afterwards, since a leftover
// password in the environment is a credential.
func (s *Service) Bootstrap(ctx context.Context, username, displayName, password string) (bool, error) {
	count, err := s.repo.CountAccounts(ctx, "")
	if err != nil {
		return false, fmt.Errorf("bootstrap: %w", err)
	}
	if count > 0 {
		return false, nil
	}

	account, err := s.createAccount(ctx, CreateAccountInput{
		Username:    username,
		DisplayName: displayName,
		Password:    password,
	})
	if err != nil {
		return false, fmt.Errorf("bootstrap: %w", err)
	}
	s.record(ctx, audit.Actor{Name: "system"}, actionBootstrap, "account", account.ID, map[string]any{
		"username": account.Username,
	})
	s.logger.InfoContext(ctx, "bootstrapped the first administrator account",
		slog.String("account_id", account.ID),
		slog.String("username", account.Username),
	)
	return true, nil
}

// HasAuditTrail reports whether the console can read what was recorded. The
// audit endpoint is mounted on this rather than on the recorder: a deployment
// that writes a trail but does not expose it should not advertise a route that
// answers 501.
func (s *Service) HasAuditTrail() bool { return s.auditStore != nil }

// ListAuditLogs returns one page of the trail, newest first.
func (s *Service) ListAuditLogs(ctx context.Context, q audit.Query) ([]*audit.Entry, string, error) {
	if s.auditStore == nil {
		return nil, "", apperr.ErrUnavailable
	}
	entries, next, err := s.auditStore.List(ctx, q)
	if err != nil {
		return nil, "", fmt.Errorf("list audit log: %w", err)
	}
	return entries, next, nil
}

// DeleteAuditLogs removes entries from the trail and reports how many went.
//
// The trail is append-only in normal operation, so this is not a retention
// tool: it exists for rows that should never have been written (a mis-click
// recorded under the wrong identity, a probe left behind by a smoke test). The
// removal is recorded as its own entry, so shortening the trail still leaves a
// trace of who shortened it.
func (s *Service) DeleteAuditLogs(ctx context.Context, ids []string, actor audit.Actor) (int, error) {
	if s.auditStore == nil {
		return 0, apperr.ErrUnavailable
	}

	unique := make([]string, 0, len(ids))
	seen := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		if _, duplicate := seen[id]; duplicate {
			continue
		}
		seen[id] = struct{}{}
		unique = append(unique, id)
	}
	if len(unique) == 0 {
		return 0, &apperr.ValidationError{Field: "ids", Code: apperr.FieldRequired, Message: "is required"}
	}
	if len(unique) > maxAuditDeleteIDs {
		return 0, &apperr.ValidationError{
			Field:   "ids",
			Code:    apperr.FieldOutOfRange,
			Message: fmt.Sprintf("must hold at most %d identifiers", maxAuditDeleteIDs),
		}
	}

	removed, err := s.auditStore.Delete(ctx, unique)
	if err != nil {
		return 0, fmt.Errorf("delete audit entries: %w", err)
	}
	s.record(ctx, actor, actionAuditDelete, "audit_log", "", map[string]any{
		"requested": len(unique),
		"affected":  removed,
	})
	return removed, nil
}

// PruneExpiredSessions deletes sessions that expired before now, so the table
// does not grow by one row per sign-in forever. It runs at startup, like the
// idempotency key prune: a pass per deploy keeps it bounded without a
// background goroutine to shut down.
func (s *Service) PruneExpiredSessions(ctx context.Context) (int64, error) {
	pruned, err := s.repo.DeleteExpiredSessions(ctx, s.timestamp())
	if err != nil {
		return 0, fmt.Errorf("prune admin sessions: %w", err)
	}
	return pruned, nil
}

// record appends to the audit trail, logging rather than failing when the trail
// cannot be written. See the package comment for why.
func (s *Service) record(ctx context.Context, actor audit.Actor, action, resource, resourceID string, detail map[string]any) {
	if s.auditor == nil {
		return
	}
	event := audit.Event{
		Actor:      actor,
		Action:     action,
		Resource:   resource,
		ResourceID: resourceID,
		Detail:     detail,
		At:         s.timestamp(),
	}
	if err := s.auditor.Record(ctx, event); err != nil {
		s.logger.WarnContext(ctx, "record audit event",
			slog.String("action", action),
			slog.String("resource_id", resourceID),
			slog.Any("error", err),
		)
	}
}

// timestamp is the clock reading trimmed to the precision storage keeps.
//
// timestamptz holds microseconds, so a value with nanoseconds looks the same
// until it is read back — and then it does not. Trimming here keeps the
// expiresAt of a login response identical to the one a later session read
// reports, which is the comparison a client makes when it refreshes a page.
func (s *Service) timestamp() time.Time {
	return s.now().UTC().Truncate(time.Microsecond)
}

// SessionTTL returns the configured session lifetime, for diagnostics.
func (s *Service) SessionTTL() time.Duration { return s.sessionTTL }
