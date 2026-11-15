package like

import (
	"context"
	"fmt"
	"strings"

	"github.com/liomix/astro-liomix-server/internal/platform/postgres"
)

// PostgresRepository keeps likes in PostgreSQL.
//
// The rules the port asks for — adding twice leaves one row, and the count is
// whatever is true right now — are enforced by the database rather than by a
// read-then-write in Go: the insert is an ON CONFLICT DO NOTHING whose row count
// answers "did this call create it?", and the tally is one aggregate. Two
// replicas liking the same post at once therefore cannot produce a duplicate or
// a count that disagrees with the rows.
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

// Add stores l, reporting whether this call created the row.
func (r *PostgresRepository) Add(ctx context.Context, l Like) (bool, error) {
	result, err := r.db.ExecContext(ctx, `
		INSERT INTO post_likes (post_slug, visitor_id, created_at)
		VALUES ($1, $2, $3)
		ON CONFLICT (post_slug, visitor_id) DO NOTHING`,
		l.PostSlug, l.VisitorID, l.CreatedAt.UTC())
	if err != nil {
		return false, fmt.Errorf("insert like on %q: %w", l.PostSlug, err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("count inserted likes on %q: %w", l.PostSlug, err)
	}
	// ON CONFLICT DO NOTHING reports zero rows when the like was already there,
	// which is exactly the signal the handler turns into 200 instead of 201.
	return affected > 0, nil
}

// Remove deletes one like, reporting whether the row existed.
func (r *PostgresRepository) Remove(ctx context.Context, postSlug, visitorID string) (bool, error) {
	result, err := r.db.ExecContext(ctx,
		`DELETE FROM post_likes WHERE post_slug = $1 AND visitor_id = $2`,
		postSlug, visitorID)
	if err != nil {
		return false, fmt.Errorf("delete like on %q: %w", postSlug, err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("count deleted likes on %q: %w", postSlug, err)
	}
	return affected > 0, nil
}

// Tally returns the count per post and whether the query's visitor liked it.
func (r *PostgresRepository) Tally(ctx context.Context, q Query) ([]Tally, error) {
	if len(q.PostSlugs) == 0 {
		return []Tally{}, nil
	}

	// $1 is the visitor, so an empty one simply matches nothing: the count still
	// comes back and `liked` is false for everyone, which is what an anonymous
	// read should say.
	args := make([]any, 0, len(q.PostSlugs)+1)
	args = append(args, q.VisitorID)
	placeholders := make([]string, 0, len(q.PostSlugs))
	for _, slug := range q.PostSlugs {
		args = append(args, slug)
		placeholders = append(placeholders, fmt.Sprintf("$%d", len(args)))
	}

	rows, err := r.db.QueryContext(ctx, `
		SELECT post_slug,
		       count(*)                             AS likes,
		       coalesce(bool_or(visitor_id = $1), false) AS liked
		FROM post_likes
		WHERE post_slug IN (`+strings.Join(placeholders, ", ")+`)
		GROUP BY post_slug`, args...)
	if err != nil {
		return nil, fmt.Errorf("read like tallies: %w", err)
	}
	defer rows.Close()

	tallies := make([]Tally, 0, len(q.PostSlugs))
	for rows.Next() {
		var t Tally
		if err := rows.Scan(&t.Slug, &t.Count, &t.Liked); err != nil {
			return nil, fmt.Errorf("read like tallies: %w", err)
		}
		tallies = append(tallies, t)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read like tallies: %w", err)
	}
	return tallies, nil
}
