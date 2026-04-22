package httpx

import (
	"errors"
	"log/slog"
	"net/http"
	"strconv"

	"netdb/internal/model"
	"netdb/internal/store"
)

type providersListData struct {
	Title     string
	Nav       string
	Providers []model.Provider
}

func (s *Server) listProviders(w http.ResponseWriter, r *http.Request) {
	providers, err := s.store.ListProviders(r.Context())
	if err != nil {
		slog.Error("list providers", "err", err)
		http.Error(w, "list failed", http.StatusInternalServerError)
		return
	}
	s.renderPage(w, "providers_list", providersListData{Title: "providers", Nav: "providers", Providers: providers})
}

func (s *Server) createProvider(w http.ResponseWriter, r *http.Request) {
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
	p, err := s.store.CreateProvider(r.Context(), name, kind, r.FormValue("config_json"), true)
	if err != nil {
		slog.Warn("create provider", "err", err)
		http.Error(w, "create failed: "+err.Error(), http.StatusBadRequest)
		return
	}
	s.renderFragment(w, "provider_row", p)
}

func (s *Server) deleteProvider(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "bad id", http.StatusBadRequest)
		return
	}
	if err := s.store.DeleteProvider(r.Context(), id); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			http.NotFound(w, r)
			return
		}
		slog.Error("delete provider", "err", err)
		http.Error(w, "delete failed", http.StatusInternalServerError)
		return
	}
	respondOK(w)
}
