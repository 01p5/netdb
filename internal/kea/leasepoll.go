package kea

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"netdb/internal/store"
)

// LeasePoller walks Kea's active DHCPv4 leases on a timer and reflects them
// into netdb as ip_assignments with kind='dynamic'. Unknown MACs get new
// host+NIC rows tagged "dhcp-discovered" with dns_enabled=0.
type LeasePoller struct {
	store    *store.Store
	client   *Client
	interval time.Duration
	trigger  chan struct{}

	mu     sync.Mutex
	status LeaseStatus
}

type LeaseStatus struct {
	LastRunAt time.Time
	Duration  time.Duration
	Success   bool
	Error     string
	LeaseRows int // active leases seen from Kea
	Removed   int // stale dynamic ip_assignments pruned this cycle
	Message   string
}

func NewLeasePoller(st *store.Store, url string, interval time.Duration) *LeasePoller {
	p := &LeasePoller{store: st, interval: interval, trigger: make(chan struct{}, 1)}
	if url != "" {
		p.client = NewClient(url)
	}
	return p
}

func (p *LeasePoller) Start(ctx context.Context) {
	if p.client == nil {
		p.setStatus(LeaseStatus{Message: "NETDB_KEA_URL not set — lease ingestion disabled"})
		return
	}
	p.runOnce(ctx)

	t := time.NewTicker(p.interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-p.trigger:
		}
		p.runOnce(ctx)
	}
}

func (p *LeasePoller) Trigger() {
	if p == nil || p.client == nil {
		return
	}
	select {
	case p.trigger <- struct{}{}:
	default:
	}
}

func (p *LeasePoller) Status() LeaseStatus {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.status
}

func (p *LeasePoller) runOnce(ctx context.Context) {
	start := time.Now()
	status := LeaseStatus{LastRunAt: start}

	leases, err := p.client.ListLeases4(ctx)
	if err != nil {
		status.Error = err.Error()
		status.Duration = time.Since(start)
		p.setStatus(status)
		slog.Warn("kea lease poll", "err", err)
		return
	}

	seen := make(map[string]bool, len(leases))
	for _, l := range leases {
		if l.State != LeaseStateActive {
			continue
		}
		if l.HWAddress == "" || l.IPAddress == "" {
			continue
		}
		seen[l.HWAddress+"|"+l.IPAddress] = true
		if err := p.store.UpsertDynamicLease(ctx, store.LeaseObserved{
			MAC: l.HWAddress, IP: l.IPAddress, Hostname: l.Hostname,
		}); err != nil {
			slog.Warn("upsert dynamic lease",
				"mac", l.HWAddress, "ip", l.IPAddress, "err", err)
		} else {
			status.LeaseRows++
		}
	}
	removed, err := p.store.DeleteStaleDynamic(ctx, seen)
	if err != nil {
		status.Error = "prune stale: " + err.Error()
		status.Duration = time.Since(start)
		p.setStatus(status)
		return
	}
	status.Removed = removed
	status.Success = true
	status.Duration = time.Since(start)
	p.setStatus(status)
}

func (p *LeasePoller) setStatus(st LeaseStatus) {
	p.mu.Lock()
	p.status = st
	p.mu.Unlock()
}
