package mcp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"netdb/internal/kea"
	"netdb/internal/model"
	"netdb/internal/reconcile"
	"netdb/internal/store"
)

// wiredServer returns a Server with every netdb tool registered and a real
// store + reconciler + kea syncer (no URLs so the kea syncer is a no-op).
// Reconciler is wired with an empty config — Trigger is non-blocking, so
// background goroutines won't impact tests.
func wiredServer(t *testing.T) (*Server, *store.Store) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "mcp.sqlite"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := st.Migrate(context.Background()); err != nil {
		st.Close()
		t.Fatalf("Migrate: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	rec := reconcile.New(st, reconcile.Config{}, time.Hour)
	ks := kea.New(st, "", time.Hour)
	lp := kea.NewLeasePoller(st, "", time.Hour)

	s := RegisterAll(New(ServerInfo{Name: "netdb", Version: "test"}), Deps{
		Store: st, Reconciler: rec, Kea: ks, LeasePoller: lp,
	})
	return s, st
}

// callTool dispatches a tools/call against the server and returns the
// decoded "result" wrapper. It returns the decoded text payload from the
// content[0].text field, plus whether isError was set.
func callTool(t *testing.T, s *Server, name string, args map[string]any) (string, bool) {
	t.Helper()
	params, _ := json.Marshal(map[string]any{"name": name, "arguments": args})
	msg := rpcMessage{JSONRPC: "2.0", Method: "tools/call", ID: json.RawMessage(`1`), Params: params}
	body, _ := json.Marshal(msg)
	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(string(body)))
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("non-200: %d body=%s", rec.Code, rec.Body.String())
	}
	var resp rpcMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v body=%s", err, rec.Body.String())
	}
	if resp.Error != nil {
		t.Fatalf("rpc error on %s: %+v", name, resp.Error)
	}
	var env struct {
		Content []struct{ Text string }
		IsError bool `json:"isError"`
	}
	if err := json.Unmarshal(resp.Result, &env); err != nil {
		t.Fatalf("envelope decode: %v result=%s", err, resp.Result)
	}
	if len(env.Content) == 0 {
		t.Fatalf("empty content: %s", resp.Result)
	}
	return env.Content[0].Text, env.IsError
}

func TestToolsListIncludesEverything(t *testing.T) {
	s, _ := wiredServer(t)
	if got := len(s.tools); got < 20 {
		t.Errorf("expected lots of tools registered, got %d", got)
	}
	// Quick sanity that destructive tools got the [DESTRUCTIVE] prefix.
	if !strings.HasPrefix(s.byName["create_host"].Description, "[DESTRUCTIVE]") {
		t.Errorf("create_host description: %q", s.byName["create_host"].Description)
	}
}

func TestHostsLifecycleViaTools(t *testing.T) {
	s, st := wiredServer(t)

	// create_host
	out, isErr := callTool(t, s, "create_host", map[string]any{
		"name": "router", "description": "edge", "tags": "core",
	})
	if isErr {
		t.Fatalf("create_host err: %s", out)
	}
	hosts, _ := st.ListHosts(context.Background())
	if len(hosts) != 1 || hosts[0].Name != "router" {
		t.Fatalf("not in DB: %+v", hosts)
	}
	hostID := hosts[0].ID

	// list_hosts
	out, isErr = callTool(t, s, "list_hosts", nil)
	if isErr || !strings.Contains(out, "router") {
		t.Errorf("list_hosts: err=%v out=%s", isErr, out)
	}

	// get_host
	out, isErr = callTool(t, s, "get_host", map[string]any{"id": float64(hostID)})
	if isErr || !strings.Contains(out, "router") {
		t.Errorf("get_host: err=%v out=%s", isErr, out)
	}

	// update_host
	_, isErr = callTool(t, s, "update_host", map[string]any{
		"id": float64(hostID), "name": "router-1",
		"dns_name": "r", "dns_enabled": true,
	})
	if isErr {
		t.Errorf("update_host failed")
	}
	got, _ := st.GetHost(context.Background(), hostID)
	if got.Name != "router-1" || got.DNSName != "r" || !got.DNSEnabled {
		t.Errorf("after update: %+v", got)
	}

	// delete_host
	_, isErr = callTool(t, s, "delete_host", map[string]any{"id": float64(hostID)})
	if isErr {
		t.Errorf("delete_host failed")
	}
}

