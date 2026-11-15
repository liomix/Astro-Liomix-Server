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
const commentColumns = `id, post_slug, parent_id, author_name, author_email, author_website, body, status, delete_token_hash, created_at, updated_at, author_is_owner`

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
		&c.AuthorIsOwner,
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
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)`,
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
		c.AuthorIsOwner,
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
	// The slug filter is optional: a thread listing always names a post, while
	// the moderation queue lists every post at once. Building the WHERE clause
	// instead of always emitting `post_slug = $1` is what lets one query serve
	// both — an empty slug used to match nothing, which read as "no pending
	// comments" no matter what was in the table.
	//
	// The filter itself comes from commentFilter so counting and listing cannot
	// disagree about what a keyword means: two WHERE clauses that drifted apart
	// would make the tab counts describe a different set than the rows below.
	where, args := commentFilter(q)

	after := ""
	if q.Cursor != nil {
		args = append(args, q.Cursor.CreatedAt.UTC(), q.Cursor.ID)
		after = fmt.Sprintf(" AND (created_at, id) < ($%d, $%d)", len(args)-1, len(args))
	}

	query := `SELECT ` + commentColumns + ` FROM comments` + where + after +
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

// commentFilter builds the WHERE clause a listing and a count share, plus the
// arguments it needs, numbered from $1.
//
// Callers that add parameters afterwards must append to the returned slice, so
// the placeholders keep counting up.
func commentFilter(q Query) (string, []any) {
	args := make([]any, 0, 4)
	where := " WHERE true"

	if q.PostSlug != "" {
		args = append(args, q.PostSlug)
		where += fmt.Sprintf(" AND post_slug = $%d", len(args))
	}
	if q.Status != "" {
		args = append(args, string(q.Status))
		where += fmt.Sprintf(" AND status = $%d", len(args))
	}
	if q.Keyword != "" {
		// One parameter reused by four columns: the keyword means the same
		// thing in each of them, and four copies of it would be four chances
		// for the placeholders to drift.
		args = append(args, "%"+escapeLikePattern(q.Keyword)+"%")
		column := fmt.Sprintf("ILIKE $%d", len(args))
		where += " AND (author_name " + column +
			" OR author_email " + column +
			" OR body " + column +
			" OR post_slug " + column + ")"
	}
	return where, args
}

// escapeLikePattern neutralises the wildcards in a user's search text, so
// searching for `50%` looks for the literal percent sign instead of matching
// every comment. PostgreSQL's LIKE treats backslash as the escape character by
// default, so it has to be doubled first.
func escapeLikePattern(value string) string {
	replacer := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return replacer.Replace(value)
}

// CountByStatus returns how many comments sit in each moderation state.
//
// One aggregate for all states rather than four counts: the console renders
// them together, and four round trips would also let a comment approved
// between two of them appear in neither total.
func (r *PostgresRepository) CountByStatus(ctx context.Context, q Query) (map[CommentStatus]int, error) {
	// Status is deliberately dropped: the answer is the count of every state.
	where, args := commentFilter(Query{PostSlug: q.PostSlug, Keyword: q.Keyword})

	rows, err := r.db.QueryContext(ctx,
		`SELECT status, count(*) FROM comments`+where+` GROUP BY status`, args...)
	if err != nil {
		return nil, fmt.Errorf("count comments by status: %w", err)
	}
	defer rows.Close()

	counts := make(map[CommentStatus]int, 4)
	for rows.Next() {
		var status CommentStatus
		var count int
		if err := rows.Scan(&status, &count); err != nil {
			return nil, fmt.Errorf("count comments by status: %w", err)
		}
		counts[status] = count
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("count comments by status: %w", err)
	}
	return counts, nil
}

// CountSince counts comments created at or after since, in any state.
func (r *PostgresRepository) CountSince(ctx context.Context, since time.Time) (int, error) {
	var total int
	err := r.db.QueryRowContext(ctx,
		`SELECT count(*) FROM comments WHERE created_at >= $1`, since.UTC()).Scan(&total)
	if err != nil {
		return 0, fmt.Errorf("count comments since %s: %w", since.Format(time.RFC3339), err)
	}
	return total, nil
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

// UpdateStatusMany applies one status to several comments in one statement.
//
// Missing IDs are not an error: the caller is an operator working through a
// queue, and a row deleted by somebody else since the page rendered is a normal
// race, not a failed request. The count says how many rows actually changed.
func (r *PostgresRepository) UpdateStatusMany(ctx context.Context, commentIDs []string, status CommentStatus, at time.Time) (int, error) {
	if len(commentIDs) == 0 {
		return 0, nil
	}
	placeholders, args := idPlaceholders(commentIDs)
	args = append(args, string(status), at.UTC())

	query := `UPDATE comments SET status = $` + fmt.Sprint(len(commentIDs)+1) +
		`, updated_at = $` + fmt.Sprint(len(commentIDs)+2) +
		` WHERE id IN (` + placeholders + `)`

	result, err := r.db.ExecContext(ctx, query, args...)
	if err != nil {
		return 0, fmt.Errorf("update %d comments: %w", len(commentIDs), err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("update %d comments: %w", len(commentIDs), err)
	}
	return int(affected), nil
}

// DeleteMany removes several comments and, through the foreign key, their
// replies. The count is the number of named rows that existed.
func (r *PostgresRepository) DeleteMany(ctx context.Context, commentIDs []string) (int, error) {
	if len(commentIDs) == 0 {
		return 0, nil
	}
	placeholders, args := idPlaceholders(commentIDs)

	result, err := r.db.ExecContext(ctx,
		`DELETE FROM comments WHERE id IN (`+placeholders+`)`, args...)
	if err != nil {
		return 0, fmt.Errorf("delete %d comments: %w", len(commentIDs), err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("delete %d comments: %w", len(commentIDs), err)
	}
	return int(affected), nil
}

// idPlaceholders numbers the IDs from $1 and returns the argument slice that
// matches, so a caller can append its own parameters afterwards.
//
// The IDs travel as parameters like every other user value; only the count of
// placeholders is interpolated, and that count is bounded by the service.
func idPlaceholders(ids []string) (string, []any) {
	args := make([]any, 0, len(ids))
	placeholders := make([]string, 0, len(ids))
	for _, id := range ids {
		args = append(args, id)
		placeholders = append(placeholders, fmt.Sprintf("$%d", len(args)))
	}
	return strings.Join(placeholders, ", "), args
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
