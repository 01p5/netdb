package kea

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"netdb/internal/store"
)

// Syncer pushes the assembled Kea config to the ctrl-agent on a timer or
// when Trigger() is called. Same pattern as reconcile.Reconciler.
type Syncer struct {
	store    *store.Store
	client   *Client
	interval time.Duration
	trigger  chan struct{}

	mu     sync.Mutex
	status Status
}

type Status struct {
	LastRunAt    time.Time
	Duration     time.Duration
	Success      bool
	Error        string
	Subnets      int
	Reservations int
	Message      string // shown when the syncer is idle (no URL, etc.)
}

// New returns a Syncer. If url is empty, the syncer is a no-op (useful when
// Kea is not part of the deployment).
func New(st *store.Store, url string, interval time.Duration) *Syncer {
	s := &Syncer{
		store:    st,
		interval: interval,
		trigger:  make(chan struct{}, 1),
	}
	if url != "" {
		s.client = NewClient(url)
	}
	return s
}

func (s *Syncer) Start(ctx context.Context) {
	if s.client == nil {
		s.setStatus(Status{Message: "NETDB_KEA_URL not set — Kea sync disabled"})
		return
	}
	s.runOnce(ctx)

	t := time.NewTicker(s.interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-s.trigger:
		}
		s.runOnce(ctx)
	}
}

func (s *Syncer) Trigger() {
	if s == nil || s.client == nil {
		return
	}
	select {
	case s.trigger <- struct{}{}:
	default:
	}
}

func (s *Syncer) Status() Status {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.status
}

func (s *Syncer) runOnce(ctx context.Context) {
	start := time.Now()
	status := Status{LastRunAt: start}

	cfg, resCount, err := BuildConfig(ctx, s.store)
	if err != nil {
		status.Error = "build config: " + err.Error()
		status.Duration = time.Since(start)
		s.setStatus(status)
		slog.Error("kea build config", "err", err)
		return
	}
	status.Subnets = len(cfg.Subnet4)
	status.Reservations = resCount

	if err := s.client.ConfigSet(ctx, cfg); err != nil {
		status.Error = err.Error()
		status.Duration = time.Since(start)
		s.setStatus(status)
		slog.Warn("kea config-set", "err", err)
		return
	}
	status.Success = true
	status.Duration = time.Since(start)
	s.setStatus(status)
}

func (s *Syncer) setStatus(st Status) {
	s.mu.Lock()
	s.status = st
	s.mu.Unlock()
}
