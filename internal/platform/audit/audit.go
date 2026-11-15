// Package audit records privileged actions for later review.
//
// The trail is an append-only table, not a log file: "who approved that comment
// and when" has to survive a redeploy, a log rotation and a rotation of log
// access. It is also the only place an operator can answer "who changed this
// account" after the account itself is gone, which is why the rows carry a
// snapshot of the actor's name instead of a foreign key.
//
// Recording is best effort. A failure to append to the trail is logged and the
// caller continues: refusing a moderation action because the audit table is
// full would turn an observability problem into an outage.
//
// Nothing updates a row: a record is appended once and read forever. The one
// exception is Delete, which exists so an operator can clear rows that should
// never have been written; the caller records that removal as a new event, so a
// purge is visible in the trail it just shortened.
package audit

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/liomix/astro-liomix-server/internal/platform/apperr"
	"github.com/liomix/astro-liomix-server/internal/platform/cursor"
	"github.com/liomix/astro-liomix-server/internal/platform/id"
	"github.com/liomix/astro-liomix-server/internal/platform/postgres"
)

// Page size bounds for List. They are constants because the audit endpoint has
// no per-deployment tuning of its own.
const (
	DefaultPageSize = 50
	MaxPageSize     = 200
)

// Actor is who performed an action and where they performed it from.
//
// ID is empty for actions that no account performed: the bootstrap of the first
// administrator, or a request authenticated with the legacy static token. Name
// is the username at the time of the action, kept verbatim so the row still
// reads correctly after the account is deleted or renamed.
type Actor struct {
	ID        string
	Name      string
	IP        string
	UserAgent string
}

// Event is one recorded action.
type Event struct {
	Actor Actor
	// Action is dotted and stable: "auth.login", "account.create",
	// "comment.moderate". A client filtering the trail matches on it.
	Action string
	// Resource names the kind of thing acted on ("account", "comment").
	Resource string
	// ResourceID identifies the thing, or is empty for actions that target
	// nothing in particular.
	ResourceID string
	// Detail carries action-specific context. It must never contain secrets:
	// the trail is readable by every administrator.
	Detail map[string]any
	// At is when the action happened. Zero means "now" at record time.
	At time.Time
}

// Entry is a stored event with the identifier the store assigned.
type Entry struct {
	ID string
	Event
}

// Recorder is the write side of the trail. Modules accept this interface rather
// than *Store so they can be tested without a database and used without one.
type Recorder interface {
	Record(ctx context.Context, e Event) error
}

// Store keeps the trail in PostgreSQL.
type Store struct {
	db  postgres.Executor
	ids id.Generator
	now func() time.Time
}

var _ Recorder = (*Store)(nil)

// NewStore returns a store running its statements on db, which is the pool in
// production and a rolled-back transaction in tests.
func NewStore(db postgres.Executor) *Store {
	return &Store{db: db, ids: id.RandomGenerator{}, now: time.Now}
}

