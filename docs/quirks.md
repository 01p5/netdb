# netdb quirks & gotchas

The sharp edges you'll actually run into. Roughly ordered by how likely they
are to bite first. Each entry: the quirk, the symptom, and the workaround
(or explicit "by design, live with it").

## Operational

### 1. Kea returns its container-internal IP as DHCP `server-id`

**Symptom.** A DHCP ACK carries `server=172.18.0.2` (or whatever Kea's
docker-network IP is) instead of the host IP. You see this in the probe
output: `server=172.18.0.2`.

**Impact.** Fine for relay'd clients — they talk to the relay, never to the
server-id. A *direct* client that takes that IP and unicasts a renew or
release to `172.18.0.2` can't reach it from outside the docker network, so
renews fail silently and the lease lapses.

**Fix path.** Set Kea's `"server-hostname"` or an explicit server-id override
per subnet in the generated config. We don't expose that in netdb yet —
add a column on `subnets` and emit `option-data: [{ name: "dhcp-server-identifier", data: "<host-ip>" }]`.

### 2. `KEA_BIND_ADDR=<host-ip>` means Kea won't see broadcast DHCP

**Symptom.** A real client on the same L2 plugs in, sends broadcast DISCOVER,
never gets an OFFER from netdb's Kea.

**By design.** Binding to a specific IP keeps us from answering broadcasts
and fighting whatever DHCP is already on that L2. The intended production
path is **a DHCP relay** (OpenWRT, `isc-dhcp-relay`, Kea's relay mode)
unicasting to `<host-ip>:67`.

**If you want broadcast-on-L2 behavior** (e.g., dedicated test network with
no other DHCP), set `KEA_BIND_ADDR=0.0.0.0` (the default). Don't do this on
a shared network.

### 3. `systemd-resolved` can come back after `apt upgrade`

**Symptom.** Technitium fails to start; port 53 already held.

**Why.** Upgrading the systemd package sometimes flips the resolved unit
back to `enabled`.

**Fix.** `sudo systemctl mask systemd-resolved` (not just `disable`).
Masking survives package upgrades.

### 4. `data/` dirs are root-owned

**Symptom.** `sqlite3 data/netdb/netdb.sqlite ...` from the host prompts for
sudo or errors.

**Why.** Docker bind-mounts take the container-process UID, which is root
inside each container.

**Workaround.** Either use `sudo sqlite3 ...` or run a one-off container:

```
docker run --rm -v "$PWD/data/netdb:/data" keinos/sqlite3 sqlite3 /data/netdb.sqlite
```

## Source-of-truth surprises

### 5. Reconciler deletes orphan records on providers

**Symptom.** You tweak an A record directly in Technitium's admin UI (or
Cloudflare's dashboard). Within one reconcile cycle it's restored to what
netdb says, or deleted if netdb doesn't know about it.

**By design.** netdb is the declared source of truth for record types it
manages (A, AAAA, CNAME, PTR, MX, TXT) in zones it's linked to. Types it
doesn't manage — SOA, NS, CAA, SRV, arbitrary TXT for DKIM/DMARC — are
**not** touched.

**If you must edit at the provider**, either (a) create the record in netdb
as `source=manual` instead, or (b) unlink the zone from the provider
before editing there (the reconciler will stop touching it).

### 6. Conflict resolution is silent

**Symptom.** You add a manual A record for an FQDN that already had an
auto-synthesized A. The auto disappears from `/dns` without any indication
that it was suppressed.

**By design.** Manual wins on `(fqdn, type)` collision — we agreed on this
early. But there's no UI badge or log line yet saying "this auto was
suppressed by a manual at the same key."

**Workaround for now.** Remember that manual rows *replace* auto ones for
their fqdn+type. The synth output in `/dns` shows the final state after
suppression, not what would have been produced without the manual.

### 7. One Technitium + one Cloudflare per netdb process

**Symptom.** Adding a second provider row of kind `cloudflare` looks like
it works, but both use the same `NETDB_CLOUDFLARE_TOKEN`. Can't manage
two CF accounts from one netdb.

**By design for now.** The env-var-based config is a singleton per kind.
Multi-instance would need per-provider-row credential storage (secure
storage or external secret manager).

## Protocol gaps

### 8. No DHCP option 15 (`domain-name`, search suffix)

**Symptom.** `ping probe-host` doesn't resolve; needs the FQDN
`probe-host.lan.example.com`.

**Easy fix.** Same shape as `dns_servers`: one more TEXT column on `subnets`,
emit `option-data: [{ name: "domain-name", data: "<suffix>" }]`.

### 9. No PXE, no IPv6

**Symptom.** Diskless / network boot doesn't get `next-server` +
`boot-file-name`; DHCPv6 requests get no response.

**By design (for now).** `subnet6` and PXE boot options aren't generated.
The Kea image has the capability; we just don't emit config for them.

### 10. Pool short-form expansion is IPv4-only

**Symptom.** An IPv6 subnet with `dhcp_range_start=::100` fails to produce
a pool; silently dropped.

**Workaround.** Use fully-qualified IP addresses for IPv6 pools until the
expander understands them.

## Security

### 11. No TLS on `:8080`, no CSRF protection

**Symptom.** Basic auth creds travel in cleartext. A malicious site open in
your browser while you're logged into netdb can POST to netdb endpoints.

**Impact.** Fine on LAN / VPN / trusted network. **Do not expose `:8080`
directly to the WAN.**

**Fix.** Put a reverse proxy (Caddy, Traefik, nginx) in front, terminating
TLS and ideally adding CSRF-token middleware. netdb itself has no
SameSite-aware cookies — we're using HTTP Basic, so CSRF is the real
concern for browser-initiated attacks.

### 12. Technitium token auto-bootstrap accretes tokens on restart

**Symptom.** Every netdb restart without `NETDB_TECHNITIUM_TOKEN` preset
creates another `netdb-sync` token in Technitium > Administration >
Sessions. Over weeks you accumulate dozens.

**Fix.** Grab one token from Technitium's UI, set `NETDB_TECHNITIUM_TOKEN`
in `.env`, unset `NETDB_TECHNITIUM_PASSWORD`. Now restarts reuse it.

## Availability

### 13. netdb is a hard dependency of Kea's DHCP service

**Symptom.** netdb down when Kea restarts → Kea comes up with an empty
`subnet4` (our seed config is intentionally minimal). No DHCP handed out
until netdb restarts and re-pushes.

**By design.** netdb is authoritative for Kea config. Mitigation would be
an optional `config-write` after `config-set` so Kea persists the last
good config to disk and survives a netdb outage — small addition.

### 14. Reconciler on a timer; mutations trigger, coalesced with depth=1

**Symptom.** Rapid sequence of UI edits doesn't reach Cloudflare/Technitium
one-per-edit; you get one run in-flight and one queued, at most.

**Why.** `trigger chan struct{}` has buffer 1; drops additional triggers
while a run is pending. Latency bound: one reconcile cycle, not zero.

**Fix.** Not needed unless you're bulk-importing. The next timer tick
converges anyway.

## Development / testing

### 15. `dhcp-probe` needs its `-giaddr` IP to be locally bindable

**Symptom.** `listen <giaddr>:67: bind: cannot assign requested address`.

**Fix.** Either pick a giaddr that's already on a local interface
(the dev machine's real IP works), or alias one in: `sudo ip addr add
<giaddr>/24 dev lo`. And stop anything else holding `:67` (dev machine's
own `kea-dhcp4` container if you're running the full stack locally):
`docker compose stop kea-dhcp4`.

### 16. Probe bind collision after Kea rescans interfaces

**Symptom.** First probe works; second probe fails with EADDRINUSE on
`<giaddr>:67` even though the giaddr IP is still there.

**Why.** Kea binds per-interface-IP on `config-set`. If you added the
giaddr IP to Kea's netns (as we did during the container-shared-netns
test), the next netdb sync causes Kea to bind it too, stealing the port.

**Fix.** Restart `kea-dhcp4` to drop the binding, re-add the IP, probe
before the next sync timer fires (60s default). Or run the probe from
a host where Kea doesn't live — the VM deployment pattern, where the
probe is on a different machine than Kea, doesn't have this problem.

### 17. Bringing down `docker compose` drops all published ports, including `:67`

**Symptom.** You `docker compose stop` on the dev box and a moment later
your host's port 67 is free. You `compose up` again and there it is again.

**Not a bug** — that's how `-p 67:67/udp` works. But if you run a probe
between those states, bindings on `:67` race with docker-proxy startup.
Wait a beat after `compose up` before probing.

## Data

### 18. Volume bind-mount doesn't do atomic backup across components

**Symptom.** Copying `data/` while the stack is running may give you an
inconsistent snapshot across netdb's sqlite, Kea's lease file, and
Technitium's zone files.

**Safer.** Use per-component backup commands:

```
# netdb (WAL-safe, runs live):
sudo sqlite3 data/netdb/netdb.sqlite ".backup /tmp/netdb-$(date +%F).sqlite"

# Kea leases: safe to copy; memfile persistence flushes on every write.
sudo cp data/kea-leases/dhcp4.leases /tmp/kea-$(date +%F).leases

# Technitium: use its admin UI "Backup" button, or cp while stopped.
```

### 19. If netdb's DB is wiped, Kea still holds leases for now-removed subnets

**Symptom.** `rm -rf data/netdb` → re-up → Kea's `lease4-get-all` still
shows old leases that no longer correspond to any configured subnet.

**Why.** Kea's lease DB is separate from netdb and lives in
`data/kea-leases/`. netdb pushing an empty config doesn't clear old
leases.

**Fix.** After wiping netdb, clear Kea leases too: `sudo rm data/kea-leases/*`
and `docker compose restart kea-dhcp4`.

---

If you hit a quirk not on this list, please add it. Errors of omission here
are worse than errors of padding.
