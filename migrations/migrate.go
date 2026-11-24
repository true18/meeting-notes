// Package migrations applies embedded SQL migrations.
package migrations

import (
	"context"
	"embed"
	"fmt"
	"io/fs"
	"sort"

	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed *.up.sql
var files embed.FS

// Apply applies every not-yet-recorded migration in filename order.
func Apply(ctx context.Context, pool *pgxpool.Pool) error {
	if _, err := pool.Exec(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (name TEXT PRIMARY KEY, applied_at TIMESTAMPTZ NOT NULL DEFAULT NOW())`); err != nil {
		return fmt.Errorf("create schema_migrations: %w", err)
	}
	entries, err := fs.Glob(files, "*.up.sql")
	if err != nil {
		return fmt.Errorf("list migrations: %w", err)
	}
	sort.Strings(entries)
	for _, name := range entries {
		var applied bool
		if err := pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM schema_migrations WHERE name = $1)`, name).Scan(&applied); err != nil {
			return fmt.Errorf("check migration %s: %w", name, err)
		}
		if applied {
			continue
		}
		body, err := files.ReadFile(name)
		if err != nil {
			return fmt.Errorf("read migration %s: %w", name, err)
		}
		tx, err := pool.Begin(ctx)
		if err != nil {
			return fmt.Errorf("begin migration %s: %w", name, err)
		}
		if _, err = tx.Exec(ctx, string(body)); err == nil {
			_, err = tx.Exec(ctx, `INSERT INTO schema_migrations(name) VALUES ($1)`, name)
		}
		if err != nil {
			_ = tx.Rollback(ctx)
			return fmt.Errorf("apply migration %s: %w", name, err)
		}
		if err := tx.Commit(ctx); err != nil {
			return fmt.Errorf("commit migration %s: %w", name, err)
		}
	}
	return nil
}
