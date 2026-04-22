package httpx

import (
	"context"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"netdb/internal/model"
	"netdb/internal/reconcile"
	"netdb/internal/store"
	"netdb/internal/synth"
)

type dnsListData struct {
	Title   string
	Nav     string
	Records []model.DNSRecordView
	Zones   []model.Zone
	Status  reconcile.Status
	Ago     string // pretty-printed "12s ago" / "never"
}

func (s *Server) listDNS(w http.ResponseWriter, r *http.Request) {
	records, err := s.store.ListDNSRecords(r.Context())
	if err != nil {
		slog.Error("list dns", "err", err)
		http.Error(w, "list failed", http.StatusInternalServerError)
		return
	}
	zones, err := s.store.ListZones(r.Context())
	if err != nil {
		slog.Error("list zones", "err", err)
		http.Error(w, "list failed", http.StatusInternalServerError)
		return
	}
	st := s.rec.Status()
	s.renderPage(w, "dns_list", dnsListData{
		Title:   "dns",
		Nav:     "dns",
		Records: records,
		Zones:   zones,
		Status:  st,
		Ago:     prettyAgo(st.LastRunAt),
	})
}

func (s *Server) createManualDNS(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	zone := r.FormValue("zone")
	fqdn := r.FormValue("fqdn")
	rtype := r.FormValue("type")
	target := r.FormValue("target")
	if zone == "" || fqdn == "" || rtype == "" || target == "" {
		http.Error(w, "zone, fqdn, type, target are required", http.StatusBadRequest)
		return
	}
	ttl := 0
	if v := r.FormValue("ttl"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			http.Error(w, "ttl must be integer", http.StatusBadRequest)
			return
		}
		ttl = n
	}
	if _, err := s.store.CreateManualDNSRecord(r.Context(), zone, fqdn, rtype, target, ttl); err != nil {
		slog.Warn("create manual dns", "err", err)
		http.Error(w, "create failed: "+err.Error(), http.StatusBadRequest)
		return
	}
	s.resync(r.Context())
	w.Header().Set("HX-Redirect", "/dns")
	w.WriteHeader(http.StatusOK)
}

func (s *Server) deleteDNSRecord(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "bad id", http.StatusBadRequest)
		return
	}
	if err := s.store.DeleteDNSRecord(r.Context(), id); err != nil {
		if err == store.ErrNotFound {
			http.NotFound(w, r)
			return
		}
		slog.Error("delete dns", "err", err)
		http.Error(w, "delete failed", http.StatusInternalServerError)
		return
	}
	s.resync(r.Context())
	respondOK(w)
}

// syncNow runs synth to refresh auto records, then triggers a reconcile pass.
// Returns HX-Redirect so HTMX navigates the user back to /dns where they
// can see the updated status from the next reconcile cycle.
func (s *Server) syncNow(w http.ResponseWriter, r *http.Request) {
	if err := synth.Run(r.Context(), s.store); err != nil {
		slog.Error("sync-now: synth", "err", err)
		http.Error(w, "synth failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
	s.rec.Trigger()
	s.kea.Trigger()
	w.Header().Set("HX-Redirect", "/dns")
	w.WriteHeader(http.StatusOK)
}

// resync is called from mutation handlers: refresh auto records and ask the
// reconciler + Kea syncer to re-push. All non-fatal; we log and move on.
func (s *Server) resync(ctx context.Context) {
	if err := synth.Run(ctx, s.store); err != nil {
		slog.Error("synth", "err", err)
	}
	if s.rec != nil {
		s.rec.Trigger()
	}
	if s.kea != nil {
		s.kea.Trigger()
	}
}

func prettyAgo(t time.Time) string {
	if t.IsZero() {
		return "never"
	}
	d := time.Since(t).Round(time.Second)
	return d.String() + " ago"
}
