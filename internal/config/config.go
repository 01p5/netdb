package config

import (
	"os"
	"strconv"
	"time"
)

type Config struct {
	Addr   string
	DBPath string

	ReconcileInterval time.Duration
	KeaURL             string // ctrl-agent base URL; empty = Kea sync disabled
	KeaSyncInterval    time.Duration
	KeaLeasePollEvery  time.Duration

	// Technitium provider — one instance per netdb process.
	TechnitiumURL      string
	TechnitiumToken    string
	TechnitiumUser     string
	TechnitiumPassword string
	TechnitiumInsecure bool

	// Cloudflare provider — one token per netdb process.
	CloudflareToken   string
	CloudflareBaseURL string // optional override for testing

	// Basic auth gate. Both empty → auth disabled (use only on trusted LAN).
	AuthUser     string
	AuthPassword string
}

func Load() (*Config, error) {
	c := &Config{
		Addr:               envOr("NETDB_ADDR", ":8080"),
		DBPath:             envOr("NETDB_DB", "netdb.sqlite"),
		ReconcileInterval:  envDuration("NETDB_RECONCILE_INTERVAL", 60*time.Second),
		KeaURL:             os.Getenv("NETDB_KEA_URL"),
		KeaSyncInterval:    envDuration("NETDB_KEA_SYNC_INTERVAL", 60*time.Second),
		KeaLeasePollEvery:  envDuration("NETDB_KEA_LEASE_POLL_INTERVAL", 30*time.Second),
		TechnitiumURL:      os.Getenv("NETDB_TECHNITIUM_URL"),
		TechnitiumToken:    os.Getenv("NETDB_TECHNITIUM_TOKEN"),
		TechnitiumUser:     os.Getenv("NETDB_TECHNITIUM_USER"),
		TechnitiumPassword: os.Getenv("NETDB_TECHNITIUM_PASSWORD"),
		TechnitiumInsecure: envBool("NETDB_TECHNITIUM_INSECURE", false),
		CloudflareToken:    os.Getenv("NETDB_CLOUDFLARE_TOKEN"),
		CloudflareBaseURL:  os.Getenv("NETDB_CLOUDFLARE_BASE_URL"),
		AuthUser:           os.Getenv("NETDB_USER"),
		AuthPassword:       os.Getenv("NETDB_PASSWORD"),
	}
	return c, nil
}

func envOr(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}

func envDuration(k string, d time.Duration) time.Duration {
	if v := os.Getenv(k); v != "" {
		if parsed, err := time.ParseDuration(v); err == nil {
			return parsed
		}
	}
	return d
}

func envBool(k string, d bool) bool {
	if v := os.Getenv(k); v != "" {
		if parsed, err := strconv.ParseBool(v); err == nil {
			return parsed
		}
	}
	return d
}
