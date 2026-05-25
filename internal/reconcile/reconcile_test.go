package reconcile

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"netdb/internal/store"
)

// fakeCF stands in for the Cloudflare API for end-to-end reconcile tests.
// Single zone "lan.example" with deterministic IDs so the test can check
// the exact sequence of add/delete requests.
type fakeCF struct {
	t       *testing.T
	mu      sync.Mutex
	records map[string]cfRec // id -> record
	nextID  int
}

type cfRec struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Type     string `json:"type"`
	Content  string `json:"content"`
	TTL      int    `json:"ttl"`
	Priority int    `json:"priority,omitempty"`
}

func newFakeCF(t *testing.T) *httptest.Server {
	f := &fakeCF{t: t, records: map[string]cfRec{}}
	mux := http.NewServeMux()
	mux.HandleFunc("/zones", func(w http.ResponseWriter, r *http.Request) {
		name := r.URL.Query().Get("name")
		writeEnv(w, []map[string]string{{"id": "z-1", "name": name}}, nil)
	})
	mux.HandleFunc("/zones/z-1/dns_records", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		switch r.Method {
		case http.MethodGet:
			recs := make([]cfRec, 0, len(f.records))
			for _, r := range f.records {
				recs = append(recs, r)
			}
			writeEnv(w, recs, map[string]any{"total_pages": 1})
		case http.MethodPost:
			var body cfRec
			_ = json.NewDecoder(r.Body).Decode(&body)
			f.nextID++
			body.ID = "rec-" + strconv.Itoa(f.nextID)
			f.records[body.ID] = body
			writeEnv(w, body, nil)
		}
	})
	mux.HandleFunc("/zones/z-1/dns_records/", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		id := strings.TrimPrefix(r.URL.Path, "/zones/z-1/dns_records/")
		if r.Method == http.MethodDelete {
			delete(f.records, id)
			writeEnv(w, map[string]string{"id": id}, nil)
		}
	})
	return httptest.NewServer(mux)
}

func writeEnv(w http.ResponseWriter, result any, info any) {
	w.Header().Set("Content-Type", "application/json")
	env := map[string]any{"success": true, "errors": []any{}, "result": result}
	if info != nil {
		env["result_info"] = info
	}
	_ = json.NewEncoder(w).Encode(env)
}

func openStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "rec.sqlite"))
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

func TestReconcileNoProviders(t *testing.T) {
	st := openStore(t)
	r := New(st, Config{}, time.Hour)
	r.runOnce(context.Background())
	got := r.Status()
	if got.Message == "" {
		t.Errorf("expected message about no providers, got %+v", got)
	}
}

func TestReconcilePushesAddsThenDeletes(t *testing.T) {
	st := openStore(t)
	ctx := context.Background()

	z, _ := st.CreateZone(ctx, "lan.example", "forward", "", 300)
	p, _ := st.CreateProvider(ctx, "cf", "cloudflare", "", true)
	if err := st.LinkZoneProvider(ctx, z.ID, p.ID); err != nil {
		t.Fatalf("LinkZoneProvider: %v", err)
	}

	if _, err := st.CreateManualDNSRecord(ctx, "lan.example", "host.lan.example", "A", "10.0.0.5", 300); err != nil {
		t.Fatalf("manual A: %v", err)
	}

	cf := newFakeCF(t)
	defer cf.Close()

	// Pre-seed a stray record at the provider that isn't in netdb's desired set —
	// reconciler should drop it.
	_, _ = http.Post(cf.URL+"/zones/z-1/dns_records", "application/json",
		strings.NewReader(`{"name":"stray.lan.example","type":"A","content":"9.9.9.9","ttl":300}`))

	r := New(st, Config{CloudflareToken: "tok", CloudflareBaseURL: cf.URL}, time.Hour)
	r.runOnce(ctx)

	got := r.Status()
	if len(got.Providers) != 1 {
		t.Fatalf("expected 1 provider status, got %+v", got)
	}
	ps := got.Providers[0]
	if !ps.OK || ps.Error != "" {
		t.Fatalf("provider not OK: %+v", ps)
	}
	if ps.Added != 1 || ps.Deleted != 1 {
		t.Errorf("expected 1 add + 1 delete, got added=%d deleted=%d", ps.Added, ps.Deleted)
	}
	if len(ps.Zones) != 1 || ps.Zones[0].Desired != 1 {
		t.Errorf("zone status: %+v", ps.Zones)
	}

	// A second run should be a no-op (idempotent).
	r.runOnce(ctx)
	ps2 := r.Status().Providers[0]
	if ps2.Added != 0 || ps2.Deleted != 0 {
		t.Errorf("expected idempotent on 2nd run, got added=%d deleted=%d", ps2.Added, ps2.Deleted)
	}
}

func TestReconcileDisabledProviderIsSkipped(t *testing.T) {
	st := openStore(t)
	ctx := context.Background()

	z, _ := st.CreateZone(ctx, "lan.example", "forward", "", 300)
	// Enabled=false — should never get reached.
	p, _ := st.CreateProvider(ctx, "cf-off", "cloudflare", "", false)
	if err := st.LinkZoneProvider(ctx, z.ID, p.ID); err != nil {
		t.Fatalf("LinkZoneProvider: %v", err)
	}

	r := New(st, Config{CloudflareToken: "tok", CloudflareBaseURL: "http://127.0.0.1:1"}, time.Hour)
	r.runOnce(ctx)
	got := r.Status()
	if len(got.Providers) != 0 {
		t.Errorf("expected disabled provider skipped, got %+v", got.Providers)
	}
}

