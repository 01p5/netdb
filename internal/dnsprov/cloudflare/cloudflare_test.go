package cloudflare

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"netdb/internal/dnsprov"
)

// fakeCF stubs just enough of the Cloudflare API surface for the adapter
// to exercise zone lookup, pagination, add, and delete.
type fakeCF struct {
	t        *testing.T
	records  map[string]cfRecord // id -> record
	nextID   int
	requests []string
}

func newFakeCF(t *testing.T) *httptest.Server {
	f := &fakeCF{t: t, records: map[string]cfRecord{}}
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer test-token" {
			t.Errorf("missing/bad Authorization header: %q", got)
		}
		f.requests = append(f.requests, r.Method+" "+r.URL.RequestURI())

		switch {
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/zones") && r.URL.Query().Get("name") != "":
			writeEnvelope(w, []map[string]string{{"id": "zone-abc", "name": r.URL.Query().Get("name")}}, nil)
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/zones/zone-abc/dns_records"):
			recs := make([]cfRecord, 0, len(f.records))
			for _, r := range f.records {
				recs = append(recs, r)
			}
			writeEnvelope(w, recs, map[string]any{"total_pages": 1})
		case r.Method == http.MethodPost && r.URL.Path == "/zones/zone-abc/dns_records":
			var body cfRecord
			_ = json.NewDecoder(r.Body).Decode(&body)
			f.nextID++
			body.ID = "rec-" + strconv.Itoa(f.nextID)
			f.records[body.ID] = body
			writeEnvelope(w, body, nil)
		case r.Method == http.MethodDelete && strings.HasPrefix(r.URL.Path, "/zones/zone-abc/dns_records/"):
			id := strings.TrimPrefix(r.URL.Path, "/zones/zone-abc/dns_records/")
			delete(f.records, id)
			writeEnvelope(w, map[string]string{"id": id}, nil)
		default:
			b, _ := io.ReadAll(r.Body)
			t.Fatalf("unhandled request %s %s body=%s", r.Method, r.URL.RequestURI(), string(b))
		}
	}))
}

func writeEnvelope(w http.ResponseWriter, result any, resultInfo any) {
	w.Header().Set("Content-Type", "application/json")
	env := map[string]any{"success": true, "errors": []any{}, "result": result}
	if resultInfo != nil {
		env["result_info"] = resultInfo
	}
	_ = json.NewEncoder(w).Encode(env)
}

func TestCloudflareAddListDelete(t *testing.T) {
	srv := newFakeCF(t)
	defer srv.Close()

	c := New("cf-test", Config{BaseURL: srv.URL, Token: "test-token"})
	ctx := context.Background()

	if err := c.EnsureZone(ctx, "example.com"); err != nil {
		t.Fatalf("EnsureZone: %v", err)
	}

	add := dnsprov.Record{Name: "a.example.com", Type: "A", Value: "1.2.3.4", TTL: 300}
	if err := c.AddRecord(ctx, "example.com", add); err != nil {
		t.Fatalf("AddRecord: %v", err)
	}

	got, err := c.ListRecords(ctx, "example.com")
	if err != nil {
		t.Fatalf("ListRecords: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 record, got %d: %+v", len(got), got)
	}
	if got[0].Name != "a.example.com" || got[0].Type != "A" || got[0].Value != "1.2.3.4" {
		t.Errorf("record round-trip mismatch: got %+v", got[0])
	}
	if got[0].ProviderID == "" {
		t.Error("ProviderID should be populated from list response")
	}

	// Delete via the record we got back (this is the contract: reconciler
	// passes back a record it received from ListRecords).
	if err := c.DeleteRecord(ctx, "example.com", got[0]); err != nil {
		t.Fatalf("DeleteRecord: %v", err)
	}

	after, err := c.ListRecords(ctx, "example.com")
	if err != nil {
		t.Fatalf("ListRecords after delete: %v", err)
	}
	if len(after) != 0 {
		t.Errorf("expected 0 records after delete, got %d", len(after))
	}
}

