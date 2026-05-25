package store

import (
	"context"
	"errors"
	"testing"
)

func TestHostsCRUD(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	h, err := st.CreateHost(ctx, "router", "edge router", "core,prod")
	if err != nil {
		t.Fatalf("CreateHost: %v", err)
	}
	if h.ID == 0 || h.Name != "router" || h.Description != "edge router" || h.Tags != "core,prod" {
		t.Errorf("created host fields wrong: %+v", h)
	}
	if !h.DNSEnabled {
		t.Error("DNS should default enabled (column default 1)")
	}

	got, err := st.GetHost(ctx, h.ID)
	if err != nil {
		t.Fatalf("GetHost: %v", err)
	}
	if got.Name != "router" {
		t.Errorf("GetHost mismatch: %+v", got)
	}

	hosts, err := st.ListHosts(ctx)
	if err != nil {
		t.Fatalf("ListHosts: %v", err)
	}
	if len(hosts) != 1 {
		t.Fatalf("expected 1 host, got %d", len(hosts))
	}

	if err := st.UpdateHost(ctx, h.ID, "router-1", "edge router renamed", "core", "rtr", false); err != nil {
		t.Fatalf("UpdateHost: %v", err)
	}
	got, err = st.GetHost(ctx, h.ID)
	if err != nil {
		t.Fatalf("GetHost after update: %v", err)
	}
	if got.Name != "router-1" || got.DNSName != "rtr" || got.DNSEnabled {
		t.Errorf("update mismatch: %+v", got)
	}

	if err := st.DeleteHost(ctx, h.ID); err != nil {
		t.Fatalf("DeleteHost: %v", err)
	}
	if _, err := st.GetHost(ctx, h.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("GetHost after delete: got %v, want ErrNotFound", err)
	}
}

func TestHostsNotFoundErrors(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	if _, err := st.GetHost(ctx, 99999); !errors.Is(err, ErrNotFound) {
		t.Errorf("GetHost missing: got %v want ErrNotFound", err)
	}
	if err := st.UpdateHost(ctx, 99999, "x", "", "", "", true); !errors.Is(err, ErrNotFound) {
		t.Errorf("UpdateHost missing: got %v want ErrNotFound", err)
	}
	if err := st.DeleteHost(ctx, 99999); !errors.Is(err, ErrNotFound) {
		t.Errorf("DeleteHost missing: got %v want ErrNotFound", err)
	}
}

func TestHostsUniqueName(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	if _, err := st.CreateHost(ctx, "dup", "", ""); err != nil {
		t.Fatalf("CreateHost 1: %v", err)
	}
	if _, err := st.CreateHost(ctx, "dup", "", ""); err == nil {
		t.Fatal("expected UNIQUE constraint failure on second CreateHost")
	}
}

func TestGetHostDetailHydratesNICsAndIPs(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	h, _ := st.CreateHost(ctx, "srv", "", "")
	nic, err := st.CreateNIC(ctx, h.ID, "aa:bb:cc:dd:ee:ff", "eth0")
	if err != nil {
		t.Fatalf("CreateNIC: %v", err)
	}
	if _, err := st.CreateIPAssignment(ctx, nic.ID, nil, "10.0.0.5", "static"); err != nil {
		t.Fatalf("CreateIPAssignment: %v", err)
	}

	d, err := st.GetHostDetail(ctx, h.ID)
	if err != nil {
		t.Fatalf("GetHostDetail: %v", err)
	}
	if d.Host.Name != "srv" || len(d.NICs) != 1 || len(d.NICs[0].IPs) != 1 || d.NICs[0].IPs[0].IP != "10.0.0.5" {
		t.Errorf("HostDetail not hydrated as expected: %+v", d)
	}
}

func TestGetHostDetailMissing(t *testing.T) {
	st := newTestStore(t)
	if _, err := st.GetHostDetail(context.Background(), 9999); !errors.Is(err, ErrNotFound) {
		t.Errorf("got %v want ErrNotFound", err)
	}
}
