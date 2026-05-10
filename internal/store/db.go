package store

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

type DB struct {
	sql *sql.DB
	// writeMu serializes writes within this process. SQLite + WAL handles cross-process
	// write contention via busy_timeout; this mutex avoids SQLITE_BUSY against ourselves.
	writeMu sync.Mutex
	path    string
}

func Open(ctx context.Context, path string) (*DB, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("creating db directory: %w", err)
	}

	// Set WAL mode out-of-band on a single connection. Putting journal_mode in
	// the connection-pool DSN races when multiple pool instances open the same
	// file concurrently — each connection tries to set WAL, only one wins, the
	// others get SQLITE_BUSY before busy_timeout has a chance to apply.
	if err := primeWAL(ctx, path); err != nil {
		return nil, fmt.Errorf("priming WAL: %w", err)
	}

	dsn := fmt.Sprintf(
		"file:%s?_pragma=busy_timeout(5000)&_pragma=synchronous(NORMAL)&_pragma=foreign_keys(ON)",
		path,
	)
	sqlDB, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("opening sqlite: %w", err)
	}
	sqlDB.SetMaxOpenConns(4)

	if err := sqlDB.PingContext(ctx); err != nil {
		_ = sqlDB.Close()
		return nil, fmt.Errorf("ping sqlite: %w", err)
	}

	db := &DB{sql: sqlDB, path: path}
	if err := db.migrate(ctx); err != nil {
		_ = sqlDB.Close()
		return nil, fmt.Errorf("migrating: %w", err)
	}
	return db, nil
}

func primeWAL(ctx context.Context, path string) error {
	dsn := fmt.Sprintf("file:%s?_pragma=busy_timeout(10000)", path)
	tmp, err := sql.Open("sqlite", dsn)
	if err != nil {
		return err
	}
	defer tmp.Close()
	tmp.SetMaxOpenConns(1)

	// PRAGMA journal_mode=WAL needs an exclusive DB lock momentarily; under
	// concurrent first-boot of multiple processes, busy_timeout doesn't always
	// cover this specific pragma in the modernc driver, so retry on SQLITE_BUSY.
	const maxAttempts = 50
	const sleep = 100 * time.Millisecond
	var mode string
	for attempt := 0; attempt < maxAttempts; attempt++ {
		err = tmp.QueryRowContext(ctx, "PRAGMA journal_mode=WAL").Scan(&mode)
		if err == nil {
			break
		}
		if !isBusyError(err) {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(sleep):
		}
	}
	if err != nil {
		return err
	}
	if mode != "wal" {
		return fmt.Errorf("expected journal_mode=wal, got %q", mode)
	}
	return nil
}

func isBusyError(err error) bool {
	if err == nil {
		return false
	}
	// Fall back to substring match — modernc.org/sqlite returns errors that
	// include "database is locked" with code (5) for SQLITE_BUSY.
	msg := err.Error()
	return strings.Contains(msg, "database is locked") || strings.Contains(msg, "SQLITE_BUSY")
}

func (db *DB) Close() error                  { return db.sql.Close() }
func (db *DB) Path() string                  { return db.path }
func (db *DB) Conn() *sql.DB                 { return db.sql }
func (db *DB) WithWriteLock(fn func() error) error {
	db.writeMu.Lock()
	defer db.writeMu.Unlock()
	return fn()
}

// DataVersion returns the current SQLite data_version pragma. It increments
// on every commit and is visible across processes sharing the same DB file —
// the basis for the cross-process notifier wake-up signal.
func (db *DB) DataVersion(ctx context.Context) (int64, error) {
	var v int64
	if err := db.sql.QueryRowContext(ctx, "PRAGMA data_version").Scan(&v); err != nil {
		return 0, err
	}
	return v, nil
}
