// Copyright 2026 Zyvor AI Labs · https://zyvor.dev
// SPDX-License-Identifier: Apache-2.0

package store

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"io/fs"
	"sort"
	"strconv"
	"strings"
)

//go:embed migrations/*.sql
var migrationFS embed.FS

// schemaMigrateLock is a process-wide Postgres advisory lock so two
// control-plane processes starting together apply each migration once.
const schemaMigrateLock int64 = 740014001

const schemaMigrationsTable = `
CREATE TABLE IF NOT EXISTS nodra_schema_migrations (
	version INT PRIMARY KEY,
	applied_at TIMESTAMPTZ NOT NULL DEFAULT now()
)`

type migration struct {
	version int
	sql     string
}

func loadMigrations() ([]migration, error) {
	entries, err := fs.ReadDir(migrationFS, "migrations")
	if err != nil {
		return nil, err
	}
	var out []migration
	seen := map[int]string{}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}
		verText, _, ok := strings.Cut(e.Name(), "_")
		if !ok {
			return nil, fmt.Errorf("migration %s: want NNN_name.sql", e.Name())
		}
		ver, err := strconv.Atoi(verText)
		if err != nil || ver < 1 {
			return nil, fmt.Errorf("migration %s: invalid version", e.Name())
		}
		if prev, dup := seen[ver]; dup {
			return nil, fmt.Errorf("duplicate migration version %d (%s and %s)", ver, prev, e.Name())
		}
		seen[ver] = e.Name()
		b, err := migrationFS.ReadFile("migrations/" + e.Name())
		if err != nil {
			return nil, err
		}
		out = append(out, migration{version: ver, sql: string(b)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].version < out[j].version })
	return out, nil
}

func maxMigrationVersion(ms []migration) int {
	if len(ms) == 0 {
		return 0
	}
	return ms[len(ms)-1].version
}

// rejectNewerSchema fails closed when the database was migrated by a newer binary.
func rejectNewerSchema(current, binaryMax int) error {
	if current > binaryMax {
		return fmt.Errorf("postgres schema version %d is newer than this binary (%d); refusing to start", current, binaryMax)
	}
	return nil
}

func applyMigrations(ctx context.Context, db *sql.DB) error {
	migrations, err := loadMigrations()
	if err != nil {
		return err
	}
	binaryMax := maxMigrationVersion(migrations)
	conn, err := db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	if _, err = conn.ExecContext(ctx, `SELECT pg_advisory_lock($1)`, schemaMigrateLock); err != nil {
		return fmt.Errorf("postgres schema lock: %w", err)
	}
	defer func() {
		_, _ = conn.ExecContext(context.Background(), `SELECT pg_advisory_unlock($1)`, schemaMigrateLock)
	}()
	if _, err = conn.ExecContext(ctx, schemaMigrationsTable); err != nil {
		return fmt.Errorf("postgres schema version table: %w", err)
	}
	var current int
	if err = conn.QueryRowContext(ctx, `SELECT COALESCE(MAX(version), 0) FROM nodra_schema_migrations`).Scan(&current); err != nil {
		return err
	}
	if err = rejectNewerSchema(current, binaryMax); err != nil {
		return err
	}
	for _, m := range migrations {
		if m.version <= current {
			continue
		}
		if err = applyOne(ctx, conn, m); err != nil {
			return err
		}
	}
	return nil
}

func applyOne(ctx context.Context, conn *sql.Conn, m migration) error {
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, stmt := range splitSQL(m.sql) {
		if _, err = tx.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("migration %d: %w", m.version, err)
		}
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO nodra_schema_migrations (version) VALUES ($1)`, m.version); err != nil {
		return fmt.Errorf("migration %d record: %w", m.version, err)
	}
	if err = tx.Commit(); err != nil {
		return fmt.Errorf("migration %d commit: %w", m.version, err)
	}
	return nil
}

func splitSQL(script string) []string {
	var b strings.Builder
	for _, line := range strings.Split(script, "\n") {
		trim := strings.TrimSpace(line)
		if strings.HasPrefix(trim, "--") {
			continue
		}
		b.WriteString(line)
		b.WriteByte('\n')
	}
	var out []string
	for _, part := range strings.Split(b.String(), ";") {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}