func TestHostsToolErrors(t *testing.T) {
	s, _ := wiredServer(t)
	// Missing required arg
	if _, isErr := callTool(t, s, "create_host", map[string]any{}); !isErr {
		t.Error("expected isError on missing name")
	}
	// get_host bad id type
	if _, isErr := callTool(t, s, "get_host", map[string]any{"id": "not-a-number"}); !isErr {
		t.Error("expected isError on non-numeric id")
	}
	// update_host missing name
	if _, isErr := callTool(t, s, "update_host", map[string]any{"id": float64(1)}); !isErr {
		t.Error("expected isError on missing name in update_host")
	}
	// delete_host missing id
	if _, isErr := callTool(t, s, "delete_host", map[string]any{}); !isErr {
		t.Error("expected isError on missing id")
	}
}

func TestNICsAndIPsViaTools(t *testing.T) {
	s, st := wiredServer(t)
	h, _ := st.CreateHost(context.Background(), "h", "", "")

	// create_nic
	_, isErr := callTool(t, s, "create_nic", map[string]any{
		"host_id": float64(h.ID), "mac": "aa:bb:cc:dd:ee:f0", "label": "eth0",
	})
	if isErr {
		t.Fatalf("create_nic failed")
	}
	nics, _ := st.ListNICsByHost(context.Background(), h.ID)
	if len(nics) != 1 {
		t.Fatalf("expected 1 nic, got %d", len(nics))
	}
	nicID := nics[0].ID

	// create_ip with subnet_id
	sb, _ := st.CreateSubnet(context.Background(), model.Subnet{CIDR: "10.0.0.0/24"})
	_, isErr = callTool(t, s, "create_ip", map[string]any{
		"nic_id": float64(nicID), "ip": "10.0.0.7", "kind": "static",
		"subnet_id": float64(sb.ID),
	})
	if isErr {
		t.Fatalf("create_ip with subnet failed")
	}
	ips, _ := st.ListIPsByNIC(context.Background(), nicID)
	if len(ips) != 1 || *ips[0].SubnetID != sb.ID {
		t.Errorf("ip not bound to subnet: %+v", ips)
	}

	// create_ip without subnet_id (covers the else branch)
	_, isErr = callTool(t, s, "create_ip", map[string]any{
		"nic_id": float64(nicID), "ip": "10.0.0.8", "kind": "reserved",
	})
	if isErr {
		t.Errorf("create_ip without subnet failed")
	}

	// delete_ip
	ips, _ = st.ListIPsByNIC(context.Background(), nicID)
	_, isErr = callTool(t, s, "delete_ip", map[string]any{"id": float64(ips[0].ID)})
	if isErr {
		t.Errorf("delete_ip failed")
	}

	// delete_nic
	_, isErr = callTool(t, s, "delete_nic", map[string]any{"id": float64(nicID)})
	if isErr {
		t.Errorf("delete_nic failed")
	}
}

func TestNICIPToolErrors(t *testing.T) {
	s, _ := wiredServer(t)
	if _, e := callTool(t, s, "create_nic", map[string]any{}); !e {
		t.Error("expected err missing host_id")
	}
	if _, e := callTool(t, s, "create_nic", map[string]any{"host_id": float64(1)}); !e {
		t.Error("expected err missing mac")
	}
	if _, e := callTool(t, s, "delete_nic", map[string]any{}); !e {
		t.Error("expected err missing id")
	}
	if _, e := callTool(t, s, "create_ip", map[string]any{}); !e {
		t.Error("expected err missing fields")
	}
	if _, e := callTool(t, s, "create_ip", map[string]any{"nic_id": float64(1), "ip": "10.0.0.1"}); !e {
		t.Error("expected err missing kind")
	}
	if _, e := callTool(t, s, "create_ip", map[string]any{"nic_id": float64(1), "ip": "10.0.0.1", "kind": "static", "subnet_id": "bad"}); !e {
		t.Error("expected err bad subnet_id")
	}
	if _, e := callTool(t, s, "delete_ip", map[string]any{}); !e {
		t.Error("expected err missing id")
	}
}

