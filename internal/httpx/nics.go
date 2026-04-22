package httpx

import (
	"errors"
	"log/slog"
	"net/http"
	"strconv"

	"netdb/internal/model"
	"netdb/internal/store"
)

func (s *Server) createNIC(w http.ResponseWriter, r *http.Request) {
	hostID, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "bad host id", http.StatusBadRequest)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	mac := r.FormValue("mac")
	if mac == "" {
		http.Error(w, "mac required", http.StatusBadRequest)
		return
	}
	n, err := s.store.CreateNIC(r.Context(), hostID, mac, r.FormValue("label"))
	if err != nil {
		slog.Warn("create nic", "err", err)
		http.Error(w, "create failed: "+err.Error(), http.StatusBadRequest)
		return
	}
	s.resync(r.Context())
	s.renderFragment(w, "nic_row", model.NICWithIPs{NIC: *n})
}

func (s *Server) deleteNIC(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "bad id", http.StatusBadRequest)
		return
	}
	if err := s.store.DeleteNIC(r.Context(), id); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			http.NotFound(w, r)
			return
		}
		slog.Error("delete nic", "err", err)
		http.Error(w, "delete failed", http.StatusInternalServerError)
		return
	}
	s.resync(r.Context())
	respondOK(w)
}
