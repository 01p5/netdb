// Package technitium adapts the Technitium DNS Server HTTP API to the
// dnsprov.Provider interface.
//
// Auth: prefer a pre-generated API token (env NETDB_TECHNITIUM_TOKEN). If
// absent but username+password are available, bootstrap a permanent token
// on first use via POST /api/user/createToken and cache it in memory.
package technitium

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"netdb/internal/dnsprov"
)

type Config struct {
	BaseURL            string // http://dns-server:5380
	Token              string // if set, used directly
	Username           string // else used with Password to bootstrap a token
	Password           string
	TokenName          string // name for the bootstrapped token (default "netdb-sync")
	InsecureSkipVerify bool   // for self-signed HTTPS
}

type Client struct {
	name string
	cfg  Config
	http *http.Client

	mu    sync.Mutex
	token string // cached; bootstrapped on first use if Config.Token is empty
}

// New returns a Client. It does not contact the server.
func New(name string, cfg Config) *Client {
	if cfg.TokenName == "" {
		cfg.TokenName = "netdb-sync"
	}
	tr := &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: cfg.InsecureSkipVerify}, //nolint:gosec // opt-in
	}
	return &Client{
		name:  name,
		cfg:   cfg,
		http:  &http.Client{Transport: tr, Timeout: 15 * time.Second},
		token: cfg.Token,
	}
}

func (c *Client) Name() string { return c.name }
func (c *Client) Kind() string { return "technitium" }

// getToken returns a usable API token. If one was pre-configured (env), uses
// that. Otherwise, bootstraps one using username+password and caches it.
func (c *Client) getToken(ctx context.Context) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.token != "" {
		return c.token, nil
	}
	if c.cfg.Username == "" || c.cfg.Password == "" {
		return "", errors.New("no technitium token and no username/password to bootstrap one")
	}
	v := url.Values{}
	v.Set("user", c.cfg.Username)
	v.Set("pass", c.cfg.Password)
	v.Set("tokenName", c.cfg.TokenName)
	var resp struct {
		Status       string `json:"status"`
		ErrorMessage string `json:"errorMessage"`
		Token        string `json:"token"`
	}
	if err := c.call(ctx, "/api/user/createToken", v, &resp); err != nil {
		return "", fmt.Errorf("bootstrap token: %w", err)
	}
	if resp.Status != "ok" || resp.Token == "" {
		return "", fmt.Errorf("bootstrap token failed: %s", resp.ErrorMessage)
	}
	c.token = resp.Token
	return c.token, nil
}

