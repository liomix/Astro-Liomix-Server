package audit_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/liomix/astro-liomix-server/internal/platform/apperr"
	"github.com/liomix/astro-liomix-server/internal/platform/audit"
	"github.com/liomix/astro-liomix-server/internal/platform/cursor"
	"github.com/liomix/astro-liomix-server/internal/platform/postgres/postgrestest"
)

// newStore returns a store backed by the development database, inside a
// transaction that is rolled back when the case ends.
func newStore(t *testing.T) *audit.Store {
	t.Helper()

	db := postgrestest.Open(t)
	postgrestest.Migrate(t, db, audit.Migrate)

	tx := postgrestest.Tx(t, db)
	if _, err := tx.ExecContext(context.Background(), `TRUNCATE audit_logs`); err != nil {
		t.Fatalf("start from an empty audit_logs table: %v", err)
	}
	return audit.NewStore(tx)
}

func TestStoreRecordAndList(t *testing.T) {
	store := newStore(t)
	ctx := context.Background()
	base := time.Now().UTC().Truncate(time.Microsecond)

	events := []audit.Event{
		{
			Actor:    audit.Actor{ID: "a-1", Name: "ada", IP: "127.0.0.1", UserAgent: "go-test"},
			Action:   "auth.login",
			Resource: "session",
			Detail:   map[string]any{"username": "ada"},
			At:       base,
		},
		{
			Actor:      audit.Actor{ID: "a-1", Name: "ada"},
			Action:     "comment.moderate",
			Resource:   "comment",
			ResourceID: "c-1",
			Detail:     map[string]any{"status": "approved"},
			At:         base.Add(time.Second),
		},
		{
			Actor:    audit.Actor{Name: "system"},
			Action:   "account.bootstrap",
			Resource: "account",
			At:       base.Add(2 * time.Second),
		},
	}
	for _, e := range events {
		if err := store.Record(ctx, e); err != nil {
			t.Fatalf("Record(%s) error = %v", e.Action, err)
		}
	}

	page, next, err := store.List(ctx, audit.Query{Limit: 2})
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(page) != 2 {
		t.Fatalf("page = %d entries, want 2", len(page))
	}
	if page[0].Action != "account.bootstrap" {
		t.Errorf("newest action = %q, want account.bootstrap", page[0].Action)
	}
	if page[0].Actor.ID != "" {
		t.Errorf("anonymous actor id = %q, want the empty string", page[0].Actor.ID)
	}
	if page[0].Detail == nil {
		t.Error("detail is nil; an event without context should read as an empty object")
	}
	if next == "" {
		t.Fatal("nextCursor is empty with a third entry in the trail")
	}

	position, err := cursor.Decode(next)
	if err != nil {
		t.Fatalf("decode cursor: %v", err)
	}
	rest, last, err := store.List(ctx, audit.Query{Limit: 10, Cursor: &position})
	if err != nil {
		t.Fatalf("List(page 2) error = %v", err)
	}
	if len(rest) != 1 || last != "" {
		t.Fatalf("page 2 = %d entries, next = %q; want 1 and no cursor", len(rest), last)
	}
	if rest[0].Action != "auth.login" {
		t.Errorf("oldest action = %q, want auth.login", rest[0].Action)
	}
	if got := rest[0].Detail["username"]; got != "ada" {
		t.Errorf("detail.username = %v, want ada", got)
	}
	if rest[0].Actor.IP != "127.0.0.1" || rest[0].Actor.UserAgent != "go-test" {
		t.Errorf("origin = (%q, %q), want the recorded values", rest[0].Actor.IP, rest[0].Actor.UserAgent)
	}
}

func TestStoreFilters(t *testing.T) {
	store := newStore(t)
	ctx := context.Background()
	base := time.Now().UTC().Truncate(time.Microsecond)

	for i, e := range []audit.Event{
		{Actor: audit.Actor{ID: "a-1", Name: "ada"}, Action: "auth.login", Resource: "session", At: base},
		{Actor: audit.Actor{ID: "a-2", Name: "grace"}, Action: "auth.login", Resource: "session", At: base.Add(time.Second)},
		{Actor: audit.Actor{ID: "a-1", Name: "ada"}, Action: "account.update", Resource: "account", At: base.Add(2 * time.Second)},
	} {
		if err := store.Record(ctx, e); err != nil {
			t.Fatalf("Record(%d) error = %v", i, err)
		}
	}

	byAction, _, err := store.List(ctx, audit.Query{Action: "auth.login"})
	if err != nil {
		t.Fatalf("List(action) error = %v", err)
	}
	if len(byAction) != 2 {
		t.Errorf("auth.login entries = %d, want 2", len(byAction))
	}

	byActor, _, err := store.List(ctx, audit.Query{ActorID: "a-1"})
	if err != nil {
		t.Fatalf("List(actor) error = %v", err)
	}
	if len(byActor) != 2 {
		t.Errorf("a-1 entries = %d, want 2", len(byActor))
	}
	for _, e := range byActor {
		if e.Actor.Name != "ada" {
			t.Errorf("actor = %q, want ada", e.Actor.Name)
		}
	}
}

func TestStoreRejectsIncompleteEvents(t *testing.T) {
	store := newStore(t)
	ctx := context.Background()

	cases := map[string]audit.Event{
		"no action":   {Resource: "account"},
		"no resource": {Action: "account.create"},
	}
	for name, event := range cases {
		t.Run(name, func(t *testing.T) {
			if err := store.Record(ctx, event); !errors.Is(err, apperr.ErrInvalid) {
				t.Errorf("Record() error = %v, want ErrInvalid", err)
			}
		})
	}
}

func TestStoreRejectsNegativeLimit(t *testing.T) {
	store := newStore(t)

	if _, _, err := store.List(context.Background(), audit.Query{Limit: -1}); !errors.Is(err, apperr.ErrInvalid) {
		t.Errorf("List() error = %v, want ErrInvalid", err)
	}
}
