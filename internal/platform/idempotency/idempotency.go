// Package idempotency makes a retried write return the first outcome instead of
// running a second time.
//
// The problem it solves is not "the client clicked twice" — the client disables
// the button for that. It is "the response never arrived": the write committed,
// the network dropped, and the client has no way to know whether retrying
// creates a duplicate or recovers the result. A key the client generates once
// per attempt turns the second request into a lookup.
//
// The key lives in a table with a unique constraint rather than in a map or a
// cache, and that is the whole design: two replicas handling the retry have to
// make the same decision, and only shared state can give them one. A process
// local store passes every single-instance test and duplicates exactly when a
// second replica exists.
package idempotency

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/liomix/astro-liomix-server/internal/platform/postgres"
)

// Outcome is what Claim decided about the key.
type Outcome int

const (
	// Claimed means the caller owns the key and must execute the work.
	Claimed Outcome = iota
	// Replayed means the work already completed; Result carries the recorded
	// status and body, which the caller should return verbatim.
	Replayed
	// InProgress means another request owns the key and has not finished. The
	// caller should answer 409 and let the client retry shortly.
	InProgress
	// Mismatch means the key was reused with a different request body, which is
	// a client bug: replaying the old result would hide it.
	Mismatch
)

// Result is the answer to a claim.
type Result struct {
	Outcome Outcome
	// StatusCode and Body are set only for Replayed.
	StatusCode int
	Body       []byte
}

// Store keeps idempotency records in PostgreSQL.
type Store struct {
	db  postgres.Executor
	now func() time.Time
}

// NewStore returns a store running its statements on db, which is the pool in
// production and a rolled-back transaction in tests.
func NewStore(db postgres.Executor) *Store {
	return &Store{db: db, now: time.Now}
}

// Claim registers the caller as the owner of key, or reports what happened to
// the request that got there first.
//
// The insert is the lock. `ON CONFLICT DO NOTHING` plus the row count answers
// "did I win?" in one atomic statement; a select-then-insert would let two
// concurrent retries both see an empty table and both proceed.
func (s *Store) Claim(ctx context.Context, scope, key, requestHash string) (Result, error) {
	// Two attempts: the retry only matters when the record is pruned between
	// the insert that lost and the read that follows it, which can happen at
	// exactly the retention boundary and nowhere else.
	for attempt := 0; attempt < 2; attempt++ {
		result, retry, err := s.claim(ctx, scope, key, requestHash)
		if err != nil {
			return Result{}, err
		}
		if !retry {
			return result, nil
		}
	}
	return Result{Outcome: Claimed}, nil
}

// claim makes one attempt. retry reports that the row disappeared underneath
// the read, so the insert has to be tried again.
func (s *Store) claim(ctx context.Context, scope, key, requestHash string) (result Result, retry bool, err error) {
	insert, err := s.db.ExecContext(ctx, `
		INSERT INTO idempotency_keys (scope, key, request_hash, created_at)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (scope, key) DO NOTHING`,
		scope, key, requestHash, s.now().UTC())
	if err != nil {
		return Result{}, false, fmt.Errorf("claim idempotency key: %w", err)
	}
	affected, err := insert.RowsAffected()
	if err != nil {
		return Result{}, false, fmt.Errorf("claim idempotency key: %w", err)
	}
	if affected == 1 {
		return Result{Outcome: Claimed}, false, nil
	}

	var (
		storedHash string
		statusCode sql.NullInt64
		body       []byte
	)
	err = s.db.QueryRowContext(ctx, `
		SELECT request_hash, status_code, response
		FROM idempotency_keys
		WHERE scope = $1 AND key = $2`,
		scope, key).Scan(&storedHash, &statusCode, &body)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return Result{}, true, nil
	case err != nil:
		return Result{}, false, fmt.Errorf("read idempotency key: %w", err)
	case storedHash != requestHash:
		return Result{Outcome: Mismatch}, false, nil
	case !statusCode.Valid:
		return Result{Outcome: InProgress}, false, nil
	default:
		return Result{Outcome: Replayed, StatusCode: int(statusCode.Int64), Body: body}, false, nil
	}
}

// Complete records the outcome so a later retry can replay it.
func (s *Store) Complete(ctx context.Context, scope, key string, statusCode int, body []byte) error {
	result, err := s.db.ExecContext(ctx, `
		UPDATE idempotency_keys
		SET status_code = $3, response = $4
		WHERE scope = $1 AND key = $2`,
		scope, key, statusCode, body)
	if err != nil {
		return fmt.Errorf("complete idempotency key: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("complete idempotency key: %w", err)
	}
	if affected == 0 {
		// The row disappeared while the request ran (pruned). The response has
		// already been sent, so this is a warning for the log, not a failure the
		// client should see.
		return fmt.Errorf("complete idempotency key %q: %w", key, sql.ErrNoRows)
	}
	return nil
}

// Release drops the claim, so a failed attempt does not leave the key blocked
// until it expires. It is called when the work produced no lasting effect.
func (s *Store) Release(ctx context.Context, scope, key string) error {
	if _, err := s.db.ExecContext(ctx,
		`DELETE FROM idempotency_keys WHERE scope = $1 AND key = $2`,
		scope, key); err != nil {
		return fmt.Errorf("release idempotency key: %w", err)
	}
	return nil
}

// Prune deletes records older than the retention window and reports how many
// went away. Without it the table outgrows the data it protects.
func (s *Store) Prune(ctx context.Context, retention time.Duration) (int64, error) {
	if retention <= 0 {
		retention = 24 * time.Hour
	}
	cutoff := s.now().UTC().Add(-retention)
	result, err := s.db.ExecContext(ctx,
		`DELETE FROM idempotency_keys WHERE created_at < $1`, cutoff)
	if err != nil {
		return 0, fmt.Errorf("prune idempotency keys: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("prune idempotency keys: %w", err)
	}
	return affected, nil
}
