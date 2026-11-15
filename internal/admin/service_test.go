package admin

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/liomix/astro-liomix-server/internal/platform/apperr"
	"github.com/liomix/astro-liomix-server/internal/platform/audit"
	"github.com/liomix/astro-liomix-server/internal/platform/cursor"
)

// testPassword satisfies the policy without being guessable.
const testPassword = "correct-horse-battery"

// testEnv is the service wired to in-memory ports and a clock a test can move.
type testEnv struct {
	svc   *Service
	repo  *memoryRepository
	trail *memoryAudit
	clock *fakeClock
}

func newTestEnv(t *testing.T) *testEnv {
	t.Helper()

	env := &testEnv{
		repo:  newMemoryRepository(),
		trail: newMemoryAudit(),
		clock: newFakeClock(),
	}
	svc, err := NewService(env.repo,
		WithLogger(discardLogger()),
		WithClock(env.clock.Now),
		WithIDGenerator(&seqIDs{}),
		WithAuditor(env.trail),
		WithAuditReader(env.trail),
		WithPasswordCost(1000),
		WithSessionTTL(time.Hour),
	)
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}
	env.svc = svc
	return env
}

// systemActor is the identity a test uses when it acts as the operator without
// signing in: bootstraps, fixtures and setup.
func systemActor() audit.Actor { return audit.Actor{Name: "test"} }

// mustCreate adds an active account and fails the test if the service refuses.
func (e *testEnv) mustCreate(t *testing.T, username string) *Account {
	t.Helper()

	account, err := e.svc.CreateAccount(context.Background(), CreateAccountInput{
		Username:    username,
		DisplayName: strings.ToUpper(username),
		Password:    testPassword,
	}, systemActor())
	if err != nil {
		t.Fatalf("CreateAccount(%q) error = %v", username, err)
	}
	return account
}

// actorOf builds the audit identity of an account.
func actorOf(a *Account) audit.Actor {
	return audit.Actor{ID: a.ID, Name: a.Username}
}

func TestBootstrapCreatesOnlyOnce(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	env := newTestEnv(t)

	created, err := env.svc.Bootstrap(ctx, "owner", "Owner", testPassword)
	if err != nil {
		t.Fatalf("Bootstrap() error = %v", err)
	}
	if !created {
		t.Fatal("Bootstrap() = false on an empty table, want true")
	}

	created, err = env.svc.Bootstrap(ctx, "intruder", "", "another-long-password")
	if err != nil {
		t.Fatalf("second Bootstrap() error = %v", err)
	}
	if created {
		t.Error("Bootstrap() = true when an account already exists; it must be a no-op")
	}

	if count, _ := env.repo.CountAccounts(ctx, ""); count != 1 {
		t.Errorf("account count = %d, want 1", count)
	}
	if events := env.trail.eventsByAction(actionBootstrap); len(events) != 1 {
		t.Errorf("bootstrap audit events = %d, want 1", len(events))
	}
}

func TestBootstrapRejectsWeakPassword(t *testing.T) {
	t.Parallel()
	env := newTestEnv(t)

	if _, err := env.svc.Bootstrap(context.Background(), "owner", "", "short"); err == nil {
		t.Fatal("Bootstrap() error = nil for a password below the minimum length")
	}
	if count, _ := env.repo.CountAccounts(context.Background(), ""); count != 0 {
		t.Errorf("account count = %d after a failed bootstrap, want 0", count)
	}
}

