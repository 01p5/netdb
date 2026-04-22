package model

// Timestamps are kept as strings (SQLite ISO-8601 form) to sidestep the
// modernc.org/sqlite driver's time-parsing quirks. The UI only displays them.

type Host struct {
	ID          int64
	Name        string
	Description string
	Tags        string
	DNSName     string
	DNSEnabled  bool
	CreatedAt   string
	UpdatedAt   string
}

type Zone struct {
	ID          int64
	Name        string
	Kind        string // forward | reverse
	DefaultTTL  int
	Description string
	CreatedAt   string
	UpdatedAt   string
}

type Provider struct {
	ID         int64
	Name       string
	Kind       string // cloudflare | technitium
	ConfigJSON string
	Enabled    bool
	CreatedAt  string
	UpdatedAt  string
}

// SubnetZoneLink carries a subnet's zone assignment + the role (forward/reverse).
type SubnetZoneLink struct {
	Zone Zone
	Role string
}

type ZoneWithProviders struct {
	Zone      Zone
	Providers []Provider
}

type SubnetWithZones struct {
	Subnet Subnet
	Zones  []SubnetZoneLink
}

// DNSRecordView is a dns_record hydrated with its zone name and the IP
// addresses it currently binds to, for display on the /dns page.
type DNSRecordView struct {
	ID       int64
	ZoneName string
	FQDN     string
	Type     string
	Target   string
	TTL      int
	Source   string // manual | auto
	IPs      []string
}

type Subnet struct {
	ID              int64
	CIDR            string
	VLAN            *int
	Gateway         string
	Description     string
	DHCPRangeStart  string
	DHCPRangeEnd    string
	DHCPOptionsJSON string
	DNSServers      string // comma-separated IPs → DHCP option 6
	CreatedAt       string
	UpdatedAt       string
}

type NIC struct {
	ID        int64
	HostID    int64
	MAC       string
	Label     string
	CreatedAt string
	UpdatedAt string
}

// IPAssignment is a single (NIC, IP) pairing. Many:many between NICs and IPs
// runs through this table. SubnetID is informational — the ip itself is the
// canonical value.
type IPAssignment struct {
	ID        int64
	NICID     int64
	SubnetID  *int64
	IP        string
	Kind      string // static | reserved | dynamic
	CreatedAt string
	UpdatedAt string
}

// HostDetail is a Host hydrated with its NICs and each NIC's IP assignments,
// for the host detail page.
type HostDetail struct {
	Host Host
	NICs []NICWithIPs
}

type NICWithIPs struct {
	NIC NIC
	IPs []IPAssignment
}
