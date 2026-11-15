package admin_test

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/liomix/astro-liomix-server/internal/admin"
	"github.com/liomix/astro-liomix-server/internal/platform/apperr"
	"github.com/liomix/astro-liomix-server/internal/platform/cursor"
	"github.com/liomix/astro-liomix-server/internal/platform/postgres/postgrestest"
)

// newPostgresRepository returns a repository backed by the development database.
//
// The statements run inside a transaction that is rolled back when the case
// ends, so the suite exercises the real PostgreSQL instance — constraints,
// cascade and all — without deleting a row the running server owns.
func newPostgresRepository(t *testing.T) *admin.PostgresRepository {
	t.Helper()

	db := postgrestest.Open(t)
	postgrestest.Migrate(t, db, admin.Migrate)

	tx := postgrestest.Tx(t, db)
	// TRUNCATE is transactional in PostgreSQL, so this empties the table for
	// the assertions and still restores it when the case rolls back.
	if _, err := tx.ExecContext(context.Background(), `TRUNCATE admin_accounts CASCADE`); err != nil {
		t.Fatalf("start from empty admin tables: %v", err)
	}
	return admin.NewPostgresRepository(tx)
}

// digestOfSHA256 returns a value shaped like a stored session token digest.
func digestOfSHA256(seed string) string {
	sum := sha256.Sum256([]byte(seed))
	return hex.EncodeToString(sum[:])
}

// testPasswordHash is a syntactically valid digest. The repository never checks
// a password, and the column constraint only cares about the shape.
func testPasswordHash() string {
	salt := base64.RawStdEncoding.EncodeToString(make([]byte, 16))
	key := base64.RawStdEncoding.EncodeToString(make([]byte, 32))
	return "pbkdf2-sha256$1000$" + salt + "$" + key
}

func storedAccount(id, username string, status admin.AccountStatus, at time.Time) *admin.Account {
	return &admin.Account{
		ID:           id,
		Username:     username,
		DisplayName:  username,
		PasswordHash: testPasswordHash(),
		Status:       status,
		CreatedAt:    at.UTC(),
		UpdatedAt:    at.UTC(),
	}
}

func storedSession(id, accountID, token string, at time.Time) *admin.Session {
	return &admin.Session{
		ID:         id,
		AccountID:  accountID,
		TokenHash:  digestOfSHA256(token),
		CreatedAt:  at.UTC(),
		LastSeenAt: at.UTC(),
		ExpiresAt:  at.Add(time.Hour).UTC(),
		IP:         "127.0.0.1",
		UserAgent:  "go-test",
	}
}

func TestPostgresAccountRoundTrip(t *testing.T) {
	repo := newPostgresRepository(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)

	account := storedAccount("a-1", "ada", admin.StatusActive, now)
	if err := repo.CreateAccount(ctx, account); err != nil {
		t.Fatalf("CreateAccount() error = %v", err)
	}

	got, err := repo.GetAccount(ctx, "a-1")
	if err != nil {
		t.Fatalf("GetAccount() error = %v", err)
	}
	if got.Username != "ada" || got.Status != admin.StatusActive {
		t.Errorf("account = %+v, want the stored values", got)
	}
	if !got.LastLoginAt.IsZero() {
		t.Error("lastLoginAt is not the zero time for an account that never signed in")
	}
	if !got.CreatedAt.Equal(now) {
		t.Errorf("createdAt = %v, want %v", got.CreatedAt, now)
	}

	byName, err := repo.GetAccountByUsername(ctx, "ada")
	if err != nil {
		t.Fatalf("GetAccountByUsername() error = %v", err)
	}
	if byName.ID != account.ID {
		t.Errorf("account = %q, want %q", byName.ID, account.ID)
	}

	if _, err := repo.GetAccount(ctx, "missing"); !errors.Is(err, apperr.ErrNotFound) {
		t.Errorf("GetAccount(missing) error = %v, want ErrNotFound", err)
	}
	if _, err := repo.GetAccountByUsername(ctx, "missing"); !errors.Is(err, apperr.ErrNotFound) {
		t.Errorf("GetAccountByUsername(missing) error = %v, want ErrNotFound", err)
	}
}

func TestPostgresAccountUniqueUsername(t *testing.T) {
	repo := newPostgresRepository(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)

	if err := repo.CreateAccount(ctx, storedAccount("a-1", "ada", admin.StatusActive, now)); err != nil {
		t.Fatalf("CreateAccount() error = %v", err)
	}
	// A different id, same username: the unique index must answer, not the
	// service's pre-check (which a second replica could not rely on anyway).
	err := repo.CreateAccount(ctx, storedAccount("a-2", "ada", admin.StatusActive, now))
	if !errors.Is(err, apperr.ErrConflict) {
		t.Errorf("duplicate CreateAccount() error = %v, want ErrConflict", err)
	}
}

