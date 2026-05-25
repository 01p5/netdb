package technitium

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	"netdb/internal/dnsprov"
)

// fakeTech is an in-memory stand-in for the Technitium HTTP API. It only
// implements the verbs the adapter exercises.
type fakeTech struct {
	t  *testing.T
	mu sync.Mutex

	// recs is keyed by name|type|value so duplicate adds return "already exists".
	recs        map[string]record
	requireAuth bool
	token       string

	// hook lets tests intercept and override specific endpoints.
	hook func(path string, params map[string]string) (status int, body string, handled bool)
}

type record struct {
	Name, Type, Value string
	TTL               int
}

func (f *fakeTech) key(name, rtype, value string) string {
	return strings.ToLower(name) + "|" + rtype + "|" + value
}

func newFakeTech(t *testing.T) *httptest.Server {
	f := &fakeTech{t: t, recs: map[string]record{}, requireAuth: true, token: "tok-1"}
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Errorf("form: %v", err)
		}
		params := map[string]string{}
		for k := range r.PostForm {
			params[k] = r.PostForm.Get(k)
		}
		path := r.URL.Path

		f.mu.Lock()
		defer f.mu.Unlock()

		if f.hook != nil {
			if status, body, handled := f.hook(path, params); handled {
				w.WriteHeader(status)
				_, _ = w.Write([]byte(body))
				return
			}
		}

		switch path {
		case "/api/user/createToken":
			if params["user"] == "" || params["pass"] == "" {
				writeJSON(w, map[string]any{"status": "error", "errorMessage": "missing creds"})
				return
			}
			writeJSON(w, map[string]any{"status": "ok", "token": f.token})
		case "/api/zones/create":
			if !f.checkAuth(w, params) {
				return
			}
			zone := params["zone"]
			if zone == "" {
				writeJSON(w, map[string]any{"status": "error", "errorMessage": "missing zone"})
				return
			}
			writeJSON(w, map[string]any{"status": "ok"})
		case "/api/zones/records/get":
			if !f.checkAuth(w, params) {
				return
			}
			var recs []map[string]any
			for _, r := range f.recs {
				recs = append(recs, recordToRdata(r))
			}
			writeJSON(w, map[string]any{
				"status": "ok",
				"response": map[string]any{
					"domain":  params["domain"],
					"zone":    params["zone"],
					"records": recs,
				},
			})
		case "/api/zones/records/add":
			if !f.checkAuth(w, params) {
				return
			}
			rec, err := paramsToRecord(params)
			if err != nil {
				writeJSON(w, map[string]any{"status": "error", "errorMessage": err.Error()})
				return
			}
			k := f.key(rec.Name, rec.Type, rec.Value)
			if _, exists := f.recs[k]; exists {
				writeJSON(w, map[string]any{"status": "error", "errorMessage": "record already exists"})
				return
			}
			f.recs[k] = rec
			writeJSON(w, map[string]any{"status": "ok"})
		case "/api/zones/records/delete":
			if !f.checkAuth(w, params) {
				return
			}
			rec, err := paramsToRecord(params)
			if err != nil {
				writeJSON(w, map[string]any{"status": "error", "errorMessage": err.Error()})
				return
			}
			k := f.key(rec.Name, rec.Type, rec.Value)
			if _, exists := f.recs[k]; !exists {
				writeJSON(w, map[string]any{"status": "error", "errorMessage": "no such record"})
				return
			}
			delete(f.recs, k)
			writeJSON(w, map[string]any{"status": "ok"})
		default:
			t.Errorf("unhandled path: %s", path)
			http.NotFound(w, r)
		}
	}))
}

func (f *fakeTech) checkAuth(w http.ResponseWriter, params map[string]string) bool {
	if !f.requireAuth {
		return true
	}
	if params["token"] != f.token {
		writeJSON(w, map[string]any{"status": "error", "errorMessage": "bad token"})
		return false
	}
	return true
}

func writeJSON(w http.ResponseWriter, body any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(body)
}

func paramsToRecord(p map[string]string) (record, error) {
	r := record{Name: p["domain"], Type: p["type"]}
	switch r.Type {
	case "A", "AAAA":
		r.Value = p["ipAddress"]
	case "CNAME":
		r.Value = p["cname"]
	case "PTR":
		r.Value = p["ptrName"]
	case "TXT":
		r.Value = p["text"]
	case "MX":
		r.Value = p["preference"] + " " + p["exchange"]
	default:
		return r, &errorString{s: "unsupported type"}
	}
	return r, nil
}

