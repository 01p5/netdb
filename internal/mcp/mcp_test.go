package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func newServer() *Server {
	return New(ServerInfo{Name: "test", Version: "0.0.0"})
}

// helper: POST a JSON-RPC body and decode the envelope.
func rpc(t *testing.T, s *Server, body string) (int, rpcMessage) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(body))
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK && rec.Code != http.StatusAccepted {
		// Transport-level error; still surface the body for the caller.
		return rec.Code, rpcMessage{}
	}
	if rec.Code == http.StatusAccepted {
		return rec.Code, rpcMessage{}
	}
	var resp rpcMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode resp: %v body=%s", err, rec.Body.String())
	}
	return rec.Code, resp
}

func TestServeHTTPRequiresPOST(t *testing.T) {
	s := newServer()
	req := httptest.NewRequest(http.MethodGet, "/mcp", nil)
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET: got %d", rec.Code)
	}
}

func TestInitialize(t *testing.T) {
	s := newServer()
	_, resp := rpc(t, s, `{"jsonrpc":"2.0","id":1,"method":"initialize"}`)
	if resp.Error != nil {
		t.Fatalf("initialize error: %+v", resp.Error)
	}
	var out struct {
		ProtocolVersion string     `json:"protocolVersion"`
		ServerInfo      ServerInfo `json:"serverInfo"`
	}
	_ = json.Unmarshal(resp.Result, &out)
	if out.ProtocolVersion == "" {
		t.Errorf("expected protocolVersion in response, got %s", resp.Result)
	}
	if out.ServerInfo.Name != "test" {
		t.Errorf("serverInfo: %+v", out.ServerInfo)
	}
}

func TestPing(t *testing.T) {
	s := newServer()
	_, resp := rpc(t, s, `{"jsonrpc":"2.0","id":1,"method":"ping"}`)
	if resp.Error != nil {
		t.Errorf("ping error: %+v", resp.Error)
	}
}

func TestNotificationReturns202(t *testing.T) {
	s := newServer()
	code, _ := rpc(t, s, `{"jsonrpc":"2.0","method":"notifications/initialized"}`)
	if code != http.StatusAccepted {
		t.Errorf("notification: got %d", code)
	}
}

func TestParseError(t *testing.T) {
	s := newServer()
	_, resp := rpc(t, s, `{not json`)
	if resp.Error == nil || resp.Error.Code != codeParseError {
		t.Errorf("expected parse error, got %+v", resp.Error)
	}
}

func TestMethodNotFound(t *testing.T) {
	s := newServer()
	_, resp := rpc(t, s, `{"jsonrpc":"2.0","id":1,"method":"unknown/thing"}`)
	if resp.Error == nil || resp.Error.Code != codeMethodNotFound {
		t.Errorf("expected method-not-found, got %+v", resp.Error)
	}
}

func TestToolsListAndCall(t *testing.T) {
	s := newServer()
	s.Register(Tool{
		Name:        "echo",
		Description: "echoes input",
		Call: func(ctx context.Context, args map[string]any) (any, error) {
			return map[string]any{"got": args["msg"]}, nil
		},
	})

	_, resp := rpc(t, s, `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
	if resp.Error != nil || !strings.Contains(string(resp.Result), "echo") {
		t.Fatalf("tools/list: err=%+v result=%s", resp.Error, resp.Result)
	}

	body := `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"echo","arguments":{"msg":"hi"}}}`
	_, resp = rpc(t, s, body)
	if resp.Error != nil {
		t.Fatalf("tools/call: %+v", resp.Error)
	}
	if !strings.Contains(string(resp.Result), `\"got\"`) || !strings.Contains(string(resp.Result), `\"hi\"`) {
		t.Errorf("expected echoed msg in result, got %s", resp.Result)
	}
}

