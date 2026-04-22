package kea

import (
	"context"
	"fmt"
	"net/netip"
	"strings"

	"netdb/internal/model"
	"netdb/internal/store"
)

// Dhcp4Config is the JSON shape Kea expects under the top-level "Dhcp4" key.
// Fields omitted here (e.g. client-classes, shared-networks) are left to
// Kea's defaults or the startup file.
type Dhcp4Config struct {
	InterfacesConfig InterfacesConfig `json:"interfaces-config"`
	ControlSocket    ControlSocket    `json:"control-socket"`
	LeaseDatabase    LeaseDatabase    `json:"lease-database"`
	ValidLifetime    int              `json:"valid-lifetime,omitempty"`
	RenewTimer       int              `json:"renew-timer,omitempty"`
	RebindTimer      int              `json:"rebind-timer,omitempty"`
	HooksLibraries   []HookLibrary    `json:"hooks-libraries,omitempty"`
	Subnet4          []Subnet4        `json:"subnet4"`
	Loggers          []Logger         `json:"loggers,omitempty"`
}

type InterfacesConfig struct {
	Interfaces     []string `json:"interfaces"`
	DHCPSocketType string   `json:"dhcp-socket-type,omitempty"`
}

type ControlSocket struct {
	SocketType string `json:"socket-type"`
	SocketName string `json:"socket-name"`
}

type LeaseDatabase struct {
	Type        string `json:"type"`
	Persist     bool   `json:"persist,omitempty"`
	Name        string `json:"name,omitempty"`
	LFCInterval int    `json:"lfc-interval,omitempty"`
}

type HookLibrary struct {
	Library string `json:"library"`
}

type Subnet4 struct {
	ID           int64         `json:"id"`
	Subnet       string        `json:"subnet"`
	Pools        []Pool        `json:"pools,omitempty"`
	OptionData   []Option      `json:"option-data,omitempty"`
	Reservations []Reservation `json:"reservations"`
}

type Pool struct {
	Pool string `json:"pool"`
}

type Option struct {
	Name string `json:"name"`
	Data string `json:"data"`
}

type Reservation struct {
	HWAddress string `json:"hw-address"`
	IPAddress string `json:"ip-address"`
	Hostname  string `json:"hostname,omitempty"`
}

type Logger struct {
	Name          string          `json:"name"`
	OutputOptions []OutputOption  `json:"output_options"`
	Severity      string          `json:"severity,omitempty"`
}

type OutputOption struct {
	Output string `json:"output"`
}

// BuildConfig reads the current state from the store and assembles a full
// Kea Dhcp4 config. Reservations are placed in the subnet whose CIDR
// contains their IP; reservations outside any known subnet are dropped.
func BuildConfig(ctx context.Context, st *store.Store) (Dhcp4Config, int, error) {
	cfg := defaultBaseConfig()

	subnets, err := st.ListSubnets(ctx)
	if err != nil {
		return cfg, 0, err
	}
	reservations, err := st.ListReservations(ctx)
	if err != nil {
		return cfg, 0, err
	}

	type parsed struct {
		subnet model.Subnet
		prefix netip.Prefix
		out    Subnet4
	}
	parsedSubnets := make([]parsed, 0, len(subnets))
	for _, s := range subnets {
		pfx, err := netip.ParsePrefix(s.CIDR)
		if err != nil {
			continue // skip unparseable CIDRs rather than fail the whole push
		}
		sub := Subnet4{
			ID:           s.ID,
			Subnet:       s.CIDR,
			Reservations: []Reservation{}, // always a JSON array, never null
		}
		if s.DHCPRangeStart != "" && s.DHCPRangeEnd != "" {
			// Forgive last-octet shorthand ("99" → "10.99.1.99" inside /24).
			// If expansion fails, skip the pool rather than pushing broken JSON
			// that Kea rejects as a whole-config failure.
			if startIP, endIP, err := expandPoolBounds(s.CIDR, s.DHCPRangeStart, s.DHCPRangeEnd); err == nil {
				sub.Pools = []Pool{{Pool: startIP + " - " + endIP}}
			}
		}
		if s.Gateway != "" {
			sub.OptionData = append(sub.OptionData, Option{Name: "routers", Data: s.Gateway})
		}
		if dns := cleanCSV(s.DNSServers); dns != "" {
			// DHCP option 6 — what clients resolve through. For the
			// netdb/Technitium setup this should be the host IP where
			// Technitium listens on :53 (NOT the docker-internal IP).
			sub.OptionData = append(sub.OptionData, Option{Name: "domain-name-servers", Data: dns})
		}
		parsedSubnets = append(parsedSubnets, parsed{subnet: s, prefix: pfx, out: sub})
	}

	count := 0
	for _, r := range reservations {
		addr, err := netip.ParseAddr(r.IP)
		if err != nil {
			continue
		}
		for i := range parsedSubnets {
			if parsedSubnets[i].prefix.Contains(addr) {
				parsedSubnets[i].out.Reservations = append(parsedSubnets[i].out.Reservations, Reservation{
					HWAddress: r.MAC,
					IPAddress: r.IP,
					Hostname:  r.HostName,
				})
				count++
				break
			}
		}
	}

	cfg.Subnet4 = make([]Subnet4, 0, len(parsedSubnets))
	for _, p := range parsedSubnets {
		cfg.Subnet4 = append(cfg.Subnet4, p.out)
	}
	return cfg, count, nil
}

