package kea

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// fakeKea is a minimal Kea control-agent fake. Each test wires the response
// it expects per command.
type fakeKea struct {
	t        *testing.T
	handlers map[string]func(cmd Command) (Response, bool)
	calls    []string
}

func newFakeKea(t *testing.T) *fakeKea {
	return &fakeKea{t: t, handlers: map[string]func(Command) (Response, bool){}}
}

func (f *fakeKea) on(cmd string, h func(Command) (Response, bool)) {
	f.handlers[cmd] = h
}

func (f *fakeKea) server() *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var cmd Command
		if err := json.Unmarshal(body, &cmd); err != nil {
			f.t.Errorf("bad request body: %v (%s)", err, body)
			http.Error(w, "bad", 400)
			return
		}
		f.calls = append(f.calls, cmd.Command)
		h, ok := f.handlers[cmd.Command]
		if !ok {
			f.t.Errorf("unhandled command %q", cmd.Command)
			http.Error(w, "no handler", 500)
			return
		}
		resp, services := h(cmd)
		w.Header().Set("Content-Type", "application/json")
		if services {
			_ = json.NewEncoder(w).Encode([]Response{resp})
		} else {
			_ = json.NewEncoder(w).Encode(resp)
		}
	}))
}

func TestClientConfigSet(t *testing.T) {
	f := newFakeKea(t)
	f.on("config-set", func(c Command) (Response, bool) {
		// Must address the dhcp4 service.
		if len(c.Service) != 1 || c.Service[0] != "dhcp4" {
			t.Errorf("expected Service=['dhcp4'], got %v", c.Service)
		}
		// Body should have a Dhcp4 wrapper.
		if !strings.Contains(string(c.Arguments), `"Dhcp4"`) {
			t.Errorf("arguments missing Dhcp4 wrapper: %s", c.Arguments)
		}
		return Response{Result: 0, Text: "ok"}, true
	})
	srv := f.server()
	defer srv.Close()

	c := NewClient(srv.URL)
	if err := c.ConfigSet(context.Background(), Dhcp4Config{}); err != nil {
		t.Fatalf("ConfigSet: %v", err)
	}
}

func TestClientConfigSetFailure(t *testing.T) {
	f := newFakeKea(t)
	f.on("config-set", func(Command) (Response, bool) {
		return Response{Result: 1, Text: "rejected"}, true
	})
	srv := f.server()
	defer srv.Close()
	if err := NewClient(srv.URL).ConfigSet(context.Background(), Dhcp4Config{}); err == nil {
		t.Fatal("expected error on non-zero result")
	}
}

func TestClientVersionGet(t *testing.T) {
	f := newFakeKea(t)
	f.on("version-get", func(Command) (Response, bool) {
		return Response{Result: 0, Text: "2.4.1"}, true
	})
	srv := f.server()
	defer srv.Close()
	got, err := NewClient(srv.URL).VersionGet(context.Background())
	if err != nil {
		t.Fatalf("VersionGet: %v", err)
	}
	if got != "2.4.1" {
		t.Errorf("got %q", got)
	}
}

func TestClientListLeases4(t *testing.T) {
	f := newFakeKea(t)
	f.on("lease4-get-all", func(Command) (Response, bool) {
		args := json.RawMessage(`{"leases":[{"ip-address":"10.0.0.5","hw-address":"aa:bb:cc:dd:ee:01","hostname":"x","state":0,"subnet-id":1}]}`)
		return Response{Result: 0, Arguments: args}, true
	})
	srv := f.server()
	defer srv.Close()
	leases, err := NewClient(srv.URL).ListLeases4(context.Background())
	if err != nil {
		t.Fatalf("ListLeases4: %v", err)
	}
	if len(leases) != 1 || leases[0].IPAddress != "10.0.0.5" {
		t.Errorf("leases: %+v", leases)
	}
}

func TestClientListLeases4Empty(t *testing.T) {
	f := newFakeKea(t)
	f.on("lease4-get-all", func(Command) (Response, bool) {
		return Response{Result: 3, Text: "no leases"}, true
	})
	srv := f.server()
	defer srv.Close()
	leases, err := NewClient(srv.URL).ListLeases4(context.Background())
	if err != nil {
		t.Fatalf("ListLeases4: %v", err)
	}
	if leases != nil {
		t.Errorf("expected nil leases on result=3, got %+v", leases)
	}
}

func TestClientListLeases4Error(t *testing.T) {
	f := newFakeKea(t)
	f.on("lease4-get-all", func(Command) (Response, bool) {
		return Response{Result: 1, Text: "boom"}, true
	})
	srv := f.server()
	defer srv.Close()
	if _, err := NewClient(srv.URL).ListLeases4(context.Background()); err == nil {
		t.Fatal("expected error on non-empty non-zero result")
	}
}

func TestClient5xx(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "server down", http.StatusBadGateway)
	}))
	defer srv.Close()
	if err := NewClient(srv.URL).ConfigSet(context.Background(), Dhcp4Config{}); err == nil {
		t.Fatal("expected error on 5xx")
	}
}
