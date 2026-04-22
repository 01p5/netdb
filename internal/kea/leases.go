package kea

import (
	"context"
	"encoding/json"
	"fmt"
)

// Lease4 mirrors the subset of Kea's lease JSON we care about.
// Full schema: https://kea.readthedocs.io/en/kea-2.4.1/arm/hooks.html#lease4-get-all
type Lease4 struct {
	IPAddress     string `json:"ip-address"`
	HWAddress     string `json:"hw-address"`
	Hostname      string `json:"hostname"`
	State         int    `json:"state"`
	CLTT          int64  `json:"cltt"`     // client last transmission time
	ValidLifetime int    `json:"valid-lft"`
	SubnetID      int    `json:"subnet-id"`
	ClientID      string `json:"client-id,omitempty"`
}

// LeaseStateActive matches Kea's default lease state. Declined (1) and
// expired-reclaimed (2) are skipped by the ingester.
const LeaseStateActive = 0

// ListLeases4 fetches every DHCPv4 lease Kea knows about. An empty lease
// DB comes back with result=3 (no records), which we translate to nil,nil.
func (c *Client) ListLeases4(ctx context.Context) ([]Lease4, error) {
	r, err := c.call(ctx, Command{Command: "lease4-get-all", Service: []string{"dhcp4"}})
	if err != nil {
		return nil, err
	}
	switch r.Result {
	case 0:
	case 3: // "empty" result — no leases
		return nil, nil
	default:
		return nil, fmt.Errorf("lease4-get-all: %s", r.Text)
	}

	var args struct {
		Leases []Lease4 `json:"leases"`
	}
	if err := json.Unmarshal(r.Arguments, &args); err != nil {
		return nil, fmt.Errorf("decode leases: %w", err)
	}
	return args.Leases, nil
}