// cleanCSV trims whitespace around comma-separated IPs and drops empty
// entries. Kea accepts "10.0.0.1, 10.0.0.2" with or without spaces; we
// produce the tight form.
func cleanCSV(s string) string {
	parts := strings.Split(s, ",")
	out := parts[:0]
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return strings.Join(out, ", ")
}

// expandPoolBounds turns user-friendly pool inputs ("99", "10.99.1.99") into
// fully-qualified IPs that Kea will accept. If the input already parses as a
// full IP, it's returned as-is. Otherwise the missing leading octets are
// taken from the subnet's network address.
func expandPoolBounds(cidr, start, end string) (string, string, error) {
	s, err := expandPartialIP(cidr, start)
	if err != nil {
		return "", "", fmt.Errorf("dhcp range start: %w", err)
	}
	e, err := expandPartialIP(cidr, end)
	if err != nil {
		return "", "", fmt.Errorf("dhcp range end: %w", err)
	}
	return s, e, nil
}

func expandPartialIP(cidr, value string) (string, error) {
	value = strings.TrimSpace(value)
	if _, err := netip.ParseAddr(value); err == nil {
		return value, nil
	}
	pfx, err := netip.ParsePrefix(cidr)
	if err != nil {
		return "", fmt.Errorf("subnet %q: %w", cidr, err)
	}
	if !pfx.Addr().Is4() {
		return "", fmt.Errorf("ipv6 short-form not supported")
	}
	baseParts := strings.Split(pfx.Addr().String(), ".")
	valueParts := strings.Split(value, ".")
	if len(valueParts) >= 4 {
		return "", fmt.Errorf("value %q doesn't parse and isn't short-form", value)
	}
	full := append([]string{}, baseParts[:4-len(valueParts)]...)
	full = append(full, valueParts...)
	joined := strings.Join(full, ".")
	if _, err := netip.ParseAddr(joined); err != nil {
		return "", fmt.Errorf("expanded %q: %w", joined, err)
	}
	return joined, nil
}

// defaultBaseConfig mirrors the fields in kea/config/kea-dhcp4.conf that
// aren't DB-driven. Kept here so the build output is self-contained and
// doesn't depend on reading a base file at runtime.
func defaultBaseConfig() Dhcp4Config {
	return Dhcp4Config{
		InterfacesConfig: InterfacesConfig{Interfaces: []string{"*"}, DHCPSocketType: "udp"},
		ControlSocket: ControlSocket{
			SocketType: "unix",
			SocketName: "/run/kea/kea-dhcp4-ctrl.sock",
		},
		LeaseDatabase: LeaseDatabase{
			Type: "memfile", Persist: true,
			Name: "/var/lib/kea/dhcp4.leases", LFCInterval: 3600,
		},
		ValidLifetime: 3600,
		RenewTimer:    900,
		RebindTimer:   1800,
		HooksLibraries: []HookLibrary{
			{Library: "/usr/lib/kea/hooks/libdhcp_lease_cmds.so"},
		},
		Subnet4: []Subnet4{},
		Loggers: []Logger{
			{Name: "kea-dhcp4", Severity: "INFO", OutputOptions: []OutputOption{{Output: "stdout"}}},
		},
	}
}
