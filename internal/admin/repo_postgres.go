package admin

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/liomix/astro-liomix-server/internal/platform/apperr"
	"github.com/liomix/astro-liomix-server/internal/platform/postgres"
)

// PostgresRepository stores accounts and sessions in PostgreSQL.
//
// Sessions live in a table rather than in signed tokens so that signing out and
// disabling an account take effect on the next request. A self-contained token
// can only be revoked by keeping a list of the revoked ones, which is a session
// table with extra steps and no way to see who is signed in.
type PostgresRepository struct {
	db postgres.Executor
}

var _ Repository = (*PostgresRepository)(nil)

// NewPostgresRepository returns a repository that runs its statements on db,
// which is either the pool owned by cmd/server or — in tests — a transaction
// that is rolled back afterwards. The caller owns it and closes it on shutdown.
func NewPostgresRepository(db postgres.Executor) *PostgresRepository {
	return &PostgresRepository{db: db}
}

// Projections shared by every read, in the order the scanners expect.
const (
	accountColumns = `id, username, display_name, password_hash, initial_password, status, password_is_initial, created_at, updated_at, last_login_at`
	sessionColumns = `id, account_id, token_hash, created_at, last_seen_at, expires_at, revoked_at, ip, user_agent`
)

// rowScanner is the part of *sql.Row and *sql.Rows that the scanners need.
type rowScanner interface {
	Scan(dest ...any) error
}

// scanAccount reads one row of accountColumns.
func scanAccount(row rowScanner) (*Account, error) {
	a := &Account{}
	// last_login_at is NULL for an account that has never signed in. The
	// domain represents that as the zero time; NULL is the only place the
	// difference is visible, and it is the right place.
	var (
		lastLogin       sql.NullTime
		initialPassword sql.NullString
	)
	if err := row.Scan(
		&a.ID,
		&a.Username,
		&a.DisplayName,
		&a.PasswordHash,
		&initialPassword,
		&a.Status,
		&a.PasswordInitial,
		&a.CreatedAt,
		&a.UpdatedAt,
		&lastLogin,
	); err != nil {
		return nil, err
	}
	a.InitialPassword = initialPassword.String
	if lastLogin.Valid {
		a.LastLoginAt = lastLogin.Time.UTC()
	}
	// timestamptz comes back in the connection's zone. The API contract is
	// UTC, so normalise at the boundary rather than in every handler.
	a.CreatedAt = a.CreatedAt.UTC()
	a.UpdatedAt = a.UpdatedAt.UTC()
	return a, nil
}

// scanSession reads one row of sessionColumns.
func scanSession(row rowScanner) (*Session, error) {
	s := &Session{}
	var revokedAt sql.NullTime
	if err := row.Scan(
		&s.ID,
		&s.AccountID,
		&s.TokenHash,
		&s.CreatedAt,
		&s.LastSeenAt,
		&s.ExpiresAt,
		&revokedAt,
		&s.IP,
		&s.UserAgent,
	); err != nil {
		return nil, err
	}
	if revokedAt.Valid {
		s.RevokedAt = revokedAt.Time.UTC()
	}
	s.CreatedAt = s.CreatedAt.UTC()
	s.LastSeenAt = s.LastSeenAt.UTC()
	s.ExpiresAt = s.ExpiresAt.UTC()
	return s, nil
}

