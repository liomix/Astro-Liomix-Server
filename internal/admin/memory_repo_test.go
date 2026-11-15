package admin

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"sort"
	"sync"
	"time"

	"github.com/liomix/astro-liomix-server/internal/platform/apperr"
	"github.com/liomix/astro-liomix-server/internal/platform/audit"
	"github.com/liomix/astro-liomix-server/internal/platform/cursor"
)

// memoryRepository is the store the unit tests run against.
//
// It lives in a _test.go file on purpose: production only ever talks to
// PostgreSQL, and a shared in-memory adapter would be a second implementation
// of the port for the sole benefit of tests. It reproduces the behaviour the
// service depends on — unique usernames, cascade on delete, absolute expiry —
// so a test that passes here is testing the rules rather than the mock.
type memoryRepository struct {
	mu       sync.Mutex
	accounts map[string]*Account
	sessions map[string]*Session
}

var _ Repository = (*memoryRepository)(nil)

func newMemoryRepository() *memoryRepository {
	return &memoryRepository{
		accounts: make(map[string]*Account),
		sessions: make(map[string]*Session),
	}
}

func (r *memoryRepository) CreateAccount(ctx context.Context, a *Account) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()

	if _, exists := r.accounts[a.ID]; exists {
		return apperr.ErrConflict
	}
	for _, existing := range r.accounts {
		if existing.Username == a.Username {
			return apperr.ErrConflict
		}
	}
	r.accounts[a.ID] = a.Clone()
	return nil
}

func (r *memoryRepository) GetAccount(ctx context.Context, accountID string) (*Account, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()

	a, ok := r.accounts[accountID]
	if !ok {
		return nil, apperr.ErrNotFound
	}
	return a.Clone(), nil
}

func (r *memoryRepository) GetAccountByUsername(ctx context.Context, username string) (*Account, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()

	for _, a := range r.accounts {
		if a.Username == username {
			return a.Clone(), nil
		}
	}
	return nil, apperr.ErrNotFound
}

func (r *memoryRepository) ListAccounts(ctx context.Context, q AccountQuery) ([]*Account, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()

	matched := make([]*Account, 0, len(r.accounts))
	for _, a := range r.accounts {
		if q.Status != "" && a.Status != q.Status {
			continue
		}
		if q.Cursor != nil {
			if a.CreatedAt.After(q.Cursor.CreatedAt) {
				continue
			}
			if a.CreatedAt.Equal(q.Cursor.CreatedAt) && a.ID >= q.Cursor.ID {
				continue
			}
		}
		matched = append(matched, a.Clone())
	}
	sort.Slice(matched, func(i, j int) bool {
		if !matched[i].CreatedAt.Equal(matched[j].CreatedAt) {
			return matched[i].CreatedAt.After(matched[j].CreatedAt)
		}
		return matched[i].ID > matched[j].ID
	})
	if q.Limit > 0 && q.Limit < len(matched) {
		matched = matched[:q.Limit]
	}
	return matched, nil
}

func (r *memoryRepository) UpdateAccount(ctx context.Context, a *Account) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()

	if _, ok := r.accounts[a.ID]; !ok {
		return apperr.ErrNotFound
	}
	for id, other := range r.accounts {
		if id != a.ID && other.Username == a.Username {
			return apperr.ErrConflict
		}
	}
	r.accounts[a.ID] = a.Clone()
	return nil
}

func (r *memoryRepository) UpdateLastLogin(ctx context.Context, accountID string, at time.Time) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()

	a, ok := r.accounts[accountID]
	if !ok {
		return apperr.ErrNotFound
	}
	updated := a.Clone()
	updated.LastLoginAt = at
	r.accounts[accountID] = updated
	return nil
}

func (r *memoryRepository) DeleteAccount(ctx context.Context, accountID string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()

	if _, ok := r.accounts[accountID]; !ok {
		return apperr.ErrNotFound
	}
	delete(r.accounts, accountID)
	for id, s := range r.sessions {
		if s.AccountID == accountID {
			delete(r.sessions, id)
		}
	}
	return nil
}

func (r *memoryRepository) CountAccounts(ctx context.Context, status AccountStatus) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()

	count := 0
	for _, a := range r.accounts {
		if status == "" || a.Status == status {
			count++
		}
	}
	return count, nil
}

func (r *memoryRepository) CreateSession(ctx context.Context, s *Session) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()

	if _, exists := r.sessions[s.ID]; exists {
		return apperr.ErrConflict
	}
	for _, existing := range r.sessions {
		if existing.TokenHash == s.TokenHash {
			return apperr.ErrConflict
		}
	}
	r.sessions[s.ID] = s.Clone()
	return nil
}

func (r *memoryRepository) GetSessionByTokenHash(ctx context.Context, tokenHash string) (*Session, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()

	for _, s := range r.sessions {
		if s.TokenHash == tokenHash {
			return s.Clone(), nil
		}
	}
	return nil, apperr.ErrNotFound
}

func (r *memoryRepository) GetSession(ctx context.Context, sessionID string) (*Session, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()

	s, ok := r.sessions[sessionID]
	if !ok {
		return nil, apperr.ErrNotFound
	}
	return s.Clone(), nil
}