func TestPostgresAccountUsernameShape(t *testing.T) {
	// A separate transaction: the failed insert above leaves PostgreSQL's
	// transaction in the aborted state, where every later statement returns
	// 25P02 instead of the error being tested. One constraint violation per
	// case is the rule this package already follows.
	repo := newPostgresRepository(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)

	invalid := storedAccount("a-3", "Ada", admin.StatusActive, now)
	if err := repo.CreateAccount(ctx, invalid); err == nil {
		t.Error("CreateAccount() accepted an uppercase username; the shape check did not run")
	}
}

func TestPostgresAccountListingAndCount(t *testing.T) {
	repo := newPostgresRepository(t)
	ctx := context.Background()
	base := time.Now().UTC().Truncate(time.Microsecond)

	names := []string{"ada", "grace", "linus"}
	for i, name := range names {
		account := storedAccount(fmt.Sprintf("a-%d", i), name, admin.StatusActive, base.Add(time.Duration(i)*time.Second))
		if err := repo.CreateAccount(ctx, account); err != nil {
			t.Fatalf("CreateAccount(%s) error = %v", name, err)
		}
	}

	page, err := repo.ListAccounts(ctx, admin.AccountQuery{Limit: 2})
	if err != nil {
		t.Fatalf("ListAccounts() error = %v", err)
	}
	if len(page) != 2 {
		t.Fatalf("page = %d accounts, want 2", len(page))
	}
	if page[0].Username != "linus" {
		t.Errorf("first account = %q, want the newest (linus)", page[0].Username)
	}

	position := cursor.Cursor{CreatedAt: page[1].CreatedAt, ID: page[1].ID}
	next, err := repo.ListAccounts(ctx, admin.AccountQuery{Limit: 2, Cursor: &position})
	if err != nil {
		t.Fatalf("ListAccounts(page 2) error = %v", err)
	}
	if len(next) != 1 || next[0].Username != "ada" {
		t.Errorf("page 2 = %+v, want the single oldest account", next)
	}

	if count, err := repo.CountAccounts(ctx, ""); err != nil || count != 3 {
		t.Errorf("CountAccounts(all) = %d, %v; want 3, nil", count, err)
	}
	if count, err := repo.CountAccounts(ctx, admin.StatusDisabled); err != nil || count != 0 {
		t.Errorf("CountAccounts(disabled) = %d, %v; want 0, nil", count, err)
	}
}

func TestPostgresAccountUpdateAndDelete(t *testing.T) {
	repo := newPostgresRepository(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)

	ada := storedAccount("a-1", "ada", admin.StatusActive, now)
	if err := repo.CreateAccount(ctx, ada); err != nil {
		t.Fatalf("CreateAccount() error = %v", err)
	}
	grace := storedAccount("a-2", "grace", admin.StatusActive, now)
	if err := repo.CreateAccount(ctx, grace); err != nil {
		t.Fatalf("CreateAccount() error = %v", err)
	}

	ada.DisplayName = "Ada L."
	ada.Status = admin.StatusDisabled
	ada.UpdatedAt = now.Add(time.Minute)
	if err := repo.UpdateAccount(ctx, ada); err != nil {
		t.Fatalf("UpdateAccount() error = %v", err)
	}
	updated, err := repo.GetAccount(ctx, ada.ID)
	if err != nil {
		t.Fatalf("GetAccount() error = %v", err)
	}
	if updated.DisplayName != "Ada L." || updated.Status != admin.StatusDisabled {
		t.Errorf("account = %+v, want the updated values", updated)
	}

	if err := repo.UpdateLastLogin(ctx, grace.ID, now.Add(2*time.Minute)); err != nil {
		t.Fatalf("UpdateLastLogin() error = %v", err)
	}
	graceReloaded, err := repo.GetAccount(ctx, grace.ID)
	if err != nil {
		t.Fatalf("GetAccount() error = %v", err)
	}
	if graceReloaded.LastLoginAt.IsZero() {
		t.Error("lastLoginAt was not stored")
	}

	// Sessions follow the account through ON DELETE CASCADE, without the
	// repository issuing a second statement.
	if err := repo.CreateSession(ctx, storedSession("s-1", grace.ID, "token-1", now)); err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}
	if err := repo.DeleteAccount(ctx, grace.ID); err != nil {
		t.Fatalf("DeleteAccount() error = %v", err)
	}
	if _, err := repo.GetSession(ctx, "s-1"); !errors.Is(err, apperr.ErrNotFound) {
		t.Errorf("session survived the account delete: %v", err)
	}

	if err := repo.DeleteAccount(ctx, grace.ID); !errors.Is(err, apperr.ErrNotFound) {
		t.Errorf("second DeleteAccount() error = %v, want ErrNotFound", err)
	}

	missing := storedAccount("a-9", "nobody", admin.StatusActive, now)
	if err := repo.UpdateAccount(ctx, missing); !errors.Is(err, apperr.ErrNotFound) {
		t.Errorf("UpdateAccount(missing) error = %v, want ErrNotFound", err)
	}
}

