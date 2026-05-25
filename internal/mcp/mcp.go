// Package mcp exposes netdb's data plane as an MCP (Model Context
// Protocol) server over HTTP. The transport is a single POST endpoint
// that speaks JSON-RPC 2.0 — the "Streamable HTTP" variant of the MCP
// spec (https://modelcontextprotocol.io). One request, one response,
// no SSE.
//
// Tools are read/write wrappers over the existing store and reconciler.
// Destructive verbs are flagged via the tool annotations so the
// downstream MCP client (Olympus) can route them through its approval
// queue. The auth posture is identical to the rest of netdb's HTTP
// surface: if basic-auth env vars are set, the existing checkAuth
// middleware sits in front of /mcp; otherwise the endpoint is open
// for the same reason / under the same conditions as / and /hosts.
package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
)

// Protocol version we report to clients. Matches the spec revision
// shipping in Olympus's libs/agentlib/mcp.py (2025-06-18).
const ProtocolVersion = "2025-06-18"

// ServerInfo block returned in the initialize handshake.
type ServerInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// JSON-RPC 2.0 envelope shared by request/response/notification.
// Notifications are distinguishable from requests by the absence of
// an id (we look at rawID below rather than ID directly so we can
// preserve a numeric or string id verbatim in responses).
type rpcMessage struct {
	JSONRPC string          `json:"jsonrpc"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	ID      json.RawMessage `json:"id,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

// Spec error codes.
const (
	codeParseError     = -32700
	codeInvalidRequest = -32600
	codeMethodNotFound = -32601
	codeInvalidParams  = -32602
	codeInternalError  = -32603
)

// Tool is a single registered MCP tool. Call gets the parsed
// arguments map and returns either an arbitrary value (which is
// JSON-marshaled and wrapped in the MCP text-content envelope) or an
// error (surfaced as isError=true to the client).
type Tool struct {
	Name        string
	Description string
	InputSchema map[string]any
	// Destructive is metadata for the client side; the MCP spec
	// doesn't carry it directly, but we surface it as a hint in the
	// tool description so the Olympus operator sees it when wiring.
	Destructive bool
	Call        func(ctx context.Context, args map[string]any) (any, error)
}

// Server is the HTTP handler. Construct with New + register tools
// before mounting.
type Server struct {
	info  ServerInfo
	tools []Tool
	byName map[string]*Tool
}

func New(info ServerInfo) *Server {
	return &Server{
		info:   info,
		byName: map[string]*Tool{},
	}
}

// Register adds a tool to the catalog. Duplicate names panic at
// startup — easier to catch wiring bugs than a silent shadow.
func (s *Server) Register(t Tool) {
	if t.Name == "" {
		panic("mcp.Register: empty tool name")
	}
	if _, dup := s.byName[t.Name]; dup {
		panic("mcp.Register: duplicate tool name " + t.Name)
	}
	if t.Destructive && t.Description != "" {
		// Make destructive-ness LLM-visible. Olympus's MCPClient
		// reads the description string into the agent's prompt;
		// without this hint the agent won't know which calls to
		// avoid by default.
		t.Description = "[DESTRUCTIVE] " + t.Description
	}
	s.tools = append(s.tools, t)
	s.byName[t.Name] = &s.tools[len(s.tools)-1]
}

// ServeHTTP implements the MCP HTTP transport. Returns 200 with a
// JSON-RPC response body for both successful calls and method-level
// errors; only transport-level problems (bad JSON, wrong method)
// produce non-200.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "MCP transport requires POST", http.StatusMethodNotAllowed)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		http.Error(w, "read body: "+err.Error(), http.StatusBadRequest)
		return
	}

	var msg rpcMessage
	if err := json.Unmarshal(body, &msg); err != nil {
		s.writeError(w, nil, codeParseError, "parse error: "+err.Error())
		return
	}

	// Notifications: no id, no response. We still ack with 202 so the
	// client knows the message landed.
	if len(msg.ID) == 0 {
		// notifications/initialized arrives after initialize. We
		// don't need to do anything with it but must not 4xx.
		w.WriteHeader(http.StatusAccepted)
		return
	}

	resp := s.dispatch(r.Context(), msg)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(resp)
}

func (s *Server) dispatch(ctx context.Context, msg rpcMessage) rpcMessage {
	resp := rpcMessage{JSONRPC: "2.0", ID: msg.ID}
	switch msg.Method {
	case "initialize":
		resp.Result = mustJSON(map[string]any{
			"protocolVersion": ProtocolVersion,
			"serverInfo":      s.info,
			"capabilities": map[string]any{
				// We support tools; no prompts, resources, sampling.
				"tools": map[string]any{},
			},
		})
	case "tools/list":
		out := make([]map[string]any, 0, len(s.tools))
		for _, t := range s.tools {
			out = append(out, map[string]any{
				"name":        t.Name,
				"description": t.Description,
				"inputSchema": t.InputSchema,
			})
		}
		resp.Result = mustJSON(map[string]any{"tools": out})
	case "tools/call":
		resp = s.callTool(ctx, msg)
	case "ping":
		resp.Result = mustJSON(map[string]any{})
	default:
		resp.Error = &rpcError{Code: codeMethodNotFound, Message: "method not found: " + msg.Method}
	}
	return resp
}

