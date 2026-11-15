package main

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/liomix/astro-liomix-server/internal/admin"
	"github.com/liomix/astro-liomix-server/internal/platform/mail"
	"github.com/liomix/astro-liomix-server/internal/platform/postgres/postgrestest"
)

// newMailPanel returns a panel backed by the development database, inside a
// transaction that is rolled back when the case ends.
func newMailPanel(t *testing.T) mailPanel {
	t.Helper()

	db := postgrestest.Open(t)
	postgrestest.Migrate(t, db, mail.Migrate)

	tx := postgrestest.Tx(t, db)
	if _, err := tx.ExecContext(context.Background(), `DELETE FROM mail_settings`); err != nil {
		t.Fatalf("start from an empty mail_settings table: %v", err)
	}
	return mailPanel{
		store:    mail.NewStore(tx),
		siteName: "Test Blog",
		logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
		timeout:  time.Second,
	}
}

func TestMailPanelSavesIncompleteForm(t *testing.T) {
	t.Parallel()
	panel := newMailPanel(t)
	ctx := context.Background()

	// Half-filled is a legal state: somebody types the server, goes to the
	// provider for an app password, and comes back.
	if err := panel.SaveSettings(ctx, admin.MailSettings{
		Host: "smtp.qq.com", Port: 465, TLS: mail.TLSImplicit,
		From: "blog@qq.com", AdminNotify: true,
		TemplateAdmin: "<html><body>{{body}}</body></html>",
	}); err != nil {
		t.Fatalf("SaveSettings(incomplete) error = %v", err)
	}
	settings, err := panel.Settings(ctx)
	if err != nil {
		t.Fatalf("Settings() error = %v", err)
	}
	if settings.Host != "smtp.qq.com" || settings.Port != 465 {
		t.Errorf("settings = %+v, want the values that were saved", settings)
	}
	if settings.HasPassword {
		t.Error("HasPassword = true although no password was ever set")
	}
	if !strings.Contains(settings.TemplateAdmin, "{{body}}") {
		t.Errorf("TemplateAdmin = %q, want the stored document", settings.TemplateAdmin)
	}
}

func TestMailPanelKeepsStoredPassword(t *testing.T) {
	t.Parallel()
	panel := newMailPanel(t)
	ctx := context.Background()

	if err := panel.SaveSettings(ctx, admin.MailSettings{
		Host: "smtp.qq.com", Port: 465, TLS: mail.TLSImplicit,
		Username: "blog@qq.com", Password: "app-token", From: "blog@qq.com",
	}); err != nil {
		t.Fatalf("SaveSettings() error = %v", err)
	}

	// The page never receives the password, so editing any other field sends an
	// empty box back. That must mean "keep it", not "erase it".
	if err := panel.SaveSettings(ctx, admin.MailSettings{
		Host: "smtp.qq.com", Port: 587, TLS: mail.TLSStartTLS,
		Username: "blog@qq.com", From: "blog@qq.com",
	}); err != nil {
		t.Fatalf("SaveSettings(without password) error = %v", err)
	}
	settings, err := panel.Settings(ctx)
	if err != nil {
		t.Fatalf("Settings() error = %v", err)
	}
	if !settings.HasPassword {
		t.Fatal("the stored password was erased by a save that left the box empty")
	}
	if settings.Port != 587 {
		t.Errorf("port = %d, want the edited value", settings.Port)
	}
}

func TestMailPanelRejectsMalformedAddress(t *testing.T) {
	t.Parallel()
	panel := newMailPanel(t)

	err := panel.SaveSettings(context.Background(), admin.MailSettings{
		Host: "smtp.qq.com", Port: 587, TLS: mail.TLSAuto, From: "not-an-address",
	})
	if err == nil {
		t.Fatal("SaveSettings(bad from) = nil, want a validation error")
	}
}