func TestSubnetCRUDViaTools(t *testing.T) {
	s, st := wiredServer(t)

	// create_subnet (full args)
	_, isErr := callTool(t, s, "create_subnet", map[string]any{
		"cidr": "10.10.0.0/24", "vlan": float64(10), "gateway": "10.10.0.1",
		"description": "lab", "dhcp_range_start": "100", "dhcp_range_end": "200",
		"dhcp_options": `{"foo":"bar"}`, "dns_servers": "10.10.0.1",
	})
	if isErr {
		t.Fatalf("create_subnet")
	}
	subs, _ := st.ListSubnets(context.Background())
	if len(subs) != 1 || subs[0].VLAN == nil || *subs[0].VLAN != 10 {
		t.Errorf("subnet wrong: %+v", subs)
	}
	sid := subs[0].ID

	// list_subnets
	out, _ := callTool(t, s, "list_subnets", nil)
	if !strings.Contains(out, "10.10.0.0/24") {
		t.Errorf("list_subnets missing cidr: %s", out)
	}

	// get_subnet
	out, _ = callTool(t, s, "get_subnet", map[string]any{"id": float64(sid)})
	if !strings.Contains(out, "10.10.0.0/24") {
		t.Errorf("get_subnet: %s", out)
	}

	// update_subnet (no vlan key → VLAN remains as-is via update path)
	_, isErr = callTool(t, s, "update_subnet", map[string]any{
		"id": float64(sid), "cidr": "10.10.0.0/24",
		"description": "lab2",
	})
	if isErr {
		t.Errorf("update_subnet")
	}

	// update_subnet with VLAN
	_, isErr = callTool(t, s, "update_subnet", map[string]any{
		"id": float64(sid), "cidr": "10.10.0.0/24", "vlan": float64(20),
	})
	if isErr {
		t.Errorf("update_subnet vlan")
	}

	// delete_subnet
	_, isErr = callTool(t, s, "delete_subnet", map[string]any{"id": float64(sid)})
	if isErr {
		t.Errorf("delete_subnet")
	}
}

func TestSubnetToolErrors(t *testing.T) {
	s, _ := wiredServer(t)
	if _, e := callTool(t, s, "create_subnet", map[string]any{}); !e {
		t.Error("expected err missing cidr")
	}
	if _, e := callTool(t, s, "get_subnet", map[string]any{}); !e {
		t.Error("expected err missing id")
	}
	if _, e := callTool(t, s, "update_subnet", map[string]any{}); !e {
		t.Error("expected err missing id")
	}
	if _, e := callTool(t, s, "update_subnet", map[string]any{"id": float64(1)}); !e {
		t.Error("expected err missing cidr")
	}
	if _, e := callTool(t, s, "update_subnet", map[string]any{"id": float64(9999), "cidr": "10.0.0.0/24"}); !e {
		t.Error("expected err not-found")
	}
	if _, e := callTool(t, s, "delete_subnet", map[string]any{}); !e {
		t.Error("expected err missing id")
	}
}

