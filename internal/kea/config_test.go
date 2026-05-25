package kea

import (
	"context"
	"path/filepath"
	"testing"

	"netdb/internal/model"
	"netdb/internal/store"
)

func openStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "kea.sqlite"))
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

func TestCleanCSV(t *testing.T) {
	cases := []struct{ in, out string }{
		{"", ""},
		{"10.0.0.1", "10.0.0.1"},
		{"10.0.0.1, 10.0.0.2", "10.0.0.1, 10.0.0.2"},
		{" 10.0.0.1 ,, 10.0.0.2 ", "10.0.0.1, 10.0.0.2"},
		{", , , ,", ""},
	}
	for _, c := range cases {
		if got := cleanCSV(c.in); got != c.out {
			t.Errorf("cleanCSV(%q): got %q want %q", c.in, got, c.out)
		}
	}
}

func TestExpandPartialIP(t *testing.T) {
	// Full IP returns unchanged.
	got, err := expandPartialIP("10.0.0.0/24", "10.0.0.99")
	if err != nil || got != "10.0.0.99" {
		t.Errorf("full IP unchanged: %q %v", got, err)
	}
	// Last-octet shorthand.
	got, err = expandPartialIP("10.99.1.0/24", "99")
	if err != nil || got != "10.99.1.99" {
		t.Errorf("short form 1 octet: %q %v", got, err)
	}
	// Two-octet shorthand.
	got, err = expandPartialIP("10.99.1.0/24", "1.99")
	if err != nil || got != "10.99.1.99" {
		t.Errorf("short form 2 octets: %q %v", got, err)
	}
	// IPv6 short-form rejected.
	if _, err := expandPartialIP("2001:db8::/64", "5"); err == nil {
		t.Error("ipv6 short-form should be rejected")
	}
	// Bad CIDR with unparseable short value.
	if _, err := expandPartialIP("not-a-cidr", "5"); err == nil {
		t.Error("expected error on bad cidr")
	}
}

func TestBuildConfigEmpty(t *testing.T) {
	st := openStore(t)
	cfg, count, err := BuildConfig(context.Background(), st)
	if err != nil {
		t.Fatalf("BuildConfig: %v", err)
	}
	if count != 0 {
		t.Errorf("expected 0 reservations, got %d", count)
	}
	if len(cfg.Subnet4) != 0 {
		t.Errorf("expected 0 subnets, got %d", len(cfg.Subnet4))
	}
	// Base config should still be wired up.
	if cfg.InterfacesConfig.DHCPSocketType != "udp" {
		t.Errorf("expected base config socket type 'udp', got %q", cfg.InterfacesConfig.DHCPSocketType)
	}
	if len(cfg.HooksLibraries) == 0 {
		t.Errorf("expected lease_cmds hook library in defaults")
	}
}

func TestBuildConfigWithSubnetAndReservation(t *testing.T) {
	st := openStore(t)
	ctx := context.Background()
	sb, err := st.CreateSubnet(ctx, model.Subnet{
		CIDR:           "10.0.0.0/24",
		Gateway:        "10.0.0.1",
		DHCPRangeStart: "100",
		DHCPRangeEnd:   "200",
		DNSServers:     "10.0.0.1, 8.8.8.8",
	})
	if err != nil {
		t.Fatalf("CreateSubnet: %v", err)
	}
	h, _ := st.CreateHost(ctx, "fileserver", "", "")
	n, _ := st.CreateNIC(ctx, h.ID, "aa:bb:cc:dd:ee:11", "lan")
	sid := sb.ID
	if _, err := st.CreateIPAssignment(ctx, n.ID, &sid, "10.0.0.50", "reserved"); err != nil {
		t.Fatalf("CreateIPAssignment: %v", err)
	}
	// One out-of-range reservation that should be dropped.
	n2, _ := st.CreateNIC(ctx, h.ID, "aa:bb:cc:dd:ee:12", "")
	if _, err := st.CreateIPAssignment(ctx, n2.ID, nil, "172.16.0.5", "reserved"); err != nil {
		t.Fatalf("oor reserved: %v", err)
	}

	cfg, count, err := BuildConfig(ctx, st)
	if err != nil {
		t.Fatalf("BuildConfig: %v", err)
	}
	if count != 1 {
		t.Errorf("expected 1 reservation placed, got %d", count)
	}
	if len(cfg.Subnet4) != 1 {
		t.Fatalf("expected 1 subnet, got %d", len(cfg.Subnet4))
	}
	s4 := cfg.Subnet4[0]
	if s4.Subnet != "10.0.0.0/24" {
		t.Errorf("subnet: %q", s4.Subnet)
	}
	if len(s4.Pools) != 1 || s4.Pools[0].Pool != "10.0.0.100 - 10.0.0.200" {
		t.Errorf("pool not expanded as expected: %+v", s4.Pools)
	}
	// Option-data should have routers + domain-name-servers.
	wantOpts := map[string]string{
		"routers":             "10.0.0.1",
		"domain-name-servers": "10.0.0.1, 8.8.8.8",
	}
	for _, o := range s4.OptionData {
		if v, ok := wantOpts[o.Name]; !ok || v != o.Data {
			t.Errorf("unexpected option %s=%q", o.Name, o.Data)
		}
		delete(wantOpts, o.Name)
	}
	if len(wantOpts) != 0 {
		t.Errorf("missing options: %v", wantOpts)
	}
	if len(s4.Reservations) != 1 || s4.Reservations[0].IPAddress != "10.0.0.50" || s4.Reservations[0].HWAddress != "aa:bb:cc:dd:ee:11" {
		t.Errorf("reservation wrong: %+v", s4.Reservations)
	}
}

func TestBuildConfigBadPoolSkipped(t *testing.T) {
	st := openStore(t)
	ctx := context.Background()
	// Bad range — expansion should fail and we skip the pool but keep the subnet.
	if _, err := st.CreateSubnet(ctx, model.Subnet{
		CIDR:           "10.0.0.0/24",
		DHCPRangeStart: "not-an-ip",
		DHCPRangeEnd:   "200",
	}); err != nil {
		t.Fatalf("CreateSubnet: %v", err)
	}
	cfg, _, err := BuildConfig(ctx, st)
	if err != nil {
		t.Fatalf("BuildConfig: %v", err)
	}
	if len(cfg.Subnet4) != 1 {
		t.Fatalf("expected 1 subnet, got %d", len(cfg.Subnet4))
	}
	if len(cfg.Subnet4[0].Pools) != 0 {
		t.Errorf("expected pools dropped on bad range, got %+v", cfg.Subnet4[0].Pools)
	}
}

func TestBuildConfigSkipsUnparseableCIDR(t *testing.T) {
	st := openStore(t)
	ctx := context.Background()
	// Write a junk CIDR straight to the DB to bypass any future validation.
	if _, err := st.DB().ExecContext(ctx,
		`INSERT INTO subnets(cidr) VALUES('garbage')`); err != nil {
		t.Fatalf("insert junk: %v", err)
	}
	cfg, _, err := BuildConfig(ctx, st)
	if err != nil {
		t.Fatalf("BuildConfig: %v", err)
	}
	if len(cfg.Subnet4) != 0 {
		t.Errorf("unparseable CIDR should be skipped, got %+v", cfg.Subnet4)
	}
}
