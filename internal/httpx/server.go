package httpx

import (
	"crypto/subtle"
	"embed"
	"fmt"
	"html/template"
	"io/fs"
	"log/slog"
	"net/http"
	"strings"

	"netdb/internal/kea"
	"netdb/internal/reconcile"
	"netdb/internal/store"
)

// Options carries construction options for New. Zero values = sensible defaults
// and (for auth) "off".
type Options struct {
	AuthUser     string
	AuthPassword string
}

//go:embed templates/*.html
var templatesFS embed.FS

//go:embed static/*
var staticFS embed.FS

type Server struct {
	store       *store.Store
	rec         *reconcile.Reconciler
	kea         *kea.Syncer
	leasePoller *kea.LeasePoller
	pages       map[string]*template.Template
	mux         *http.ServeMux
	opts        Options
}

var funcMap = template.FuncMap{
	"splitTags": func(s string) []string {
		if s == "" {
			return nil
		}
		out := []string{}
		for _, p := range strings.Split(s, ",") {
			p = strings.TrimSpace(p)
			if p != "" {
				out = append(out, p)
			}
		}
		return out
	},
	// dict builds a map[string]any from alternating key/value args, so
	// templates can pass named fields into sub-template calls.
	"dict": func(vals ...any) (map[string]any, error) {
		if len(vals)%2 != 0 {
			return nil, fmt.Errorf("dict: odd number of arguments")
		}
		m := make(map[string]any, len(vals)/2)
		for i := 0; i < len(vals); i += 2 {
			k, ok := vals[i].(string)
			if !ok {
				return nil, fmt.Errorf("dict: key at position %d is not a string", i)
			}
			m[k] = vals[i+1]
		}
		return m, nil
	},
}

// pageTemplates pairs each page with its base so that its "content"
// define wins over anything else in the set.
var pageFiles = map[string][]string{
	"hosts_list":     {"templates/base.html", "templates/hosts_list.html"},
	"host_detail":    {"templates/base.html", "templates/host_detail.html"},
	"subnets_list":   {"templates/base.html", "templates/subnets_list.html"},
	"zones_list":     {"templates/base.html", "templates/zones_list.html"},
	"providers_list": {"templates/base.html", "templates/providers_list.html"},
	"dns_list":       {"templates/base.html", "templates/dns_list.html"},
	"dhcp_page":      {"templates/base.html", "templates/dhcp_page.html"},
}

// fragmentTemplates are parsed as standalone (no base wrapper) for HTMX
// partial responses.
var fragmentFiles = []string{
	"templates/hosts_list.html",
	"templates/host_detail.html",
	"templates/subnets_list.html",
	"templates/zones_list.html",
	"templates/providers_list.html",
	"templates/dns_list.html",
	"templates/dhcp_page.html",
}

func New(st *store.Store, rec *reconcile.Reconciler, keaSync *kea.Syncer, leasePoller *kea.LeasePoller, opts Options) *Server {
	pages := map[string]*template.Template{}
	for name, files := range pageFiles {
		t := template.Must(template.New(name).Funcs(funcMap).ParseFS(templatesFS, files...))
		pages[name] = t
	}
	// Fragments: one template set with all partial definitions, used by
	// HTMX handlers that return a single row/list-item.
	frag := template.Must(template.New("fragments").Funcs(funcMap).ParseFS(templatesFS, fragmentFiles...))
	pages["_fragments"] = frag

	s := &Server{
		store: st, rec: rec, kea: keaSync, leasePoller: leasePoller,
		opts: opts, pages: pages, mux: http.NewServeMux(),
	}
	s.routes()
	return s
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !s.checkAuth(w, r) {
		return
	}
	s.mux.ServeHTTP(w, r)
}

// checkAuth gates every request when NETDB_USER/NETDB_PASSWORD are set.
// /healthz and /static/* bypass so monitoring/assets work unauthenticated.
// Returns true if the request should proceed.
func (s *Server) checkAuth(w http.ResponseWriter, r *http.Request) bool {
	if s.opts.AuthUser == "" && s.opts.AuthPassword == "" {
		return true
	}
	if r.URL.Path == "/healthz" || strings.HasPrefix(r.URL.Path, "/static/") {
		return true
	}
	u, p, ok := r.BasicAuth()
	if !ok ||
		subtle.ConstantTimeCompare([]byte(u), []byte(s.opts.AuthUser)) != 1 ||
		subtle.ConstantTimeCompare([]byte(p), []byte(s.opts.AuthPassword)) != 1 {
		w.Header().Set("WWW-Authenticate", `Basic realm="netdb"`)
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return false
	}
	return true
}