func TestReconcileMissingTokenIsClientError(t *testing.T) {
	st := openStore(t)
	ctx := context.Background()

	// Provider exists but we set no Cloudflare token in cfg.
	if _, err := st.CreateProvider(ctx, "cf", "cloudflare", "", true); err != nil {
		t.Fatalf("CreateProvider: %v", err)
	}

	r := New(st, Config{}, time.Hour)
	r.runOnce(ctx)
	ps := r.Status().Providers
	if len(ps) != 1 || ps[0].Error == "" {
		t.Errorf("expected provider error about missing token, got %+v", ps)
	}
}

func TestReconcileTechnitiumMissingURL(t *testing.T) {
	st := openStore(t)
	ctx := context.Background()
	if _, err := st.CreateProvider(ctx, "tech", "technitium", "", true); err != nil {
		t.Fatalf("CreateProvider: %v", err)
	}
	r := New(st, Config{}, time.Hour)
	r.runOnce(ctx)
	ps := r.Status().Providers
	if len(ps) != 1 || ps[0].Error == "" {
		t.Errorf("expected technitium URL error, got %+v", ps)
	}
}

func TestReconcileStartReturnsOnCtxCancel(t *testing.T) {
	st := openStore(t)
	r := New(st, Config{}, 50*time.Millisecond)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { r.Start(ctx); close(done) }()
	// Let one tick happen, then cancel.
	time.Sleep(75 * time.Millisecond)
	r.Trigger() // also exercise the trigger select branch
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Start did not return on ctx cancel")
	}
}

func TestReconcileZoneAddDeleteErrors(t *testing.T) {
	st := openStore(t)
	ctx := context.Background()

	z, _ := st.CreateZone(ctx, "lan.example", "forward", "", 300)
	p, _ := st.CreateProvider(ctx, "cf", "cloudflare", "", true)
	if err := st.LinkZoneProvider(ctx, z.ID, p.ID); err != nil {
		t.Fatalf("LinkZoneProvider: %v", err)
	}
	if _, err := st.CreateManualDNSRecord(ctx, "lan.example", "host.lan.example", "A", "10.0.0.5", 300); err != nil {
		t.Fatalf("manual A: %v", err)
	}

	// Server that fails every add/delete after the initial zone lookup + empty list.
	cf := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/zones":
			_, _ = w.Write([]byte(`{"success":true,"errors":[],"result":[{"id":"z-1","name":"lan.example"}]}`))
		case r.URL.Path == "/zones/z-1/dns_records" && r.Method == http.MethodGet:
			// Return one stray that the reconciler will want to DELETE.
			_, _ = w.Write([]byte(`{"success":true,"errors":[],"result":[{"id":"r-1","name":"stray.lan.example","type":"A","content":"9.9.9.9","ttl":300}],"result_info":{"total_pages":1}}`))
		case r.URL.Path == "/zones/z-1/dns_records" && r.Method == http.MethodPost:
			// Adds fail with success=false.
			_, _ = w.Write([]byte(`{"success":false,"errors":[{"code":7003,"message":"nope"}]}`))
		case strings.HasPrefix(r.URL.Path, "/zones/z-1/dns_records/") && r.Method == http.MethodDelete:
			_, _ = w.Write([]byte(`{"success":false,"errors":[{"code":7003,"message":"nope"}]}`))
		}
	}))
	defer cf.Close()

	rec := New(st, Config{CloudflareToken: "tok", CloudflareBaseURL: cf.URL}, time.Hour)
	rec.runOnce(ctx)
	st2 := rec.Status()
	if len(st2.Providers) != 1 {
		t.Fatalf("expected provider entry, got %+v", st2)
	}
	ps := st2.Providers[0]
	if ps.OK {
		t.Errorf("provider OK should be false, got %+v", ps)
	}
	if len(ps.Zones) == 0 || ps.Zones[0].Error == "" {
		t.Errorf("expected zone-level error, got %+v", ps.Zones)
	}
}

func TestReconcileZoneEnsureFailure(t *testing.T) {
	st := openStore(t)
	ctx := context.Background()
	z, _ := st.CreateZone(ctx, "lan.example", "forward", "", 300)
	p, _ := st.CreateProvider(ctx, "cf", "cloudflare", "", true)
	if err := st.LinkZoneProvider(ctx, z.ID, p.ID); err != nil {
		t.Fatalf("LinkZoneProvider: %v", err)
	}
	// CF returns an empty zones array — EnsureZone fails.
	cf := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"success":true,"errors":[],"result":[]}`))
	}))
	defer cf.Close()
	rec := New(st, Config{CloudflareToken: "tok", CloudflareBaseURL: cf.URL}, time.Hour)
	rec.runOnce(ctx)
	ps := rec.Status().Providers
	if len(ps) != 1 || len(ps[0].Zones) != 1 || ps[0].Zones[0].Error == "" {
		t.Errorf("expected EnsureZone error path, got %+v", ps)
	}
}

func TestReconcileNoZonesLinked(t *testing.T) {
	st := openStore(t)
	ctx := context.Background()
	if _, err := st.CreateProvider(ctx, "cf", "cloudflare", "", true); err != nil {
		t.Fatalf("CreateProvider: %v", err)
	}
	r := New(st, Config{CloudflareToken: "tok", CloudflareBaseURL: "http://unused"}, time.Hour)
	r.runOnce(ctx)
	ps := r.Status().Providers
	if len(ps) != 1 || !ps[0].OK || ps[0].Error != "no zones linked" {
		t.Errorf("expected OK with no-zones message, got %+v", ps)
	}
}

func TestReconcileTriggerCoalesces(t *testing.T) {
	st := openStore(t)
	r := New(st, Config{}, time.Hour)
	// Multiple triggers should not block (channel size 1, others drop).
	for i := 0; i < 10; i++ {
		r.Trigger()
	}
}