func TestLoginAndAuthenticate(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	env := newTestEnv(t)
	account := env.mustCreate(t, "ada")

	gotAccount, session, token, err := env.svc.Login(ctx, LoginInput{
		Username:  "  ADA ",
		Password:  testPassword,
		IP:        "127.0.0.1",
		UserAgent: "go-test",
	})
	if err != nil {
		t.Fatalf("Login() error = %v", err)
	}
	if gotAccount.ID != account.ID {
		t.Errorf("Login() account = %q, want %q", gotAccount.ID, account.ID)
	}
	if !strings.HasPrefix(token, sessionTokenPrefix) {
		t.Errorf("token = %q, want the %q prefix", token, sessionTokenPrefix)
	}
	if want := env.clock.Now().Add(time.Hour); !session.ExpiresAt.Equal(want) {
		t.Errorf("expiresAt = %v, want %v", session.ExpiresAt, want)
	}
	if session.IP != "127.0.0.1" || session.UserAgent != "go-test" {
		t.Errorf("session origin = (%q, %q), want the values Login was given", session.IP, session.UserAgent)
	}

	authenticated, gotSession, err := env.svc.Authenticate(ctx, token)
	if err != nil {
		t.Fatalf("Authenticate() error = %v", err)
	}
	if authenticated.ID != account.ID {
		t.Errorf("Authenticate() account = %q, want %q", authenticated.ID, account.ID)
	}
	if gotSession.ID != session.ID {
		t.Errorf("Authenticate() session = %q, want %q", gotSession.ID, session.ID)
	}
	if gotAccount.LastLoginAt.IsZero() {
		t.Error("lastLoginAt was not recorded")
	}
	if events := env.trail.eventsByAction(actionLogin); len(events) != 1 {
		t.Errorf("login audit events = %d, want 1", len(events))
	}
}

func TestLoginRejectsBadCredentials(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	env := newTestEnv(t)
	account := env.mustCreate(t, "ada")

	cases := map[string]LoginInput{
		"wrong password": {Username: "ada", Password: "correct-horse-batterY"},
		"unknown user":   {Username: "grace", Password: testPassword},
		"empty username": {Username: "", Password: testPassword},
		"empty password": {Username: "ada", Password: ""},
	}
	for name, in := range cases {
		t.Run(name, func(t *testing.T) {
			if _, _, _, err := env.svc.Login(ctx, in); !errors.Is(err, apperr.ErrUnauthorized) {
				t.Errorf("Login() error = %v, want ErrUnauthorized", err)
			}
		})
	}

	// A disabled account rejects the correct password too, and the rejection
	// must look identical to a wrong password. The status is written through
	// the repository because the service refuses to disable the last active
	// administrator, which this account is.
	disabledAccount := account.Clone()
	disabledAccount.Status = StatusDisabled
	if err := env.repo.UpdateAccount(ctx, disabledAccount); err != nil {
		t.Fatalf("disable account: %v", err)
	}
	if _, _, _, err := env.svc.Login(ctx, LoginInput{Username: "ada", Password: testPassword}); !errors.Is(err, apperr.ErrUnauthorized) {
		t.Errorf("Login() error = %v for a disabled account, want ErrUnauthorized", err)
	}
}

func TestSessionExpiryAndRevocation(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	env := newTestEnv(t)
	env.mustCreate(t, "ada")

	_, session, token, err := env.svc.Login(ctx, LoginInput{Username: "ada", Password: testPassword})
	if err != nil {
		t.Fatalf("Login() error = %v", err)
	}

	env.clock.Advance(time.Hour + time.Second)
	if _, _, err := env.svc.Authenticate(ctx, token); !errors.Is(err, apperr.ErrUnauthorized) {
		t.Errorf("Authenticate() error = %v after expiry, want ErrUnauthorized", err)
	}

	env.clock.Advance(-time.Hour - time.Second)
	if _, _, err := env.svc.Authenticate(ctx, token); err != nil {
		t.Fatalf("Authenticate() error = %v before expiry", err)
	}
	if err := env.svc.Logout(ctx, session.ID, actorOf(&Account{ID: session.AccountID, Username: "ada"})); err != nil {
		t.Fatalf("Logout() error = %v", err)
	}
	if _, _, err := env.svc.Authenticate(ctx, token); !errors.Is(err, apperr.ErrUnauthorized) {
		t.Error("Authenticate() succeeded after logout")
	}
	if err := env.svc.Logout(ctx, session.ID, systemActor()); !errors.Is(err, apperr.ErrNotFound) {
		t.Errorf("second Logout() error = %v, want ErrNotFound", err)
	}
}