// callTool implements tools/call. The response always has Content+IsError
// fields per spec; agent-side errors come back as isError=true (with the
// error text in content), not as JSON-RPC errors.
func (s *Server) callTool(ctx context.Context, msg rpcMessage) rpcMessage {
	resp := rpcMessage{JSONRPC: "2.0", ID: msg.ID}
	var params struct {
		Name      string         `json:"name"`
		Arguments map[string]any `json:"arguments"`
	}
	if err := json.Unmarshal(msg.Params, &params); err != nil {
		resp.Error = &rpcError{Code: codeInvalidParams, Message: "invalid params: " + err.Error()}
		return resp
	}
	tool, ok := s.byName[params.Name]
	if !ok {
		resp.Error = &rpcError{Code: codeMethodNotFound, Message: "unknown tool: " + params.Name}
		return resp
	}
	if params.Arguments == nil {
		params.Arguments = map[string]any{}
	}

	result, err := tool.Call(ctx, params.Arguments)
	if err != nil {
		// Agent-recoverable error: report via the MCP envelope
		// (isError=true), not as a JSON-RPC error. This is what the
		// spec expects for tool-level failures, and Olympus's
		// MCPClient parses it correctly.
		slog.Warn("mcp tool failed", "tool", params.Name, "err", err)
		resp.Result = mustJSON(map[string]any{
			"content": []map[string]any{{"type": "text", "text": err.Error()}},
			"isError": true,
		})
		return resp
	}

	// Successful tool call. Marshal the return value to JSON so the
	// LLM sees structured data, not a Go fmt.Stringer rendering.
	text, jerr := json.MarshalIndent(result, "", "  ")
	if jerr != nil {
		resp.Error = &rpcError{Code: codeInternalError, Message: "marshal result: " + jerr.Error()}
		return resp
	}
	resp.Result = mustJSON(map[string]any{
		"content": []map[string]any{{"type": "text", "text": string(text)}},
		"isError": false,
	})
	return resp
}

func (s *Server) writeError(w http.ResponseWriter, id json.RawMessage, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(rpcMessage{
		JSONRPC: "2.0",
		ID:      id,
		Error:   &rpcError{Code: code, Message: msg},
	})
}

// mustJSON is a small helper for code paths where the input is known
// to be marshalable — only used for the static initialize/tools/list
// payloads above. Marshalling cannot fail there, so we use it inline
// without an error return to keep the dispatch readable.
func mustJSON(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}

// ArgInt64 extracts an int64 from a tools/call arguments map. JSON
// numbers arrive as float64 in Go; the bare conversion would silently
// truncate large IDs. We accept both number and string forms.
func ArgInt64(args map[string]any, key string) (int64, error) {
	v, ok := args[key]
	if !ok {
		return 0, fmt.Errorf("missing argument %q", key)
	}
	switch x := v.(type) {
	case float64:
		return int64(x), nil
	case int64:
		return x, nil
	case int:
		return int64(x), nil
	case string:
		var out int64
		if _, err := fmt.Sscanf(x, "%d", &out); err != nil {
			return 0, fmt.Errorf("argument %q not numeric: %v", key, err)
		}
		return out, nil
	default:
		return 0, fmt.Errorf("argument %q wrong type %T", key, v)
	}
}

// ArgString extracts a required string; empty values are an error so
// callers don't have to defensive-check every field. Use ArgStringOpt
// for optional fields.
func ArgString(args map[string]any, key string) (string, error) {
	v, ok := args[key]
	if !ok {
		return "", fmt.Errorf("missing argument %q", key)
	}
	s, ok := v.(string)
	if !ok {
		return "", fmt.Errorf("argument %q wrong type %T (want string)", key, v)
	}
	if s == "" {
		return "", fmt.Errorf("argument %q is empty", key)
	}
	return s, nil
}

func ArgStringOpt(args map[string]any, key string) string {
	v, ok := args[key]
	if !ok {
		return ""
	}
	s, _ := v.(string)
	return s
}

func ArgBoolOpt(args map[string]any, key string, def bool) bool {
	v, ok := args[key]
	if !ok {
		return def
	}
	b, ok := v.(bool)
	if !ok {
		return def
	}
	return b
}

func ArgIntOpt(args map[string]any, key string, def int) int {
	v, ok := args[key]
	if !ok {
		return def
	}
	switch x := v.(type) {
	case float64:
		return int(x)
	case int:
		return x
	}
	return def
}

// ErrNotImplemented is returned by tool stubs that haven't been
// wired up yet. Kept exported so tools.go can produce a uniform
// message without sprinkling magic strings.
var ErrNotImplemented = errors.New("tool not implemented yet")
