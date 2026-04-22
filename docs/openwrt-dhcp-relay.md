# Configuring OpenWRT as a DHCP Relay to netdb/Kea

netdb runs ISC Kea as the authoritative DHCP server. OpenWRT sits on each L2
segment and relays client broadcasts to Kea over L3. This doc covers only the
OpenWRT side; the last section lists the assumptions Kea makes so the two ends
match.

## Topology

```
   client ──broadcast──▶ OpenWRT (relay) ──unicast──▶ Kea @ netdb-host
                            │                             │
                         giaddr set                   subnet4 selected
                         to egress IP                 by giaddr / option 82
```

- OpenWRT keeps doing DNS (dnsmasq) and routing. Only its DHCPv4 server role is
  turned off.
- For IPv6, `odhcpd` has a native relay mode and is used instead of installing
  anything extra.
- Multi-VLAN: one relay instance can listen on several interfaces; Kea
  distinguishes subnets by the relay's giaddr (v4) or the link-address in the
  Relay-Forward message (v6).

## Prerequisites

- Kea reachable from the router at a stable IP. Example uses `192.0.2.10`.
- You know which OpenWRT interfaces should hand out leases (`br-lan`,
  `br-lan.20`, etc.). Example uses `br-lan`.
- Root SSH or LuCI access to the router.

## 1. Disable OpenWRT's built-in DHCPv4 server

dnsmasq on OpenWRT is the default DHCPv4 server. Disable its DHCP role per
interface while keeping DNS.

```sh
uci set dhcp.lan.ignore='1'          # stop dnsmasq from answering DHCPv4 on LAN
uci set dhcp.lan.dhcpv4='disabled'   # belt-and-suspenders on recent OpenWRT
uci commit dhcp
/etc/init.d/dnsmasq restart
```

Repeat the `uci set dhcp.<iface>.ignore='1'` line for every VLAN interface you
want Kea to own. Verify nothing's listening on UDP/67 anymore:

```sh
netstat -lnup | grep ':67 '          # should be empty
logread -e dnsmasq | tail            # should show DHCP disabled
```

## 2. Install and configure the IPv4 relay

```sh
opkg update
opkg install isc-dhcp-relay-ipv4
```

The package ships with `/etc/config/isc-dhcp-relay`. Typical config:

```
# /etc/config/isc-dhcp-relay
config dhcrelay 'relay'
    option enabled '1'
    list interface 'br-lan'          # add more lines for more VLANs
    list server    '192.0.2.10'      # Kea's address
    option options '-a'              # append relay agent info (option 82)
```

Apply:

```sh
/etc/init.d/isc-dhcp-relay enable
/etc/init.d/isc-dhcp-relay restart
ps w | grep dhcrelay
```

### Why option 82 matters

`-a` makes dhcrelay stamp each forwarded packet with the circuit-id (interface
the request came in on) and the relay's IP as remote-id. Kea can key off either
when it has to disambiguate subnets that share a giaddr, and netdb can log the
physical ingress port for audits. Leave it on unless you have a reason not to.

## 3. Relay IPv6 via odhcpd

odhcpd is already on the router; switch the per-interface role from `server` to
`relay`. Do this on every interface that has clients and on the upstream
interface it relays through.

```sh
# client-facing VLAN
uci set dhcp.lan.dhcpv6='relay'
uci set dhcp.lan.ra='relay'
uci set dhcp.lan.ndp='relay'

# upstream interface toward netdb/Kea
uci set dhcp.wan6=interface
uci set dhcp.wan6.interface='wan6'
uci set dhcp.wan6.dhcpv6='relay'
uci set dhcp.wan6.ra='relay'
uci set dhcp.wan6.ndp='relay'
uci set dhcp.wan6.master='1'         # marks this as the uplink for relay mode

uci commit dhcp
/etc/init.d/odhcpd restart
```

Exactly one interface must have `master='1'`; that's the one odhcpd will
forward Relay-Forward messages out of. If Kea is on the LAN side rather than
past a WAN, set `master='1'` on whichever interface faces it.

## 4. Firewall

By default the `lan` zone can reach the router itself and the WAN, but traffic
from the relay out to Kea may need a rule depending on where Kea lives.

- If Kea is on the LAN subnet: nothing to do.
- If Kea is across a zone (e.g. a management VLAN), add a traffic rule
  allowing UDP/67 from the router's egress interface to Kea's IP, and
  UDP/546-547 for v6 if you relay IPv6.

Example traffic rule:

```
uci add firewall rule
uci set firewall.@rule[-1].name='dhcp-relay-to-kea'
uci set firewall.@rule[-1].src='lan'
uci set firewall.@rule[-1].dest='mgmt'
uci set firewall.@rule[-1].dest_ip='192.0.2.10'
uci set firewall.@rule[-1].proto='udp'
uci set firewall.@rule[-1].dest_port='67'
uci set firewall.@rule[-1].target='ACCEPT'
uci commit firewall
/etc/init.d/firewall reload
```

## 5. Multiple VLANs

For each extra VLAN:

1. `uci set dhcp.<iface>.ignore='1'` to silence dnsmasq.
2. Add a `list interface '<iface>'` line to `isc-dhcp-relay`.
3. For v6, set `dhcpv6/ra/ndp` to `relay` on that iface.

Kea uses the giaddr (v4) or link-address (v6) to pick the right `subnet4` /
`subnet6` entry, so the addressing on each VLAN interface on OpenWRT must match
what Kea expects.

## 6. Verification

On OpenWRT, watch the relay handle a request:

```sh
logread -f | grep -Ei 'dhcrelay|odhcpd'
# on a client, run: udhcpc -i eth0 -n
```

You should see a DISCOVER forwarded, an OFFER coming back from `192.0.2.10`,
and matching REQUEST/ACK lines. If you see DISCOVERs but nothing returning,
the problem is on the Kea/firewall side, not the relay.

Sanity checks:

```sh
tcpdump -ni br-lan port 67 or port 68          # client-side
tcpdump -ni wan   host 192.0.2.10 and port 67  # server-side
```

## 7. Rolling back

If something goes wrong, the safe restore is:

```sh
uci delete dhcp.lan.ignore
uci delete dhcp.lan.dhcpv4
uci set dhcp.lan.dhcpv6='server'
uci set dhcp.lan.ra='server'
uci commit dhcp
/etc/init.d/dnsmasq restart
/etc/init.d/odhcpd restart
/etc/init.d/isc-dhcp-relay stop
/etc/init.d/isc-dhcp-relay disable
```

That puts OpenWRT back to being the authoritative DHCP server for LAN.

## What Kea expects on the other side

For reference so OpenWRT's config lines up:

- Kea `dhcp4` listens on UDP/67 on the interface facing the relay. It must
  have a `subnet4` whose `subnet` matches the CIDR of the router's LAN
  interface (giaddr-based selection).
- If you use shared-networks or overlapping subnets, Kea picks via
  `client-class` or option 82 sub-options, so the `-a` flag on dhcrelay is
  required.
- Kea `dhcp6` uses the `interface-id` or `link-address` from the odhcpd
  Relay-Forward. Each VLAN's OpenWRT IPv6 prefix needs a matching `subnet6`.
- Lease events reach netdb via Kea's control channel (`kea-ctrl-agent` or the
  `lease_cmds` hook). That's a netdb-side concern; the relay doesn't need to
  know about it.
