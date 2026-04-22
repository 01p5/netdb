package httpx

import (
	"errors"
	"log/slog"
	"net/http"
	"strconv"

	"netdb/internal/model"
	"netdb/internal/store"
)

// zoneRowData carries everything a zone row template needs: the zone itself,
// its linked providers, and the full provider list for the "+ link" dropdown.
// Baked per-row so sub-templates don't need to traverse back to page scope.
type zoneRowData struct {
	Zone         model.Zone
	Providers    []model.Provider
	AllProviders []model.Provider
}

type zonesListData struct {
	Title string
	Nav   string
	Zones []zoneRowData
}

func (s *Server) listZones(w http.ResponseWriter, r *http.Request) {
	zones, err := s.store.ListZones(r.Context())
	if err != nil {
		slog.Error("list zones", "err", err)
		http.Error(w, "list zones failed", http.StatusInternalServerError)
		return
	}
	all, err := s.store.ListProviders(r.Context())
	if err != nil {
		slog.Error("list providers", "err", err)
		http.Error(w, "list zones failed", http.StatusInternalServerError)
		return
	}
	rows := make([]zoneRowData, 0, len(zones))
	for _, z := range zones {
		providers, err := s.store.ListProvidersForZone(r.Context(), z.ID)
		if err != nil {
			slog.Error("list providers for zone", "zone_id", z.ID, "err", err)
			http.Error(w, "list zones failed", http.StatusInternalServerError)
			return
		}
		rows = append(rows, zoneRowData{Zone: z, Providers: providers, AllProviders: all})
	}
	s.renderPage(w, "zones_list", zonesListData{Title: "zones", Nav: "zones", Zones: rows})
}

func (s *Server) createZone(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	name := r.FormValue("name")
	kind := r.FormValue("kind")
	if name == "" || kind == "" {
		http.Error(w, "name and kind required", http.StatusBadRequest)
		return
	}
	ttl := 300
	if v := r.FormValue("default_ttl"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			http.Error(w, "ttl must be integer", http.StatusBadRequest)
			return
		}
		ttl = n
	}
	z, err := s.store.CreateZone(r.Context(), name, kind, r.FormValue("description"), ttl)
	if err != nil {
		slog.Warn("create zone", "err", err)
		http.Error(w, "create failed: "+err.Error(), http.StatusBadRequest)
		return
	}
	all, err := s.store.ListProviders(r.Context())
	if err != nil {
		slog.Error("list providers", "err", err)
		http.Error(w, "create failed", http.StatusInternalServerError)
		return
	}
	s.resync(r.Context())
	s.renderFragment(w, "zone_row", zoneRowData{Zone: *z, AllProviders: all})
}

func (s *Server) deleteZone(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "bad id", http.StatusBadRequest)
		return
	}
	if err := s.store.DeleteZone(r.Context(), id); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			http.NotFound(w, r)
			return
		}
		slog.Error("delete zone", "err", err)
		http.Error(w, "delete failed", http.StatusInternalServerError)
		return
	}
	s.resync(r.Context())
	respondOK(w)
}

func (s *Server) linkZoneProvider(w http.ResponseWriter, r *http.Request) {
	zoneID, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "bad zone id", http.StatusBadRequest)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	providerID, err := strconv.ParseInt(r.FormValue("provider_id"), 10, 64)
	if err != nil {
		http.Error(w, "provider_id required", http.StatusBadRequest)
		return
	}
	if err := s.store.LinkZoneProvider(r.Context(), zoneID, providerID); err != nil {
		slog.Error("link zone provider", "err", err)
		http.Error(w, "link failed", http.StatusInternalServerError)
		return
	}
	p, err := s.store.GetProvider(r.Context(), providerID)
	if err != nil {
		slog.Error("get provider", "err", err)
		http.Error(w, "link failed", http.StatusInternalServerError)
		return
	}
	s.renderFragment(w, "zone_provider_pill", map[string]any{
		"ZoneID":   zoneID,
		"Provider": *p,
	})
}

func (s *Server) unlinkZoneProvider(w http.ResponseWriter, r *http.Request) {
	zoneID, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "bad zone id", http.StatusBadRequest)
		return
	}
	providerID, err := strconv.ParseInt(r.PathValue("pid"), 10, 64)
	if err != nil {
		http.Error(w, "bad provider id", http.StatusBadRequest)
		return
	}
	if err := s.store.UnlinkZoneProvider(r.Context(), zoneID, providerID); err != nil {
		slog.Error("unlink zone provider", "err", err)
		http.Error(w, "unlink failed", http.StatusInternalServerError)
		return
	}
	respondOK(w)
}
