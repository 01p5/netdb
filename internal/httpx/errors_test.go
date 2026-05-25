package httpx

import (
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// Many handlers have a slog.Error("...", err) + http.Error(500) branch when
// the store fails. Closing the store mid-test forces every read/write to
// hit those paths so they get covered without needing a mock store.

func TestHandlersReportStoreErrors(t *testing.T) {
	srv, st := newTestServer(t, Options{})
	_ = st.Close()

	type rt struct {
		method, target string
		body           string
		wantCode       int
	}
	cases := []rt{
		// Read endpoints — should 500 because store query fails.
		{"GET", "/hosts", "", 500},
		{"GET", "/subnets", "", 500},
		{"GET", "/zones", "", 500},
		{"GET", "/providers", "", 500},
		{"GET", "/dns", "", 500},
		{"GET", "/healthz", "", 500},
		// Write endpoints with valid form input — store fails so 400/500.
		{"POST", "/hosts", url.Values{"name": {"x"}}.Encode(), 400},
		{"POST", "/subnets", url.Values{"cidr": {"10.0.0.0/24"}}.Encode(), 400},
		{"POST", "/zones", url.Values{"name": {"z"}, "kind": {"forward"}}.Encode(), 400},
		{"POST", "/providers", url.Values{"name": {"p"}, "kind": {"cloudflare"}}.Encode(), 400},
		{"POST", "/dns", url.Values{
			"zone": {"z"}, "fqdn": {"x"}, "type": {"A"}, "target": {"1.1.1.1"},
		}.Encode(), 400},
		// Detail / mutation by ID — these hit the store too.
		{"GET", "/hosts/1", "", 500},
		{"POST", "/hosts/1", url.Values{"name": {"x"}}.Encode(), 400},
		{"DELETE", "/hosts/1", "", 500},
		{"DELETE", "/nics/1", "", 500},
		{"DELETE", "/ips/1", "", 500},
		{"DELETE", "/dns/1", "", 500},
		{"DELETE", "/zones/1", "", 500},
		{"DELETE", "/providers/1", "", 500},
		{"DELETE", "/subnets/1", "", 500},
		{"DELETE", "/subnets/1/zones/1/forward", "", 500},
		{"DELETE", "/zones/1/providers/1", "", 500},
		{"POST", "/hosts/1/nics", url.Values{"mac": {"aa:bb:cc:dd:ee:ff"}}.Encode(), 400},
		{"POST", "/nics/1/ips", url.Values{"ip": {"10.0.0.1"}, "kind": {"static"}}.Encode(), 400},
		{"POST", "/subnets/1", url.Values{"cidr": {"10.0.0.0/24"}}.Encode(), 400},
		{"POST", "/subnets/1/zones", url.Values{"zone_id": {"1"}, "role": {"forward"}}.Encode(), 400},
		{"POST", "/zones/1/providers", url.Values{"provider_id": {"1"}}.Encode(), 500},
	}
	for _, c := range cases {
		rec := do(t, srv, c.method, c.target, c.body)
		if rec.Code != c.wantCode {
			t.Errorf("%s %s: got %d want %d body=%s", c.method, c.target, rec.Code, c.wantCode, rec.Body.String())
		}
	}
}

func TestBadIDsArePOSTBadRequest(t *testing.T) {
	srv, _ := newTestServer(t, Options{})
	// Each mutation endpoint with a non-numeric id should hit ParseInt error.
	cases := []struct{ method, target string }{
		{"POST", "/hosts/garbage"},
		{"DELETE", "/hosts/garbage"},
		{"DELETE", "/nics/garbage"},
		{"POST", "/nics/garbage/ips"},
		{"DELETE", "/ips/garbage"},
		{"POST", "/subnets/garbage"},
		{"DELETE", "/subnets/garbage"},
		{"POST", "/subnets/garbage/zones"},
		{"POST", "/subnets/1/zones"}, // missing zone_id form val
		{"DELETE", "/subnets/1/zones/garbage/forward"},
		{"DELETE", "/zones/garbage"},
		{"POST", "/zones/garbage/providers"},
		{"POST", "/zones/1/providers"}, // missing provider_id
		{"DELETE", "/zones/garbage/providers/1"},
		{"DELETE", "/zones/1/providers/garbage"},
		{"DELETE", "/providers/garbage"},
		{"DELETE", "/dns/garbage"},
		{"POST", "/hosts/garbage/nics"},
	}
	for _, c := range cases {
		rec := do(t, srv, c.method, c.target, "name=x")
		if rec.Code != 400 {
			t.Errorf("%s %s: got %d want 400 body=%s", c.method, c.target, rec.Code, rec.Body.String())
		}
	}
}

func TestUpdateHostMissingName(t *testing.T) {
	srv, _ := newTestServer(t, Options{})
	rec := do(t, srv, "POST", "/hosts/1", "")
	if rec.Code != 400 {
		t.Errorf("expected 400, got %d", rec.Code)
	}
}

// Hit the host-update 404 path explicitly (id parses but row missing).
func TestUpdateHostNotFound(t *testing.T) {
	srv, _ := newTestServer(t, Options{})
	body := url.Values{"name": {"x"}}.Encode()
	req := httptest.NewRequest("POST", "/hosts/99999", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != 404 {
		t.Errorf("expected 404, got %d", rec.Code)
	}
}

// Hit the subnet-update 404 path (well-formed CIDR + missing id).
func TestUpdateSubnetNotFound(t *testing.T) {
	srv, _ := newTestServer(t, Options{})
	body := url.Values{"cidr": {"10.99.0.0/24"}}.Encode()
	req := httptest.NewRequest("POST", "/subnets/99999", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != 404 {
		t.Errorf("expected 404, got %d", rec.Code)
	}
}

// The renderPage helper has an "unknown page" branch — exercise it via reflection.
func TestRenderPageUnknown(t *testing.T) {
	srv, _ := newTestServer(t, Options{})
	rec := httptest.NewRecorder()
	srv.renderPage(rec, "no-such-page", nil)
	if rec.Code != 500 {
		t.Errorf("expected 500 for unknown page, got %d", rec.Code)
	}
}
