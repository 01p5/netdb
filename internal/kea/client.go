// Package kea pushes netdb's DHCP state (subnets + reserved ip_assignments)
// into an ISC Kea DHCP4 server via its Control Agent REST interface.
//
// Mechanism: each sync cycle, build a complete Dhcp4 config JSON from the DB
// and send `config-set` to the ctrl-agent. Kea replaces its running config
// atomically. No file writes, no config include games.
//
// Trade-off: if netdb is down when Kea restarts, Kea comes up with whatever
// startup config file it has (we ship a minimal one, so effectively no DHCP).
// The assumption is netdb is always reachable in a dev/lab deployment. A
// future slice could add `config-write` to persist for survivability.
package kea

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

type Client struct {
	baseURL string
	http    *http.Client
}

func NewClient(baseURL string) *Client {
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		http:    &http.Client{Timeout: 15 * time.Second},
	}
}

// Command is the JSON envelope the ctrl-agent accepts. When Service is set,
// the ctrl-agent proxies to that daemon and wraps the response in an array.
type Command struct {
	Command   string          `json:"command"`
	Service   []string        `json:"service,omitempty"`
	Arguments json.RawMessage `json:"arguments,omitempty"`
}

// Response captures the subset of Kea's reply we care about.
type Response struct {
	Result    int             `json:"result"`
	Text      string          `json:"text"`
	Arguments json.RawMessage `json:"arguments,omitempty"`
}

// call posts cmd and unwraps the response. If Service is set, Kea returns
// a JSON array (one entry per service); we expect exactly one.
func (c *Client) call(ctx context.Context, cmd Command) (*Response, error) {
	body, err := json.Marshal(cmd)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(res.Body)
	if err != nil {
		return nil, err
	}
	if res.StatusCode >= 500 {
		return nil, fmt.Errorf("kea %s: http %d: %s", cmd.Command, res.StatusCode, string(raw))
	}

	if len(cmd.Service) > 0 {
		var arr []Response
		if err := json.Unmarshal(raw, &arr); err != nil {
			return nil, fmt.Errorf("kea %s: decode array: %w (body=%s)", cmd.Command, err, string(raw))
		}
		if len(arr) == 0 {
			return nil, fmt.Errorf("kea %s: empty response array", cmd.Command)
		}
		return &arr[0], nil
	}
	var r Response
	if err := json.Unmarshal(raw, &r); err != nil {
		return nil, fmt.Errorf("kea %s: decode: %w (body=%s)", cmd.Command, err, string(raw))
	}
	return &r, nil
}

// ConfigSet pushes a full Dhcp4 configuration. Kea applies it atomically.
func (c *Client) ConfigSet(ctx context.Context, dhcp4 Dhcp4Config) error {
	args, err := json.Marshal(map[string]Dhcp4Config{"Dhcp4": dhcp4})
	if err != nil {
		return err
	}
	r, err := c.call(ctx, Command{
		Command:   "config-set",
		Service:   []string{"dhcp4"},
		Arguments: args,
	})
	if err != nil {
		return err
	}
	if r.Result != 0 {
		return fmt.Errorf("kea config-set failed: %s", r.Text)
	}
	return nil
}

// VersionGet is useful as a cheap liveness probe.
func (c *Client) VersionGet(ctx context.Context) (string, error) {
	r, err := c.call(ctx, Command{Command: "version-get", Service: []string{"dhcp4"}})
	if err != nil {
		return "", err
	}
	if r.Result != 0 {
		return "", fmt.Errorf("kea version-get: %s", r.Text)
	}
	return r.Text, nil
}