func recordToRdata(r record) map[string]any {
	rdata := map[string]any{}
	switch r.Type {
	case "A", "AAAA":
		rdata["ipAddress"] = r.Value
	case "CNAME":
		rdata["cname"] = r.Value
	case "PTR":
		rdata["ptrName"] = r.Value
	case "TXT":
		rdata["text"] = r.Value
	case "MX":
		// "10 mail.example.com"
		parts := strings.SplitN(r.Value, " ", 2)
		if len(parts) == 2 {
			rdata["preference"] = atof(parts[0])
			rdata["exchange"] = parts[1]
		}
	}
	return map[string]any{
		"name":  r.Name,
		"type":  r.Type,
		"ttl":   r.TTL,
		"rData": rdata,
	}
}

func atof(s string) float64 {
	var n float64
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0
		}
		n = n*10 + float64(c-'0')
	}
	return n
}

type errorString struct{ s string }

func (e *errorString) Error() string { return e.s }

// --- tests ----------------------------------------------------------------

func TestEnsureZoneOK(t *testing.T) {
	srv := newFakeTech(t)
	defer srv.Close()
	c := New("t", Config{BaseURL: srv.URL, Token: "tok-1"})
	if err := c.EnsureZone(context.Background(), "lan.example"); err != nil {
		t.Fatalf("EnsureZone: %v", err)
	}
}

func TestEnsureZoneAlreadyExistsIsOK(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"status": "error", "errorMessage": "Zone Already Exists"})
	}))
	defer srv.Close()
	c := New("t", Config{BaseURL: srv.URL, Token: "tok"})
	if err := c.EnsureZone(context.Background(), "z"); err != nil {
		t.Errorf("already-exists should be OK, got %v", err)
	}
}

func TestEnsureZoneOtherError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"status": "error", "errorMessage": "permission denied"})
	}))
	defer srv.Close()
	c := New("t", Config{BaseURL: srv.URL, Token: "tok"})
	if err := c.EnsureZone(context.Background(), "z"); err == nil {
		t.Error("expected error")
	}
}

func TestAddListDeleteRoundTrip(t *testing.T) {
	srv := newFakeTech(t)
	defer srv.Close()
	c := New("t", Config{BaseURL: srv.URL, Token: "tok-1"})
	ctx := context.Background()

	add := dnsprov.Record{Name: "host.lan.example", Type: "A", Value: "10.0.0.5", TTL: 300}
	if err := c.AddRecord(ctx, "lan.example", add); err != nil {
		t.Fatalf("AddRecord A: %v", err)
	}
	// Duplicate add is harmless.
	if err := c.AddRecord(ctx, "lan.example", add); err != nil {
		t.Errorf("dup AddRecord should be ok: %v", err)
	}

	got, err := c.ListRecords(ctx, "lan.example")
	if err != nil {
		t.Fatalf("ListRecords: %v", err)
	}
	if len(got) != 1 || got[0].Value != "10.0.0.5" {
		t.Errorf("list mismatch: %+v", got)
	}

	if err := c.DeleteRecord(ctx, "lan.example", add); err != nil {
		t.Fatalf("DeleteRecord: %v", err)
	}
	// Deleting again returns "does not exist" — adapter treats as OK.
	if err := c.DeleteRecord(ctx, "lan.example", add); err != nil {
		t.Errorf("re-delete should be ok: %v", err)
	}
}

func TestMXRoundTrip(t *testing.T) {
	srv := newFakeTech(t)
	defer srv.Close()
	c := New("t", Config{BaseURL: srv.URL, Token: "tok-1"})
	rec := dnsprov.Record{Name: "lan.example", Type: "MX", Value: "10 mail.lan.example", TTL: 600}
	if err := c.AddRecord(context.Background(), "lan.example", rec); err != nil {
		t.Fatalf("AddRecord MX: %v", err)
	}
	got, err := c.ListRecords(context.Background(), "lan.example")
	if err != nil || len(got) != 1 || got[0].Type != "MX" || got[0].Value != "10 mail.lan.example" {
		t.Errorf("MX list: err=%v rec=%+v", err, got)
	}
}

func TestAddRecordBadMX(t *testing.T) {
	srv := newFakeTech(t)
	defer srv.Close()
	c := New("t", Config{BaseURL: srv.URL, Token: "tok-1"})
	err := c.AddRecord(context.Background(), "z", dnsprov.Record{Name: "z", Type: "MX", Value: "no-space"})
	if err == nil {
		t.Error("expected MX format error")
	}
}