func TestRevokeSessionKeepsTheOthers(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	env := newTestEnv(t)
	account := env.mustCreate(t, "ada")

	_, first, firstToken, err := env.svc.Login(ctx, LoginInput{Username: "ada", Password: testPassword})
	if err != nil {
		t.Fatalf("Login() error = %v", err)
	}
	_, _, secondToken, err := env.svc.Login(ctx, LoginInput{Username: "ada", Password: testPassword})
	if err != nil {
		t.Fatalf("second Login() error = %v", err)
	}

	if err := env.svc.RevokeSession(ctx, account.ID, first.ID, actorOf(account)); err != nil {
		t.Fatalf("RevokeSession() error = %v", err)
	}
	if _, _, err := env.svc.Authenticate(ctx, firstToken); !errors.Is(err, apperr.ErrUnauthorized) {
		t.Error("revoked session still authenticates")
	}
	if _, _, err := env.svc.Authenticate(ctx, secondToken); err != nil {
		t.Errorf("Authenticate() error = %v for the other session", err)
	}
	// A session cannot be revoked through the wrong account: the ID is random
	// and confirming it exists would be a free oracle.
	if err := env.svc.RevokeSession(ctx, "someone-else", first.ID, actorOf(account)); !errors.Is(err, apperr.ErrNotFound) {
		t.Errorf("RevokeSession() error = %v for the wrong account, want ErrNotFound", err)
	}
}

func TestCreateAccount(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	env := newTestEnv(t)

	t.Run("defaults display name to the username", func(t *testing.T) {
		account, err := env.svc.CreateAccount(ctx, CreateAccountInput{
			Username: "Ada.Lovelace",
			Password: testPassword,
		}, systemActor())
		if err != nil {
			t.Fatalf("CreateAccount() error = %v", err)
		}
		if account.Username != "ada.lovelace" {
			t.Errorf("username = %q, want it folded to lowercase", account.Username)
		}
		if account.DisplayName != "ada.lovelace" {
			t.Errorf("displayName = %q, want the username as fallback", account.DisplayName)
		}
		if !account.Active() {
			t.Error("a new account is not active")
		}
	})

	t.Run("duplicate username is a conflict", func(t *testing.T) {
		_, err := env.svc.CreateAccount(ctx, CreateAccountInput{
			Username: "ada.lovelace",
			Password: testPassword,
		}, systemActor())
		if !errors.Is(err, apperr.ErrConflict) {
			t.Fatalf("CreateAccount() error = %v, want ErrConflict", err)
		}
		var detail *apperr.DetailError
		if !errors.As(err, &detail) || detail.Message == "" {
			t.Error("conflict carries no detail sentence for the client")
		}
	})

	t.Run("reports every invalid field at once", func(t *testing.T) {
		_, err := env.svc.CreateAccount(ctx, CreateAccountInput{
			Username: "A",
			Password: "short",
		}, systemActor())
		if !errors.Is(err, apperr.ErrInvalid) {
			t.Fatalf("CreateAccount() error = %v, want ErrInvalid", err)
		}
		if fields := apperr.Fields(err); len(fields) != 2 {
			t.Errorf("fields = %+v, want username and password together", fields)
		}
	})
}

