// Tool catalog. One Register call per MCP tool; each tool is a thin
// wrapper over the underlying store / reconciler / kea-syncer methods,
// keeping the HTTP rendering layer out of the MCP path.
package mcp

import (
	"context"
	"fmt"

	"netdb/internal/kea"
	"netdb/internal/model"
	"netdb/internal/reconcile"
	"netdb/internal/store"
)

// Deps bundles the runtime objects the tools need. Mirrors what
// internal/httpx.Server holds — kept separate so the mcp package
// doesn't import httpx (avoids a cycle, and keeps the MCP surface
// independent of the UI handlers' lifecycle).
type Deps struct {
	Store       *store.Store
	Reconciler  *reconcile.Reconciler
	Kea         *kea.Syncer
	LeasePoller *kea.LeasePoller
}

// RegisterAll wires every netdb tool onto the given server. Returns
// the server for chaining; ordering of the tools is the order they
// appear in tools/list — kept logical (hosts → nics → ips → subnets →
// zones → providers → dns → dhcp) so the agent prompt is scannable.
func RegisterAll(s *Server, d Deps) *Server {
	// ---------- hosts ----------
	s.Register(Tool{
		Name:        "list_hosts",
		Description: "List all hosts in netdb (id, name, description, tags, dns_name, dns_enabled).",
		InputSchema: objectSchema(nil, nil),
		Call: func(ctx context.Context, _ map[string]any) (any, error) {
			return d.Store.ListHosts(ctx)
		},
	})
	s.Register(Tool{
		Name:        "get_host",
		Description: "Get a single host with its NICs and IP assignments.",
		InputSchema: objectSchema(map[string]any{
			"id": intSchema("Host ID"),
		}, []string{"id"}),
		Call: func(ctx context.Context, args map[string]any) (any, error) {
			id, err := ArgInt64(args, "id")
			if err != nil {
				return nil, err
			}
			return d.Store.GetHostDetail(ctx, id)
		},
	})
	s.Register(Tool{
		Name:        "create_host",
		Description: "Create a host. name is required; description and tags optional.",
		InputSchema: objectSchema(map[string]any{
			"name":        stringSchema("Host name"),
			"description": stringSchema("Free-form description"),
			"tags":        stringSchema("Comma-separated tags"),
		}, []string{"name"}),
		Destructive: true,
		Call: func(ctx context.Context, args map[string]any) (any, error) {
			name, err := ArgString(args, "name")
			if err != nil {
				return nil, err
			}
			out, err := d.Store.CreateHost(ctx, name, ArgStringOpt(args, "description"), ArgStringOpt(args, "tags"))
			if err == nil {
				go d.Reconciler.Trigger()
			}
			return out, err
		},
	})
	s.Register(Tool{
		Name:        "update_host",
		Description: "Update a host's metadata. Supplied fields are replaced; omit fields you don't want to change.",
		InputSchema: objectSchema(map[string]any{
			"id":          intSchema("Host ID"),
			"name":        stringSchema("Host name"),
			"description": stringSchema("Description"),
			"tags":        stringSchema("Comma-separated tags"),
			"dns_name":    stringSchema("DNS label for this host"),
			"dns_enabled": boolSchema("Whether DNS records get synthesized for this host"),
		}, []string{"id", "name"}),
		Destructive: true,
		Call: func(ctx context.Context, args map[string]any) (any, error) {
			id, err := ArgInt64(args, "id")
			if err != nil {
				return nil, err
			}
			name, err := ArgString(args, "name")
			if err != nil {
				return nil, err
			}
			err = d.Store.UpdateHost(ctx, id,
				name,
				ArgStringOpt(args, "description"),
				ArgStringOpt(args, "tags"),
				ArgStringOpt(args, "dns_name"),
				ArgBoolOpt(args, "dns_enabled", false),
			)
			if err == nil {
				go d.Reconciler.Trigger()
			}
			return map[string]any{"updated": id}, err
		},
	})
	s.Register(Tool{
		Name:        "delete_host",
		Description: "Delete a host and all its NICs / IPs. Irreversible.",
		InputSchema: objectSchema(map[string]any{
			"id": intSchema("Host ID"),
		}, []string{"id"}),
		Destructive: true,
		Call: func(ctx context.Context, args map[string]any) (any, error) {
			id, err := ArgInt64(args, "id")
			if err != nil {
				return nil, err
			}
			err = d.Store.DeleteHost(ctx, id)
			if err == nil {
				go d.Reconciler.Trigger()
			}
			return map[string]any{"deleted": id}, err
		},
	})

	// ---------- nics ----------
	s.Register(Tool{
		Name:        "create_nic",
		Description: "Attach a new NIC to a host. mac required; label optional.",
		InputSchema: objectSchema(map[string]any{
			"host_id": intSchema("Host ID"),
			"mac":     stringSchema("MAC address (any common format)"),
			"label":   stringSchema("Human label like eth0 / lan / wan"),
		}, []string{"host_id", "mac"}),
		Destructive: true,
		Call: func(ctx context.Context, args map[string]any) (any, error) {
			hostID, err := ArgInt64(args, "host_id")
			if err != nil {
				return nil, err
			}
			mac, err := ArgString(args, "mac")
			if err != nil {
				return nil, err
			}
			out, err := d.Store.CreateNIC(ctx, hostID, mac, ArgStringOpt(args, "label"))
			if err == nil {
				go d.Reconciler.Trigger()
			}
			return out, err
		},
	})
	s.Register(Tool{
		Name:        "delete_nic",
		Description: "Delete a NIC and its IP assignments.",
		InputSchema: objectSchema(map[string]any{
			"id": intSchema("NIC ID"),
		}, []string{"id"}),
		Destructive: true,
		Call: func(ctx context.Context, args map[string]any) (any, error) {
			id, err := ArgInt64(args, "id")
			if err != nil {
				return nil, err
			}
			err = d.Store.DeleteNIC(ctx, id)
			if err == nil {
				go d.Reconciler.Trigger()
			}
			return map[string]any{"deleted": id}, err
		},
	})

	// ---------- ips ----------
	s.Register(Tool{
		Name:        "create_ip",
		Description: "Assign an IP to a NIC. kind is 'static' | 'reserved' | 'dynamic'. subnet_id optional but recommended.",
		InputSchema: objectSchema(map[string]any{
			"nic_id":    intSchema("NIC ID"),
			"ip":        stringSchema("IP address (v4 or v6)"),
			"kind":      stringSchema("static / reserved / dynamic"),
			"subnet_id": intSchema("Optional subnet ID this IP belongs to"),
		}, []string{"nic_id", "ip", "kind"}),
		Destructive: true,
		Call: func(ctx context.Context, args map[string]any) (any, error) {
			nicID, err := ArgInt64(args, "nic_id")
			if err != nil {
				return nil, err
			}
			ip, err := ArgString(args, "ip")
			if err != nil {
				return nil, err
			}
			kind, err := ArgString(args, "kind")
			if err != nil {
				return nil, err
			}
			var subnetPtr *int64
			if _, ok := args["subnet_id"]; ok {
				sid, err := ArgInt64(args, "subnet_id")
				if err != nil {
					return nil, err
				}
				subnetPtr = &sid
			}
			out, err := d.Store.CreateIPAssignment(ctx, nicID, subnetPtr, ip, kind)
			if err == nil {
				go d.Reconciler.Trigger()
			}
			return out, err
		},
	})
	s.Register(Tool{
		Name:        "delete_ip",
		Description: "Remove an IP assignment.",
		InputSchema: objectSchema(map[string]any{
			"id": intSchema("IP assignment ID"),
		}, []string{"id"}),
		Destructive: true,
		Call: func(ctx context.Context, args map[string]any) (any, error) {
			id, err := ArgInt64(args, "id")
			if err != nil {
				return nil, err
			}
			err = d.Store.DeleteIPAssignment(ctx, id)
			if err == nil {
				go d.Reconciler.Trigger()
			}
			return map[string]any{"deleted": id}, err
		},
	})

	// ---------- subnets ----------
	s.Register(Tool{
		Name:        "list_subnets",
		Description: "List all subnets (cidr, vlan, gateway, dhcp range, options).",
		InputSchema: objectSchema(nil, nil),
		Call: func(ctx context.Context, _ map[string]any) (any, error) {
			return d.Store.ListSubnets(ctx)
		},
	})
	s.Register(Tool{
		Name:        "get_subnet",
		Description: "Get a single subnet by ID.",
		InputSchema: objectSchema(map[string]any{
			"id": intSchema("Subnet ID"),
		}, []string{"id"}),
		Call: func(ctx context.Context, args map[string]any) (any, error) {
			id, err := ArgInt64(args, "id")
			if err != nil {
				return nil, err
			}
			return d.Store.GetSubnet(ctx, id)
		},
	})
	s.Register(Tool{
		Name: "create_subnet",
		Description: "Create a subnet. cidr is required. dhcp_range_start / dhcp_range_end enable Kea DHCP for the range. " +
			"dns_servers is a comma-separated list pushed to DHCP option 6.",
		InputSchema: objectSchema(map[string]any{
			"cidr":             stringSchema("e.g. 10.0.3.0/24"),
			"vlan":             intSchema("Optional VLAN id"),
			"gateway":          stringSchema("Default gateway IP"),
			"description":      stringSchema("Description"),
			"dhcp_range_start": stringSchema("First IP in the dynamic range"),
			"dhcp_range_end":   stringSchema("Last IP in the dynamic range"),
			"dhcp_options":     stringSchema("Raw JSON for extra Kea options"),
			"dns_servers":      stringSchema("Comma-separated DNS server IPs"),
		}, []string{"cidr"}),
		Destructive: true,
		Call: func(ctx context.Context, args map[string]any) (any, error) {
			cidr, err := ArgString(args, "cidr")
			if err != nil {
				return nil, err
			}
			sb := model.Subnet{
				CIDR:            cidr,
				Gateway:         ArgStringOpt(args, "gateway"),
				Description:     ArgStringOpt(args, "description"),
				DHCPRangeStart:  ArgStringOpt(args, "dhcp_range_start"),
				DHCPRangeEnd:    ArgStringOpt(args, "dhcp_range_end"),
				DHCPOptionsJSON: ArgStringOpt(args, "dhcp_options"),
				DNSServers:      ArgStringOpt(args, "dns_servers"),
			}
			if v, ok := args["vlan"]; ok {
				if f, ok := v.(float64); ok {
					vi := int(f)
					sb.VLAN = &vi
				}
			}
			out, err := d.Store.CreateSubnet(ctx, sb)
			if err == nil {
				go d.Reconciler.Trigger()
				go d.Kea.Trigger()
			}
			return out, err
		},
	})
	s.Register(Tool{
		Name:        "update_subnet",
		Description: "Update a subnet. Pass id plus any fields to replace; cidr is required.",
		InputSchema: objectSchema(map[string]any{
			"id":               intSchema("Subnet ID"),
			"cidr":             stringSchema("e.g. 10.0.3.0/24"),
			"vlan":             intSchema("Optional VLAN id"),
			"gateway":          stringSchema("Default gateway IP"),
			"description":      stringSchema("Description"),
			"dhcp_range_start": stringSchema("First IP in the dynamic range"),
			"dhcp_range_end":   stringSchema("Last IP in the dynamic range"),
			"dhcp_options":     stringSchema("Raw JSON for extra Kea options"),
			"dns_servers":      stringSchema("Comma-separated DNS server IPs"),
		}, []string{"id", "cidr"}),
		Destructive: true,
		Call: func(ctx context.Context, args map[string]any) (any, error) {
			id, err := ArgInt64(args, "id")
			if err != nil {
				return nil, err
			}
			cidr, err := ArgString(args, "cidr")
			if err != nil {
				return nil, err
			}
			sb := model.Subnet{
				CIDR:            cidr,
				Gateway:         ArgStringOpt(args, "gateway"),
				Description:     ArgStringOpt(args, "description"),
				DHCPRangeStart:  ArgStringOpt(args, "dhcp_range_start"),
				DHCPRangeEnd:    ArgStringOpt(args, "dhcp_range_end"),
				DHCPOptionsJSON: ArgStringOpt(args, "dhcp_options"),
				DNSServers:      ArgStringOpt(args, "dns_servers"),
			}
			if v, ok := args["vlan"]; ok {
				if f, ok := v.(float64); ok {
					vi := int(f)
					sb.VLAN = &vi
				}
			}
			if err := d.Store.UpdateSubnet(ctx, id, sb); err != nil {
				return nil, err
			}
			go d.Reconciler.Trigger()
			go d.Kea.Trigger()
			return map[string]any{"updated": id}, nil
		},
	})
	s.Register(Tool{
		Name:        "delete_subnet",
		Description: "Delete a subnet. Fails if IPs are still assigned to it.",
		InputSchema: objectSchema(map[string]any{
			"id": intSchema("Subnet ID"),
		}, []string{"id"}),
		Destructive: true,
		Call: func(ctx context.Context, args map[string]any) (any, error) {
			id, err := ArgInt64(args, "id")
			if err != nil {
				return nil, err
			}
			if err := d.Store.DeleteSubnet(ctx, id); err != nil {
				return nil, err
			}
			go d.Reconciler.Trigger()
			go d.Kea.Trigger()
			return map[string]any{"deleted": id}, nil
		},
	})
	s.Register(Tool{
		Name: "link_subnet_zone",
		Description: "Bind a subnet to a DNS zone in a given role (forward / reverse). " +
			"The reconciler then synthesizes A / PTR records as hosts get IPs in this subnet.",
		InputSchema: objectSchema(map[string]any{
			"subnet_id": intSchema("Subnet ID"),
			"zone_id":   intSchema("Zone ID"),
			"role":      stringSchema("forward or reverse"),
		}, []string{"subnet_id", "zone_id", "role"}),
		Destructive: true,
		Call: func(ctx context.Context, args map[string]any) (any, error) {
			sid, err := ArgInt64(args, "subnet_id")
			if err != nil {
				return nil, err
			}
			zid, err := ArgInt64(args, "zone_id")
			if err != nil {
				return nil, err
			}
			role, err := ArgString(args, "role")
			if err != nil {
				return nil, err
			}
			if err := d.Store.LinkSubnetZone(ctx, sid, zid, role); err != nil {
				return nil, err
			}
			go d.Reconciler.Trigger()
			return map[string]any{"linked": map[string]any{"subnet": sid, "zone": zid, "role": role}}, nil
		},
	})
	s.Register(Tool{
		Name:        "unlink_subnet_zone",
		Description: "Remove a subnet ↔ zone binding for a given role.",
		InputSchema: objectSchema(map[string]any{
			"subnet_id": intSchema("Subnet ID"),
			"zone_id":   intSchema("Zone ID"),
			"role":      stringSchema("forward or reverse"),
		}, []string{"subnet_id", "zone_id", "role"}),
		Destructive: true,
		Call: func(ctx context.Context, args map[string]any) (any, error) {
			sid, err := ArgInt64(args, "subnet_id")
			if err != nil {
				return nil, err
			}
			zid, err := ArgInt64(args, "zone_id")
			if err != nil {
				return nil, err
			}
			role, err := ArgString(args, "role")
			if err != nil {
				return nil, err
			}
			if err := d.Store.UnlinkSubnetZone(ctx, sid, zid, role); err != nil {
				return nil, err
			}
			go d.Reconciler.Trigger()
			return map[string]any{"unlinked": map[string]any{"subnet": sid, "zone": zid, "role": role}}, nil
		},
	})

	// ---------- zones ----------
	s.Register(Tool{
		Name:        "list_zones",
		Description: "List all DNS zones (name, kind, default_ttl).",
		InputSchema: objectSchema(nil, nil),
		Call: func(ctx context.Context, _ map[string]any) (any, error) {
			return d.Store.ListZones(ctx)
		},
	})
	s.Register(Tool{
		Name:        "create_zone",
		Description: "Create a DNS zone. kind is 'forward' or 'reverse'.",
		InputSchema: objectSchema(map[string]any{
			"name":        stringSchema("Zone name (e.g. example.com)"),
			"kind":        stringSchema("forward or reverse"),
			"description": stringSchema("Description"),
			"default_ttl": intSchema("Default TTL for synthesized records"),
		}, []string{"name", "kind"}),
		Destructive: true,
		Call: func(ctx context.Context, args map[string]any) (any, error) {
			name, err := ArgString(args, "name")
			if err != nil {
				return nil, err
			}
			kind, err := ArgString(args, "kind")
			if err != nil {
				return nil, err
			}
			ttl := ArgIntOpt(args, "default_ttl", 300)
			out, err := d.Store.CreateZone(ctx, name, kind, ArgStringOpt(args, "description"), ttl)
			if err == nil {
				go d.Reconciler.Trigger()
			}
			return out, err
		},
	})
	s.Register(Tool{
		Name:        "delete_zone",
		Description: "Delete a DNS zone. Fails if records still exist.",
		InputSchema: objectSchema(map[string]any{
			"id": intSchema("Zone ID"),
		}, []string{"id"}),
		Destructive: true,
		Call: func(ctx context.Context, args map[string]any) (any, error) {
			id, err := ArgInt64(args, "id")
			if err != nil {
				return nil, err
			}
			if err := d.Store.DeleteZone(ctx, id); err != nil {
				return nil, err
			}
			go d.Reconciler.Trigger()
			return map[string]any{"deleted": id}, nil
		},
	})
	s.Register(Tool{
		Name:        "link_zone_provider",
		Description: "Attach a DNS provider to a zone — the reconciler will push this zone's records to that provider.",
		InputSchema: objectSchema(map[string]any{
			"zone_id":     intSchema("Zone ID"),
			"provider_id": intSchema("Provider ID"),
		}, []string{"zone_id", "provider_id"}),
		Destructive: true,
		Call: func(ctx context.Context, args map[string]any) (any, error) {
			zid, err := ArgInt64(args, "zone_id")
			if err != nil {
				return nil, err
			}
			pid, err := ArgInt64(args, "provider_id")
			if err != nil {
				return nil, err
			}
			if err := d.Store.LinkZoneProvider(ctx, zid, pid); err != nil {
				return nil, err
			}
			go d.Reconciler.Trigger()
			return map[string]any{"linked": map[string]any{"zone": zid, "provider": pid}}, nil
		},
	})
	s.Register(Tool{
		Name:        "unlink_zone_provider",
		Description: "Detach a provider from a zone.",
		InputSchema: objectSchema(map[string]any{
			"zone_id":     intSchema("Zone ID"),
			"provider_id": intSchema("Provider ID"),
		}, []string{"zone_id", "provider_id"}),
		Destructive: true,
		Call: func(ctx context.Context, args map[string]any) (any, error) {
			zid, err := ArgInt64(args, "zone_id")
			if err != nil {
				return nil, err
			}
			pid, err := ArgInt64(args, "provider_id")
			if err != nil {
				return nil, err
			}
			if err := d.Store.UnlinkZoneProvider(ctx, zid, pid); err != nil {
				return nil, err
			}
			go d.Reconciler.Trigger()
			return map[string]any{"unlinked": map[string]any{"zone": zid, "provider": pid}}, nil
		},
	})

	// ---------- providers ----------
	s.Register(Tool{
		Name:        "list_providers",
		Description: "List configured DNS providers (Cloudflare, Technitium, …).",
		InputSchema: objectSchema(nil, nil),
		Call: func(ctx context.Context, _ map[string]any) (any, error) {
			return d.Store.ListProviders(ctx)
		},
	})
	s.Register(Tool{
		Name:        "create_provider",
		Description: "Register a new DNS provider. kind is 'cloudflare' or 'technitium'. config_json is provider-specific JSON.",
		InputSchema: objectSchema(map[string]any{
			"name":        stringSchema("Provider name"),
			"kind":        stringSchema("cloudflare or technitium"),
			"config_json": stringSchema("Provider-specific JSON config"),
			"enabled":     boolSchema("Whether this provider participates in reconciliation"),
		}, []string{"name", "kind"}),
		Destructive: true,
		Call: func(ctx context.Context, args map[string]any) (any, error) {
			name, err := ArgString(args, "name")
			if err != nil {
				return nil, err
			}
			kind, err := ArgString(args, "kind")
			if err != nil {
				return nil, err
			}
			enabled := ArgBoolOpt(args, "enabled", true)
			out, err := d.Store.CreateProvider(ctx, name, kind, ArgStringOpt(args, "config_json"), enabled)
			if err == nil {
				go d.Reconciler.Trigger()
			}
			return out, err
		},
	})
	s.Register(Tool{
		Name:        "delete_provider",
		Description: "Delete a DNS provider. Records pushed via this provider will no longer be reconciled.",
		InputSchema: objectSchema(map[string]any{
			"id": intSchema("Provider ID"),
		}, []string{"id"}),
		Destructive: true,
		Call: func(ctx context.Context, args map[string]any) (any, error) {
			id, err := ArgInt64(args, "id")
			if err != nil {
				return nil, err
			}
			if err := d.Store.DeleteProvider(ctx, id); err != nil {
				return nil, err
			}
			go d.Reconciler.Trigger()
			return map[string]any{"deleted": id}, nil
		},
	})

	// ---------- dns ----------
	s.Register(Tool{
		Name:        "list_dns_records",
		Description: "List all DNS records netdb is managing (auto-synthesized + manually added).",
		InputSchema: objectSchema(nil, nil),
		Call: func(ctx context.Context, _ map[string]any) (any, error) {
			return d.Store.ListDNSRecords(ctx)
		},
	})
	s.Register(Tool{
		Name: "create_manual_dns",
		Description: "Add a manual DNS record outside the host-driven synth path. " +
			"rtype is one of A / AAAA / CNAME / TXT / MX / NS / PTR.",
		InputSchema: objectSchema(map[string]any{
			"zone":   stringSchema("Zone name the record belongs to"),
			"fqdn":   stringSchema("Fully-qualified record name"),
			"rtype":  stringSchema("Record type"),
			"target": stringSchema("Record value (IP, FQDN, text, …)"),
			"ttl":    intSchema("TTL seconds"),
		}, []string{"zone", "fqdn", "rtype", "target"}),
		Destructive: true,
		Call: func(ctx context.Context, args map[string]any) (any, error) {
			zone, err := ArgString(args, "zone")
			if err != nil {
				return nil, err
			}
			fqdn, err := ArgString(args, "fqdn")
			if err != nil {
				return nil, err
			}
			rtype, err := ArgString(args, "rtype")
			if err != nil {
				return nil, err
			}
			target, err := ArgString(args, "target")
			if err != nil {
				return nil, err
			}
			ttl := ArgIntOpt(args, "ttl", 300)
			id, err := d.Store.CreateManualDNSRecord(ctx, zone, fqdn, rtype, target, ttl)
			if err == nil {
				go d.Reconciler.Trigger()
			}
			return map[string]any{"id": id}, err
		},
	})
	s.Register(Tool{
		Name:        "delete_dns_record",
		Description: "Delete a DNS record by netdb id.",
		InputSchema: objectSchema(map[string]any{
			"id": intSchema("DNS record ID"),
		}, []string{"id"}),
		Destructive: true,
		Call: func(ctx context.Context, args map[string]any) (any, error) {
			id, err := ArgInt64(args, "id")
			if err != nil {
				return nil, err
			}
			if err := d.Store.DeleteDNSRecord(ctx, id); err != nil {
				return nil, err
			}
			go d.Reconciler.Trigger()
			return map[string]any{"deleted": id}, nil
		},
	})
	s.Register(Tool{
		Name:        "dns_sync_now",
		Description: "Trigger an immediate DNS reconciliation cycle. Returns the post-trigger status.",
		InputSchema: objectSchema(nil, nil),
		Destructive: true,
		Call: func(ctx context.Context, _ map[string]any) (any, error) {
			d.Reconciler.Trigger()
			return map[string]any{"triggered": true, "status": d.Reconciler.Status()}, nil
		},
	})

	// ---------- dhcp ----------
	s.Register(Tool{
		Name:        "dhcp_status",
		Description: "Return the current Kea sync + lease-poll status (last-run timestamps, last-run errors).",
		InputSchema: objectSchema(nil, nil),
		Call: func(ctx context.Context, _ map[string]any) (any, error) {
			return map[string]any{
				"sync":   d.Kea.Status(),
				"leases": d.LeasePoller.Status(),
			}, nil
		},
	})
	s.Register(Tool{
		Name:        "kea_sync_now",
		Description: "Push the current netdb DHCP config to Kea immediately. Idempotent.",
		InputSchema: objectSchema(nil, nil),
		Destructive: true,
		Call: func(ctx context.Context, _ map[string]any) (any, error) {
			d.Kea.Trigger()
			return map[string]any{"triggered": true, "status": d.Kea.Status()}, nil
		},
	})
	s.Register(Tool{
		Name:        "kea_poll_leases",
		Description: "Trigger an immediate lease poll from Kea; new dynamic leases land in netdb.",
		InputSchema: objectSchema(nil, nil),
		Destructive: true,
		Call: func(ctx context.Context, _ map[string]any) (any, error) {
			d.LeasePoller.Trigger()
			return map[string]any{"triggered": true, "status": d.LeasePoller.Status()}, nil
		},
	})

	// ---------- reservations (read-only convenience) ----------
	s.Register(Tool{
		Name:        "list_reservations",
		Description: "List DHCP reservations (static IPs that get pushed as Kea host reservations).",
		InputSchema: objectSchema(nil, nil),
		Call: func(ctx context.Context, _ map[string]any) (any, error) {
			return d.Store.ListReservations(ctx)
		},
	})

	return s
}

// objectSchema builds a JSON-Schema object descriptor. nil props is fine;
// some tools take no args.
func objectSchema(props map[string]any, required []string) map[string]any {
	if props == nil {
		props = map[string]any{}
	}
	out := map[string]any{
		"type":       "object",
		"properties": props,
	}
	if len(required) > 0 {
		out["required"] = required
	}
	return out
}

func stringSchema(desc string) map[string]any {
	return map[string]any{"type": "string", "description": desc}
}
func intSchema(desc string) map[string]any {
	return map[string]any{"type": "integer", "description": desc}
}
func boolSchema(desc string) map[string]any {
	return map[string]any{"type": "boolean", "description": desc}
}

// Compile-time guard so unused imports don't slip past.
var _ = fmt.Sprintf
