// Package cloudflare adapts the Cloudflare DNS v4 API to dnsprov.Provider.
//
// Auth: single API token via Bearer header. Scope required: Zone:DNS:Edit
// on every zone you intend to manage. Account-level zone creation is out
// of scope — EnsureZone just verifies the zone already exists on the CF side.
package cloudflare

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"netdb/internal/dnsprov"
)

const defaultBaseURL = "https://api.cloudflare.com/client/v4"

type Config struct {
	BaseURL string // override for testing; empty = default
	Token   string // API token
}

type Client struct {
	name string
	cfg  Config
	http *http.Client

	mu        sync.Mutex
	zoneCache map[string]string // zone name -> CF zone id
}

func New(name string, cfg Config) *Client {
	if cfg.BaseURL == "" {
		cfg.BaseURL = defaultBaseURL
	}
	return &Client{
		name:      name,
		cfg:       cfg,
		http:      &http.Client{Timeout: 15 * time.Second},
		zoneCache: map[string]string{},
	}
}

func (c *Client) Name() string { return c.name }
func (c *Client) Kind() string { return "cloudflare" }

// apiError captures Cloudflare's standard {success, errors[]} envelope.
type apiError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type baseResp struct {
	Success    bool            `json:"success"`
	Errors     []apiError      `json:"errors"`
	Messages   []apiError      `json:"messages"`
	Result     json.RawMessage `json:"result"`
	ResultInfo json.RawMessage `json:"result_info"`
}

func firstError(errs []apiError) string {
	if len(errs) == 0 {
		return "unknown error"
	}
	return fmt.Sprintf("code %d: %s", errs[0].Code, errs[0].Message)
}

func (c *Client) do(ctx context.Context, method, path string, body any, out *baseResp) error {
	var reader io.Reader
	if body != nil {
		buf, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(buf)
	}
	u := strings.TrimRight(c.cfg.BaseURL, "/") + path
	req, err := http.NewRequestWithContext(ctx, method, u, reader)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.cfg.Token)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(res.Body)
	if err != nil {
		return err
	}
	if res.StatusCode >= 500 {
		return fmt.Errorf("cloudflare %s %s: http %d: %s", method, path, res.StatusCode, string(raw))
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("cloudflare %s %s: decode: %w (body=%s)", method, path, err, string(raw))
	}
	if !out.Success {
		return fmt.Errorf("cloudflare %s %s: %s", method, path, firstError(out.Errors))
	}
	return nil
}

func (c *Client) zoneID(ctx context.Context, zone string) (string, error) {
	c.mu.Lock()
	if id, ok := c.zoneCache[zone]; ok {
		c.mu.Unlock()
		return id, nil
	}
	c.mu.Unlock()

	var resp baseResp
	if err := c.do(ctx, http.MethodGet, "/zones?name="+zone, nil, &resp); err != nil {
		return "", err
	}
	var zones []struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	if err := json.Unmarshal(resp.Result, &zones); err != nil {
		return "", fmt.Errorf("decode zones list: %w", err)
	}
	if len(zones) == 0 {
		return "", fmt.Errorf("zone %q not found in cloudflare account — create it at dash.cloudflare.com first", zone)
	}
	c.mu.Lock()
	c.zoneCache[zone] = zones[0].ID
	c.mu.Unlock()
	return zones[0].ID, nil
}

// EnsureZone: Cloudflare zone creation is account-level and typically
// performed in the dashboard; this just verifies the zone is reachable
// with the token's scope.
func (c *Client) EnsureZone(ctx context.Context, zone string) error {
	_, err := c.zoneID(ctx, zone)
	return err
}

type cfRecord struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Type     string `json:"type"`
	Content  string `json:"content"`
	TTL      int    `json:"ttl"`
	Priority int    `json:"priority,omitempty"`
}

// managedTypes are the record types netdb will add/delete in a CF zone.
// Other types (SOA, NS, CAA, DMARC TXT, SPF TXT, etc.) are left alone so
// we don't wipe out infra the operator set up elsewhere.
var managedTypes = map[string]bool{
	"A": true, "AAAA": true, "CNAME": true, "PTR": true, "MX": true, "TXT": true,
}

func (c *Client) ListRecords(ctx context.Context, zone string) ([]dnsprov.Record, error) {
	zoneID, err := c.zoneID(ctx, zone)
	if err != nil {
		return nil, err
	}

	var out []dnsprov.Record
	for page := 1; ; page++ {
		var resp baseResp
		p := fmt.Sprintf("/zones/%s/dns_records?per_page=100&page=%d", zoneID, page)
		if err := c.do(ctx, http.MethodGet, p, nil, &resp); err != nil {
			return nil, err
		}
		var recs []cfRecord
		if err := json.Unmarshal(resp.Result, &recs); err != nil {
			return nil, fmt.Errorf("decode records: %w", err)
		}
		for _, r := range recs {
			if !managedTypes[r.Type] {
				continue
			}
			value := r.Content
			if r.Type == "MX" {
				value = fmt.Sprintf("%d %s", r.Priority, strings.TrimSuffix(r.Content, "."))
			}
			out = append(out, dnsprov.Record{
				Name:       strings.TrimSuffix(r.Name, "."),
				Type:       r.Type,
				Value:      value,
				TTL:        r.TTL,
				ProviderID: r.ID,
			})
		}

		var info struct {
			TotalPages int `json:"total_pages"`
		}
		// result_info may be empty on some responses; ignore parse errors.
		_ = json.Unmarshal(resp.ResultInfo, &info)
		if page >= info.TotalPages || info.TotalPages == 0 {
			break
		}
	}
	return out, nil
}

func (c *Client) AddRecord(ctx context.Context, zone string, rec dnsprov.Record) error {
	zoneID, err := c.zoneID(ctx, zone)
	if err != nil {
		return err
	}
	body := map[string]any{
		"type":    rec.Type,
		"name":    rec.Name,
		"content": rec.Value,
		"ttl":     rec.TTL,
		"proxied": false,
	}
	if rec.Type == "MX" {
		parts := strings.SplitN(rec.Value, " ", 2)
		if len(parts) != 2 {
			return fmt.Errorf("MX value must be %q form, got %q", "<preference> <exchange>", rec.Value)
		}
		prio, err := strconv.Atoi(parts[0])
		if err != nil {
			return fmt.Errorf("MX preference not an integer: %w", err)
		}
		body["priority"] = prio
		body["content"] = parts[1]
	}
	var resp baseResp
	return c.do(ctx, http.MethodPost, "/zones/"+zoneID+"/dns_records", body, &resp)
}

func (c *Client) DeleteRecord(ctx context.Context, zone string, rec dnsprov.Record) error {
	if rec.ProviderID == "" {
		return errors.New("cloudflare: cannot delete record without ProviderID (pass the record returned by ListRecords)")
	}
	zoneID, err := c.zoneID(ctx, zone)
	if err != nil {
		return err
	}
	var resp baseResp
	return c.do(ctx, http.MethodDelete, "/zones/"+zoneID+"/dns_records/"+rec.ProviderID, nil, &resp)
}
