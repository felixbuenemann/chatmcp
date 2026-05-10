package store

import (
	"context"
	"database/sql"
	_ "embed"
	"fmt"
	"time"
)

//go:embed schema.sql
var schemaSQL string

const currentSchemaVersion = 1

func (db *DB) migrate(ctx context.Context) error {
	// Race-safe migration: explicit BEGIN IMMEDIATE acquires the SQLite
	// reserved lock up front, so contenders serialize under busy_timeout (5s)
	// instead of all racing past a deferred BEGIN's SELECT and crashing on
	// duplicate CREATE TABLE.
	conn, err := db.sql.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()

	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return fmt.Errorf("BEGIN IMMEDIATE: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_, _ = conn.ExecContext(ctx, "ROLLBACK")
		}
	}()

	exists, err := tableExistsConn(ctx, conn, "schema_version")
	if err != nil {
		return fmt.Errorf("checking schema_version: %w", err)
	}

	if exists {
		var version int
		if err := conn.QueryRowContext(ctx,
			"SELECT COALESCE(MAX(version), 0) FROM schema_version",
		).Scan(&version); err != nil {
			return fmt.Errorf("reading schema_version: %w", err)
		}
		if version >= currentSchemaVersion {
			if _, err := conn.ExecContext(ctx, "COMMIT"); err != nil {
				return err
			}
			committed = true
			return nil
		}
		return fmt.Errorf("schema_version %d unsupported (max %d); forward migrations not yet implemented",
			version, currentSchemaVersion)
	}

	if _, err := conn.ExecContext(ctx, schemaSQL); err != nil {
		return fmt.Errorf("applying schema: %w", err)
	}
	if _, err := conn.ExecContext(ctx,
		"INSERT INTO schema_version (version, applied_at) VALUES (?, ?)",
		currentSchemaVersion, time.Now().UnixMicro(),
	); err != nil {
		return fmt.Errorf("recording schema_version: %w", err)
	}
	if _, err := conn.ExecContext(ctx, "COMMIT"); err != nil {
		return fmt.Errorf("COMMIT: %w", err)
	}
	committed = true
	return nil
}

func tableExistsConn(ctx context.Context, conn *sql.Conn, name string) (bool, error) {
	var found string
	err := conn.QueryRowContext(ctx,
		"SELECT name FROM sqlite_master WHERE type = 'table' AND name = ?",
		name,
	).Scan(&found)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return found == name, nil
}
