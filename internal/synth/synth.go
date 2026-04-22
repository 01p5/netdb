// Package synth derives the set of auto-generated DNS records from the
// current state of hosts, nics, ip_assignments, subnets, and subnet_zones,
// and materializes them into dns_records / dns_bindings with source='auto'.
//
// It's idempotent: calling Run() repeatedly converges to the same state.
// Manual records (source='manual') are left alone.
package synth

import (
	"context"
	"database/sql"
	"fmt"
	"net/netip"
	"strings"

	"netdb/internal/model"
	"netdb/internal/store"
)

type recordKey struct {
	FQDN, Type, Target string
}

type record struct {
	FQDN, Type, Target string
	TTL                int
	ZoneID             int64
	Bindings           []int64
}

// Run computes desired auto records and upserts them, deleting stale ones.
// Records whose (fqdn, type) already exist as source='manual' are suppressed —
// manual overrides win. This matches the "manual wins on conflict" policy
// agreed during design.
func Run(ctx context.Context, st *store.Store) error {
	desired, err := buildDesired(ctx, st)
	if err != nil {
		return fmt.Errorf("build desired: %w", err)
	}
	manualKeys, err := loadManualFQDNType(ctx, st.DB())
	if err != nil {
		return fmt.Errorf("load manual keys: %w", err)
	}
	for k := range desired {
		if manualKeys[k.FQDN+"|"+k.Type] {
			delete(desired, k)
		}
	}
	if err := apply(ctx, st.DB(), desired); err != nil {
		return fmt.Errorf("apply: %w", err)
	}
	return nil
}

func loadManualFQDNType(ctx context.Context, db *sql.DB) (map[string]bool, error) {
	rows, err := db.QueryContext(ctx,
		`SELECT fqdn, type FROM dns_records WHERE source='manual' AND enabled=1`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var fqdn, rtype string
		if err := rows.Scan(&fqdn, &rtype); err != nil {
			return nil, err
		}
		out[fqdn+"|"+rtype] = true
	}
	return out, rows.Err()
}

func buildDesired(ctx context.Context, st *store.Store) (map[recordKey]*record, error) {
	hosts, err := st.ListHosts(ctx)
	if err != nil {
		return nil, err
	}
	subnets, err := st.ListSubnets(ctx)
	if err != nil {
		return nil, err
	}

	type parsedSubnet struct {
		Subnet model.Subnet
		Prefix netip.Prefix
	}
	parsed := make([]parsedSubnet, 0, len(subnets))
	for _, sb := range subnets {
		p, err := netip.ParsePrefix(sb.CIDR)
		if err != nil {
			// Skip subnets with unparseable CIDRs — they just don't contribute.
			continue
		}
		parsed = append(parsed, parsedSubnet{Subnet: sb, Prefix: p})
	}

	// subnet id -> zone links (forward/reverse)
	subnetZones := make(map[int64][]model.SubnetZoneLink, len(subnets))
	for _, sb := range subnets {
		links, err := st.ListZonesForSubnet(ctx, sb.ID)
		if err != nil {
			return nil, err
		}
		subnetZones[sb.ID] = links
	}

	desired := make(map[recordKey]*record)

	for _, h := range hosts {
		if !h.DNSEnabled {
			continue
		}
		label := h.Name
		if h.DNSName != "" {
			label = h.DNSName
		}

		nics, err := st.ListNICsByHost(ctx, h.ID)
		if err != nil {
			return nil, err
		}
		for _, n := range nics {
			ips, err := st.ListIPsByNIC(ctx, n.ID)
			if err != nil {
				return nil, err
			}
			for _, ip := range ips {
				if ip.Kind != "static" && ip.Kind != "reserved" {
					continue
				}
				addr, err := netip.ParseAddr(ip.IP)
				if err != nil {
					continue
				}

				// First matching subnet wins.
				var match *parsedSubnet
				for i := range parsed {
					if parsed[i].Prefix.Contains(addr) {
						match = &parsed[i]
						break
					}
				}
				if match == nil {
					continue
				}

				links := subnetZones[match.Subnet.ID]

				// Deterministic PTR target: the alphabetically-first forward
				// zone linked to this subnet. If none, PTR is suppressed.
				var forwardForPTR *model.Zone
				for i := range links {
					if links[i].Role != "forward" {
						continue
					}
					z := links[i].Zone
					if forwardForPTR == nil || z.Name < forwardForPTR.Name {
						zCopy := z
						forwardForPTR = &zCopy
					}
				}

				// Forward A/AAAA records — one per (fqdn, type), bindings accrete.
				for _, l := range links {
					if l.Role != "forward" {
						continue
					}
					rtype := "AAAA"
					if addr.Is4() {
						rtype = "A"
					}
					fqdn := label + "." + l.Zone.Name
					k := recordKey{FQDN: fqdn, Type: rtype, Target: ""}
					rec, ok := desired[k]
					if !ok {
						rec = &record{FQDN: fqdn, Type: rtype, TTL: l.Zone.DefaultTTL, ZoneID: l.Zone.ID}
						desired[k] = rec
					}
					rec.Bindings = append(rec.Bindings, ip.ID)
				}

				// Reverse PTR records — require both a reverse zone covering
				// this IP and a forward zone to point at.
				if forwardForPTR == nil {
					continue
				}
				ptr := reverseFQDN(addr)
				target := label + "." + forwardForPTR.Name
				for _, l := range links {
					if l.Role != "reverse" {
						continue
					}
					if !strings.HasSuffix(ptr, l.Zone.Name) {
						continue
					}
					k := recordKey{FQDN: ptr, Type: "PTR", Target: target}
					if _, ok := desired[k]; ok {
						continue
					}
					desired[k] = &record{
						FQDN: ptr, Type: "PTR", Target: target,
						TTL: l.Zone.DefaultTTL, ZoneID: l.Zone.ID,
						Bindings: []int64{ip.ID},
					}
				}
			}
		}
	}

	return desired, nil
}