// CreateAccount stores a new account.
func (r *PostgresRepository) CreateAccount(ctx context.Context, a *Account) error {
	if a == nil {
		return &apperr.ValidationError{Field: "account", Message: "must not be nil"}
	}
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO admin_accounts (`+accountColumns+`)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`,
		a.ID,
		a.Username,
		a.DisplayName,
		a.PasswordHash,
		nullString(a.InitialPassword),
		string(a.Status),
		a.PasswordInitial,
		a.CreatedAt.UTC(),
		a.UpdatedAt.UTC(),
		nullTime(a.LastLoginAt),
	)
	if err != nil {
		if postgres.IsUniqueViolation(err) {
			return fmt.Errorf("create account %q: %w", a.Username, apperr.ErrConflict)
		}
		return fmt.Errorf("create account %q: %w", a.Username, err)
	}
	return nil
}

// GetAccount returns the account with the given ID.
func (r *PostgresRepository) GetAccount(ctx context.Context, accountID string) (*Account, error) {
	row := r.db.QueryRowContext(ctx,
		`SELECT `+accountColumns+` FROM admin_accounts WHERE id = $1`, accountID)
	a, err := scanAccount(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("get account %q: %w", accountID, apperr.ErrNotFound)
	}
	if err != nil {
		return nil, fmt.Errorf("get account %q: %w", accountID, err)
	}
	return a, nil
}

// GetAccountByUsername returns the account with the given normalized username.
func (r *PostgresRepository) GetAccountByUsername(ctx context.Context, username string) (*Account, error) {
	row := r.db.QueryRowContext(ctx,
		`SELECT `+accountColumns+` FROM admin_accounts WHERE username = $1`, username)
	a, err := scanAccount(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("get account %q: %w", username, apperr.ErrNotFound)
	}
	if err != nil {
		return nil, fmt.Errorf("get account %q: %w", username, err)
	}
	return a, nil
}

// ListAccounts returns the accounts matching q, newest first.
func (r *PostgresRepository) ListAccounts(ctx context.Context, q AccountQuery) ([]*Account, error) {
	args := make([]any, 0, 3)
	where := " WHERE true"
	if q.Status != "" {
		args = append(args, string(q.Status))
		where += fmt.Sprintf(" AND status = $%d", len(args))
	}
	if q.Cursor != nil {
		args = append(args, q.Cursor.CreatedAt.UTC(), q.Cursor.ID)
		where += fmt.Sprintf(" AND (created_at, id) < ($%d, $%d)", len(args)-1, len(args))
	}

	query := `SELECT ` + accountColumns + ` FROM admin_accounts` + where +
		` ORDER BY created_at DESC, id DESC`
	// Limit is a bound the service already validated, so it is interpolated as
	// an integer; every user-supplied value above still travels as a parameter.
	if q.Limit > 0 {
		query += fmt.Sprintf(" LIMIT %d", q.Limit)
	}

	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list accounts: %w", err)
	}
	defer rows.Close()

	accounts := make([]*Account, 0, 16)
	for rows.Next() {
		a, err := scanAccount(rows)
		if err != nil {
			return nil, fmt.Errorf("list accounts: %w", err)
		}
		accounts = append(accounts, a)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list accounts: %w", err)
	}
	return accounts, nil
}

// UpdateAccount stores every mutable field of a.
func (r *PostgresRepository) UpdateAccount(ctx context.Context, a *Account) error {
	if a == nil {
		return &apperr.ValidationError{Field: "account", Message: "must not be nil"}
	}
	result, err := r.db.ExecContext(ctx, `
		UPDATE admin_accounts
		SET username = $2, display_name = $3, password_hash = $4,
		    initial_password = $5, status = $6, updated_at = $7,
		    last_login_at = $8, password_is_initial = $9
		WHERE id = $1`,
		a.ID,
		a.Username,
		a.DisplayName,
		a.PasswordHash,
		nullString(a.InitialPassword),
		string(a.Status),
		a.UpdatedAt.UTC(),
		nullTime(a.LastLoginAt),
		a.PasswordInitial,
	)
	if err != nil {
		if postgres.IsUniqueViolation(err) {
			return fmt.Errorf("update account %q: %w", a.ID, apperr.ErrConflict)
		}
		return fmt.Errorf("update account %q: %w", a.ID, err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("update account %q: %w", a.ID, err)
	}
	if affected == 0 {
		return fmt.Errorf("update account %q: %w", a.ID, apperr.ErrNotFound)
	}
	return nil
}

// UpdateLastLogin records a successful sign-in.
func (r *PostgresRepository) UpdateLastLogin(ctx context.Context, accountID string, at time.Time) error {
	result, err := r.db.ExecContext(ctx,
		`UPDATE admin_accounts SET last_login_at = $2 WHERE id = $1`, accountID, at.UTC())
	if err != nil {
		return fmt.Errorf("record last login for %q: %w", accountID, err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("record last login for %q: %w", accountID, err)
	}
	if affected == 0 {
		return fmt.Errorf("record last login for %q: %w", accountID, apperr.ErrNotFound)
	}
	return nil
}

// DeleteAccount removes an account. Sessions follow through ON DELETE CASCADE.
func (r *PostgresRepository) DeleteAccount(ctx context.Context, accountID string) error {
	result, err := r.db.ExecContext(ctx, `DELETE FROM admin_accounts WHERE id = $1`, accountID)
	if err != nil {
		return fmt.Errorf("delete account %q: %w", accountID, err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("delete account %q: %w", accountID, err)
	}
	if affected == 0 {
		return fmt.Errorf("delete account %q: %w", accountID, apperr.ErrNotFound)
	}
	return nil
}

// CountAccounts reports how many accounts exist in the given state.
func (r *PostgresRepository) CountAccounts(ctx context.Context, status AccountStatus) (int, error) {
	query := `SELECT count(*) FROM admin_accounts`
	args := make([]any, 0, 1)
	if status != "" {
		query += ` WHERE status = $1`
		args = append(args, string(status))
	}
	var count int
	if err := r.db.QueryRowContext(ctx, query, args...).Scan(&count); err != nil {
		return 0, fmt.Errorf("count accounts: %w", err)
	}
	return count, nil
}

// CreateSession stores a new session.
func (r *PostgresRepository) CreateSession(ctx context.Context, s *Session) error {
	if s == nil {
		return &apperr.ValidationError{Field: "session", Message: "must not be nil"}
	}
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO admin_sessions (`+sessionColumns+`)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`,
		s.ID,
		s.AccountID,
		s.TokenHash,
		s.CreatedAt.UTC(),
		s.LastSeenAt.UTC(),
		s.ExpiresAt.UTC(),
		nullTime(s.RevokedAt),
		s.IP,
		s.UserAgent,
	)
	if err != nil {
		if postgres.IsUniqueViolation(err) {
			return fmt.Errorf("create session: %w", apperr.ErrConflict)
		}
		return fmt.Errorf("create session: %w", err)
	}
	return nil
}

