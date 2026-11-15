package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/liomix/astro-liomix-server/internal/admin"
	"github.com/liomix/astro-liomix-server/internal/platform/apperr"
	"github.com/liomix/astro-liomix-server/internal/platform/audit"
)

// The development default administrator.
//
// These are deliberately obvious credentials for a local console: the account is
// created only when APP_ENV is development, and the login page shows them until
// the password is changed. Production never seeds this account and never stores
// the plaintext (see admin.WithPlaintextInitialPassword).
const (
	developmentAdminUsername = "admin"
	developmentAdminPassword = "astroadmin123"
)

// seedDevelopmentAdmin makes sure the development database has the default
// administrator, so a fresh checkout can sign in without running a command
// first.
//
// It is idempotent by username: an existing account is left alone, including one
// whose password has already been changed. Deleting the account brings it back
// on the next start, which is the point of a development fixture.
func seedDevelopmentAdmin(ctx context.Context, svc *admin.Service, logger *slog.Logger) error {
	if _, err := svc.GetAccountByUsername(ctx, developmentAdminUsername); err == nil {
		return nil
	} else if !errors.Is(err, apperr.ErrNotFound) {
		return fmt.Errorf("look up default administrator: %w", err)
	}

	_, err := svc.CreateAccount(ctx, admin.CreateAccountInput{
		Username:    developmentAdminUsername,
		DisplayName: "Admin",
		Password:    developmentAdminPassword,
	}, audit.Actor{Name: "system"})
	if errors.Is(err, apperr.ErrConflict) {
		// Another process created it between the lookup and the insert.
		return nil
	}
	if err != nil {
		return fmt.Errorf("create default administrator: %w", err)
	}

	logger.Warn("created the development default administrator; change the password before exposing the console",
		slog.String("username", developmentAdminUsername),
	)
	return nil
}
