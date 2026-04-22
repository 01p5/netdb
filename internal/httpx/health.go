package httpx

import (
	"net/http"
)

// healthz is a liveness+readiness probe: runs a trivial DB query and returns
// 200 if that succeeds. Exempted from auth in ServeHTTP so monitoring can
// hit it without credentials.
func (s *Server) healthz(w http.ResponseWriter, r *http.Request) {
	var one int
	if err := s.store.DB().QueryRowContext(r.Context(), "SELECT 1").Scan(&one); err != nil {
		http.Error(w, "db error: "+err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/plain")
	_, _ = w.Write([]byte("ok\n"))
}