// reverseFQDN returns the canonical in-addr.arpa / ip6.arpa name for addr.
func reverseFQDN(addr netip.Addr) string {
	if addr.Is4() {
		b := addr.As4()
		return fmt.Sprintf("%d.%d.%d.%d.in-addr.arpa", b[3], b[2], b[1], b[0])
	}
	b := addr.As16()
	var sb strings.Builder
	for i := 15; i >= 0; i-- {
		lo := b[i] & 0x0F
		hi := b[i] >> 4
		fmt.Fprintf(&sb, "%x.%x.", lo, hi)
	}
	sb.WriteString("ip6.arpa")
	return sb.String()
}

// apply diffs desired against the current source='auto' rows and converges
// them in a single transaction.
func apply(ctx context.Context, db *sql.DB, desired map[recordKey]*record) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	existing, err := loadExistingAuto(ctx, tx)
	if err != nil {
		return err
	}

	for k, d := range desired {
		var recordID int64
		if id, ok := existing[k]; ok {
			if _, err := tx.ExecContext(ctx,
				`UPDATE dns_records SET ttl=?, zone_id=?, updated_at=CURRENT_TIMESTAMP WHERE id=?`,
				d.TTL, d.ZoneID, id); err != nil {
				return err
			}
			recordID = id
			delete(existing, k)
		} else {
			res, err := tx.ExecContext(ctx,
				`INSERT INTO dns_records(fqdn, type, target, ttl, zone_id, source)
				 VALUES(?, ?, ?, ?, ?, 'auto')`,
				k.FQDN, k.Type, k.Target, d.TTL, d.ZoneID)
			if err != nil {
				return err
			}
			recordID, err = res.LastInsertId()
			if err != nil {
				return err
			}
		}
		if err := syncBindings(ctx, tx, recordID, d.Bindings); err != nil {
			return err
		}
	}

	for _, id := range existing {
		if _, err := tx.ExecContext(ctx, `DELETE FROM dns_records WHERE id=?`, id); err != nil {
			return err
		}
	}

	return tx.Commit()
}

func loadExistingAuto(ctx context.Context, tx *sql.Tx) (map[recordKey]int64, error) {
	rows, err := tx.QueryContext(ctx,
		`SELECT id, fqdn, type, target FROM dns_records WHERE source='auto'`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := map[recordKey]int64{}
	for rows.Next() {
		var id int64
		var fqdn, typ, tgt string
		if err := rows.Scan(&id, &fqdn, &typ, &tgt); err != nil {
			return nil, err
		}
		out[recordKey{FQDN: fqdn, Type: typ, Target: tgt}] = id
	}
	return out, rows.Err()
}

func syncBindings(ctx context.Context, tx *sql.Tx, recordID int64, desired []int64) error {
	rows, err := tx.QueryContext(ctx,
		`SELECT ip_assignment_id FROM dns_bindings WHERE dns_record_id=?`, recordID)
	if err != nil {
		return err
	}
	current := map[int64]bool{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		current[id] = true
	}
	rows.Close()

	desSet := map[int64]bool{}
	for _, id := range desired {
		desSet[id] = true
	}

	for id := range desSet {
		if !current[id] {
			if _, err := tx.ExecContext(ctx,
				`INSERT INTO dns_bindings(dns_record_id, ip_assignment_id) VALUES(?, ?)`,
				recordID, id); err != nil {
				return err
			}
		}
	}
	for id := range current {
		if !desSet[id] {
			if _, err := tx.ExecContext(ctx,
				`DELETE FROM dns_bindings WHERE dns_record_id=? AND ip_assignment_id=?`,
				recordID, id); err != nil {
				return err
			}
		}
	}
	return nil
}