func (r *memoryRepository) TouchSession(ctx context.Context, sessionID string, at time.Time) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()

	s, ok := r.sessions[sessionID]
	if !ok || !s.RevokedAt.IsZero() {
		return apperr.ErrNotFound
	}
	updated := s.Clone()
	updated.LastSeenAt = at
	r.sessions[sessionID] = updated
	return nil
}

func (r *memoryRepository) RevokeSession(ctx context.Context, sessionID string, at time.Time) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()

	s, ok := r.sessions[sessionID]
	if !ok || !s.RevokedAt.IsZero() {
		return apperr.ErrNotFound
	}
	updated := s.Clone()
	updated.RevokedAt = at
	r.sessions[sessionID] = updated
	return nil
}

func (r *memoryRepository) RevokeAccountSessions(ctx context.Context, accountID string, at time.Time) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()

	for id, s := range r.sessions {
		if s.AccountID == accountID && s.RevokedAt.IsZero() {
			updated := s.Clone()
			updated.RevokedAt = at
			r.sessions[id] = updated
		}
	}
	return nil
}

func (r *memoryRepository) ListSessions(ctx context.Context, accountID string, at time.Time) ([]*Session, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()

	sessions := make([]*Session, 0, 4)
	for _, s := range r.sessions {
		if s.AccountID != accountID || !s.Active(at) {
			continue
		}
		sessions = append(sessions, s.Clone())
	}
	sort.Slice(sessions, func(i, j int) bool {
		if !sessions[i].CreatedAt.Equal(sessions[j].CreatedAt) {
			return sessions[i].CreatedAt.After(sessions[j].CreatedAt)
		}
		return sessions[i].ID > sessions[j].ID
	})
	return sessions, nil
}

func (r *memoryRepository) DeleteExpiredSessions(ctx context.Context, before time.Time) (int64, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()

	var pruned int64
	for id, s := range r.sessions {
		if s.ExpiresAt.Before(before) {
			delete(r.sessions, id)
			pruned++
		}
	}
	return pruned, nil
}

// memoryAudit records and lists the trail without a database. It implements
// both admin.AuditReader and audit.Recorder, which is what cmd/server does with
// *audit.Store.
type memoryAudit struct {
	mu     sync.Mutex
	events []*audit.Entry
	next   int
}

var (
	_ audit.Recorder = (*memoryAudit)(nil)
	_ AuditReader    = (*memoryAudit)(nil)
)

func newMemoryAudit() *memoryAudit { return &memoryAudit{} }

func (m *memoryAudit) Record(ctx context.Context, e audit.Event) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()

	m.next++
	if e.At.IsZero() {
		e.At = time.Now()
	}
	e.At = e.At.UTC().Truncate(time.Microsecond)
	m.events = append(m.events, &audit.Entry{ID: fmt.Sprintf("audit-%03d", m.next), Event: e})
	return nil
}

func (m *memoryAudit) List(ctx context.Context, q audit.Query) ([]*audit.Entry, string, error) {
	if err := ctx.Err(); err != nil {
		return nil, "", err
	}
	m.mu.Lock()
	defer m.mu.Unlock()

	limit := q.Limit
	switch {
	case limit <= 0:
		limit = audit.DefaultPageSize
	case limit > audit.MaxPageSize:
		limit = audit.MaxPageSize
	}

	matched := make([]*audit.Entry, 0, len(m.events))
	for _, e := range m.events {
		if q.Action != "" && e.Action != q.Action {
			continue
		}
		if q.ActorID != "" && e.Actor.ID != q.ActorID {
			continue
		}
		if q.Cursor != nil {
			if e.At.After(q.Cursor.CreatedAt) {
				continue
			}
			if e.At.Equal(q.Cursor.CreatedAt) && e.ID >= q.Cursor.ID {
				continue
			}
		}
		matched = append(matched, e)
	}
	sort.Slice(matched, func(i, j int) bool {
		if !matched[i].At.Equal(matched[j].At) {
			return matched[i].At.After(matched[j].At)
		}
		return matched[i].ID > matched[j].ID
	})
	if len(matched) <= limit {
		return matched, "", nil
	}
	matched = matched[:limit]
	last := matched[len(matched)-1]
	return matched, cursor.Encode(cursor.Cursor{CreatedAt: last.At, ID: last.ID}), nil
}

// eventsByAction counts recorded events with the given action.
func (m *memoryAudit) eventsByAction(action string) []*audit.Entry {
	m.mu.Lock()
	defer m.mu.Unlock()

	out := make([]*audit.Entry, 0, len(m.events))
	for _, e := range m.events {
		if e.Action == action {
			out = append(out, e)
		}
	}
	return out
}

// fakeClock is a manually advanced time source, so a test can cross a session
// expiry without sleeping.
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func newFakeClock() *fakeClock {
	return &fakeClock{t: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)}
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// seqIDs mints predictable identifiers so assertions can name them.
type seqIDs struct {
	mu sync.Mutex
	n  int
}

func (g *seqIDs) New() string {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.n++
	return fmt.Sprintf("id-%03d", g.n)
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError}))
}
