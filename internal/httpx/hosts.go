package httpx

import (
	"errors"
	"log/slog"
	"net/http"
	"strconv"

	"netdb/internal/model"
	"netdb/internal/store"
)

type hostsListData struct {
	Title string
	Nav   string
	Hosts []model.Host
}

type hostDetailData struct {
	Title string
	Nav   string
	Host  model.Host
	NICs  []model.NICWithIPs
}

func (s *Server) listHosts(w http.ResponseWriter, r *http.Request) {
	hosts, err := s.store.ListHosts(r.Context())
	if err != nil {
		slog.Error("list hosts", "err", err)
		http.Error(w, "list hosts failed", http.StatusInternalServerError)
		return
	}
	s.renderPage(w, "hosts_list", hostsListData{Title: "hosts", Nav: "hosts", Hosts: hosts})
}

func (s *Server) createHost(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	name := r.FormValue("name")
	if name == "" {
		http.Error(w, "name required", http.StatusBadRequest)
		return
	}
	h, err := s.store.CreateHost(r.Context(), name, r.FormValue("description"), r.FormValue("tags"))
	if err != nil {
		slog.Error("create host", "err", err)
		http.Error(w, "create failed: "+err.Error(), http.StatusBadRequest)
		return
	}
	s.resync(r.Context())
	s.renderFragment(w, "host_row", h)
}

func (s *Server) hostDetail(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "bad id", http.StatusBadRequest)
		return
	}
	d, err := s.store.GetHostDetail(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		slog.Error("host detail", "err", err)
		http.Error(w, "host detail failed", http.StatusInternalServerError)
		return
	}
	s.renderPage(w, "host_detail", hostDetailData{
		Title: d.Host.Name, Nav: "hosts", Host: d.Host, NICs: d.NICs,
	})
}

func (s *Server) updateHost(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "bad id", http.StatusBadRequest)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	name := r.FormValue("name")
	if name == "" {
		http.Error(w, "name required", http.StatusBadRequest)
		return
	}
	dnsEnabled := r.FormValue("dns_enabled") == "on" || r.FormValue("dns_enabled") == "true"
	if err := s.store.UpdateHost(r.Context(), id, name,
		r.FormValue("description"), r.FormValue("tags"),
		r.FormValue("dns_name"), dnsEnabled); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			http.NotFound(w, r)
			return
		}
		slog.Warn("update host", "err", err)
		http.Error(w, "update failed: "+err.Error(), http.StatusBadRequest)
		return
	}
	s.resync(r.Context())
	// Redirect HTMX back to the host detail so edits show fresh.
	w.Header().Set("HX-Redirect", "/hosts/"+r.PathValue("id"))
	w.WriteHeader(http.StatusOK)
}

func (s *Server) deleteHost(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "bad id", http.StatusBadRequest)
		return
	}
	if err := s.store.DeleteHost(r.Context(), id); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			http.NotFound(w, r)
			return
		}
		slog.Error("delete host", "err", err)
		http.Error(w, "delete failed", http.StatusInternalServerError)
		return
	}
	s.resync(r.Context())
	respondOK(w)
}
