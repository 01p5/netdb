-- Per-subnet DNS server list (DHCP option 6, "domain-name-servers").
-- Comma-separated IPs, validated in application code. Empty = don't emit
-- the option at all, so behavior is unchanged for subnets created before
-- this migration.
ALTER TABLE subnets ADD COLUMN dns_servers TEXT NOT NULL DEFAULT '';
