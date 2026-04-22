// Package reconcile pushes netdb's dns_records to the configured providers.
//
// Flow per run:
//   for each enabled provider:
//     build a client from its kind + global env config
//     for each zone linked to the provider:
//       desired = flatten dns_records for that zone
//       actual  = provider.ListRecords(zone), filtered to types we manage
//       add   = desired - actual
//       drop  = actual  - desired   (netdb is the source of truth)
//       apply each delta and collect counts
//
// A ticker drives it on a schedule; Trigger() lets handlers request an
// earlier run without blocking. Run results live in memory (Status()) —
// persistence is deliberately deferred until we need it.
package reconcile

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"netdb/internal/dnsprov"
	"netdb/internal/dnsprov/cloudflare"
	"netdb/internal/dnsprov/technitium"
	"netdb/internal/model"
	"netdb/internal/store"
)

// Config carries the subset of app config the reconciler needs. It's
// populated by main.go from env vars so the reconciler has no direct
// dependency on internal/config.
type Config struct {
	TechnitiumURL      string
	TechnitiumToken    string
	TechnitiumUser     string
	TechnitiumPassword string
	TechnitiumInsecure bool

	CloudflareToken   string
	CloudflareBaseURL string // optional override; empty = production
}

type Reconciler struct {
	store    *store.Store
	cfg      Config
	interval time.Duration
	trigger  chan struct{}

	mu     sync.Mutex
	status Status
}

type Status struct {
	LastRunAt time.Time     // zero value means "never run"
	Duration  time.Duration
	Providers []ProviderStatus
	Message   string // overall message (e.g. "no providers configured")
}

type ProviderStatus struct {
	Name    string
	Kind    string
	OK      bool
	Error   string
	Zones   []ZoneStatus
	Added   int
	Deleted int
}

type ZoneStatus struct {
	Name    string
	OK      bool
	Error   string
	Desired int
	Actual  int
	Added   int
	Deleted int
}

func New(st *store.Store, cfg Config, interval time.Duration) *Reconciler {
	return &Reconciler{
		store:    st,
		cfg:      cfg,
		interval: interval,
		trigger:  make(chan struct{}, 1),
	}
}

// Start blocks until ctx is done, running reconcile cycles on a ticker or
// whenever Trigger() is called.
func (r *Reconciler) Start(ctx context.Context) {
	// Kick off an initial run so the UI shows state without waiting for a tick.
	r.runOnce(ctx)

	t := time.NewTicker(r.interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-r.trigger:
		}
		r.runOnce(ctx)
	}
}

// Trigger asks for an out-of-schedule reconcile. Non-blocking; multiple
// calls coalesce into one pending run.
func (r *Reconciler) Trigger() {
	select {
	case r.trigger <- struct{}{}:
	default:
	}
}

func (r *Reconciler) Status() Status {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.status
}

func (r *Reconciler) runOnce(ctx context.Context) {
	start := time.Now()
	providers, err := r.store.ListProviders(ctx)
	if err != nil {
		r.setStatus(Status{LastRunAt: start, Duration: time.Since(start),
			Message: "list providers: " + err.Error()})
		slog.Error("reconcile list providers", "err", err)
		return
	}

	status := Status{LastRunAt: start}

	if len(providers) == 0 {
		status.Message = "no providers configured — add one at /providers"
		status.Duration = time.Since(start)
		r.setStatus(status)
		return
	}

	for _, p := range providers {
		if !p.Enabled {
			continue
		}
		ps := r.reconcileProvider(ctx, p)
		status.Providers = append(status.Providers, ps)
	}
	status.Duration = time.Since(start)
	r.setStatus(status)
}

