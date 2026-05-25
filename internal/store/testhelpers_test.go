package store

import (
	"context"
	"path/filepath"
	"testing"

	"netdb/internal/model"
)

// modelSubnetCIDR returns a minimal Subnet whose only meaningful field is
// CIDR. Most store tests don't care about VLAN/gateway/etc — they just need
// a subnet to point IPs at.
func modelSubnetCIDR(cidr string) model.Subnet {
	return model.Subnet{CIDR: cidr}
}

// newTestStore returns a fully-migrated Store backed by a temp-dir sqlite
// file. Using a file (not :memory:) keeps WAL pragmas in the DSN happy and
// matches how the binary opens the DB in production.
func newTestStore(t *testing.T) *Store {
	t.Helper()
	dir := t.TempDir()
	st, err := Open(filepath.Join(dir, "netdb.sqlite"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := st.Migrate(context.Background()); err != nil {
		st.Close()
		t.Fatalf("Migrate: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}
