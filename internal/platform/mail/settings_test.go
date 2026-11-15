package mail_test

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/liomix/astro-liomix-server/internal/platform/apperr"
	"github.com/liomix/astro-liomix-server/internal/platform/mail"
	"github.com/liomix/astro-liomix-server/internal/platform/postgres/postgrestest"
)

// newStore returns a store backed by the development database, inside a
// transaction that is rolled back when the case ends.
func newStore(t *testing.T) *mail.Store {
	t.Helper()

	db := postgrestest.Open(t)
	postgrestest.Migrate(t, db, mail.Migrate)

	tx := postgrestest.Tx(t, db)
	if _, err := tx.ExecContext(context.Background(), `DELETE FROM mail_settings`); err != nil {
		t.Fatalf("start from an empty mail_settings table: %v", err)
	}
	return mail.NewStore(tx)
}

func complete() mail.Settings {
	return mail.Settings{
		Host:          "smtp.qq.com",
		Port:          465,
		TLS:           mail.TLSImplicit,
		Username:      "blog@qq.com",
		Password:      "app-token",
		From:          "blog@qq.com",
		FromName:      "Astro Liomix",
		AdminTo:       []string{"owner@example.com", "second@example.com"},
		ReplyNotify:   true,
		AdminNotify:   true,
		TemplateAdmin: "<html><body>{{authorName}} 留言了</body></html>",
		TemplateReply: "<html><body>{{parentAuthor}} 有新回复</body></html>",
	}
}

func TestSettingsValidateAcceptsIncompleteForms(t *testing.T) {
	// A half-filled form must save: the operator is allowed to type the server
	// first and the app password after fetching it from the provider.
	cases := map[string]mail.Settings{
		"empty":         mail.DefaultSettings(),
		"host only":     {Host: "smtp.qq.com", Port: 587, TLS: mail.TLSAuto},
		"no host":       {Port: 587, TLS: mail.TLSAuto},
		"password only": {Port: 587, TLS: mail.TLSAuto, Password: "app-token"},
		"admin switch on, no recipient": {
			Host: "smtp.qq.com", Port: 587, TLS: mail.TLSAuto,
			From: "blog@qq.com", AdminNotify: true,
		},
	}
	for name, settings := range cases {
		if err := settings.Validate(); err != nil {
			t.Errorf("%s: Validate() = %v, want nil", name, err)
		}
	}
}

func TestSettingsValidateRejectsMalformedValues(t *testing.T) {
	cases := map[string]struct {
		settings mail.Settings
		field    string
	}{
		"port zero":     {mail.Settings{Port: 0, TLS: mail.TLSAuto}, "port"},
		"port too big":  {mail.Settings{Port: 70000, TLS: mail.TLSAuto}, "port"},
		"unknown tls":   {mail.Settings{Port: 587, TLS: "ssl"}, "tls"},
		"host with gap": {mail.Settings{Port: 587, TLS: mail.TLSAuto, Host: "smtp qq.com"}, "host"},
		"bad from":      {mail.Settings{Port: 587, TLS: mail.TLSAuto, From: "not-an-address"}, "from"},
		"bad admin":     {mail.Settings{Port: 587, TLS: mail.TLSAuto, AdminTo: []string{"nope"}}, "adminTo"},
	}
	for name, c := range cases {
		err := c.settings.Validate()
		if err == nil {
			t.Errorf("%s: Validate() = nil, want an error", name)
			continue
		}
		if !errors.Is(err, apperr.ErrInvalid) {
			t.Errorf("%s: Validate() = %v, want a validation error", name, err)
		}
		fields := apperr.Fields(err)
		if len(fields) == 0 || fields[0].Field != c.field {
			t.Errorf("%s: field = %+v, want %q", name, fields, c.field)
		}
	}
}

func TestSettingsConfigured(t *testing.T) {
	if !complete().Configured() {
		t.Error("complete settings: Configured = false, want true")
	}
	if mail.DefaultSettings().Configured() {
		t.Error("default settings: Configured = true, want false without an account")
	}
	// The notices default to on; what keeps a fresh install quiet is that it has
	// no SMTP account, not a second switch.
	if defaults := mail.DefaultSettings(); !defaults.ReplyNotify || !defaults.AdminNotify {
		t.Errorf("defaults = %+v, want both notices on", defaults)
	}
}

func TestStoreRoundTrip(t *testing.T) {
	store := newStore(t)
	ctx := context.Background()

	// Before the first save there is no row, and the defaults say so.
	settings, found, err := store.Load(ctx)
	if err != nil {
		t.Fatalf("Load() on an empty table error = %v", err)
	}
	if found {
		t.Error("Load() found = true on an empty table")
	}
	if settings.Port != 587 || settings.TLS != mail.TLSAuto || settings.Configured() {
		t.Errorf("defaults = %+v, want port 587, tls auto, not configured", settings)
	}

	want := complete()
	if err := store.Save(ctx, want); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	got, found, err := store.Load(ctx)
	if err != nil || !found {
		t.Fatalf("Load() = %+v, %v, %v; want a row", got, found, err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Load() = %+v, want %+v", got, want)
	}

	// A second save updates the same row rather than adding one.
	second := want
	second.FromName = "Renamed"
	second.AdminTo = []string{"only@example.com"}
	if err := store.Save(ctx, second); err != nil {
		t.Fatalf("Save() again error = %v", err)
	}
	got, _, err = store.Load(ctx)
	if err != nil {
		t.Fatalf("Load() after update error = %v", err)
	}
	if got.FromName != "Renamed" || len(got.AdminTo) != 1 || got.AdminTo[0] != "only@example.com" {
		t.Errorf("updated settings = %+v, want the second save", got)
	}
}

func TestStoreEnsureSeedsOnce(t *testing.T) {
	store := newStore(t)
	ctx := context.Background()

	seeded, err := store.Ensure(ctx, complete())
	if err != nil {
		t.Fatalf("Ensure() error = %v", err)
	}
	if !seeded {
		t.Error("Ensure() wrote = false on an empty table")
	}

	// A second start must not overwrite what the page saved: a stale
	// MAIL_PASSWORD in a service definition cannot win over the console.
	changed := complete()
	changed.Host = "smtp.example.com"
	if err := store.Save(ctx, changed); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	seeded, err = store.Ensure(ctx, complete())
	if err != nil {
		t.Fatalf("Ensure() again error = %v", err)
	}
	if seeded {
		t.Error("Ensure() wrote = true although a row existed")
	}
	got, _, err := store.Load(ctx)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if got.Host != "smtp.example.com" {
		t.Errorf("host = %q, want the saved value", got.Host)
	}
}

func TestSettingsSenderRefusesWhenUnconfigured(t *testing.T) {
	store := newStore(t)
	ctx := context.Background()

	if err := store.Save(ctx, mail.DefaultSettings()); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	sender := mail.NewSettingsSender(store)
	err := sender.Send(ctx, mail.Message{To: "reader@example.com", Subject: "hi", Text: "hi"})
	if !errors.Is(err, mail.ErrNotConfigured) {
		t.Errorf("Send() error = %v, want ErrNotConfigured", err)
	}
}
