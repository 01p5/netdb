package store

import (
	"context"
	"errors"
	"testing"
)

func TestZonesCRUD(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	z, err := st.CreateZone(ctx, "lan.example.com", "forward", "lab forward", 600)
	if err != nil {
		t.Fatalf("CreateZone: %v", err)
	}
	if z.Kind != "forward" || z.DefaultTTL != 600 {
		t.Errorf("created zone fields wrong: %+v", z)
	}

	got, err := st.GetZone(ctx, z.ID)
	if err != nil || got.Name != z.Name {
		t.Fatalf("GetZone: %v %+v", err, got)
	}

	list, err := st.ListZones(ctx)
	if err != nil || len(list) != 1 {
		t.Fatalf("ListZones: %v len=%d", err, len(list))
	}

	if err := st.DeleteZone(ctx, z.ID); err != nil {
		t.Fatalf("DeleteZone: %v", err)
	}
	if _, err := st.GetZone(ctx, z.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("after delete: %v want ErrNotFound", err)
	}
}

func TestZoneKindAndTTLDefaults(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	if _, err := st.CreateZone(ctx, "bad", "weird", "", 0); err == nil {
		t.Fatal("expected kind validation error")
	}
	z, err := st.CreateZone(ctx, "good.example", "forward", "", 0)
	if err != nil {
		t.Fatalf("CreateZone with 0 TTL: %v", err)
	}
	if z.DefaultTTL != 300 {
		t.Errorf("expected TTL default 300, got %d", z.DefaultTTL)
	}
}

func TestZoneProviderLinks(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	z, _ := st.CreateZone(ctx, "lan.example", "forward", "", 0)
	p, err := st.CreateProvider(ctx, "cf", "cloudflare", "", true)
	if err != nil {
		t.Fatalf("CreateProvider: %v", err)
	}

	if err := st.LinkZoneProvider(ctx, z.ID, p.ID); err != nil {
		t.Fatalf("LinkZoneProvider: %v", err)
	}
	// Idempotent.
	if err := st.LinkZoneProvider(ctx, z.ID, p.ID); err != nil {
		t.Fatalf("LinkZoneProvider idempotent: %v", err)
	}

	zones, err := st.ListZonesForProvider(ctx, p.ID)
	if err != nil || len(zones) != 1 {
		t.Fatalf("ListZonesForProvider: %v len=%d", err, len(zones))
	}
	provs, err := st.ListProvidersForZone(ctx, z.ID)
	if err != nil || len(provs) != 1 {
		t.Fatalf("ListProvidersForZone: %v len=%d", err, len(provs))
	}

	if err := st.UnlinkZoneProvider(ctx, z.ID, p.ID); err != nil {
		t.Fatalf("UnlinkZoneProvider: %v", err)
	}
	zones, _ = st.ListZonesForProvider(ctx, p.ID)
	if len(zones) != 0 {
		t.Errorf("expected 0 zones after unlink, got %d", len(zones))
	}
}

func TestSubnetZoneLinks(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	sb, _ := st.CreateSubnet(ctx, modelSubnetCIDR("192.168.10.0/24"))
	zf, _ := st.CreateZone(ctx, "fwd.example", "forward", "", 0)
	zr, _ := st.CreateZone(ctx, "10.168.192.in-addr.arpa", "reverse", "", 0)

	if err := st.LinkSubnetZone(ctx, sb.ID, zf.ID, "forward"); err != nil {
		t.Fatalf("LinkSubnetZone forward: %v", err)
	}
	if err := st.LinkSubnetZone(ctx, sb.ID, zr.ID, "reverse"); err != nil {
		t.Fatalf("LinkSubnetZone reverse: %v", err)
	}
	if err := st.LinkSubnetZone(ctx, sb.ID, zf.ID, "weird"); err == nil {
		t.Fatal("expected role validation error")
	}

	links, err := st.ListZonesForSubnet(ctx, sb.ID)
	if err != nil {
		t.Fatalf("ListZonesForSubnet: %v", err)
	}
	if len(links) != 2 {
		t.Fatalf("expected 2 links, got %d", len(links))
	}

	if err := st.UnlinkSubnetZone(ctx, sb.ID, zf.ID, "forward"); err != nil {
		t.Fatalf("UnlinkSubnetZone: %v", err)
	}
	links, _ = st.ListZonesForSubnet(ctx, sb.ID)
	if len(links) != 1 {
		t.Errorf("expected 1 link after unlink, got %d", len(links))
	}
}
