package audit

import (
	"context"
	"database/sql"
	"embed"
	"fmt"

	"github.com/liomix/astro-liomix-server/internal/platform/postgres"
)

// migrations holds the audit table's DDL, embedded so deploying a new version
// is enough to bring the schema along.
//
//go:embed migrations/*.sql
var migrations embed.FS

// Migrate applies the package's outstanding migrations. The caller runs it once
// at startup, before anything records or reads the trail.
func Migrate(ctx context.Context, db *sql.DB) error {
	if err := postgres.Migrate(ctx, db, migrations, "migrations"); err != nil {
		return fmt.Errorf("audit: migrate: %w", err)
	}
	return nil
}
