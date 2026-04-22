package httpx

import (
	"log/slog"
	"net/http"

	"netdb/internal/kea"
)

type dhcpPageData struct {
	Title         string
	Nav           string
	Status        kea.Status
	Ago           string
	LeaseStatus   kea.LeaseStatus
	LeaseAgo      string
}

func (s *Server) dhcpPage(w http.ResponseWriter, r *http.Request) {
	st := s.kea.Status()
	ls := s.leasePoller.Status()
	s.renderPage(w, "dhcp_page", dhcpPageData{
		Title:       "dhcp",
		Nav:         "dhcp",
		Status:      st,
		Ago:         prettyAgo(st.LastRunAt),
		LeaseStatus: ls,
		LeaseAgo:    prettyAgo(ls.LastRunAt),
	})
}

// pollLeases runs the lease poller out of schedule.
func (s *Server) pollLeases(w http.ResponseWriter, r *http.Request) {
	s.leasePoller.Trigger()
	w.Header().Set("HX-Redirect", "/dhcp")
	w.WriteHeader(http.StatusOK)
}

// syncKea runs the Kea config push out of schedule.
func (s *Server) syncKea(w http.ResponseWriter, r *http.Request) {
	s.kea.Trigger()
	w.Header().Set("HX-Redirect", "/dhcp")
	w.WriteHeader(http.StatusOK)
	slog.Info("kea sync triggered via UI")
}
