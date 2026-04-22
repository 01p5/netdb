package store

import "context"

// Reservation is a flat DHCP reservation row: MAC → IP with the owning
// host's name as the DHCP hostname option. Used by the Kea config builder;
// not exposed in the main model because it's a join output, not an entity.
type Reservation struct {
	MAC      string
	IP       string
	HostName string // mirrored into DHCP option 12
	NICLabel string
}

// ListReservations returns every reserved ip_assignment joined with its
// NIC's MAC and the host's name. Dynamic and plain-static IPs are
// deliberately excluded — only kind='reserved' feeds Kea reservations.
func (s *Store) ListReservations(ctx context.Context) ([]Reservation, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT n.mac, ip.ip, h.name, n.label
		FROM ip_assignments ip
		JOIN nics  n ON n.id = ip.nic_id
		JOIN hosts h ON h.id = n.host_id
		WHERE ip.kind = 'reserved'
		ORDER BY ip.ip`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Reservation
	for rows.Next() {
		var r Reservation
		if err := rows.Scan(&r.MAC, &r.IP, &r.HostName, &r.NICLabel); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