func TestToolCallToolError(t *testing.T) {
	s := newServer()
	s.Register(Tool{
		Name:        "broken",
		Description: "always fails",
		Call: func(ctx context.Context, args map[string]any) (any, error) {
			return nil, errors.New("nope")
		},
	})
	body := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"broken","arguments":{}}}`
	_, resp := rpc(t, s, body)
	if resp.Error != nil {
		t.Fatalf("transport-level error should be nil, got %+v", resp.Error)
	}
	// isError=true in the envelope.
	if !strings.Contains(string(resp.Result), `"isError":true`) {
		t.Errorf("expected isError=true, got %s", resp.Result)
	}
	if !strings.Contains(string(resp.Result), "nope") {
		t.Errorf("expected err text in result: %s", resp.Result)
	}
}

func TestToolUnknownName(t *testing.T) {
	s := newServer()
	body := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"nope"}}`
	_, resp := rpc(t, s, body)
	if resp.Error == nil || resp.Error.Code != codeMethodNotFound {
		t.Errorf("expected method-not-found, got %+v", resp.Error)
	}
}

func TestToolBadParams(t *testing.T) {
	s := newServer()
	body := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":"not-an-object"}`
	_, resp := rpc(t, s, body)
	if resp.Error == nil || resp.Error.Code != codeInvalidParams {
		t.Errorf("expected invalid-params, got %+v", resp.Error)
	}
}

func TestRegisterDuplicatePanics(t *testing.T) {
	s := newServer()
	s.Register(Tool{Name: "x", Call: func(context.Context, map[string]any) (any, error) { return nil, nil }})
	defer func() {
		if recover() == nil {
			t.Error("expected panic on duplicate registration")
		}
	}()
	s.Register(Tool{Name: "x", Call: func(context.Context, map[string]any) (any, error) { return nil, nil }})
}

func TestRegisterEmptyNamePanics(t *testing.T) {
	s := newServer()
	defer func() {
		if recover() == nil {
			t.Error("expected panic on empty tool name")
		}
	}()
	s.Register(Tool{Name: ""})
}

func TestRegisterDestructivePrefixesDescription(t *testing.T) {
	s := newServer()
	s.Register(Tool{Name: "rm", Description: "deletes things", Destructive: true,
		Call: func(context.Context, map[string]any) (any, error) { return nil, nil }})
	if got := s.byName["rm"].Description; !strings.HasPrefix(got, "[DESTRUCTIVE]") {
		t.Errorf("description: %q", got)
	}
}

func TestArgHelpers(t *testing.T) {
	args := map[string]any{
		"id":    float64(42),
		"idStr": "100",
		"name":  "alice",
		"flag":  true,
		"size":  float64(7),
	}
	if v, err := ArgInt64(args, "id"); err != nil || v != 42 {
		t.Errorf("ArgInt64 float: %d %v", v, err)
	}
	if v, err := ArgInt64(args, "idStr"); err != nil || v != 100 {
		t.Errorf("ArgInt64 string: %d %v", v, err)
	}
	if _, err := ArgInt64(args, "missing"); err == nil {
		t.Error("expected missing-arg error")
	}
	if _, err := ArgInt64(args, "name"); err == nil {
		t.Error("expected wrong-type error for ArgInt64(name)")
	}
	if v, err := ArgString(args, "name"); err != nil || v != "alice" {
		t.Errorf("ArgString: %q %v", v, err)
	}
	if _, err := ArgString(args, "id"); err == nil {
		t.Error("ArgString wrong type")
	}
	if _, err := ArgString(map[string]any{"x": ""}, "x"); err == nil {
		t.Error("ArgString empty")
	}
	if v := ArgStringOpt(args, "missing"); v != "" {
		t.Errorf("ArgStringOpt missing: %q", v)
	}
	if v := ArgStringOpt(args, "id"); v != "" {
		t.Errorf("ArgStringOpt wrong type should default to '': %q", v)
	}
	if !ArgBoolOpt(args, "flag", false) {
		t.Errorf("ArgBoolOpt: expected true")
	}
	if ArgBoolOpt(args, "missing", true) != true {
		t.Errorf("ArgBoolOpt missing default")
	}
	if ArgBoolOpt(args, "id", true) != true {
		t.Errorf("ArgBoolOpt wrong type falls back to default")
	}
	if got := ArgIntOpt(args, "size", 99); got != 7 {
		t.Errorf("ArgIntOpt: %d", got)
	}
	if got := ArgIntOpt(args, "missing", 99); got != 99 {
		t.Errorf("ArgIntOpt missing default: %d", got)
	}
}
