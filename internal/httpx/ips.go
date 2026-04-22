package httpx

import (
	"errors"
	"log/slog"
	"net/http"
	"strconv"

	"netdb/internal/store"
)

func (s *Server) createIP(w http.ResponseWriter, r *http.Request) {
	nicID, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "bad nic id", http.StatusBadRequest)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	ip := r.FormValue("ip")
	kind := r.FormValue("kind")
	if ip == "" || kind == "" {
		http.Error(w, "ip and kind required", http.StatusBadRequest)
		return
	}
	a, err := s.store.CreateIPAssignment(r.Context(), nicID, nil, ip, kind)
	if err != nil {
		slog.Warn("create ip", "err", err)
		http.Error(w, "create failed: "+err.Error(), http.StatusBadRequest)
		return
	}
	s.resync(r.Context())
	s.renderFragment(w, "ip_li", a)
}

func (s *Server) deleteIP(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "bad id", http.StatusBadRequest)
		return
	}
	if err := s.store.DeleteIPAssignment(r.Context(), id); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			http.NotFound(w, r)
			return
		}
		slog.Error("delete ip", "err", err)
		http.Error(w, "delete failed", http.StatusInternalServerError)
		return
	}
	s.resync(r.Context())
	respondOK(w)
}
