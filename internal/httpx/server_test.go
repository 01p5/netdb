package httpx

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"netdb/internal/kea"
	"netdb/internal/reconcile"
	"netdb/internal/store"
)

func newTestServer(t *testing.T, opts Options) (*Server, *store.Store) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "httpx.sqlite"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := st.Migrate(context.Background()); err != nil {
		st.Close()
		t.Fatalf("Migrate: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	rec := reconcile.New(st, reconcile.Config{}, time.Hour)
	keaSync := kea.New(st, "", time.Hour) // no URL — no-op
	leasePoller := kea.NewLeasePoller(st, "", time.Hour)
	srv := New(st, rec, keaSync, leasePoller, opts)
	return srv, st
}

// do is a small helper to run a request against the server in-process.
func do(t *testing.T, srv *Server, method, target string, body string, headers ...string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	return rec
}

func TestHealthzOK(t *testing.T) {
	srv, _ := newTestServer(t, Options{})
	rec := do(t, srv, "GET", "/healthz", "")
	if rec.Code != 200 {
		t.Fatalf("healthz code: %d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "ok") {
		t.Errorf("body: %s", rec.Body.String())
	}
}

func TestHealthzBypassesAuth(t *testing.T) {
	srv, _ := newTestServer(t, Options{AuthUser: "u", AuthPassword: "p"})
	rec := do(t, srv, "GET", "/healthz", "")
	if rec.Code != 200 {
		t.Errorf("expected healthz unauthenticated, got %d", rec.Code)
	}
}

func TestAuthGate(t *testing.T) {
	srv, _ := newTestServer(t, Options{AuthUser: "u", AuthPassword: "p"})
	rec := do(t, srv, "GET", "/hosts", "")
	if rec.Code != 401 {
		t.Errorf("expected 401, got %d", rec.Code)
	}
	if got := rec.Header().Get("WWW-Authenticate"); !strings.Contains(got, "Basic") {
		t.Errorf("WWW-Authenticate header: %q", got)
	}

	// Now with creds.
	req := httptest.NewRequest("GET", "/hosts", nil)
	req.SetBasicAuth("u", "p")
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Errorf("authed got %d body=%s", rec.Code, rec.Body.String())
	}

	// Static files bypass.
	rec = do(t, srv, "GET", "/static/htmx.min.js", "")
	if rec.Code != 200 {
		t.Errorf("static bypass: got %d", rec.Code)
	}
}

func TestRootRedirects(t *testing.T) {
	srv, _ := newTestServer(t, Options{})
	rec := do(t, srv, "GET", "/", "")
	if rec.Code != http.StatusFound {
		t.Errorf("expected 302 from /, got %d", rec.Code)
	}
	if rec.Header().Get("Location") != "/hosts" {
		t.Errorf("Location: %q", rec.Header().Get("Location"))
	}
}

func TestRootNonRootIs404(t *testing.T) {
	srv, _ := newTestServer(t, Options{})
	rec := do(t, srv, "GET", "/does-not-exist", "")
	if rec.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", rec.Code)
	}
}

