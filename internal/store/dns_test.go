package store

import (
	"context"
	"errors"
	"testing"
)

func TestCreateManualDNSRecord(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	z, _ := st.CreateZone(ctx, "lan.example", "forward", "", 600)

	// Bad type rejected.
	if _, err := st.CreateManualDNSRecord(ctx, "lan.example", "x.lan.example", "BOGUS", "10.0.0.1", 0); err == nil {
		t.Fatal("expected unsupported type error")
	}
	// Unknown zone rejected.
	if _, err := st.CreateManualDNSRecord(ctx, "nope", "x.nope", "A", "10.0.0.1", 0); err == nil {
		t.Fatal("expected unknown zone error")
	}

	id, err := st.CreateManualDNSRecord(ctx, "lan.example", "x.lan.example", "A", "10.0.0.1", 0)
	if err != nil {
		t.Fatalf("CreateManualDNSRecord: %v", err)
	}
	_ = z

	// TTL=0 should fall back to zone default (600).
	views, err := st.ListDNSRecords(ctx)
	if err != nil {
		t.Fatalf("ListDNSRecords: %v", err)
	}
	if len(views) != 1 || views[0].TTL != 600 || views[0].Source != "manual" {
		t.Errorf("view mismatch: %+v", views)
	}

	if err := st.DeleteDNSRecord(ctx, id); err != nil {
		t.Fatalf("DeleteDNSRecord: %v", err)
	}
	if err := st.DeleteDNSRecord(ctx, id); !errors.Is(err, ErrNotFound) {
		t.Errorf("got %v want ErrNotFound", err)
	}
}

func TestListDesiredRecordsByZone(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	z, _ := st.CreateZone(ctx, "lan.example", "forward", "", 300)

	// Manual CNAME — uses target directly.
	if _, err := st.CreateManualDNSRecord(ctx, "lan.example", "alias.lan.example", "CNAME", "real.lan.example", 120); err != nil {
		t.Fatalf("manual cname: %v", err)
	}

	// Auto A record with bindings to a real ip_assignment.
	h, _ := st.CreateHost(ctx, "h", "", "")
	n, _ := st.CreateNIC(ctx, h.ID, "aa:bb:cc:dd:ee:f0", "")
	ip, _ := st.CreateIPAssignment(ctx, n.ID, nil, "10.0.0.5", "static")

	// Manually wire up an auto record + binding so we can test the JOIN.
	res, err := st.DB().ExecContext(ctx,
		`INSERT INTO dns_records(zone_id, fqdn, type, target, ttl, source) VALUES(?, ?, ?, '', ?, 'auto')`,
		z.ID, "h.lan.example", "A", 300)
	if err != nil {
		t.Fatalf("insert auto A: %v", err)
	}
	recID, _ := res.LastInsertId()
	if _, err := st.DB().ExecContext(ctx,
		`INSERT INTO dns_bindings(dns_record_id, ip_assignment_id) VALUES(?, ?)`,
		recID, ip.ID); err != nil {
		t.Fatalf("insert binding: %v", err)
	}

	// And an auto record with no bindings — should be skipped by the desired view.
	if _, err := st.DB().ExecContext(ctx,
		`INSERT INTO dns_records(zone_id, fqdn, type, target, ttl, source) VALUES(?, 'orphan.lan.example', 'A', '', 300, 'auto')`,
		z.ID); err != nil {
		t.Fatalf("insert orphan auto: %v", err)
	}

	got, err := st.ListDesiredRecordsByZone(ctx, z.ID)
	if err != nil {
		t.Fatalf("ListDesiredRecordsByZone: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 desired (manual CNAME + auto A bound), got %d: %+v", len(got), got)
	}
	// Sort is by fqdn,type,value; alias < h.
	if got[0].Name != "alias.lan.example" || got[0].Value != "real.lan.example" || got[0].Type != "CNAME" {
		t.Errorf("CNAME row wrong: %+v", got[0])
	}
	if got[1].Name != "h.lan.example" || got[1].Value != "10.0.0.5" || got[1].Type != "A" {
		t.Errorf("A row wrong: %+v", got[1])
	}
}

func TestListDNSRecordsAggregatesIPs(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	z, _ := st.CreateZone(ctx, "z.example", "forward", "", 300)

	h, _ := st.CreateHost(ctx, "h2", "", "")
	n, _ := st.CreateNIC(ctx, h.ID, "aa:bb:cc:dd:ee:ab", "")
	ip1, _ := st.CreateIPAssignment(ctx, n.ID, nil, "10.0.0.1", "static")
	ip2, _ := st.CreateIPAssignment(ctx, n.ID, nil, "10.0.0.2", "static")

	res, _ := st.DB().ExecContext(ctx,
		`INSERT INTO dns_records(zone_id, fqdn, type, target, ttl, source) VALUES(?, 'h2.z.example', 'A', '', 300, 'auto')`, z.ID)
	id, _ := res.LastInsertId()
	st.DB().ExecContext(ctx, `INSERT INTO dns_bindings VALUES(?, ?)`, id, ip1.ID)
	st.DB().ExecContext(ctx, `INSERT INTO dns_bindings VALUES(?, ?)`, id, ip2.ID)

	views, err := st.ListDNSRecords(ctx)
	if err != nil {
		t.Fatalf("ListDNSRecords: %v", err)
	}
	if len(views) != 1 {
		t.Fatalf("expected 1 view, got %d", len(views))
	}
	if len(views[0].IPs) != 2 {
		t.Errorf("expected 2 IPs aggregated, got %v", views[0].IPs)
	}
}
