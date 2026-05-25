package synth

import (
	"context"
	"path/filepath"
	"sort"
	"testing"

	"netdb/internal/model"
	"netdb/internal/store"
)

func subnet(cidr string) model.Subnet { return model.Subnet{CIDR: cidr} }

func openStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "synth.sqlite"))
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

func listRecordRows(t *testing.T, st *store.Store) []rowSummary {
	t.Helper()
	rows, err := st.DB().Query(`SELECT fqdn, type, target, ttl, source FROM dns_records ORDER BY fqdn, type, target`)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	defer rows.Close()
	var out []rowSummary
	for rows.Next() {
		var r rowSummary
		if err := rows.Scan(&r.FQDN, &r.Type, &r.Target, &r.TTL, &r.Source); err != nil {
			t.Fatalf("scan: %v", err)
		}
		out = append(out, r)
	}
	return out
}

type rowSummary struct {
	FQDN, Type, Target string
	TTL                int
	Source             string
}

func TestRunSynthesizesForwardAndReverse(t *testing.T) {
	st := openStore(t)
	ctx := context.Background()

	sb, _ := st.CreateSubnet(ctx, subnet("10.20.30.0/24"))
	zf, _ := st.CreateZone(ctx, "lan.example.com", "forward", "", 300)
	zr, _ := st.CreateZone(ctx, "30.20.10.in-addr.arpa", "reverse", "", 600)
	if err := st.LinkSubnetZone(ctx, sb.ID, zf.ID, "forward"); err != nil {
		t.Fatalf("link fwd: %v", err)
	}
	if err := st.LinkSubnetZone(ctx, sb.ID, zr.ID, "reverse"); err != nil {
		t.Fatalf("link rev: %v", err)
	}

	h, _ := st.CreateHost(ctx, "router", "", "")
	n, _ := st.CreateNIC(ctx, h.ID, "aa:bb:cc:dd:ee:01", "")
	if _, err := st.CreateIPAssignment(ctx, n.ID, nil, "10.20.30.5", "static"); err != nil {
		t.Fatalf("ip: %v", err)
	}

	if err := Run(ctx, st); err != nil {
		t.Fatalf("Run: %v", err)
	}
	rows := listRecordRows(t, st)
	if len(rows) != 2 {
		t.Fatalf("expected 2 auto rows (A + PTR), got %d: %+v", len(rows), rows)
	}
	// Find A row.
	var a, ptr *rowSummary
	for i, r := range rows {
		if r.Type == "A" {
			a = &rows[i]
		}
		if r.Type == "PTR" {
			ptr = &rows[i]
		}
	}
	if a == nil || a.FQDN != "router.lan.example.com" || a.TTL != 300 || a.Source != "auto" {
		t.Errorf("A row mismatch: %+v", a)
	}
	if ptr == nil || ptr.FQDN != "5.30.20.10.in-addr.arpa" || ptr.Target != "router.lan.example.com" || ptr.TTL != 600 {
		t.Errorf("PTR row mismatch: %+v", ptr)
	}
}

func TestRunSkipsHostsWithDNSDisabled(t *testing.T) {
	st := openStore(t)
	ctx := context.Background()

	sb, _ := st.CreateSubnet(ctx, subnet("10.0.0.0/24"))
	z, _ := st.CreateZone(ctx, "lan.example", "forward", "", 300)
	if err := st.LinkSubnetZone(ctx, sb.ID, z.ID, "forward"); err != nil {
		t.Fatalf("link: %v", err)
	}
	h, _ := st.CreateHost(ctx, "silent", "", "")
	if err := st.UpdateHost(ctx, h.ID, "silent", "", "", "", false); err != nil {
		t.Fatalf("disable: %v", err)
	}
	n, _ := st.CreateNIC(ctx, h.ID, "aa:bb:cc:dd:ee:02", "")
	if _, err := st.CreateIPAssignment(ctx, n.ID, nil, "10.0.0.5", "static"); err != nil {
		t.Fatalf("ip: %v", err)
	}

	if err := Run(ctx, st); err != nil {
		t.Fatalf("Run: %v", err)
	}
	rows := listRecordRows(t, st)
	if len(rows) != 0 {
		t.Errorf("expected no records for DNS-disabled host, got %+v", rows)
	}
}

func TestRunUsesDNSNameOverride(t *testing.T) {
	st := openStore(t)
	ctx := context.Background()
	sb, _ := st.CreateSubnet(ctx, subnet("10.0.0.0/24"))
	z, _ := st.CreateZone(ctx, "lan.example", "forward", "", 300)
	if err := st.LinkSubnetZone(ctx, sb.ID, z.ID, "forward"); err != nil {
		t.Fatalf("link: %v", err)
	}
	h, _ := st.CreateHost(ctx, "the-host-name-is-long", "", "")
	if err := st.UpdateHost(ctx, h.ID, "the-host-name-is-long", "", "", "short", true); err != nil {
		t.Fatalf("set dnsname: %v", err)
	}
	n, _ := st.CreateNIC(ctx, h.ID, "aa:bb:cc:dd:ee:03", "")
	if _, err := st.CreateIPAssignment(ctx, n.ID, nil, "10.0.0.5", "static"); err != nil {
		t.Fatalf("ip: %v", err)
	}

	if err := Run(ctx, st); err != nil {
		t.Fatalf("Run: %v", err)
	}
	rows := listRecordRows(t, st)
	if len(rows) != 1 || rows[0].FQDN != "short.lan.example" {
		t.Errorf("expected dns_name override -> short.lan.example, got %+v", rows)
	}
}