func TestHostsCreateAndList(t *testing.T) {
	srv, st := newTestServer(t, Options{})
	form := url.Values{"name": {"router"}, "description": {"edge"}, "tags": {"prod"}}.Encode()
	rec := do(t, srv, "POST", "/hosts", form)
	if rec.Code != 200 {
		t.Fatalf("POST /hosts: %d body=%s", rec.Code, rec.Body.String())
	}
	// HX-fragment should contain the host name.
	if !strings.Contains(rec.Body.String(), "router") {
		t.Errorf("fragment missing host name: %s", rec.Body.String())
	}
	hosts, _ := st.ListHosts(context.Background())
	if len(hosts) != 1 {
		t.Errorf("expected 1 host, got %d", len(hosts))
	}

	rec = do(t, srv, "GET", "/hosts", "")
	if rec.Code != 200 {
		t.Errorf("list page: %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "router") {
		t.Errorf("list page missing host")
	}
}

func TestHostsCreateMissingName(t *testing.T) {
	srv, _ := newTestServer(t, Options{})
	rec := do(t, srv, "POST", "/hosts", "")
	if rec.Code != 400 {
		t.Errorf("expected 400 missing name, got %d", rec.Code)
	}
}

func TestHostDetailAndUpdate(t *testing.T) {
	srv, st := newTestServer(t, Options{})
	h, _ := st.CreateHost(context.Background(), "svr", "", "")

	rec := do(t, srv, "GET", "/hosts/"+toStr(h.ID), "")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "svr") {
		t.Errorf("detail: %d %s", rec.Code, rec.Body.String())
	}

	form := url.Values{"name": {"svr2"}, "dns_name": {"s"}, "dns_enabled": {"on"}}.Encode()
	rec = do(t, srv, "POST", "/hosts/"+toStr(h.ID), form)
	if rec.Code != 200 {
		t.Errorf("update: %d body=%s", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("HX-Redirect") == "" {
		t.Errorf("expected HX-Redirect on update")
	}
	got, _ := st.GetHost(context.Background(), h.ID)
	if got.Name != "svr2" || got.DNSName != "s" || !got.DNSEnabled {
		t.Errorf("after update: %+v", got)
	}
}

func TestHostDetailMissing(t *testing.T) {
	srv, _ := newTestServer(t, Options{})
	rec := do(t, srv, "GET", "/hosts/9999", "")
	if rec.Code != 404 {
		t.Errorf("got %d", rec.Code)
	}
	rec = do(t, srv, "GET", "/hosts/garbage", "")
	if rec.Code != 400 {
		t.Errorf("got %d", rec.Code)
	}
}

func TestHostDelete(t *testing.T) {
	srv, st := newTestServer(t, Options{})
	h, _ := st.CreateHost(context.Background(), "h", "", "")
	rec := do(t, srv, "DELETE", "/hosts/"+toStr(h.ID), "")
	if rec.Code != 200 {
		t.Errorf("delete: %d", rec.Code)
	}
	rec = do(t, srv, "DELETE", "/hosts/"+toStr(h.ID), "")
	if rec.Code != 404 {
		t.Errorf("re-delete: %d", rec.Code)
	}
}

func TestNICAndIPCRUD(t *testing.T) {
	srv, st := newTestServer(t, Options{})
	h, _ := st.CreateHost(context.Background(), "h", "", "")

	form := url.Values{"mac": {"aa:bb:cc:dd:ee:01"}, "label": {"eth0"}}.Encode()
	rec := do(t, srv, "POST", "/hosts/"+toStr(h.ID)+"/nics", form)
	if rec.Code != 200 {
		t.Fatalf("create nic: %d %s", rec.Code, rec.Body.String())
	}
	nics, _ := st.ListNICsByHost(context.Background(), h.ID)
	if len(nics) != 1 {
		t.Fatalf("expected 1 nic, got %d", len(nics))
	}

	form = url.Values{"ip": {"10.0.0.5"}, "kind": {"static"}}.Encode()
	rec = do(t, srv, "POST", "/nics/"+toStr(nics[0].ID)+"/ips", form)
	if rec.Code != 200 {
		t.Fatalf("create ip: %d %s", rec.Code, rec.Body.String())
	}
	ips, _ := st.ListIPsByNIC(context.Background(), nics[0].ID)
	if len(ips) != 1 {
		t.Fatalf("expected 1 ip, got %d", len(ips))
	}

	// Bad MAC -> 400.
	form = url.Values{"mac": {"garbage"}}.Encode()
	rec = do(t, srv, "POST", "/hosts/"+toStr(h.ID)+"/nics", form)
	if rec.Code != 400 {
		t.Errorf("bad mac: %d", rec.Code)
	}
	// Missing fields -> 400.
	rec = do(t, srv, "POST", "/nics/"+toStr(nics[0].ID)+"/ips", "")
	if rec.Code != 400 {
		t.Errorf("missing ip/kind: %d", rec.Code)
	}

	rec = do(t, srv, "DELETE", "/ips/"+toStr(ips[0].ID), "")
	if rec.Code != 200 {
		t.Errorf("delete ip: %d", rec.Code)
	}
	rec = do(t, srv, "DELETE", "/nics/"+toStr(nics[0].ID), "")
	if rec.Code != 200 {
		t.Errorf("delete nic: %d", rec.Code)
	}
}

func TestSubnetCRUDAndZoneLink(t *testing.T) {
	srv, st := newTestServer(t, Options{})

	form := url.Values{
		"cidr": {"10.10.0.0/24"}, "vlan": {"10"}, "gateway": {"10.10.0.1"},
		"dhcp_range_start": {"100"}, "dhcp_range_end": {"200"},
		"dns_servers": {"10.10.0.1"},
	}.Encode()
	rec := do(t, srv, "POST", "/subnets", form)
	if rec.Code != 200 {
		t.Fatalf("create subnet: %d %s", rec.Code, rec.Body.String())
	}
	subs, _ := st.ListSubnets(context.Background())
	if len(subs) != 1 || subs[0].VLAN == nil || *subs[0].VLAN != 10 {
		t.Errorf("subnet not created: %+v", subs)
	}

	// vlan must be integer
	bad := url.Values{"cidr": {"10.20.0.0/24"}, "vlan": {"nope"}}.Encode()
	rec = do(t, srv, "POST", "/subnets", bad)
	if rec.Code != 400 {
		t.Errorf("expected 400 bad vlan, got %d", rec.Code)
	}

	// List page renders.
	rec = do(t, srv, "GET", "/subnets", "")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "10.10.0.0/24") {
		t.Errorf("subnet list bad: %d %s", rec.Code, rec.Body.String())
	}

	// Update with VLAN=nil
	form = url.Values{"cidr": {subs[0].CIDR}, "vlan": {""}}.Encode()
	rec = do(t, srv, "POST", "/subnets/"+toStr(subs[0].ID), form)
	if rec.Code != 200 {
		t.Errorf("update: %d %s", rec.Code, rec.Body.String())
	}

	// Link zone (need a zone first).
	z, _ := st.CreateZone(context.Background(), "lan.example", "forward", "", 300)
	form = url.Values{"zone_id": {toStr(z.ID)}, "role": {"forward"}}.Encode()
	rec = do(t, srv, "POST", "/subnets/"+toStr(subs[0].ID)+"/zones", form)
	if rec.Code != 200 {
		t.Errorf("link zone: %d %s", rec.Code, rec.Body.String())
	}

	rec = do(t, srv, "DELETE", "/subnets/"+toStr(subs[0].ID)+"/zones/"+toStr(z.ID)+"/forward", "")
	if rec.Code != 200 {
		t.Errorf("unlink: %d", rec.Code)
	}

	rec = do(t, srv, "DELETE", "/subnets/"+toStr(subs[0].ID), "")
	if rec.Code != 200 {
		t.Errorf("delete: %d", rec.Code)
	}
}

func TestZonesAndProviders(t *testing.T) {
	srv, st := newTestServer(t, Options{})

	form := url.Values{"name": {"lan.example"}, "kind": {"forward"}, "default_ttl": {"600"}}.Encode()
	rec := do(t, srv, "POST", "/zones", form)
	if rec.Code != 200 {
		t.Fatalf("create zone: %d %s", rec.Code, rec.Body.String())
	}
	// Bad ttl
	bad := url.Values{"name": {"x"}, "kind": {"forward"}, "default_ttl": {"nope"}}.Encode()
	if rec := do(t, srv, "POST", "/zones", bad); rec.Code != 400 {
		t.Errorf("expected 400 bad ttl, got %d", rec.Code)
	}
	// Missing fields
	if rec := do(t, srv, "POST", "/zones", ""); rec.Code != 400 {
		t.Errorf("expected 400 missing fields, got %d", rec.Code)
	}

	rec = do(t, srv, "GET", "/zones", "")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "lan.example") {
		t.Errorf("zones list: %d", rec.Code)
	}

	// Create provider, link to zone.
	form = url.Values{"name": {"cf"}, "kind": {"cloudflare"}}.Encode()
	rec = do(t, srv, "POST", "/providers", form)
	if rec.Code != 200 {
		t.Errorf("create provider: %d %s", rec.Code, rec.Body.String())
	}
	zones, _ := st.ListZones(context.Background())
	provs, _ := st.ListProviders(context.Background())
	if len(zones) != 1 || len(provs) != 1 {
		t.Fatalf("setup wrong: %d zones %d providers", len(zones), len(provs))
	}

	form = url.Values{"provider_id": {toStr(provs[0].ID)}}.Encode()
	rec = do(t, srv, "POST", "/zones/"+toStr(zones[0].ID)+"/providers", form)
	if rec.Code != 200 {
		t.Errorf("link provider: %d %s", rec.Code, rec.Body.String())
	}
	rec = do(t, srv, "DELETE", "/zones/"+toStr(zones[0].ID)+"/providers/"+toStr(provs[0].ID), "")
	if rec.Code != 200 {
		t.Errorf("unlink: %d", rec.Code)
	}

	// providers list page renders.
	rec = do(t, srv, "GET", "/providers", "")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "cf") {
		t.Errorf("providers list: %d %s", rec.Code, rec.Body.String())
	}

	rec = do(t, srv, "DELETE", "/providers/"+toStr(provs[0].ID), "")
	if rec.Code != 200 {
		t.Errorf("delete provider: %d", rec.Code)
	}
	rec = do(t, srv, "DELETE", "/zones/"+toStr(zones[0].ID), "")
	if rec.Code != 200 {
		t.Errorf("delete zone: %d", rec.Code)
	}
}

