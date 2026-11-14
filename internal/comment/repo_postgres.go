package comment

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/liomix/astro-liomix-server/internal/platform/apperr"
	"github.com/liomix/astro-liomix-server/internal/platform/postgres"
)

// PostgresRepository stores comments in PostgreSQL.
//
// The thread lives in the table rather than in memory: deleting a comment is
// one DELETE and the ON DELETE CASCADE foreign key takes the replies with it,
// so no code here walks the tree. That is the whole reason the port asks for a
// transitive delete instead of a single-row one.
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

// commentColumns is the projection every read shares, in the order
// scanComment expects.
const commentColumns = `id, post_slug, parent_id, author_name, author_email, author_website, body, status, delete_token_hash, created_at, updated_at`

// rowScanner is the part of *sql.Row and *sql.Rows that scanComment needs.
type rowScanner interface {
	Scan(dest ...any) error
}

// scanComment reads one row of commentColumns.
func scanComment(row rowScanner) (*Comment, error) {
	c := &Comment{}
	// Two columns are nullable: a top-level comment has no parent, and a
	// comment stored without a credential (an import) has no digest. NULL reads
	// back as the empty string, which is what the domain uses for "absent" —
	// the database is the only place that distinguishes them.
	var parentID, deleteTokenHash sql.NullString
	if err := row.Scan(
		&c.ID,
		&c.PostSlug,
		&parentID,
		&c.Author.Name,
		&c.Author.Email,
		&c.Author.Website,
		&c.Body,
		&c.Status,
		&deleteTokenHash,
		&c.CreatedAt,
		&c.UpdatedAt,
	); err != nil {
		return nil, err
	}
	c.ParentID = parentID.String
	c.DeleteTokenHash = deleteTokenHash.String
	// timestamptz comes back in the connection's zone (usually the server's).
	// The entities and the API contract are UTC, and a deployment moving
	// timezone should not change the strings clients receive, so normalise here
	// rather than at the edge. PostgreSQL keeps microseconds, so nanoseconds
	// are already gone — that is the storage precision, not a loss to hide.
	c.CreatedAt = c.CreatedAt.UTC()
	c.UpdatedAt = c.UpdatedAt.UTC()
	return c, nil
}

// Create stores a new comment.
func (r *PostgresRepository) Create(ctx context.Context, c *Comment) error {
	if c == nil {
		return &apperr.ValidationError{Field: "comment", Message: "must not be nil"}
	}

	// The driver needs an untyped nil for SQL NULL, not an empty string: the
	// foreign key would otherwise look for a comment whose id is "".
	var parentID any
	if c.ParentID != "" {
		parentID = c.ParentID
	}

	// Same for the digest: "no credential" is absence, not a zero-length hash,
	// and a NULL says so without the reader having to know that "" means none.
	var deleteTokenHash any
	if c.DeleteTokenHash != "" {
		deleteTokenHash = c.DeleteTokenHash
	}

	_, err := r.db.ExecContext(ctx, `
		INSERT INTO comments (`+commentColumns+`)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)`,
		c.ID,
		c.PostSlug,
		parentID,
		c.Author.Name,
		c.Author.Email,
		c.Author.Website,
		c.Body,
		string(c.Status),
		deleteTokenHash,
		c.CreatedAt.UTC(),
		c.UpdatedAt.UTC(),
	)
	if err != nil {
		switch {
		case postgres.IsUniqueViolation(err):
			return fmt.Errorf("create comment %q: %w", c.ID, apperr.ErrConflict)
		case postgres.IsForeignKeyViolation(err):
			// The service checks the parent first, but a concurrent delete can
			// still win the race. Reporting it as a bad parentId keeps the
			// client from seeing a 500 for something it can fix.
			return fmt.Errorf("create comment %q: %w", c.ID, &apperr.ValidationError{
				Field:   "parentId",
				Message: "refers to a comment that no longer exists",
			})
		}
		return fmt.Errorf("create comment %q: %w", c.ID, err)
	}
	return nil
}

// Get returns the comment with the given ID.
func (r *PostgresRepository) Get(ctx context.Context, commentID string) (*Comment, error) {
	row := r.db.QueryRowContext(ctx,
		`SELECT `+commentColumns+` FROM comments WHERE id = $1`, commentID)
	c, err := scanComment(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("get comment %q: %w", commentID, apperr.ErrNotFound)
	}
	if err != nil {
		return nil, fmt.Errorf("get comment %q: %w", commentID, err)
	}
	return c, nil
}