func (s *Server) routes() {
	sub, _ := fs.Sub(staticFS, "static")
	s.mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServer(http.FS(sub))))

	s.mux.HandleFunc("GET /healthz", s.healthz)

	s.mux.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		http.Redirect(w, r, "/hosts", http.StatusFound)
	})

	s.mux.HandleFunc("GET /hosts", s.listHosts)
	s.mux.HandleFunc("POST /hosts", s.createHost)
	s.mux.HandleFunc("GET /hosts/{id}", s.hostDetail)
	s.mux.HandleFunc("POST /hosts/{id}", s.updateHost)
	s.mux.HandleFunc("DELETE /hosts/{id}", s.deleteHost)

	s.mux.HandleFunc("POST /hosts/{id}/nics", s.createNIC)
	s.mux.HandleFunc("DELETE /nics/{id}", s.deleteNIC)

	s.mux.HandleFunc("POST /nics/{id}/ips", s.createIP)
	s.mux.HandleFunc("DELETE /ips/{id}", s.deleteIP)

	s.mux.HandleFunc("GET /subnets", s.listSubnets)
	s.mux.HandleFunc("POST /subnets", s.createSubnet)
	s.mux.HandleFunc("POST /subnets/{id}", s.updateSubnet)
	s.mux.HandleFunc("DELETE /subnets/{id}", s.deleteSubnet)
	s.mux.HandleFunc("POST /subnets/{id}/zones", s.linkSubnetZone)
	s.mux.HandleFunc("DELETE /subnets/{id}/zones/{zid}/{role}", s.unlinkSubnetZone)

	s.mux.HandleFunc("GET /zones", s.listZones)
	s.mux.HandleFunc("POST /zones", s.createZone)
	s.mux.HandleFunc("DELETE /zones/{id}", s.deleteZone)
	s.mux.HandleFunc("POST /zones/{id}/providers", s.linkZoneProvider)
	s.mux.HandleFunc("DELETE /zones/{id}/providers/{pid}", s.unlinkZoneProvider)

	s.mux.HandleFunc("GET /providers", s.listProviders)
	s.mux.HandleFunc("POST /providers", s.createProvider)
	s.mux.HandleFunc("DELETE /providers/{id}", s.deleteProvider)

	s.mux.HandleFunc("GET /dns", s.listDNS)
	s.mux.HandleFunc("POST /dns", s.createManualDNS)
	s.mux.HandleFunc("DELETE /dns/{id}", s.deleteDNSRecord)
	s.mux.HandleFunc("POST /dns/sync", s.syncNow)

	s.mux.HandleFunc("GET /dhcp", s.dhcpPage)
	s.mux.HandleFunc("POST /dhcp/sync", s.syncKea)
	s.mux.HandleFunc("POST /dhcp/poll", s.pollLeases)
}

// renderPage executes the "base" template of a named page set.
func (s *Server) renderPage(w http.ResponseWriter, page string, data any) {
	t, ok := s.pages[page]
	if !ok {
		http.Error(w, fmt.Sprintf("unknown page %q", page), http.StatusInternalServerError)
		return
	}
	if err := t.ExecuteTemplate(w, "base", data); err != nil {
		slog.Error("render page", "page", page, "err", err)
	}
}

// renderFragment executes a named fragment (row, list-item) for HTMX swaps.
func (s *Server) renderFragment(w http.ResponseWriter, name string, data any) {
	if err := s.pages["_fragments"].ExecuteTemplate(w, name, data); err != nil {
		slog.Error("render fragment", "name", name, "err", err)
	}
}

// respondOK is used for DELETE endpoints where HTMX swaps the target with
// outerHTML to an empty body.
func respondOK(w http.ResponseWriter) {
	w.WriteHeader(http.StatusOK)
}
