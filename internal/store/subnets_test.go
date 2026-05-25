package store

import (
	"context"
	"errors"
	"testing"

	"netdb/internal/model"
)

func TestSubnetsCRUD(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	vlan := 10
	sb := model.Subnet{
		CIDR:           "10.1.0.0/24",
		VLAN:           &vlan,
		Gateway:        "10.1.0.1",
		Description:    "lab",
		DHCPRangeStart: "10.1.0.100",
		DHCPRangeEnd:   "10.1.0.200",
		DNSServers:     "10.1.0.1, 10.1.0.2",
	}
	created, err := st.CreateSubnet(ctx, sb)
	if err != nil {
		t.Fatalf("CreateSubnet: %v", err)
	}
	if created.VLAN == nil || *created.VLAN != 10 {
		t.Errorf("VLAN not persisted: %+v", created.VLAN)
	}
	if created.DHCPOptionsJSON != "{}" {
		t.Errorf("expected DHCPOptionsJSON defaulted to {} got %q", created.DHCPOptionsJSON)
	}

	got, err := st.GetSubnet(ctx, created.ID)
	if err != nil {
		t.Fatalf("GetSubnet: %v", err)
	}
	if got.DNSServers != "10.1.0.1, 10.1.0.2" {
		t.Errorf("DNSServers: %q", got.DNSServers)
	}

	list, err := st.ListSubnets(ctx)
	if err != nil || len(list) != 1 {
		t.Fatalf("ListSubnets: %v len=%d", err, len(list))
	}

	// Update strips VLAN by setting nil.
	updated := *got
	updated.VLAN = nil
	updated.Description = "lab2"
	if err := st.UpdateSubnet(ctx, got.ID, updated); err != nil {
		t.Fatalf("UpdateSubnet: %v", err)
	}
	got, err = st.GetSubnet(ctx, created.ID)
	if err != nil {
		t.Fatalf("GetSubnet after update: %v", err)
	}
	if got.VLAN != nil {
		t.Errorf("VLAN should be nil after update, got %v", *got.VLAN)
	}
	if got.Description != "lab2" {
		t.Errorf("description not updated: %q", got.Description)
	}

	if err := st.DeleteSubnet(ctx, created.ID); err != nil {
		t.Fatalf("DeleteSubnet: %v", err)
	}
	if _, err := st.GetSubnet(ctx, created.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("after delete: %v", err)
	}
}

func TestSubnetsNotFound(t *testing.T) {
	st := newTestStore(t)
	if _, err := st.GetSubnet(context.Background(), 999); !errors.Is(err, ErrNotFound) {
		t.Errorf("got %v", err)
	}
	if err := st.UpdateSubnet(context.Background(), 999, model.Subnet{CIDR: "x"}); !errors.Is(err, ErrNotFound) {
		t.Errorf("got %v", err)
	}
	if err := st.DeleteSubnet(context.Background(), 999); !errors.Is(err, ErrNotFound) {
		t.Errorf("got %v", err)
	}
}

func TestSubnetUniqueCIDR(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	if _, err := st.CreateSubnet(ctx, model.Subnet{CIDR: "10.0.0.0/24"}); err != nil {
		t.Fatalf("CreateSubnet 1: %v", err)
	}
	if _, err := st.CreateSubnet(ctx, model.Subnet{CIDR: "10.0.0.0/24"}); err == nil {
		t.Fatal("expected UNIQUE(cidr) failure")
	}
}
