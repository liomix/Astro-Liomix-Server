package like

import (
	"context"
	"database/sql"
	"embed"
	"fmt"

	"github.com/liomix/astro-liomix-server/internal/platform/postgres"
)

// migrations holds this module's DDL, shipped inside the binary.
//
//go:embed migrations/*.sql
var migrations embed.FS

// Migrate applies the module's outstanding migrations. The caller runs it once
// at startup, before the repository serves any request.
func Migrate(ctx context.Context, db *sql.DB) error {
	if err := postgres.Migrate(ctx, db, migrations, "migrations"); err != nil {
		return fmt.Errorf("like: migrate: %w", err)
	}
	return nil
}