func (r *Reconciler) reconcileProvider(ctx context.Context, p model.Provider) ProviderStatus {
	ps := ProviderStatus{Name: p.Name, Kind: p.Kind}

	client, err := r.newClient(p)
	if err != nil {
		ps.Error = err.Error()
		return ps
	}

	zones, err := r.store.ListZonesForProvider(ctx, p.ID)
	if err != nil {
		ps.Error = "list zones: " + err.Error()
		return ps
	}
	if len(zones) == 0 {
		ps.OK = true
		ps.Error = "no zones linked"
		return ps
	}

	for _, z := range zones {
		zs := r.reconcileZone(ctx, client, z)
		ps.Zones = append(ps.Zones, zs)
		ps.Added += zs.Added
		ps.Deleted += zs.Deleted
	}
	ps.OK = true
	for _, zs := range ps.Zones {
		if !zs.OK {
			ps.OK = false
			break
		}
	}
	return ps
}

func (r *Reconciler) reconcileZone(ctx context.Context, client dnsprov.Provider, z model.Zone) ZoneStatus {
	zs := ZoneStatus{Name: z.Name}

	desiredRows, err := r.store.ListDesiredRecordsByZone(ctx, z.ID)
	if err != nil {
		zs.Error = "load desired: " + err.Error()
		return zs
	}
	desired := make(map[string]dnsprov.Record, len(desiredRows))
	for _, d := range desiredRows {
		rec := dnsprov.Record{Name: d.Name, Type: d.Type, Value: d.Value, TTL: d.TTL}
		desired[rec.Key()] = rec
	}
	zs.Desired = len(desired)

	if err := client.EnsureZone(ctx, z.Name); err != nil {
		zs.Error = "ensure zone: " + err.Error()
		return zs
	}

	actualList, err := client.ListRecords(ctx, z.Name)
	if err != nil {
		zs.Error = "list remote: " + err.Error()
		return zs
	}
	actual := make(map[string]dnsprov.Record, len(actualList))
	for _, a := range actualList {
		actual[a.Key()] = a
	}
	zs.Actual = len(actual)

	var errs []string
	for k, rec := range desired {
		if _, ok := actual[k]; ok {
			continue
		}
		if err := client.AddRecord(ctx, z.Name, rec); err != nil {
			errs = append(errs, fmt.Sprintf("add %s: %v", k, err))
			continue
		}
		zs.Added++
	}
	for k, rec := range actual {
		if _, ok := desired[k]; ok {
			continue
		}
		if err := client.DeleteRecord(ctx, z.Name, rec); err != nil {
			errs = append(errs, fmt.Sprintf("del %s: %v", k, err))
			continue
		}
		zs.Deleted++
	}

	if len(errs) > 0 {
		zs.Error = fmt.Sprintf("%d error(s); first: %s", len(errs), errs[0])
		return zs
	}
	zs.OK = true
	return zs
}

func (r *Reconciler) newClient(p model.Provider) (dnsprov.Provider, error) {
	switch p.Kind {
	case "technitium":
		if r.cfg.TechnitiumURL == "" {
			return nil, errors.New("NETDB_TECHNITIUM_URL not configured")
		}
		return technitium.New(p.Name, technitium.Config{
			BaseURL:            r.cfg.TechnitiumURL,
			Token:              r.cfg.TechnitiumToken,
			Username:           r.cfg.TechnitiumUser,
			Password:           r.cfg.TechnitiumPassword,
			InsecureSkipVerify: r.cfg.TechnitiumInsecure,
		}), nil
	case "cloudflare":
		if r.cfg.CloudflareToken == "" {
			return nil, errors.New("NETDB_CLOUDFLARE_TOKEN not configured")
		}
		return cloudflare.New(p.Name, cloudflare.Config{
			Token:   r.cfg.CloudflareToken,
			BaseURL: r.cfg.CloudflareBaseURL,
		}), nil
	default:
		return nil, fmt.Errorf("unknown provider kind: %s", p.Kind)
	}
}

func (r *Reconciler) setStatus(s Status) {
	r.mu.Lock()
	r.status = s
	r.mu.Unlock()
}