func TestDNSPageAndManualRecord(t *testing.T) {
	srv, st := newTestServer(t, Options{})
	_, _ = st.CreateZone(context.Background(), "lan.example", "forward", "", 300)

	rec := do(t, srv, "GET", "/dns", "")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "dns") {
		t.Errorf("dns page: %d", rec.Code)
	}

	form := url.Values{
		"zone": {"lan.example"}, "fqdn": {"x.lan.example"},
		"type": {"A"}, "target": {"10.0.0.1"}, "ttl": {"60"},
	}.Encode()
	rec = do(t, srv, "POST", "/dns", form)
	if rec.Code != 200 {
		t.Errorf("create manual dns: %d %s", rec.Code, rec.Body.String())
	}
	views, _ := st.ListDNSRecords(context.Background())
	if len(views) != 1 || views[0].FQDN != "x.lan.example" {
		t.Errorf("not persisted: %+v", views)
	}

	// missing fields
	if rec := do(t, srv, "POST", "/dns", ""); rec.Code != 400 {
		t.Errorf("expected 400, got %d", rec.Code)
	}
	// bad ttl
	form = url.Values{"zone": {"lan.example"}, "fqdn": {"y.lan.example"},
		"type": {"A"}, "target": {"10.0.0.2"}, "ttl": {"bad"}}.Encode()
	if rec := do(t, srv, "POST", "/dns", form); rec.Code != 400 {
		t.Errorf("expected 400 bad ttl, got %d", rec.Code)
	}

	// delete
	rec = do(t, srv, "DELETE", "/dns/"+toStr(views[0].ID), "")
	if rec.Code != 200 {
		t.Errorf("delete dns: %d", rec.Code)
	}
	rec = do(t, srv, "DELETE", "/dns/"+toStr(views[0].ID), "")
	if rec.Code != 404 {
		t.Errorf("re-delete should 404: %d", rec.Code)
	}

	// sync now
	rec = do(t, srv, "POST", "/dns/sync", "")
	if rec.Code != 200 {
		t.Errorf("sync now: %d %s", rec.Code, rec.Body.String())
	}
}

func TestDHCPPage(t *testing.T) {
	srv, _ := newTestServer(t, Options{})
	rec := do(t, srv, "GET", "/dhcp", "")
	if rec.Code != 200 {
		t.Errorf("dhcp page: %d body=%s", rec.Code, rec.Body.String())
	}
	if rec := do(t, srv, "POST", "/dhcp/sync", ""); rec.Code != 200 {
		t.Errorf("dhcp/sync: %d", rec.Code)
	}
	if rec := do(t, srv, "POST", "/dhcp/poll", ""); rec.Code != 200 {
		t.Errorf("dhcp/poll: %d", rec.Code)
	}
}

func TestPrettyAgo(t *testing.T) {
	if got := prettyAgo(time.Time{}); got != "never" {
		t.Errorf("zero time: %q", got)
	}
	if got := prettyAgo(time.Now().Add(-2 * time.Second)); !strings.HasSuffix(got, "ago") {
		t.Errorf("recent: %q", got)
	}
}

func toStr(i int64) string { return strconv.FormatInt(i, 10) }
