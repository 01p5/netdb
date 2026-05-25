package store

import (
	"context"
	"path/filepath"
	"testing"
)

func TestOpenAndMigrateIdempotent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "netdb.sqlite")

	st, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := st.Migrate(context.Background()); err != nil {
		t.Fatalf("first Migrate: %v", err)
	}
	// Second run must be a no-op (no duplicate-table errors).
	if err := st.Migrate(context.Background()); err != nil {
		t.Fatalf("second Migrate: %v", err)
	}

	// Sanity: schema_migrations should have rows.
	var count int
	if err := st.DB().QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM schema_migrations`).Scan(&count); err != nil {
		t.Fatalf("count migrations: %v", err)
	}
	if count == 0 {
		t.Fatal("expected at least one applied migration")
	}
	if err := st.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

func TestOpenInvalidPath(t *testing.T) {
	// Opening a directory-as-file via sqlite usually still succeeds on Open
	// (lazy), but Ping fails. The helper bundles both, so an obviously bad
	// path (a non-existent directory) should surface as an error.
	_, err := Open("/this/path/does/not/exist/netdb.sqlite")
	if err == nil {
		t.Fatal("expected error opening sqlite in non-existent directory")
	}
}