func TestUpdateAccount(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	env := newTestEnv(t)
	ada := env.mustCreate(t, "ada")
	env.clock.Advance(time.Minute)

	name := "Ada L."
	updated, err := env.svc.UpdateAccount(ctx, ada.ID, UpdateAccountInput{DisplayName: &name}, actorOf(ada))
	if err != nil {
		t.Fatalf("UpdateAccount() error = %v", err)
	}
	if updated.DisplayName != name {
		t.Errorf("displayName = %q, want %q", updated.DisplayName, name)
	}
	if !updated.UpdatedAt.After(ada.UpdatedAt) {
		t.Error("updatedAt did not move forward")
	}

	// A PATCH that changes nothing must not bump updatedAt: the row would
	// claim an edit that never happened.
	env.clock.Advance(time.Minute)
	same, err := env.svc.UpdateAccount(ctx, ada.ID, UpdateAccountInput{DisplayName: &name}, actorOf(ada))
	if err != nil {
		t.Fatalf("no-op UpdateAccount() error = %v", err)
	}
	if !same.UpdatedAt.Equal(updated.UpdatedAt) {
		t.Error("a no-op update changed updatedAt")
	}

	var disabled AccountStatus = StatusDisabled
	if _, err := env.svc.UpdateAccount(ctx, ada.ID, UpdateAccountInput{Status: &disabled}, actorOf(ada)); !errors.Is(err, apperr.ErrConflict) {
		t.Errorf("disabling oneself error = %v, want ErrConflict", err)
	}

	// The last active administrator cannot be disabled either, even by someone
	// else: that would leave the console with nobody who can sign in.
	if _, err := env.svc.UpdateAccount(ctx, ada.ID, UpdateAccountInput{Status: &disabled}, audit.Actor{ID: "ghost", Name: "ghost"}); !errors.Is(err, apperr.ErrConflict) {
		t.Errorf("disabling the last active account error = %v, want ErrConflict", err)
	}

	grace := env.mustCreate(t, "grace")
	if _, err := env.svc.UpdateAccount(ctx, grace.ID, UpdateAccountInput{Status: &disabled}, actorOf(ada)); err != nil {
		t.Fatalf("disabling another account error = %v", err)
	}

	bad := AccountStatus("sleeping")
	if _, err := env.svc.UpdateAccount(ctx, grace.ID, UpdateAccountInput{Status: &bad}, actorOf(ada)); !errors.Is(err, apperr.ErrInvalid) {
		t.Errorf("unknown status error = %v, want ErrInvalid", err)
	}
	if _, err := env.svc.UpdateAccount(ctx, grace.ID, UpdateAccountInput{}, actorOf(ada)); !errors.Is(err, apperr.ErrInvalid) {
		t.Errorf("empty update error = %v, want ErrInvalid", err)
	}
}

func TestDeleteAccount(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	env := newTestEnv(t)
	ada := env.mustCreate(t, "ada")

	if err := env.svc.DeleteAccount(ctx, ada.ID, actorOf(ada)); !errors.Is(err, apperr.ErrConflict) {
		t.Errorf("deleting oneself error = %v, want ErrConflict", err)
	}
	if err := env.svc.DeleteAccount(ctx, ada.ID, audit.Actor{ID: "ghost", Name: "ghost"}); !errors.Is(err, apperr.ErrConflict) {
		t.Errorf("deleting the last account error = %v, want ErrConflict", err)
	}

	grace := env.mustCreate(t, "grace")
	if _, _, _, err := env.svc.Login(ctx, LoginInput{Username: "grace", Password: testPassword}); err != nil {
		t.Fatalf("Login() error = %v", err)
	}
	if err := env.svc.DeleteAccount(ctx, grace.ID, actorOf(ada)); err != nil {
		t.Fatalf("DeleteAccount() error = %v", err)
	}
	if _, err := env.svc.GetAccount(ctx, grace.ID); !errors.Is(err, apperr.ErrNotFound) {
		t.Error("deleted account is still readable")
	}
	if sessions, _ := env.repo.ListSessions(ctx, grace.ID, env.clock.Now()); len(sessions) != 0 {
		t.Errorf("sessions = %d after deleting the account, want 0", len(sessions))
	}
	if err := env.svc.DeleteAccount(ctx, grace.ID, actorOf(ada)); !errors.Is(err, apperr.ErrNotFound) {
		t.Errorf("second delete error = %v, want ErrNotFound", err)
	}
}

