package idempotency

import (
	"context"
	"database/sql"
	"embed"
	"fmt"

	"github.com/liomix/astro-liomix-server/internal/platform/postgres"
)

// migrations holds the table this package owns.
//
//go:embed migrations/*.sql
var migrations embed.FS

// Migrate applies the outstanding migrations. The caller runs it once at
// startup, next to the domain modules' own migrations.
func Migrate(ctx context.Context, db *sql.DB) error {
	if err := postgres.Migrate(ctx, db, migrations, "migrations"); err != nil {
		return fmt.Errorf("idempotency: migrate: %w", err)
	}
	return nil
}