func TestPostgresSessionTokenUnique(t *testing.T) {
	// Its own transaction: the unique violation aborts the transaction, so it
	// cannot share one with the lifecycle assertions.
	repo := newPostgresRepository(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)

	ada := storedAccount("a-1", "ada", admin.StatusActive, now)
	if err := repo.CreateAccount(ctx, ada); err != nil {
		t.Fatalf("CreateAccount() error = %v", err)
	}
	if err := repo.CreateSession(ctx, storedSession("s-1", ada.ID, "token-1", now)); err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}
	err := repo.CreateSession(ctx, storedSession("s-2", ada.ID, "token-1", now))
	if !errors.Is(err, apperr.ErrConflict) {
		t.Errorf("duplicate token hash error = %v, want ErrConflict", err)
	}
}

func TestPostgresSessionLifecycle(t *testing.T) {
	repo := newPostgresRepository(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)

	ada := storedAccount("a-1", "ada", admin.StatusActive, now)
	if err := repo.CreateAccount(ctx, ada); err != nil {
		t.Fatalf("CreateAccount() error = %v", err)
	}

	first := storedSession("s-1", ada.ID, "token-1", now)
	second := storedSession("s-2", ada.ID, "token-2", now.Add(time.Second))
	for _, s := range []*admin.Session{first, second} {
		if err := repo.CreateSession(ctx, s); err != nil {
			t.Fatalf("CreateSession(%s) error = %v", s.ID, err)
		}
	}

	found, err := repo.GetSessionByTokenHash(ctx, digestOfSHA256("token-1"))
	if err != nil {
		t.Fatalf("GetSessionByTokenHash() error = %v", err)
	}
	if found.ID != first.ID {
		t.Errorf("session = %q, want %q", found.ID, first.ID)
	}

	if err := repo.TouchSession(ctx, first.ID, now.Add(time.Minute)); err != nil {
		t.Fatalf("TouchSession() error = %v", err)
	}
	touched, err := repo.GetSession(ctx, first.ID)
	if err != nil {
		t.Fatalf("GetSession() error = %v", err)
	}
	if !touched.LastSeenAt.After(now) {
		t.Error("lastSeenAt did not move forward")
	}

	live, err := repo.ListSessions(ctx, ada.ID, now.Add(2*time.Minute))
	if err != nil {
		t.Fatalf("ListSessions() error = %v", err)
	}
	if len(live) != 2 {
		t.Fatalf("live sessions = %d, want 2", len(live))
	}
	if live[0].ID != second.ID {
		t.Errorf("first listed session = %q, want the newest (%q)", live[0].ID, second.ID)
	}

	if err := repo.RevokeSession(ctx, first.ID, now.Add(3*time.Minute)); err != nil {
		t.Fatalf("RevokeSession() error = %v", err)
	}
	if err := repo.RevokeSession(ctx, first.ID, now.Add(3*time.Minute)); !errors.Is(err, apperr.ErrNotFound) {
		t.Errorf("second RevokeSession() error = %v, want ErrNotFound", err)
	}
	live, err = repo.ListSessions(ctx, ada.ID, now.Add(3*time.Minute))
	if err != nil {
		t.Fatalf("ListSessions() error = %v", err)
	}
	if len(live) != 1 {
		t.Errorf("live sessions after revoking one = %d, want 1", len(live))
	}

	if err := repo.RevokeAccountSessions(ctx, ada.ID, now.Add(4*time.Minute)); err != nil {
		t.Fatalf("RevokeAccountSessions() error = %v", err)
	}
	live, err = repo.ListSessions(ctx, ada.ID, now.Add(4*time.Minute))
	if err != nil {
		t.Fatalf("ListSessions() error = %v", err)
	}
	if len(live) != 0 {
		t.Errorf("live sessions after revoking all = %d, want 0", len(live))
	}

	// Expired sessions disappear at the prune, including the revoked rows kept
	// until then.
	expired := storedSession("s-9", ada.ID, "token-9", now.Add(-2*time.Hour))
	if err := repo.CreateSession(ctx, expired); err != nil {
		t.Fatalf("CreateSession(expired) error = %v", err)
	}
	pruned, err := repo.DeleteExpiredSessions(ctx, now)
	if err != nil {
		t.Fatalf("DeleteExpiredSessions() error = %v", err)
	}
	if pruned != 1 {
		t.Errorf("pruned = %d, want 1", pruned)
	}
	if _, err := repo.GetSessionByTokenHash(ctx, digestOfSHA256("token-1")); err != nil {
		t.Errorf("prune removed a session that has not expired: %v", err)
	}
}
