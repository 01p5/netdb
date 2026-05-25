package store

import (
	"context"
	"testing"
)

func TestUpsertDynamicLeaseExistingNIC(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	h, _ := st.CreateHost(ctx, "known", "", "")
	n, _ := st.CreateNIC(ctx, h.ID, "aa:bb:cc:dd:ee:11", "eth0")

	err := st.UpsertDynamicLease(ctx, LeaseObserved{
		MAC: "AA-BB-CC-DD-EE-11", IP: "10.5.0.5", Hostname: "ignored",
	})
	if err != nil {
		t.Fatalf("UpsertDynamicLease: %v", err)
	}
	ips, _ := st.ListIPsByNIC(ctx, n.ID)
	if len(ips) != 1 || ips[0].IP != "10.5.0.5" || ips[0].Kind != "dynamic" {
		t.Errorf("expected one dynamic ip 10.5.0.5, got %+v", ips)
	}

	// Re-upsert is a no-op (idempotent).
	if err := st.UpsertDynamicLease(ctx, LeaseObserved{MAC: "aa:bb:cc:dd:ee:11", IP: "10.5.0.5"}); err != nil {
		t.Fatalf("re-upsert: %v", err)
	}
	ips, _ = st.ListIPsByNIC(ctx, n.ID)
	if len(ips) != 1 {
		t.Errorf("expected idempotent, got %d ips", len(ips))
	}
}

func TestUpsertDynamicLeaseUnknownMACCreatesHost(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	err := st.UpsertDynamicLease(ctx, LeaseObserved{
		MAC: "12:34:56:78:9a:bc", IP: "10.1.1.50", Hostname: "Phone-X",
	})
	if err != nil {
		t.Fatalf("UpsertDynamicLease: %v", err)
	}

	hosts, _ := st.ListHosts(ctx)
	if len(hosts) != 1 {
		t.Fatalf("expected 1 discovered host, got %d", len(hosts))
	}
	if hosts[0].DNSEnabled {
		t.Error("discovered host should have DNSEnabled=false")
	}
	if hosts[0].Tags != "dhcp-discovered" {
		t.Errorf("tags: %q", hosts[0].Tags)
	}
	// sanitizeHostname lowercases + strips '-' is kept, so "Phone-X" -> "phone-x".
	if hosts[0].Name != "phone-x" {
		t.Errorf("expected name 'phone-x', got %q", hosts[0].Name)
	}
}

func TestUpsertDynamicLeaseRejectsBadInput(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	if err := st.UpsertDynamicLease(ctx, LeaseObserved{MAC: "bad", IP: "10.0.0.1"}); err == nil {
		t.Error("expected MAC error")
	}
	if err := st.UpsertDynamicLease(ctx, LeaseObserved{MAC: "aa:bb:cc:dd:ee:99", IP: "bad-ip"}); err == nil {
		t.Error("expected IP error")
	}
}

func TestUpsertDynamicLeaseNameCollisionGetsSuffix(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	// Pre-create a host with the synthetic name shape.
	if _, err := st.CreateHost(ctx, "phone-x", "", ""); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := st.UpsertDynamicLease(ctx, LeaseObserved{
		MAC: "ab:cd:ef:12:34:56", IP: "10.0.0.99", Hostname: "phone-x",
	}); err != nil {
		t.Fatalf("UpsertDynamicLease: %v", err)
	}
	hosts, _ := st.ListHosts(ctx)
	if len(hosts) != 2 {
		t.Fatalf("expected 2 hosts after collision, got %d", len(hosts))
	}
	// The new host's name should be "phone-x-<6 hex>".
	var found bool
	for _, h := range hosts {
		if h.Name == "phone-x-abcdef" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected collision-suffixed host, got %+v", hosts)
	}
}

func TestDeleteStaleDynamic(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	if err := st.UpsertDynamicLease(ctx, LeaseObserved{MAC: "aa:bb:cc:dd:ee:01", IP: "10.0.0.1"}); err != nil {
		t.Fatalf("upsert 1: %v", err)
	}
	if err := st.UpsertDynamicLease(ctx, LeaseObserved{MAC: "aa:bb:cc:dd:ee:02", IP: "10.0.0.2"}); err != nil {
		t.Fatalf("upsert 2: %v", err)
	}

	// Only first lease is still active.
	seen := map[string]bool{"aa:bb:cc:dd:ee:01|10.0.0.1": true}
	removed, err := st.DeleteStaleDynamic(ctx, seen)
	if err != nil {
		t.Fatalf("DeleteStaleDynamic: %v", err)
	}
	if removed != 1 {
		t.Errorf("expected 1 removed, got %d", removed)
	}
}

func TestDeleteStaleDynamicSkipsNonDynamic(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	h, _ := st.CreateHost(ctx, "static-host", "", "")
	n, _ := st.CreateNIC(ctx, h.ID, "aa:bb:cc:dd:ee:f1", "")
	if _, err := st.CreateIPAssignment(ctx, n.ID, nil, "10.0.0.7", "static"); err != nil {
		t.Fatalf("CreateIPAssignment: %v", err)
	}

	if _, err := st.DeleteStaleDynamic(ctx, nil); err != nil {
		t.Fatalf("DeleteStaleDynamic: %v", err)
	}
	ips, _ := st.ListIPsByNIC(ctx, n.ID)
	if len(ips) != 1 {
		t.Errorf("static IP should survive stale-dynamic prune, got %d ips", len(ips))
	}
}
