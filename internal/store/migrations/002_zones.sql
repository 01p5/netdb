-- Zones: first-class DNS zones. Forward (lan.example.com) or reverse
-- (10.168.192.in-addr.arpa). Decoupled from providers so one zone can be
-- pushed to multiple providers, and vice versa.
CREATE TABLE zones (
    id          INTEGER PRIMARY KEY,
    name        TEXT NOT NULL UNIQUE,
    kind        TEXT NOT NULL CHECK (kind IN ('forward','reverse')),
    default_ttl INTEGER NOT NULL DEFAULT 300,
    description TEXT NOT NULL DEFAULT '',
    created_at  TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at  TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- N:N between zones and providers. A zone can go to Cloudflare AND Technitium.
CREATE TABLE zone_providers (
    zone_id     INTEGER NOT NULL REFERENCES zones(id)         ON DELETE CASCADE,
    provider_id INTEGER NOT NULL REFERENCES dns_providers(id) ON DELETE CASCADE,
    PRIMARY KEY (zone_id, provider_id)
);

CREATE INDEX ix_zone_providers_provider ON zone_providers(provider_id);

-- Subnet <-> zone links, tagged with role. One subnet may have one forward
-- zone AND one reverse zone (or multiples of either, nothing in the schema
-- forbids it; the synthesizer just emits records to all of them).
CREATE TABLE subnet_zones (
    subnet_id INTEGER NOT NULL REFERENCES subnets(id) ON DELETE CASCADE,
    zone_id   INTEGER NOT NULL REFERENCES zones(id)   ON DELETE CASCADE,
    role      TEXT NOT NULL CHECK (role IN ('forward','reverse')),
    PRIMARY KEY (subnet_id, zone_id, role)
);

CREATE INDEX ix_subnet_zones_zone ON subnet_zones(zone_id);

-- Attach auto/manual tagging + zone scoping to dns_records.
-- SQLite ALTER TABLE ADD COLUMN can't carry a CHECK constraint, so 'source'
-- is validated in application code.
ALTER TABLE dns_records ADD COLUMN zone_id INTEGER REFERENCES zones(id) ON DELETE CASCADE;
ALTER TABLE dns_records ADD COLUMN source  TEXT    NOT NULL DEFAULT 'manual';

CREATE INDEX ix_dns_records_zone   ON dns_records(zone_id);
CREATE INDEX ix_dns_records_source ON dns_records(source);

-- Per-host DNS overrides. dns_name overrides the auto-generated label
-- (defaults to hosts.name when empty). dns_enabled=0 opts a host out of
-- auto-record synthesis entirely.
ALTER TABLE hosts ADD COLUMN dns_name    TEXT    NOT NULL DEFAULT '';
ALTER TABLE hosts ADD COLUMN dns_enabled INTEGER NOT NULL DEFAULT 1;

-- Providers are no longer tied to a single zone; the zone_providers N:N
-- replaces that. Drop the legacy column from the initial schema.
ALTER TABLE dns_providers DROP COLUMN zone;
