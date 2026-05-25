package config

import (
	"testing"
	"time"
)

func TestLoadDefaults(t *testing.T) {
	// Wipe every env var Load reads so the test sees only defaults,
	// regardless of how the test process was launched.
	for _, k := range []string{
		"NETDB_ADDR", "NETDB_DB",
		"NETDB_RECONCILE_INTERVAL", "NETDB_KEA_URL",
		"NETDB_KEA_SYNC_INTERVAL", "NETDB_KEA_LEASE_POLL_INTERVAL",
		"NETDB_TECHNITIUM_URL", "NETDB_TECHNITIUM_TOKEN",
		"NETDB_TECHNITIUM_USER", "NETDB_TECHNITIUM_PASSWORD",
		"NETDB_TECHNITIUM_INSECURE",
		"NETDB_CLOUDFLARE_TOKEN", "NETDB_CLOUDFLARE_BASE_URL",
		"NETDB_USER", "NETDB_PASSWORD",
	} {
		t.Setenv(k, "")
	}

	c, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.Addr != ":8080" {
		t.Errorf("Addr default: got %q", c.Addr)
	}
	if c.DBPath != "netdb.sqlite" {
		t.Errorf("DBPath default: got %q", c.DBPath)
	}
	if c.ReconcileInterval != 60*time.Second {
		t.Errorf("ReconcileInterval default: got %v", c.ReconcileInterval)
	}
	if c.KeaSyncInterval != 60*time.Second {
		t.Errorf("KeaSyncInterval default: got %v", c.KeaSyncInterval)
	}
	if c.KeaLeasePollEvery != 30*time.Second {
		t.Errorf("KeaLeasePollEvery default: got %v", c.KeaLeasePollEvery)
	}
	if c.TechnitiumInsecure {
		t.Error("TechnitiumInsecure default should be false")
	}
}

func TestLoadOverrides(t *testing.T) {
	t.Setenv("NETDB_ADDR", ":9999")
	t.Setenv("NETDB_DB", "/tmp/foo.db")
	t.Setenv("NETDB_RECONCILE_INTERVAL", "5s")
	t.Setenv("NETDB_KEA_URL", "http://kea:8000")
	t.Setenv("NETDB_KEA_SYNC_INTERVAL", "15s")
	t.Setenv("NETDB_KEA_LEASE_POLL_INTERVAL", "7s")
	t.Setenv("NETDB_TECHNITIUM_URL", "http://t:5380")
	t.Setenv("NETDB_TECHNITIUM_TOKEN", "tok")
	t.Setenv("NETDB_TECHNITIUM_USER", "u")
	t.Setenv("NETDB_TECHNITIUM_PASSWORD", "p")
	t.Setenv("NETDB_TECHNITIUM_INSECURE", "true")
	t.Setenv("NETDB_CLOUDFLARE_TOKEN", "cftok")
	t.Setenv("NETDB_CLOUDFLARE_BASE_URL", "http://cf.local")
	t.Setenv("NETDB_USER", "alice")
	t.Setenv("NETDB_PASSWORD", "s3cret")

	c, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	cases := []struct {
		name string
		got  any
		want any
	}{
		{"Addr", c.Addr, ":9999"},
		{"DBPath", c.DBPath, "/tmp/foo.db"},
		{"ReconcileInterval", c.ReconcileInterval, 5 * time.Second},
		{"KeaURL", c.KeaURL, "http://kea:8000"},
		{"KeaSyncInterval", c.KeaSyncInterval, 15 * time.Second},
		{"KeaLeasePollEvery", c.KeaLeasePollEvery, 7 * time.Second},
		{"TechnitiumURL", c.TechnitiumURL, "http://t:5380"},
		{"TechnitiumToken", c.TechnitiumToken, "tok"},
		{"TechnitiumUser", c.TechnitiumUser, "u"},
		{"TechnitiumPassword", c.TechnitiumPassword, "p"},
		{"TechnitiumInsecure", c.TechnitiumInsecure, true},
		{"CloudflareToken", c.CloudflareToken, "cftok"},
		{"CloudflareBaseURL", c.CloudflareBaseURL, "http://cf.local"},
		{"AuthUser", c.AuthUser, "alice"},
		{"AuthPassword", c.AuthPassword, "s3cret"},
	}
	for _, tc := range cases {
		if tc.got != tc.want {
			t.Errorf("%s: got %v want %v", tc.name, tc.got, tc.want)
		}
	}
}

func TestLoadBadValuesFallBackToDefaults(t *testing.T) {
	t.Setenv("NETDB_RECONCILE_INTERVAL", "not-a-duration")
	t.Setenv("NETDB_TECHNITIUM_INSECURE", "not-a-bool")
	c, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.ReconcileInterval != 60*time.Second {
		t.Errorf("bad duration should fall back to default; got %v", c.ReconcileInterval)
	}
	if c.TechnitiumInsecure {
		t.Error("bad bool should fall back to default (false)")
	}
}