func TestRunSkipsDynamicIPs(t *testing.T) {
	st := openStore(t)
	ctx := context.Background()
	sb, _ := st.CreateSubnet(ctx, subnet("10.0.0.0/24"))
	z, _ := st.CreateZone(ctx, "lan.example", "forward", "", 300)
	if err := st.LinkSubnetZone(ctx, sb.ID, z.ID, "forward"); err != nil {
		t.Fatalf("link: %v", err)
	}
	if err := st.UpsertDynamicLease(ctx, store.LeaseObserved{MAC: "aa:bb:cc:dd:ee:f0", IP: "10.0.0.5"}); err != nil {
		t.Fatalf("dynamic: %v", err)
	}

	if err := Run(ctx, st); err != nil {
		t.Fatalf("Run: %v", err)
	}
	rows := listRecordRows(t, st)
	if len(rows) != 0 {
		t.Errorf("dynamic IPs should NOT produce auto records, got %+v", rows)
	}
}

func TestRunManualWinsOverAuto(t *testing.T) {
	st := openStore(t)
	ctx := context.Background()
	sb, _ := st.CreateSubnet(ctx, subnet("10.0.0.0/24"))
	z, _ := st.CreateZone(ctx, "lan.example", "forward", "", 300)
	if err := st.LinkSubnetZone(ctx, sb.ID, z.ID, "forward"); err != nil {
		t.Fatalf("link: %v", err)
	}
	h, _ := st.CreateHost(ctx, "srv", "", "")
	n, _ := st.CreateNIC(ctx, h.ID, "aa:bb:cc:dd:ee:04", "")
	if _, err := st.CreateIPAssignment(ctx, n.ID, nil, "10.0.0.7", "static"); err != nil {
		t.Fatalf("ip: %v", err)
	}
	// Pre-existing manual A for srv.lan.example — synth should suppress auto.
	if _, err := st.CreateManualDNSRecord(ctx, "lan.example", "srv.lan.example", "A", "9.9.9.9", 60); err != nil {
		t.Fatalf("manual: %v", err)
	}

	if err := Run(ctx, st); err != nil {
		t.Fatalf("Run: %v", err)
	}
	rows := listRecordRows(t, st)
	if len(rows) != 1 || rows[0].Source != "manual" || rows[0].Target != "9.9.9.9" {
		t.Errorf("manual should win, got %+v", rows)
	}
}

func TestRunIdempotentAndConverges(t *testing.T) {
	st := openStore(t)
	ctx := context.Background()
	sb, _ := st.CreateSubnet(ctx, subnet("10.0.0.0/24"))
	z, _ := st.CreateZone(ctx, "lan.example", "forward", "", 300)
	if err := st.LinkSubnetZone(ctx, sb.ID, z.ID, "forward"); err != nil {
		t.Fatalf("link: %v", err)
	}
	h, _ := st.CreateHost(ctx, "h", "", "")
	n, _ := st.CreateNIC(ctx, h.ID, "aa:bb:cc:dd:ee:05", "")
	if _, err := st.CreateIPAssignment(ctx, n.ID, nil, "10.0.0.8", "static"); err != nil {
		t.Fatalf("ip: %v", err)
	}

	if err := Run(ctx, st); err != nil {
		t.Fatalf("Run 1: %v", err)
	}
	first := listRecordRows(t, st)
	if err := Run(ctx, st); err != nil {
		t.Fatalf("Run 2: %v", err)
	}
	second := listRecordRows(t, st)
	if len(first) != len(second) {
		t.Fatalf("idempotency broke: %d -> %d", len(first), len(second))
	}

	// Delete the IP — second Run should drop the auto record.
	if err := st.DeleteHost(ctx, h.ID); err != nil {
		t.Fatalf("delete host: %v", err)
	}
	if err := Run(ctx, st); err != nil {
		t.Fatalf("Run 3: %v", err)
	}
	after := listRecordRows(t, st)
	if len(after) != 0 {
		t.Errorf("auto record should be deleted when source IP gone, got %+v", after)
	}
}

func TestRunIPv6PTR(t *testing.T) {
	st := openStore(t)
	ctx := context.Background()
	sb, _ := st.CreateSubnet(ctx, subnet("2001:db8::/64"))
	zf, _ := st.CreateZone(ctx, "lan.example", "forward", "", 300)
	// /64 reverse zone: nibbles of 2001:db8:: prefix.
	zr, _ := st.CreateZone(ctx, "0.0.0.0.0.0.0.0.0.0.0.0.8.b.d.0.1.0.0.2.ip6.arpa", "reverse", "", 300)
	if err := st.LinkSubnetZone(ctx, sb.ID, zf.ID, "forward"); err != nil {
		t.Fatalf("link fwd: %v", err)
	}
	if err := st.LinkSubnetZone(ctx, sb.ID, zr.ID, "reverse"); err != nil {
		t.Fatalf("link rev: %v", err)
	}
	h, _ := st.CreateHost(ctx, "v6host", "", "")
	n, _ := st.CreateNIC(ctx, h.ID, "aa:bb:cc:dd:ee:06", "")
	if _, err := st.CreateIPAssignment(ctx, n.ID, nil, "2001:db8::1", "static"); err != nil {
		t.Fatalf("ip: %v", err)
	}

	if err := Run(ctx, st); err != nil {
		t.Fatalf("Run: %v", err)
	}
	rows := listRecordRows(t, st)
	types := []string{}
	for _, r := range rows {
		types = append(types, r.Type)
	}
	sort.Strings(types)
	if len(types) != 2 || types[0] != "AAAA" || types[1] != "PTR" {
		t.Errorf("expected AAAA + PTR for v6, got %v (rows=%+v)", types, rows)
	}
}