func TestCloudflareDeleteWithoutID(t *testing.T) {
	c := New("cf-test", Config{BaseURL: "http://unused", Token: "t"})
	err := c.DeleteRecord(context.Background(), "example.com",
		dnsprov.Record{Name: "a.example.com", Type: "A", Value: "1.2.3.4"})
	if err == nil {
		t.Fatal("expected error when deleting without ProviderID")
	}
}

func TestCloudflareNameKind(t *testing.T) {
	c := New("cf-main", Config{Token: "t"})
	if c.Name() != "cf-main" {
		t.Errorf("Name: %q", c.Name())
	}
	if c.Kind() != "cloudflare" {
		t.Errorf("Kind: %q", c.Kind())
	}
}

func TestCloudflareDefaultBaseURL(t *testing.T) {
	c := New("cf", Config{Token: "t"})
	if c.cfg.BaseURL != defaultBaseURL {
		t.Errorf("default BaseURL not applied: %q", c.cfg.BaseURL)
	}
}

func TestCloudflareFirstError(t *testing.T) {
	if got := firstError(nil); got != "unknown error" {
		t.Errorf("nil errs: %q", got)
	}
	if got := firstError([]apiError{{Code: 7003, Message: "boom"}}); got != "code 7003: boom" {
		t.Errorf("formatted: %q", got)
	}
}

func TestCloudflareErrorEnvelope(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeEnvelope(w, nil, nil)
		// Overwrite the envelope to be an error one.
	}))
	// Replace with a custom server that returns success=false.
	srv.Close()
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"success":false,"errors":[{"code":1,"message":"nope"}]}`))
	}))
	defer srv.Close()
	c := New("cf", Config{BaseURL: srv.URL, Token: "t"})
	if err := c.EnsureZone(context.Background(), "x.example"); err == nil {
		t.Error("expected error on success=false")
	}
}

func TestCloudflareZoneNotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeEnvelope(w, []any{}, nil)
	}))
	defer srv.Close()
	c := New("cf", Config{BaseURL: srv.URL, Token: "t"})
	if err := c.EnsureZone(context.Background(), "missing.example"); err == nil {
		t.Error("expected error when zone missing")
	}
}

func TestCloudflareAddMXBadFormat(t *testing.T) {
	srv := newFakeCF(t)
	defer srv.Close()
	c := New("cf", Config{BaseURL: srv.URL, Token: "test-token"})
	err := c.AddRecord(context.Background(), "example.com",
		dnsprov.Record{Name: "example.com", Type: "MX", Value: "no-pref"})
	if err == nil {
		t.Error("expected error for MX without prefix integer")
	}
	err = c.AddRecord(context.Background(), "example.com",
		dnsprov.Record{Name: "example.com", Type: "MX", Value: "abc mail.example.com"})
	if err == nil {
		t.Error("expected error for non-integer preference")
	}
}

func TestCloudflareMXRoundTrip(t *testing.T) {
	srv := newFakeCF(t)
	defer srv.Close()

	c := New("cf-test", Config{BaseURL: srv.URL, Token: "test-token"})
	ctx := context.Background()

	if err := c.AddRecord(ctx, "example.com",
		dnsprov.Record{Name: "example.com", Type: "MX", Value: "10 mail.example.com", TTL: 3600}); err != nil {
		t.Fatalf("AddRecord MX: %v", err)
	}
	got, err := c.ListRecords(ctx, "example.com")
	if err != nil {
		t.Fatalf("ListRecords: %v", err)
	}
	if len(got) != 1 || got[0].Type != "MX" {
		t.Fatalf("expected one MX, got %+v", got)
	}
	// Round-trip: "10 mail.example.com" back to same form
	if got[0].Value != "10 mail.example.com" {
		t.Errorf("MX value round-trip mismatch: got %q", got[0].Value)
	}
}
