package comment

import (
	"context"
	"database/sql"
	"embed"
	"fmt"

	"github.com/liomix/astro-liomix-server/internal/platform/postgres"
)

// migrations holds this module's DDL. It ships with the binary, so deploying a
// new version is enough to bring the schema along.
//
//go:embed migrations/*.sql
var migrations embed.FS

// Migrate applies the module's outstanding migrations. The caller runs it once
// at startup, before the repository serves any request.
func Migrate(ctx context.Context, db *sql.DB) error {
	if err := postgres.Migrate(ctx, db, migrations, "migrations"); err != nil {
		return fmt.Errorf("comment: migrate: %w", err)
	}
	return nil
}