// call performs a POST to path with the given form params (plus token if
// already obtained) and decodes the JSON response into out.
// We use POST for everything since that's what the Technitium docs show,
// even for read-only calls.
func (c *Client) call(ctx context.Context, path string, params url.Values, out any) error {
	u := strings.TrimRight(c.cfg.BaseURL, "/") + path
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, strings.NewReader(params.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	res, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	body, err := io.ReadAll(res.Body)
	if err != nil {
		return err
	}
	if res.StatusCode >= 500 {
		return fmt.Errorf("technitium %s: http %d: %s", path, res.StatusCode, string(body))
	}
	if out != nil {
		if err := json.Unmarshal(body, out); err != nil {
			return fmt.Errorf("technitium %s: decode: %w (body=%s)", path, err, string(body))
		}
	}
	return nil
}

// callAuth adds the token to params and calls.
func (c *Client) callAuth(ctx context.Context, path string, params url.Values, out any) error {
	token, err := c.getToken(ctx)
	if err != nil {
		return err
	}
	if params == nil {
		params = url.Values{}
	}
	params.Set("token", token)
	return c.call(ctx, path, params, out)
}

type baseResp struct {
	Status       string          `json:"status"`
	ErrorMessage string          `json:"errorMessage"`
	Response     json.RawMessage `json:"response"`
}

// EnsureZone creates zone as a Primary if it doesn't already exist.
// Technitium returns an error if the zone already exists; we treat that as OK.
func (c *Client) EnsureZone(ctx context.Context, zone string) error {
	v := url.Values{}
	v.Set("zone", zone)
	v.Set("type", "Primary")
	var r baseResp
	if err := c.callAuth(ctx, "/api/zones/create", v, &r); err != nil {
		return err
	}
	if r.Status == "ok" {
		return nil
	}
	// Already-exists is expected and not a failure.
	if strings.Contains(strings.ToLower(r.ErrorMessage), "already exists") {
		return nil
	}
	return fmt.Errorf("create zone %s: %s", zone, r.ErrorMessage)
}

// rawRecord mirrors the shape Technitium returns from /api/zones/records/get.
// rData is type-tagged — we decode the whole thing as a map and pull the
// right key per record type.
type rawRecord struct {
	Name     string                 `json:"name"`
	Type     string                 `json:"type"`
	TTL      int                    `json:"ttl"`
	Disabled bool                   `json:"disabled"`
	RData    map[string]interface{} `json:"rData"`
}

type recordsResp struct {
	Domain  string      `json:"domain"`
	Zone    interface{} `json:"zone"` // sometimes a string, sometimes an object
	Records []rawRecord `json:"records"`
}

func (c *Client) ListRecords(ctx context.Context, zone string) ([]dnsprov.Record, error) {
	v := url.Values{}
	v.Set("domain", zone)
	v.Set("zone", zone)
	v.Set("listZone", "true")
	var r baseResp
	if err := c.callAuth(ctx, "/api/zones/records/get", v, &r); err != nil {
		return nil, err
	}
	if r.Status != "ok" {
		return nil, fmt.Errorf("list records in %s: %s", zone, r.ErrorMessage)
	}
	var payload recordsResp
	if err := json.Unmarshal(r.Response, &payload); err != nil {
		return nil, fmt.Errorf("decode list response: %w", err)
	}
	out := make([]dnsprov.Record, 0, len(payload.Records))
	for _, raw := range payload.Records {
		val, ok := rdataToValue(raw.Type, raw.RData)
		if !ok {
			continue // skip SOA / NS / unknown types
		}
		out = append(out, dnsprov.Record{
			Name: strings.TrimSuffix(raw.Name, "."),
			Type: raw.Type,
			Value: val,
			TTL:  raw.TTL,
		})
	}
	return out, nil
}

func rdataToValue(rtype string, rdata map[string]interface{}) (string, bool) {
	s := func(k string) string {
		if v, ok := rdata[k].(string); ok {
			return v
		}
		return ""
	}
	switch rtype {
	case "A", "AAAA":
		return s("ipAddress"), true
	case "CNAME":
		return strings.TrimSuffix(s("cname"), "."), true
	case "PTR":
		return strings.TrimSuffix(s("ptrName"), "."), true
	case "TXT":
		return s("text"), true
	case "MX":
		pref, _ := rdata["preference"].(float64)
		return fmt.Sprintf("%d %s", int(pref), strings.TrimSuffix(s("exchange"), ".")), true
	default:
		return "", false
	}
}

func (c *Client) AddRecord(ctx context.Context, zone string, rec dnsprov.Record) error {
	v := url.Values{}
	v.Set("domain", rec.Name)
	v.Set("zone", zone)
	v.Set("type", rec.Type)
	v.Set("ttl", strconv.Itoa(rec.TTL))
	if err := setRdataParams(rec, v); err != nil {
		return err
	}
	var r baseResp
	if err := c.callAuth(ctx, "/api/zones/records/add", v, &r); err != nil {
		return err
	}
	if r.Status != "ok" {
		// Adding a duplicate is harmless for our purposes.
		if strings.Contains(strings.ToLower(r.ErrorMessage), "already exists") {
			return nil
		}
		return fmt.Errorf("add %s %s %s: %s", rec.Type, rec.Name, rec.Value, r.ErrorMessage)
	}
	return nil
}

func (c *Client) DeleteRecord(ctx context.Context, zone string, rec dnsprov.Record) error {
	v := url.Values{}
	v.Set("domain", rec.Name)
	v.Set("zone", zone)
	v.Set("type", rec.Type)
	if err := setRdataParams(rec, v); err != nil {
		return err
	}
	var r baseResp
	if err := c.callAuth(ctx, "/api/zones/records/delete", v, &r); err != nil {
		return err
	}
	if r.Status != "ok" {
		if strings.Contains(strings.ToLower(r.ErrorMessage), "does not exist") ||
			strings.Contains(strings.ToLower(r.ErrorMessage), "no such record") {
			return nil
		}
		return fmt.Errorf("delete %s %s %s: %s", rec.Type, rec.Name, rec.Value, r.ErrorMessage)
	}
	return nil
}

// setRdataParams translates our flat Record.Value into the per-type fields
// Technitium expects on the add/delete endpoints.
func setRdataParams(rec dnsprov.Record, v url.Values) error {
	switch rec.Type {
	case "A", "AAAA":
		v.Set("ipAddress", rec.Value)
	case "CNAME":
		v.Set("cname", rec.Value)
	case "PTR":
		v.Set("ptrName", rec.Value)
	case "TXT":
		v.Set("text", rec.Value)
	case "MX":
		// "10 mail.example.com"
		parts := strings.SplitN(rec.Value, " ", 2)
		if len(parts) != 2 {
			return fmt.Errorf("MX value must be %q form, got %q", "<preference> <exchange>", rec.Value)
		}
		v.Set("preference", parts[0])
		v.Set("exchange", parts[1])
	default:
		return fmt.Errorf("unsupported record type: %s", rec.Type)
	}
	return nil
}