// Record appends one event.
func (s *Store) Record(ctx context.Context, e Event) error {
	if e.Action == "" {
		return &apperr.ValidationError{Field: "action", Code: apperr.FieldRequired, Message: "is required"}
	}
	if e.Resource == "" {
		return &apperr.ValidationError{Field: "resource", Code: apperr.FieldRequired, Message: "is required"}
	}

	detail := e.Detail
	if detail == nil {
		detail = map[string]any{}
	}
	payload, err := json.Marshal(detail)
	if err != nil {
		return fmt.Errorf("encode audit detail: %w", err)
	}

	at := e.At
	if at.IsZero() {
		at = s.now()
	}
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO audit_logs (
			id, actor_id, actor_name, action, resource, resource_id,
			detail, ip, user_agent, created_at
		)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`,
		s.ids.New(),
		nullString(e.Actor.ID),
		e.Actor.Name,
		e.Action,
		e.Resource,
		e.ResourceID,
		string(payload),
		e.Actor.IP,
		e.Actor.UserAgent,
		at.UTC(),
	)
	if err != nil {
		return fmt.Errorf("record audit event %q: %w", e.Action, err)
	}
	return nil
}

// Query narrows a trail listing.
//
// It has no time-range filter: an operator investigating an incident knows who
// or what, and the cursor already makes reading backwards cheap. A range filter
// is easy to add later without changing the shape of this one.
type Query struct {
	Limit int
	// Cursor continues after the row it names. Nil starts at the newest entry.
	Cursor *cursor.Cursor
	// Action selects one exact action, e.g. "auth.login".
	Action string
	// ActorID selects one actor. It cannot select anonymous actions: those
	// have no id by definition.
	ActorID string
}

// Validate reports whether the query can be executed as written.
func (q Query) Validate() error {
	if q.Limit < 0 {
		return &apperr.ValidationError{Field: "limit", Code: apperr.FieldOutOfRange, Message: "must not be negative"}
	}
	if q.Cursor != nil {
		if err := q.Cursor.Validate(); err != nil {
			return err
		}
	}
	return nil
}

// List returns one page of the trail, newest first, plus the cursor that
// continues after it. The cursor is empty exactly when the page is the last
// one, which the store decides by asking for one row more than the page holds.
func (s *Store) List(ctx context.Context, q Query) ([]*Entry, string, error) {
	if err := q.Validate(); err != nil {
		return nil, "", fmt.Errorf("list audit log: %w", err)
	}
	limit := q.Limit
	switch {
	case limit <= 0:
		limit = DefaultPageSize
	case limit > MaxPageSize:
		limit = MaxPageSize
	}

	args := make([]any, 0, 4)
	where := " WHERE true"
	if q.Action != "" {
		args = append(args, q.Action)
		where += fmt.Sprintf(" AND action = $%d", len(args))
	}
	if q.ActorID != "" {
		args = append(args, q.ActorID)
		where += fmt.Sprintf(" AND actor_id = $%d", len(args))
	}
	if q.Cursor != nil {
		args = append(args, q.Cursor.CreatedAt.UTC(), q.Cursor.ID)
		where += fmt.Sprintf(" AND (created_at, id) < ($%d, $%d)", len(args)-1, len(args))
	}

	query := `SELECT id, actor_id, actor_name, action, resource, resource_id,
			detail, ip, user_agent, created_at
		FROM audit_logs` + where + ` ORDER BY created_at DESC, id DESC LIMIT ` +
		fmt.Sprintf("%d", limit+1)

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, "", fmt.Errorf("list audit log: %w", err)
	}
	defer rows.Close()

	entries := make([]*Entry, 0, limit+1)
	for rows.Next() {
		e, err := scanEntry(rows)
		if err != nil {
			return nil, "", fmt.Errorf("list audit log: %w", err)
		}
		entries = append(entries, e)
	}
	if err := rows.Err(); err != nil {
		return nil, "", fmt.Errorf("list audit log: %w", err)
	}

	if len(entries) <= limit {
		return entries, "", nil
	}
	entries = entries[:limit]
	last := entries[len(entries)-1]
	return entries, cursor.Encode(cursor.Cursor{CreatedAt: last.At, ID: last.ID}), nil
}

// Delete removes the named entries and reports how many rows went away.
//
// The trail is append-only by design, so this exists for one job: an operator
// clearing rows that should never have been written (a mis-click recorded with
// the wrong identity, a test entry left in production). It is not a retention
// mechanism — that belongs in a scheduled prune with a documented window.
func (s *Store) Delete(ctx context.Context, ids []string) (int, error) {
	if len(ids) == 0 {
		return 0, nil
	}

	args := make([]any, 0, len(ids))
	placeholders := make([]string, 0, len(ids))
	for _, id := range ids {
		args = append(args, id)
		placeholders = append(placeholders, fmt.Sprintf("$%d", len(args)))
	}

	result, err := s.db.ExecContext(ctx,
		`DELETE FROM audit_logs WHERE id IN (`+strings.Join(placeholders, ", ")+`)`, args...)
	if err != nil {
		return 0, fmt.Errorf("delete %d audit entries: %w", len(ids), err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("delete %d audit entries: %w", len(ids), err)
	}
	return int(affected), nil
}

// rowScanner is the part of *sql.Rows that scanEntry needs.
type rowScanner interface {
	Scan(dest ...any) error
}

// scanEntry reads one row of the projection List selects.
func scanEntry(row rowScanner) (*Entry, error) {
	var (
		e       Entry
		actorID sql.NullString
		detail  []byte
		at      time.Time
	)
	if err := row.Scan(
		&e.ID,
		&actorID,
		&e.Actor.Name,
		&e.Action,
		&e.Resource,
		&e.ResourceID,
		&detail,
		&e.Actor.IP,
		&e.Actor.UserAgent,
		&at,
	); err != nil {
		return nil, err
	}
	e.Actor.ID = actorID.String
	e.At = at.UTC()
	if len(detail) > 0 {
		if err := json.Unmarshal(detail, &e.Detail); err != nil {
			// A row whose detail is not JSON was written by something other
			// than this package. Keep the entry — who did what matters more
			// than the context — and leave Detail empty rather than failing
			// the whole page.
			e.Detail = map[string]any{}
		}
	}
	if e.Detail == nil {
		e.Detail = map[string]any{}
	}
	return &e, nil
}

// nullString maps the domain's empty string to SQL NULL, so "no account" is an
// absence rather than a row that claims to have been performed by id "".
func nullString(value string) any {
	if value == "" {
		return nil
	}
	return value
}