func TestChangePassword(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	env := newTestEnv(t)
	ada := env.mustCreate(t, "ada")

	_, _, token, err := env.svc.Login(ctx, LoginInput{Username: "ada", Password: testPassword})
	if err != nil {
		t.Fatalf("Login() error = %v", err)
	}

	if err := env.svc.ChangePassword(ctx, ada.ID, ChangePasswordInput{NewPassword: "another-long-password"}, actorOf(ada)); !errors.Is(err, apperr.ErrInvalid) {
		t.Errorf("missing current password error = %v, want ErrInvalid", err)
	}
	if err := env.svc.ChangePassword(ctx, ada.ID, ChangePasswordInput{
		CurrentPassword: "wrong",
		NewPassword:     "another-long-password",
	}, actorOf(ada)); !errors.Is(err, apperr.ErrInvalid) {
		t.Errorf("wrong current password error = %v, want ErrInvalid", err)
	}
	if err := env.svc.ChangePassword(ctx, ada.ID, ChangePasswordInput{
		CurrentPassword: testPassword,
		NewPassword:     "short",
	}, actorOf(ada)); !errors.Is(err, apperr.ErrInvalid) {
		t.Errorf("short new password error = %v, want ErrInvalid", err)
	}

	if err := env.svc.ChangePassword(ctx, ada.ID, ChangePasswordInput{
		CurrentPassword: testPassword,
		NewPassword:     "another-long-password",
	}, actorOf(ada)); err != nil {
		t.Fatalf("ChangePassword() error = %v", err)
	}

	// A password change assumes the old credential leaked, so every session of
	// the account dies with it — including the one that made the request.
	if _, _, err := env.svc.Authenticate(ctx, token); !errors.Is(err, apperr.ErrUnauthorized) {
		t.Error("the old session still authenticates after a password change")
	}
	if _, _, _, err := env.svc.Login(ctx, LoginInput{Username: "ada", Password: testPassword}); !errors.Is(err, apperr.ErrUnauthorized) {
		t.Error("the old password still works")
	}
	if _, _, _, err := env.svc.Login(ctx, LoginInput{Username: "ada", Password: "another-long-password"}); err != nil {
		t.Errorf("Login() with the new password error = %v", err)
	}
}

func TestAdminCanResetAnotherPassword(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	env := newTestEnv(t)
	ada := env.mustCreate(t, "ada")
	grace := env.mustCreate(t, "grace")

	if _, _, _, err := env.svc.Login(ctx, LoginInput{Username: "grace", Password: testPassword}); err != nil {
		t.Fatalf("Login() error = %v", err)
	}

	// No current password is required: the caller is resetting someone else,
	// and the audit trail records who did it.
	if err := env.svc.ChangePassword(ctx, grace.ID, ChangePasswordInput{NewPassword: "another-long-password"}, actorOf(ada)); err != nil {
		t.Fatalf("reset error = %v", err)
	}
	if sessions, _ := env.repo.ListSessions(ctx, grace.ID, env.clock.Now()); len(sessions) != 0 {
		t.Errorf("sessions = %d after a reset, want 0", len(sessions))
	}
	if _, _, _, err := env.svc.Login(ctx, LoginInput{Username: "grace", Password: "another-long-password"}); err != nil {
		t.Errorf("Login() with the reset password error = %v", err)
	}
}

func TestListAccountsPagination(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	env := newTestEnv(t)

	for _, name := range []string{"ada", "grace", "linus"} {
		env.mustCreate(t, name)
		env.clock.Advance(time.Second)
	}

	first, next, err := env.svc.ListAccounts(ctx, AccountQuery{Limit: 2})
	if err != nil {
		t.Fatalf("ListAccounts() error = %v", err)
	}
	if len(first) != 2 || next == "" {
		t.Fatalf("first page = %d items, next = %q; want 2 and a cursor", len(first), next)
	}
	if first[0].Username != "linus" {
		t.Errorf("first account = %q, want the newest (linus)", first[0].Username)
	}

	position, err := cursor.Decode(next)
	if err != nil {
		t.Fatalf("decode cursor: %v", err)
	}
	second, rest, err := env.svc.ListAccounts(ctx, AccountQuery{Limit: 2, Cursor: &position})
	if err != nil {
		t.Fatalf("ListAccounts() page 2 error = %v", err)
	}
	if len(second) != 1 || rest != "" {
		t.Errorf("second page = %d items, next = %q; want 1 and no cursor", len(second), rest)
	}
	if second[0].Username != "ada" {
		t.Errorf("second page account = %q, want the oldest (ada)", second[0].Username)
	}

	disabled := AccountStatus("disabled")
	filtered, _, err := env.svc.ListAccounts(ctx, AccountQuery{Status: disabled})
	if err != nil {
		t.Fatalf("filtered ListAccounts() error = %v", err)
	}
	if len(filtered) != 0 {
		t.Errorf("disabled accounts = %d, want 0", len(filtered))
	}
}

