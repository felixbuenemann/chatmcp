package store_test

import (
	"context"
	"path/filepath"
	"sync"
	"testing"

	"github.com/buenemann/chatmcp/internal/store"
)

func TestOpenAndMigrate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "chat.db")
	db, err := store.Open(context.Background(), path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	v, err := db.DataVersion(context.Background())
	if err != nil {
		t.Fatalf("DataVersion: %v", err)
	}
	if v <= 0 {
		t.Fatalf("expected nonzero data_version, got %d", v)
	}
}

func TestConcurrentOpenMigratesOnce(t *testing.T) {
	path := filepath.Join(t.TempDir(), "chat.db")

	const N = 4
	var wg sync.WaitGroup
	wg.Add(N)
	errs := make([]error, N)
	dbs := make([]*store.DB, N)

	for i := 0; i < N; i++ {
		go func(i int) {
			defer wg.Done()
			db, err := store.Open(context.Background(), path)
			dbs[i], errs[i] = db, err
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("worker %d: Open: %v", i, err)
		}
	}
	for _, db := range dbs {
		if db != nil {
			db.Close()
		}
	}

	// Reopen once more and confirm schema is intact (single row in schema_version).
	db, err := store.Open(context.Background(), path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer db.Close()

	var n int
	if err := db.Conn().QueryRowContext(context.Background(),
		"SELECT COUNT(*) FROM schema_version").Scan(&n); err != nil {
		t.Fatalf("count schema_version: %v", err)
	}
	if n != 1 {
		t.Fatalf("expected exactly 1 schema_version row after race, got %d", n)
	}
}
