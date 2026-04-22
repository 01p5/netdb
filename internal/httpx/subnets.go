package httpx

import (
	"errors"
	"log/slog"
	"net/http"
	"strconv"

	"netdb/internal/model"
	"netdb/internal/store"
)

// subnetRowData is the per-row template input: the subnet, its linked zones,
// and the full zone list for the "+ link" dropdown.
type subnetRowData struct {
	Subnet   model.Subnet
	Zones    []model.SubnetZoneLink
	AllZones []model.Zone
}

type subnetsListData struct {
	Title   string
	Nav     string
	Subnets []subnetRowData
}

func (s *Server) listSubnets(w http.ResponseWriter, r *http.Request) {
	subnets, err := s.store.ListSubnets(r.Context())
	if err != nil {
		slog.Error("list subnets", "err", err)
		http.Error(w, "list failed", http.StatusInternalServerError)
		return
	}
	zones, err := s.store.ListZones(r.Context())
	if err != nil {
		slog.Error("list zones", "err", err)
		http.Error(w, "list failed", http.StatusInternalServerError)
		return
	}
	rows := make([]subnetRowData, 0, len(subnets))
	for _, sb := range subnets {
		links, err := s.store.ListZonesForSubnet(r.Context(), sb.ID)
		if err != nil {
			slog.Error("list zones for subnet", "subnet_id", sb.ID, "err", err)
			http.Error(w, "list failed", http.StatusInternalServerError)
			return
		}
		rows = append(rows, subnetRowData{Subnet: sb, Zones: links, AllZones: zones})
	}
	s.renderPage(w, "subnets_list", subnetsListData{Title: "subnets", Nav: "subnets", Subnets: rows})
}

func (s *Server) createSubnet(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	sb := model.Subnet{
		CIDR:           r.FormValue("cidr"),
		Gateway:        r.FormValue("gateway"),
		Description:    r.FormValue("description"),
		DHCPRangeStart: r.FormValue("dhcp_range_start"),
		DHCPRangeEnd:   r.FormValue("dhcp_range_end"),
		DNSServers:     r.FormValue("dns_servers"),
	}
	if v := r.FormValue("vlan"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			http.Error(w, "vlan must be integer", http.StatusBadRequest)
			return
		}
		sb.VLAN = &n
	}
	if sb.CIDR == "" {
		http.Error(w, "cidr required", http.StatusBadRequest)
		return
	}
	created, err := s.store.CreateSubnet(r.Context(), sb)
	if err != nil {
		slog.Warn("create subnet", "err", err)
		http.Error(w, "create failed: "+err.Error(), http.StatusBadRequest)
		return
	}
	zones, err := s.store.ListZones(r.Context())
	if err != nil {
		slog.Error("list zones", "err", err)
		http.Error(w, "create failed", http.StatusInternalServerError)
		return
	}
	s.resync(r.Context())
	s.renderFragment(w, "subnet_row", subnetRowData{Subnet: *created, AllZones: zones})
}

func (s *Server) updateSubnet(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "bad id", http.StatusBadRequest)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	sb := model.Subnet{
		CIDR:           r.FormValue("cidr"),
		Gateway:        r.FormValue("gateway"),
		Description:    r.FormValue("description"),
		DHCPRangeStart: r.FormValue("dhcp_range_start"),
		DHCPRangeEnd:   r.FormValue("dhcp_range_end"),
		DNSServers:     r.FormValue("dns_servers"),
	}
	if v := r.FormValue("vlan"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			http.Error(w, "vlan must be integer", http.StatusBadRequest)
			return
		}
		sb.VLAN = &n
	}
	if err := s.store.UpdateSubnet(r.Context(), id, sb); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			http.NotFound(w, r)
			return
		}
		slog.Warn("update subnet", "err", err)
		http.Error(w, "update failed: "+err.Error(), http.StatusBadRequest)
		return
	}
	s.resync(r.Context())
	w.Header().Set("HX-Redirect", "/subnets")
	w.WriteHeader(http.StatusOK)
}

func (s *Server) linkSubnetZone(w http.ResponseWriter, r *http.Request) {
	subnetID, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "bad subnet id", http.StatusBadRequest)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	zoneID, err := strconv.ParseInt(r.FormValue("zone_id"), 10, 64)
	if err != nil {
		http.Error(w, "zone_id required", http.StatusBadRequest)
		return
	}
	role := r.FormValue("role")
	if err := s.store.LinkSubnetZone(r.Context(), subnetID, zoneID, role); err != nil {
		slog.Warn("link subnet zone", "err", err)
		http.Error(w, "link failed: "+err.Error(), http.StatusBadRequest)
		return
	}
	z, err := s.store.GetZone(r.Context(), zoneID)
	if err != nil {
		slog.Error("get zone", "err", err)
		http.Error(w, "link failed", http.StatusInternalServerError)
		return
	}
	s.resync(r.Context())
	s.renderFragment(w, "subnet_zone_pill", map[string]any{
		"SubnetID": subnetID,
		"Link":     model.SubnetZoneLink{Zone: *z, Role: role},
	})
}

func (s *Server) unlinkSubnetZone(w http.ResponseWriter, r *http.Request) {
	subnetID, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "bad subnet id", http.StatusBadRequest)
		return
	}
	zoneID, err := strconv.ParseInt(r.PathValue("zid"), 10, 64)
	if err != nil {
		http.Error(w, "bad zone id", http.StatusBadRequest)
		return
	}
	role := r.PathValue("role")
	if err := s.store.UnlinkSubnetZone(r.Context(), subnetID, zoneID, role); err != nil {
		slog.Error("unlink subnet zone", "err", err)
		http.Error(w, "unlink failed", http.StatusInternalServerError)
		return
	}
	s.resync(r.Context())
	respondOK(w)
}

func (s *Server) deleteSubnet(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "bad id", http.StatusBadRequest)
		return
	}
	if err := s.store.DeleteSubnet(r.Context(), id); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			http.NotFound(w, r)
			return
		}
		slog.Error("delete subnet", "err", err)
		http.Error(w, "delete failed", http.StatusInternalServerError)
		return
	}
	s.resync(r.Context())
	respondOK(w)
}