func TestListSessions(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	env := newTestEnv(t)
	ada := env.mustCreate(t, "ada")

	for i := 0; i < 2; i++ {
		if _, _, _, err := env.svc.Login(ctx, LoginInput{Username: "ada", Password: testPassword}); err != nil {
			t.Fatalf("Login() error = %v", err)
		}
		env.clock.Advance(time.Second)
	}

	sessions, err := env.svc.ListSessions(ctx, ada.ID)
	if err != nil {
		t.Fatalf("ListSessions() error = %v", err)
	}
	if len(sessions) != 2 {
		t.Fatalf("sessions = %d, want 2", len(sessions))
	}
	if err := env.svc.RevokeSession(ctx, ada.ID, sessions[0].ID, actorOf(ada)); err != nil {
		t.Fatalf("RevokeSession() error = %v", err)
	}
	sessions, err = env.svc.ListSessions(ctx, ada.ID)
	if err != nil {
		t.Fatalf("ListSessions() error = %v", err)
	}
	if len(sessions) != 1 {
		t.Errorf("sessions after revoking one = %d, want 1", len(sessions))
	}
}

func TestPruneExpiredSessions(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	env := newTestEnv(t)
	env.mustCreate(t, "ada")

	if _, _, _, err := env.svc.Login(ctx, LoginInput{Username: "ada", Password: testPassword}); err != nil {
		t.Fatalf("Login() error = %v", err)
	}
	env.clock.Advance(2 * time.Hour)

	pruned, err := env.svc.PruneExpiredSessions(ctx)
	if err != nil {
		t.Fatalf("PruneExpiredSessions() error = %v", err)
	}
	if pruned != 1 {
		t.Errorf("pruned = %d, want 1", pruned)
	}
}

func TestAuditTrailListing(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	env := newTestEnv(t)
	ada := env.mustCreate(t, "ada")

	if _, _, _, err := env.svc.Login(ctx, LoginInput{Username: "ada", Password: testPassword}); err != nil {
		t.Fatalf("Login() error = %v", err)
	}
	name := "Ada L."
	if _, err := env.svc.UpdateAccount(ctx, ada.ID, UpdateAccountInput{DisplayName: &name}, actorOf(ada)); err != nil {
		t.Fatalf("UpdateAccount() error = %v", err)
	}

	entries, next, err := env.svc.ListAuditLogs(ctx, audit.Query{Limit: 1})
	if err != nil {
		t.Fatalf("ListAuditLogs() error = %v", err)
	}
	if len(entries) != 1 || next == "" {
		t.Fatalf("first page = %d entries, next = %q; want 1 and a cursor", len(entries), next)
	}
	if entries[0].Action != actionAccountUpdate {
		t.Errorf("newest action = %q, want %q", entries[0].Action, actionAccountUpdate)
	}

	position, err := cursor.Decode(next)
	if err != nil {
		t.Fatalf("decode cursor: %v", err)
	}
	rest, _, err := env.svc.ListAuditLogs(ctx, audit.Query{Limit: 10, Cursor: &position})
	if err != nil {
		t.Fatalf("second page error = %v", err)
	}
	if len(rest) == 0 {
		t.Fatal("second page is empty; the cursor skipped the rest of the trail")
	}

	logins, _, err := env.svc.ListAuditLogs(ctx, audit.Query{Action: actionLogin})
	if err != nil {
		t.Fatalf("filtered ListAuditLogs() error = %v", err)
	}
	if len(logins) != 1 || logins[0].Action != actionLogin {
		t.Errorf("filtered entries = %+v, want the single login event", logins)
	}

	if !env.svc.HasAuditTrail() {
		t.Error("HasAuditTrail() = false with a reader configured")
	}
}
