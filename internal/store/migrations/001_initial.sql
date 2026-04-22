-- Hosts: logical machines
CREATE TABLE hosts (
    id          INTEGER PRIMARY KEY,
    name        TEXT NOT NULL UNIQUE,
    description TEXT NOT NULL DEFAULT '',
    tags        TEXT NOT NULL DEFAULT '',
    created_at  TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at  TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- Subnets
CREATE TABLE subnets (
    id                INTEGER PRIMARY KEY,
    cidr              TEXT NOT NULL UNIQUE,
    vlan              INTEGER,
    gateway           TEXT NOT NULL DEFAULT '',
    description       TEXT NOT NULL DEFAULT '',
    dhcp_range_start  TEXT NOT NULL DEFAULT '',
    dhcp_range_end    TEXT NOT NULL DEFAULT '',
    dhcp_options_json TEXT NOT NULL DEFAULT '{}',
    created_at        TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at        TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- NICs: a NIC belongs to exactly one host (1:N)
CREATE TABLE nics (
    id         INTEGER PRIMARY KEY,
    host_id    INTEGER NOT NULL REFERENCES hosts(id) ON DELETE CASCADE,
    mac        TEXT NOT NULL UNIQUE,    -- normalized "aa:bb:cc:dd:ee:ff"
    label      TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX ix_nics_host ON nics(host_id);

-- IP assignments: each row is one IP on one NIC.
-- Same IP may legitimately appear on multiple NICs (VRRP/HA), so no UNIQUE on ip alone.
CREATE TABLE ip_assignments (
    id         INTEGER PRIMARY KEY,
    nic_id     INTEGER NOT NULL REFERENCES nics(id) ON DELETE CASCADE,
    subnet_id  INTEGER REFERENCES subnets(id) ON DELETE SET NULL,
    ip         TEXT NOT NULL,           -- canonical form, v4 or v6
    kind       TEXT NOT NULL CHECK (kind IN ('static','reserved','dynamic')),
    created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(nic_id, ip)
);

CREATE INDEX ix_ip_assignments_ip     ON ip_assignments(ip);
CREATE INDEX ix_ip_assignments_subnet ON ip_assignments(subnet_id);

-- DNS records (desired state)
CREATE TABLE dns_records (
    id         INTEGER PRIMARY KEY,
    fqdn       TEXT NOT NULL,
    type       TEXT NOT NULL CHECK (type IN ('A','AAAA','CNAME','TXT','MX','PTR','SRV')),
    target     TEXT NOT NULL DEFAULT '', -- literal target for CNAME/TXT/MX/SRV/PTR; empty for A/AAAA (use bindings)
    ttl        INTEGER NOT NULL DEFAULT 300,
    enabled    INTEGER NOT NULL DEFAULT 1,
    notes      TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(fqdn, type, target)
);

CREATE INDEX ix_dns_records_fqdn ON dns_records(fqdn);

-- DNS bindings: A/AAAA records resolve through IP assignments (N:N)
CREATE TABLE dns_bindings (
    dns_record_id    INTEGER NOT NULL REFERENCES dns_records(id) ON DELETE CASCADE,
    ip_assignment_id INTEGER NOT NULL REFERENCES ip_assignments(id) ON DELETE CASCADE,
    PRIMARY KEY (dns_record_id, ip_assignment_id)
);

CREATE INDEX ix_dns_bindings_ip ON dns_bindings(ip_assignment_id);

-- DNS providers
CREATE TABLE dns_providers (
    id          INTEGER PRIMARY KEY,
    name        TEXT NOT NULL UNIQUE,
    kind        TEXT NOT NULL CHECK (kind IN ('cloudflare','technitium')),
    zone        TEXT NOT NULL,
    config_json TEXT NOT NULL DEFAULT '{}',
    enabled     INTEGER NOT NULL DEFAULT 1,
    created_at  TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at  TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- Reconciler state, per (record, provider)
CREATE TABLE record_sync (
    dns_record_id  INTEGER NOT NULL REFERENCES dns_records(id) ON DELETE CASCADE,
    provider_id    INTEGER NOT NULL REFERENCES dns_providers(id) ON DELETE CASCADE,
    remote_id      TEXT NOT NULL DEFAULT '',
    last_hash      TEXT NOT NULL DEFAULT '',
    state          TEXT NOT NULL DEFAULT 'pending'
                   CHECK (state IN ('pending','synced','error','deleting')),
    last_error     TEXT NOT NULL DEFAULT '',
    last_synced_at TEXT,
    PRIMARY KEY (dns_record_id, provider_id)
);

-- Audit log
CREATE TABLE audit_log (
    id        INTEGER PRIMARY KEY,
    at        TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
    actor     TEXT NOT NULL DEFAULT 'system',
    action    TEXT NOT NULL,
    entity    TEXT NOT NULL,
    entity_id INTEGER,
    summary   TEXT NOT NULL DEFAULT ''
);

CREATE INDEX ix_audit_log_entity ON audit_log(entity, entity_id);