func TestSubnetZoneAndZoneProviderLinks(t *testing.T) {
	s, st := wiredServer(t)
	sb, _ := st.CreateSubnet(context.Background(), model.Subnet{CIDR: "10.0.0.0/24"})

	// create_zone
	_, isErr := callTool(t, s, "create_zone", map[string]any{
		"name": "lan.example", "kind": "forward", "description": "x", "default_ttl": float64(600),
	})
	if isErr {
		t.Fatalf("create_zone")
	}
	zones, _ := st.ListZones(context.Background())
	if len(zones) != 1 {
		t.Fatalf("expected 1 zone")
	}
	zid := zones[0].ID

	// link_subnet_zone
	_, isErr = callTool(t, s, "link_subnet_zone", map[string]any{
		"subnet_id": float64(sb.ID), "zone_id": float64(zid), "role": "forward",
	})
	if isErr {
		t.Errorf("link_subnet_zone")
	}

	// unlink_subnet_zone
	_, isErr = callTool(t, s, "unlink_subnet_zone", map[string]any{
		"subnet_id": float64(sb.ID), "zone_id": float64(zid), "role": "forward",
	})
	if isErr {
		t.Errorf("unlink_subnet_zone")
	}

	// list_zones
	out, _ := callTool(t, s, "list_zones", nil)
	if !strings.Contains(out, "lan.example") {
		t.Errorf("list_zones: %s", out)
	}

	// create_provider then link/unlink
	_, isErr = callTool(t, s, "create_provider", map[string]any{
		"name": "cf", "kind": "cloudflare", "config_json": `{}`, "enabled": true,
	})
	if isErr {
		t.Fatalf("create_provider")
	}
	provs, _ := st.ListProviders(context.Background())
	pid := provs[0].ID

	_, isErr = callTool(t, s, "link_zone_provider", map[string]any{
		"zone_id": float64(zid), "provider_id": float64(pid),
	})
	if isErr {
		t.Errorf("link_zone_provider")
	}
	_, isErr = callTool(t, s, "unlink_zone_provider", map[string]any{
		"zone_id": float64(zid), "provider_id": float64(pid),
	})
	if isErr {
		t.Errorf("unlink_zone_provider")
	}

	// list_providers
	out, _ = callTool(t, s, "list_providers", nil)
	if !strings.Contains(out, "cf") {
		t.Errorf("list_providers: %s", out)
	}

	// delete_provider
	_, isErr = callTool(t, s, "delete_provider", map[string]any{"id": float64(pid)})
	if isErr {
		t.Errorf("delete_provider")
	}

	// delete_zone
	_, isErr = callTool(t, s, "delete_zone", map[string]any{"id": float64(zid)})
	if isErr {
		t.Errorf("delete_zone")
	}
}

func TestZoneAndProviderToolErrors(t *testing.T) {
	s, _ := wiredServer(t)
	if _, e := callTool(t, s, "create_zone", map[string]any{}); !e {
		t.Error("create_zone missing name")
	}
	if _, e := callTool(t, s, "create_zone", map[string]any{"name": "x"}); !e {
		t.Error("create_zone missing kind")
	}
	if _, e := callTool(t, s, "delete_zone", map[string]any{}); !e {
		t.Error("delete_zone missing id")
	}
	if _, e := callTool(t, s, "delete_zone", map[string]any{"id": float64(9999)}); !e {
		t.Error("delete_zone not found")
	}
	if _, e := callTool(t, s, "link_zone_provider", map[string]any{}); !e {
		t.Error("link_zone_provider missing zone_id")
	}
	if _, e := callTool(t, s, "link_zone_provider", map[string]any{"zone_id": float64(1)}); !e {
		t.Error("link_zone_provider missing provider_id")
	}
	if _, e := callTool(t, s, "unlink_zone_provider", map[string]any{}); !e {
		t.Error("unlink missing zone_id")
	}
	if _, e := callTool(t, s, "unlink_zone_provider", map[string]any{"zone_id": float64(1)}); !e {
		t.Error("unlink missing provider_id")
	}
	if _, e := callTool(t, s, "create_provider", map[string]any{}); !e {
		t.Error("create_provider missing name")
	}
	if _, e := callTool(t, s, "create_provider", map[string]any{"name": "x"}); !e {
		t.Error("create_provider missing kind")
	}
	if _, e := callTool(t, s, "delete_provider", map[string]any{}); !e {
		t.Error("delete_provider missing id")
	}
	if _, e := callTool(t, s, "link_subnet_zone", map[string]any{}); !e {
		t.Error("link_subnet_zone missing subnet_id")
	}
	if _, e := callTool(t, s, "link_subnet_zone", map[string]any{"subnet_id": float64(1)}); !e {
		t.Error("missing zone_id")
	}
	if _, e := callTool(t, s, "link_subnet_zone", map[string]any{"subnet_id": float64(1), "zone_id": float64(2)}); !e {
		t.Error("missing role")
	}
	if _, e := callTool(t, s, "unlink_subnet_zone", map[string]any{}); !e {
		t.Error("unlink_subnet_zone missing subnet_id")
	}
	if _, e := callTool(t, s, "unlink_subnet_zone", map[string]any{"subnet_id": float64(1)}); !e {
		t.Error("missing zone_id")
	}
	if _, e := callTool(t, s, "unlink_subnet_zone", map[string]any{"subnet_id": float64(1), "zone_id": float64(2)}); !e {
		t.Error("missing role")
	}
}