// GetSessionByTokenHash returns the session a presented token belongs to.
func (r *PostgresRepository) GetSessionByTokenHash(ctx context.Context, tokenHash string) (*Session, error) {
	row := r.db.QueryRowContext(ctx,
		`SELECT `+sessionColumns+` FROM admin_sessions WHERE token_hash = $1`, tokenHash)
	s, err := scanSession(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("get session: %w", apperr.ErrNotFound)
	}
	if err != nil {
		return nil, fmt.Errorf("get session: %w", err)
	}
	return s, nil
}

// GetSession returns one session by ID.
func (r *PostgresRepository) GetSession(ctx context.Context, sessionID string) (*Session, error) {
	row := r.db.QueryRowContext(ctx,
		`SELECT `+sessionColumns+` FROM admin_sessions WHERE id = $1`, sessionID)
	s, err := scanSession(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("get session %q: %w", sessionID, apperr.ErrNotFound)
	}
	if err != nil {
		return nil, fmt.Errorf("get session %q: %w", sessionID, err)
	}
	return s, nil
}

// TouchSession records that the session was used.
func (r *PostgresRepository) TouchSession(ctx context.Context, sessionID string, at time.Time) error {
	result, err := r.db.ExecContext(ctx,
		`UPDATE admin_sessions SET last_seen_at = $2 WHERE id = $1 AND revoked_at IS NULL`,
		sessionID, at.UTC())
	if err != nil {
		return fmt.Errorf("touch session %q: %w", sessionID, err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("touch session %q: %w", sessionID, err)
	}
	if affected == 0 {
		return fmt.Errorf("touch session %q: %w", sessionID, apperr.ErrNotFound)
	}
	return nil
}

// RevokeSession marks one session as signed out.
func (r *PostgresRepository) RevokeSession(ctx context.Context, sessionID string, at time.Time) error {
	result, err := r.db.ExecContext(ctx,
		`UPDATE admin_sessions SET revoked_at = $2 WHERE id = $1 AND revoked_at IS NULL`,
		sessionID, at.UTC())
	if err != nil {
		return fmt.Errorf("revoke session %q: %w", sessionID, err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("revoke session %q: %w", sessionID, err)
	}
	if affected == 0 {
		return fmt.Errorf("revoke session %q: %w", sessionID, apperr.ErrNotFound)
	}
	return nil
}

// RevokeAccountSessions marks every live session of an account as signed out.
func (r *PostgresRepository) RevokeAccountSessions(ctx context.Context, accountID string, at time.Time) error {
	if _, err := r.db.ExecContext(ctx,
		`UPDATE admin_sessions SET revoked_at = $2 WHERE account_id = $1 AND revoked_at IS NULL`,
		accountID, at.UTC()); err != nil {
		return fmt.Errorf("revoke sessions for %q: %w", accountID, err)
	}
	return nil
}

// ListSessions returns the live sessions of an account at the given time.
func (r *PostgresRepository) ListSessions(ctx context.Context, accountID string, at time.Time) ([]*Session, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT `+sessionColumns+` FROM admin_sessions
		WHERE account_id = $1 AND revoked_at IS NULL AND expires_at > $2
		ORDER BY created_at DESC, id DESC`,
		accountID, at.UTC())
	if err != nil {
		return nil, fmt.Errorf("list sessions for %q: %w", accountID, err)
	}
	defer rows.Close()

	sessions := make([]*Session, 0, 8)
	for rows.Next() {
		s, err := scanSession(rows)
		if err != nil {
			return nil, fmt.Errorf("list sessions for %q: %w", accountID, err)
		}
		sessions = append(sessions, s)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list sessions for %q: %w", accountID, err)
	}
	return sessions, nil
}

// DeleteExpiredSessions removes sessions that expired before the given time.
func (r *PostgresRepository) DeleteExpiredSessions(ctx context.Context, before time.Time) (int64, error) {
	result, err := r.db.ExecContext(ctx,
		`DELETE FROM admin_sessions WHERE expires_at < $1`, before.UTC())
	if err != nil {
		return 0, fmt.Errorf("prune admin sessions: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("prune admin sessions: %w", err)
	}
	return affected, nil
}

// nullTime maps the domain's zero time to SQL NULL. "Never signed in" and
// "not revoked" are absences, not 1970 and not the epoch.
func nullTime(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t.UTC()
}

// nullString maps the domain's empty string to SQL NULL. "No plaintext kept"
// is an absence, not a zero-length password.
func nullString(value string) any {
	if value == "" {
		return nil
	}
	return value
}
