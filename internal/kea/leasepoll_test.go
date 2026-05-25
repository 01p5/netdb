package kea

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestLeasePollerNoURLIsNoOp(t *testing.T) {
	st := openStore(t)
	p := NewLeasePoller(st, "", time.Minute)
	done := make(chan struct{})
	go func() {
		p.Start(context.Background())
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("LeasePoller.Start did not return on empty URL")
	}
	p.Trigger() // should not panic
}

func TestLeasePollerIngestsAndPrunes(t *testing.T) {
	st := openStore(t)
	ctx := context.Background()

	// Seed a stale dynamic lease whose MAC won't be present in Kea's response
	// so the poller has something to prune. We insert via SQL to avoid the
	// import cycle that pulling in store.LeaseObserved would introduce.
	if _, err := st.DB().ExecContext(ctx, `
		INSERT INTO hosts(name, dns_enabled, tags) VALUES('stale-host', 0, 'dhcp-discovered');
		INSERT INTO nics(host_id, mac, label) VALUES(last_insert_rowid(), 'aa:bb:cc:dd:ee:99', 'dhcp');
		INSERT INTO ip_assignments(nic_id, ip, kind) VALUES(last_insert_rowid(), '10.0.0.99', 'dynamic');
	`); err != nil {
		t.Fatalf("seed stale: %v", err)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var cmd Command
		_ = json.Unmarshal(body, &cmd)
		if cmd.Command != "lease4-get-all" {
			t.Errorf("unexpected cmd: %q", cmd.Command)
		}
		args := json.RawMessage(`{"leases":[
			{"ip-address":"10.0.0.10","hw-address":"11:22:33:44:55:66","hostname":"fresh","state":0,"subnet-id":1},
			{"ip-address":"10.0.0.20","hw-address":"11:22:33:44:55:77","hostname":"","state":1,"subnet-id":1},
			{"ip-address":"","hw-address":"","state":0}
		]}`)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode([]Response{{Result: 0, Arguments: args}})
	}))
	defer srv.Close()

	p := NewLeasePoller(st, srv.URL, time.Hour)
	p.runOnce(ctx)

	stx := p.Status()
	if !stx.Success || stx.Error != "" {
		t.Fatalf("status: %+v", stx)
	}
	if stx.LeaseRows != 1 {
		t.Errorf("expected 1 active lease ingested (state=0 with addr+mac), got %d", stx.LeaseRows)
	}
	if stx.Removed != 1 {
		t.Errorf("expected 1 stale dynamic pruned, got %d", stx.Removed)
	}
}

func TestLeasePollerKeaError(t *testing.T) {
	st := openStore(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode([]Response{{Result: 1, Text: "boom"}})
	}))
	defer srv.Close()
	p := NewLeasePoller(st, srv.URL, time.Hour)
	p.runOnce(context.Background())
	if p.Status().Error == "" {
		t.Errorf("expected error in status")
	}
}