func TestAddRecordUnsupportedType(t *testing.T) {
	c := New("t", Config{BaseURL: "http://unused", Token: "tok"})
	err := c.AddRecord(context.Background(), "z", dnsprov.Record{Name: "z", Type: "SRV", Value: "?"})
	if err == nil {
		t.Error("expected unsupported type error")
	}
}

func TestBootstrapToken(t *testing.T) {
	srv := newFakeTech(t)
	defer srv.Close()
	c := New("t", Config{BaseURL: srv.URL, Username: "admin", Password: "pw"})
	if err := c.EnsureZone(context.Background(), "boot.example"); err != nil {
		t.Fatalf("bootstrap then EnsureZone: %v", err)
	}
}

func TestBootstrapTokenMissingCreds(t *testing.T) {
	c := New("t", Config{BaseURL: "http://unused"})
	err := c.EnsureZone(context.Background(), "z")
	if err == nil {
		t.Error("expected error without token or user/pass")
	}
}

func TestNameKind(t *testing.T) {
	c := New("tech-main", Config{Token: "t"})
	if c.Name() != "tech-main" {
		t.Errorf("Name: %q", c.Name())
	}
	if c.Kind() != "technitium" {
		t.Errorf("Kind: %q", c.Kind())
	}
}

func TestRdataToValueAllTypes(t *testing.T) {
	cases := []struct {
		rtype string
		data  map[string]any
		want  string
		ok    bool
	}{
		{"A", map[string]any{"ipAddress": "10.0.0.1"}, "10.0.0.1", true},
		{"AAAA", map[string]any{"ipAddress": "::1"}, "::1", true},
		{"CNAME", map[string]any{"cname": "x.example."}, "x.example", true},
		{"PTR", map[string]any{"ptrName": "host.lan.example."}, "host.lan.example", true},
		{"TXT", map[string]any{"text": "hello"}, "hello", true},
		{"MX", map[string]any{"preference": float64(10), "exchange": "mail.example."}, "10 mail.example", true},
		{"SOA", map[string]any{}, "", false},
		{"NS", map[string]any{}, "", false},
	}
	for _, c := range cases {
		got, ok := rdataToValue(c.rtype, c.data)
		if ok != c.ok || got != c.want {
			t.Errorf("%s: got (%q,%v) want (%q,%v)", c.rtype, got, ok, c.want, c.ok)
		}
	}
}

func TestSetRdataParamsAllTypes(t *testing.T) {
	rec := dnsprov.Record{Name: "x", Value: "10.0.0.1"}
	for _, rt := range []string{"A", "AAAA", "CNAME", "PTR", "TXT"} {
		rec.Type = rt
		v := url.Values{}
		if err := setRdataParams(rec, v); err != nil {
			t.Errorf("%s: %v", rt, err)
		}
	}
	rec.Type = "SRV"
	if err := setRdataParams(rec, url.Values{}); err == nil {
		t.Error("expected SRV unsupported error")
	}
}

func TestListRecordsErrorStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"status": "error", "errorMessage": "denied"})
	}))
	defer srv.Close()
	c := New("t", Config{BaseURL: srv.URL, Token: "tok"})
	if _, err := c.ListRecords(context.Background(), "z"); err == nil {
		t.Error("expected error on status!=ok")
	}
}

func TestServerError5xx(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "down", http.StatusBadGateway)
	}))
	defer srv.Close()
	c := New("t", Config{BaseURL: srv.URL, Token: "tok"})
	if err := c.EnsureZone(context.Background(), "z"); err == nil {
		t.Error("expected error on 5xx")
	}
}

func TestBadJSONResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte("not json"))
	}))
	defer srv.Close()
	c := New("t", Config{BaseURL: srv.URL, Token: "tok"})
	if err := c.EnsureZone(context.Background(), "z"); err == nil {
		t.Error("expected decode error")
	}
}

func TestDeleteRecordToleratesDoesNotExist(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"status": "error", "errorMessage": "Record does not exist"})
	}))
	defer srv.Close()
	c := New("t", Config{BaseURL: srv.URL, Token: "tok"})
	if err := c.DeleteRecord(context.Background(), "z",
		dnsprov.Record{Name: "x", Type: "A", Value: "10.0.0.1"}); err != nil {
		t.Errorf("does-not-exist should be OK: %v", err)
	}
}
