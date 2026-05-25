package kea

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestSyncerNoURLIsNoOp(t *testing.T) {
	st := openStore(t)
	s := New(st, "", time.Minute)
	// Start should return immediately when client is nil.
	done := make(chan struct{})
	go func() {
		s.Start(context.Background())
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Start did not return for empty URL")
	}
	if s.Status().Message == "" {
		t.Error("expected status message about NETDB_KEA_URL")
	}
	// Trigger on no-op should be safe.
	s.Trigger()
}

func TestSyncerRunOncePushesConfig(t *testing.T) {
	st := openStore(t)

	var pushed int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var cmd Command
		_ = json.Unmarshal(body, &cmd)
		if cmd.Command == "config-set" {
			atomic.AddInt32(&pushed, 1)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode([]Response{{Result: 0}})
	}))
	defer srv.Close()

	s := New(st, srv.URL, time.Hour)
	s.runOnce(context.Background())

	if atomic.LoadInt32(&pushed) != 1 {
		t.Errorf("expected config-set called once, got %d", pushed)
	}
	st2 := s.Status()
	if !st2.Success || st2.Error != "" {
		t.Errorf("status not OK: %+v", st2)
	}
}

func TestSyncerReportsBuildError(t *testing.T) {
	st := openStore(t)
	// Closing the store forces BuildConfig's ListSubnets to fail with "database is closed".
	_ = st.Close()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode([]Response{{Result: 0}})
	}))
	defer srv.Close()
	s := New(st, srv.URL, time.Hour)
	s.runOnce(context.Background())
	got := s.Status()
	if got.Error == "" || got.Success {
		t.Errorf("expected build error, got %+v", got)
	}
}

func TestSyncerStartReturnsOnCtxCancel(t *testing.T) {
	st := openStore(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode([]Response{{Result: 0}})
	}))
	defer srv.Close()
	s := New(st, srv.URL, 50*time.Millisecond)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { s.Start(ctx); close(done) }()
	time.Sleep(75 * time.Millisecond)
	s.Trigger()
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Syncer.Start did not return on ctx cancel")
	}
}

func TestSyncerReportsKeaError(t *testing.T) {
	st := openStore(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode([]Response{{Result: 1, Text: "rejected"}})
	}))
	defer srv.Close()
	s := New(st, srv.URL, time.Hour)
	s.runOnce(context.Background())
	got := s.Status()
	if got.Error == "" || got.Success {
		t.Errorf("expected push error, got %+v", got)
	}
}