func TestDNSAndReservationsTools(t *testing.T) {
	s, st := wiredServer(t)
	_, _ = st.CreateZone(context.Background(), "lan.example", "forward", "", 300)

	// create_manual_dns
	out, isErr := callTool(t, s, "create_manual_dns", map[string]any{
		"zone": "lan.example", "fqdn": "x.lan.example",
		"rtype": "A", "target": "10.0.0.1", "ttl": float64(60),
	})
	if isErr {
		t.Fatalf("create_manual_dns: %s", out)
	}
	views, _ := st.ListDNSRecords(context.Background())
	if len(views) != 1 {
		t.Fatalf("expected 1 record")
	}

	// list_dns_records
	out, _ = callTool(t, s, "list_dns_records", nil)
	if !strings.Contains(out, "x.lan.example") {
		t.Errorf("list_dns_records: %s", out)
	}

	// delete_dns_record
	_, isErr = callTool(t, s, "delete_dns_record", map[string]any{"id": float64(views[0].ID)})
	if isErr {
		t.Errorf("delete_dns_record")
	}

	// dns_sync_now
	out, isErr = callTool(t, s, "dns_sync_now", nil)
	if isErr || !strings.Contains(out, "triggered") {
		t.Errorf("dns_sync_now: err=%v out=%s", isErr, out)
	}

	// list_reservations
	out, _ = callTool(t, s, "list_reservations", nil)
	if out == "" {
		t.Errorf("expected non-empty list_reservations result")
	}
}

func TestDNSToolErrors(t *testing.T) {
	s, _ := wiredServer(t)
	// missing every required field
	if _, e := callTool(t, s, "create_manual_dns", map[string]any{}); !e {
		t.Error("missing zone")
	}
	if _, e := callTool(t, s, "create_manual_dns", map[string]any{"zone": "z"}); !e {
		t.Error("missing fqdn")
	}
	if _, e := callTool(t, s, "create_manual_dns", map[string]any{"zone": "z", "fqdn": "x"}); !e {
		t.Error("missing rtype")
	}
	if _, e := callTool(t, s, "create_manual_dns", map[string]any{
		"zone": "z", "fqdn": "x", "rtype": "A",
	}); !e {
		t.Error("missing target")
	}
	if _, e := callTool(t, s, "delete_dns_record", map[string]any{}); !e {
		t.Error("delete_dns missing id")
	}
	if _, e := callTool(t, s, "delete_dns_record", map[string]any{"id": float64(9999)}); !e {
		t.Error("delete_dns not found")
	}
}

func TestDHCPTools(t *testing.T) {
	s, _ := wiredServer(t)
	// dhcp_status returns sync + leases keys
	out, isErr := callTool(t, s, "dhcp_status", nil)
	if isErr || !strings.Contains(out, "sync") || !strings.Contains(out, "leases") {
		t.Errorf("dhcp_status: err=%v out=%s", isErr, out)
	}
	// kea_sync_now / kea_poll_leases are safe to call (kea/leasePoller no-op when URL is empty)
	if out, isErr := callTool(t, s, "kea_sync_now", nil); isErr || !strings.Contains(out, "triggered") {
		t.Errorf("kea_sync_now: %s", out)
	}
	if out, isErr := callTool(t, s, "kea_poll_leases", nil); isErr || !strings.Contains(out, "triggered") {
		t.Errorf("kea_poll_leases: %s", out)
	}
}

func TestSchemaHelpers(t *testing.T) {
	// objectSchema with nil props
	got := objectSchema(nil, nil)
	if got["type"] != "object" {
		t.Errorf("objectSchema type: %v", got["type"])
	}
	if _, has := got["required"]; has {
		t.Errorf("expected no 'required' on empty required slice")
	}
	// objectSchema with required
	got = objectSchema(map[string]any{"x": stringSchema("d")}, []string{"x"})
	req, _ := got["required"].([]string)
	if len(req) != 1 || req[0] != "x" {
		t.Errorf("required slice not preserved: %v", got["required"])
	}
	// stringSchema/intSchema/boolSchema return type-tagged maps
	if stringSchema("d")["type"] != "string" || intSchema("d")["type"] != "integer" || boolSchema("d")["type"] != "boolean" {
		t.Error("schema helpers return wrong type")
	}
}

