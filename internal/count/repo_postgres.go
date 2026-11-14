package count

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/liomix/astro-liomix-server/internal/platform/postgres"
)

// PostgresRepository keeps the counters in PostgreSQL.
//
// The two rules the port asks for — one call adds exactly one view, and adds one
// visitor only when that visitor is new — are enforced by the database instead
// of by a process-wide lock: the visitor insert is an ON CONFLICT DO NOTHING
// whose row count says whether the visitor was new, and the counter update is a
// single atomic upsert. Both live in one statement, so two replicas writing at
// once cannot double-count a visitor.
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

// RecordVisit applies v and returns the resulting snapshot.
func (r *PostgresRepository) RecordVisit(ctx context.Context, v Visit) (SiteStats, error) {
	var stats SiteStats
	at := v.At.UTC()

	// One statement instead of an explicit transaction: the visitor row and the
	// counter bump have to land together, and a data-modifying CTE gives that
	// without Go holding a transaction open. The CTE inserts at most one row —
	// and only when the visit carries an ID — so counting its output is the
	// same "was this visitor new?" signal RowsAffected used to provide, without
	// a second round trip that a concurrent caller could interleave with.
	err := r.db.QueryRowContext(ctx, `
		WITH new_visitor AS (
			INSERT INTO site_visitors (visitor_id, first_seen)
			SELECT $1::text, $2::timestamptz
			WHERE $1::text <> ''
			ON CONFLICT (visitor_id) DO NOTHING
			RETURNING 1
		)
		INSERT INTO site_counters (id, views, visitors, updated_at)
		VALUES (1, 1, (SELECT count(*) FROM new_visitor), $2::timestamptz)
		ON CONFLICT (id) DO UPDATE
		SET views      = site_counters.views + 1,
		    visitors   = site_counters.visitors + EXCLUDED.visitors,
		    updated_at = EXCLUDED.updated_at
		RETURNING views, visitors, updated_at`,
		v.VisitorID, at).Scan(&stats.Views, &stats.Visitors, &stats.UpdatedAt)
	if err != nil {
		return SiteStats{}, fmt.Errorf("record visit: %w", err)
	}
	stats.UpdatedAt = stats.UpdatedAt.UTC()
	return stats, nil
}

// SiteStats returns the current counters.
func (r *PostgresRepository) SiteStats(ctx context.Context) (SiteStats, error) {
	var stats SiteStats
	err := r.db.QueryRowContext(ctx,
		`SELECT views, visitors, updated_at FROM site_counters WHERE id = 1`).
		Scan(&stats.Views, &stats.Visitors, &stats.UpdatedAt)
	// The migration seeds the row, so a missing one means somebody deleted it
	// by hand. Reporting zeros keeps the blog running instead of turning the
	// footer into a 500.
	if errors.Is(err, sql.ErrNoRows) {
		return SiteStats{}, nil
	}
	if err != nil {
		return SiteStats{}, fmt.Errorf("read site stats: %w", err)
	}
	// Same reason as in the comment repository: the wire format is UTC
	// regardless of the database's or the server's zone.
	stats.UpdatedAt = stats.UpdatedAt.UTC()
	return stats, nil
}

// RecordPostView adds one read to the post and returns its new total.
func (r *PostgresRepository) RecordPostView(ctx context.Context, v PostView) (PostViews, error) {
	var views PostViews
	err := r.db.QueryRowContext(ctx, `
		INSERT INTO post_views (slug, views, updated_at)
		VALUES ($1, 1, $2)
		ON CONFLICT (slug) DO UPDATE
		SET views      = post_views.views + 1,
		    updated_at = EXCLUDED.updated_at
		RETURNING slug, views`,
		v.Slug, v.At.UTC()).Scan(&views.Slug, &views.Views)
	if err != nil {
		return PostViews{}, fmt.Errorf("record post view %q: %w", v.Slug, err)
	}
	return views, nil
}

// PostViews returns the read counts of slugs in the requested order, reporting
// zero for a post that was never read.
func (r *PostgresRepository) PostViews(ctx context.Context, slugs []string) ([]PostViews, error) {
	if len(slugs) == 0 {
		return []PostViews{}, nil
	}

	args := make([]any, 0, len(slugs))
	placeholders := make([]string, 0, len(slugs))
	for _, slug := range slugs {
		args = append(args, slug)
		placeholders = append(placeholders, fmt.Sprintf("$%d", len(args)))
	}

	// Only rows that exist come back; the requested order and the zeros are
	// restored below, which is what lets the caller stop distinguishing
	// "never read" from "unknown post".
	rows, err := r.db.QueryContext(ctx,
		`SELECT slug, views FROM post_views WHERE slug IN (`+strings.Join(placeholders, ", ")+`)`,
		args...)
	if err != nil {
		return nil, fmt.Errorf("read post views: %w", err)
	}
	defer rows.Close()

	stored := make(map[string]int64, len(slugs))
	for rows.Next() {
		var slug string
		var views int64
		if err := rows.Scan(&slug, &views); err != nil {
			return nil, fmt.Errorf("read post views: %w", err)
		}
		stored[slug] = views
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read post views: %w", err)
	}

	counts := make([]PostViews, 0, len(slugs))
	for _, slug := range slugs {
		counts = append(counts, PostViews{Slug: slug, Views: stored[slug]})
	}
	return counts, nil
}
