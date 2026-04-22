// Package dnsprov defines the provider-agnostic interface for pushing
// records to a DNS server (Cloudflare, Technitium, etc.).
//
// Records are flattened to a single value per row. An A record with three
// round-robin IPs is three Record values. This matches both backends' native
// shape (Cloudflare stores each A as a separate row; Technitium accepts
// adds one rdata at a time).
package dnsprov

import "context"

type Record struct {
	Name  string // FQDN
	Type  string // A | AAAA | CNAME | PTR | MX | TXT | SRV
	Value string // rdata in a type-specific string form
	TTL   int
	// ProviderID is the provider's opaque handle for an existing record —
	// Cloudflare uses UUIDs, Technitium identifies by (name,type,value) and
	// leaves this empty. Callers pass back the exact Record they received
	// from ListRecords when deleting; adapters that need an ID use it.
	ProviderID string
}

// Key uniquely identifies a record for set-comparison during reconcile.
// Two records with the same Key are considered equivalent regardless of TTL.
// We treat TTL changes as "update = delete + add" at the apply layer to
// keep the provider surface minimal.
func (r Record) Key() string { return r.Name + "|" + r.Type + "|" + r.Value }

type Provider interface {
	Name() string
	Kind() string
	// EnsureZone creates the zone in the provider if it doesn't exist.
	EnsureZone(ctx context.Context, zone string) error
	// ListRecords returns the zone's current records, filtered to the
	// types the provider manages (A/AAAA/CNAME/PTR/MX/TXT/SRV — not SOA/NS).
	ListRecords(ctx context.Context, zone string) ([]Record, error)
	AddRecord(ctx context.Context, zone string, r Record) error
	DeleteRecord(ctx context.Context, zone string, r Record) error
}