// List returns the comments matching q, newest first.
//
// The page is bounded by a cursor rather than an offset. `(created_at, id) <
// ($n, $n+1)` is a single seek into comments_thread_status_idx, so reading the
// tenth page costs what reading the first one costs — and a comment posted
// while the reader pages does not shift the boundary under them. An OFFSET
// would re-scan everything above the page and repeat a row whenever the thread
// grew between two requests.
func (r *PostgresRepository) List(ctx context.Context, q Query) ([]*Comment, error) {
	args := []any{q.PostSlug}
	status := ""
	if q.Status != "" {
		args = append(args, string(q.Status))
		status = fmt.Sprintf(" AND status = $%d", len(args))
	}

	after := ""
	if q.Cursor != nil {
		args = append(args, q.Cursor.CreatedAt.UTC(), q.Cursor.ID)
		after = fmt.Sprintf(" AND (created_at, id) < ($%d, $%d)", len(args)-1, len(args))
	}

	query := `SELECT ` + commentColumns + ` FROM comments WHERE post_slug = $1` + status + after +
		` ORDER BY created_at DESC, id DESC`
	// Limit is a bound the service already validated against MaxPageSize, so it
	// is interpolated as an integer rather than sent as a parameter that would
	// change the plan's parameterisation for no benefit. Every user-supplied
	// value above still travels as a parameter.
	if q.Limit > 0 {
		query += fmt.Sprintf(" LIMIT %d", q.Limit)
	}

	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list comments for %q: %w", q.PostSlug, err)
	}
	defer rows.Close()

	comments := make([]*Comment, 0, 16)
	for rows.Next() {
		c, err := scanComment(rows)
		if err != nil {
			return nil, fmt.Errorf("list comments for %q: %w", q.PostSlug, err)
		}
		comments = append(comments, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list comments for %q: %w", q.PostSlug, err)
	}
	return comments, nil
}

// CountByPost returns how many comments each requested post has, honoring the
// status filter in q.
func (r *PostgresRepository) CountByPost(ctx context.Context, q CountQuery) (map[string]int, error) {
	if len(q.PostSlugs) == 0 {
		return map[string]int{}, nil
	}

	args := make([]any, 0, len(q.PostSlugs)+1)
	placeholders := make([]string, 0, len(q.PostSlugs))
	for _, slug := range q.PostSlugs {
		args = append(args, slug)
		placeholders = append(placeholders, fmt.Sprintf("$%d", len(args)))
	}

	// One aggregate answers the whole batch. Spelling the slugs out as an IN
	// list instead of = ANY($n) keeps the driver from having to guess an array
	// type, and the batch is capped at MaxCountSlugs so the list stays short.
	query := `SELECT post_slug, count(*) FROM comments WHERE post_slug IN (` +
		strings.Join(placeholders, ", ") + `)`
	if q.Status != "" {
		args = append(args, string(q.Status))
		query += fmt.Sprintf(" AND status = $%d", len(args))
	}
	query += ` GROUP BY post_slug`

	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("count comments: %w", err)
	}
	defer rows.Close()

	counts := make(map[string]int, len(q.PostSlugs))
	for rows.Next() {
		var slug string
		var count int
		if err := rows.Scan(&slug, &count); err != nil {
			return nil, fmt.Errorf("count comments: %w", err)
		}
		counts[slug] = count
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("count comments: %w", err)
	}
	return counts, nil
}

// UpdateStatus stores a new moderation state on the comment.
func (r *PostgresRepository) UpdateStatus(ctx context.Context, commentID string, status CommentStatus, at time.Time) (*Comment, error) {
	row := r.db.QueryRowContext(ctx, `
		UPDATE comments SET status = $2, updated_at = $3
		WHERE id = $1
		RETURNING `+commentColumns,
		commentID, string(status), at.UTC())

	c, err := scanComment(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("update comment %q: %w", commentID, apperr.ErrNotFound)
	}
	if err != nil {
		return nil, fmt.Errorf("update comment %q: %w", commentID, err)
	}
	return c, nil
}

// Delete removes the comment and every reply beneath it.
func (r *PostgresRepository) Delete(ctx context.Context, commentID string) error {
	result, err := r.db.ExecContext(ctx, `DELETE FROM comments WHERE id = $1`, commentID)
	if err != nil {
		return fmt.Errorf("delete comment %q: %w", commentID, err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("delete comment %q: %w", commentID, err)
	}
	if affected == 0 {
		return fmt.Errorf("delete comment %q: %w", commentID, apperr.ErrNotFound)
	}
	return nil
}
