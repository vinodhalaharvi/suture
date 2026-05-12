// Package mcp is a minimal, dependency-free MCP (Model Context Protocol)
// server implementation: JSON-RPC 2.0 over stdio, supporting the three
// methods needed for tool-serving (initialize, tools/list, tools/call).
//
// This exists because the published mark3labs/mcp-go versions require
// Go 1.23+, which our environment doesn't have. The MCP spec is small
// enough that hand-rolling the server is not unreasonable, and it
// removes a transitive dependency for the hackathon submission. To swap
// in mark3labs/mcp-go later, only this package needs to change — the
// tool handlers in pkg/tools are protocol-agnostic.
package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
)

// JSON-RPC 2.0 envelopes.

type request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"` // nil for notifications
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

// Tool describes a tool the server exposes.
type Tool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"inputSchema"`
}

// ToolHandler is invoked when a client calls "tools/call".
// args is the raw JSON for the tool input; meta is the request metadata
// (used to carry SHARP context fields). Return a JSON-serializable result.
type ToolHandler func(ctx context.Context, args json.RawMessage, meta map[string]any) (any, error)

// Server is a small MCP server. Tools are registered before Serve is called.
type Server struct {
	name    string
	version string

	mu       sync.RWMutex
	tools    []Tool
	handlers map[string]ToolHandler

	// fhirScopes is the list of SMART-on-FHIR scopes this server
	// requests under the Prompt Opinion FHIR context extension.
	// Declared in the response to "initialize" under
	//   capabilities.extensions["ai.promptopinion/fhir-context"].
	// See https://docs.promptopinion.ai/fhir-context/mcp-fhir-context
	fhirScopes []FHIRScope
}

// FHIRScope is a single SMART-on-FHIR scope the server requests.
type FHIRScope struct {
	Name     string `json:"name"`               // e.g. "patient/Patient.rs"
	Required bool   `json:"required,omitempty"` // optional scopes default to false
}

// NewServer creates a server with the given name/version (returned in initialize).
func NewServer(name, version string) *Server {
	return &Server{
		name:     name,
		version:  version,
		handlers: make(map[string]ToolHandler),
	}
}

// RequestFHIRScope declares a SMART-on-FHIR scope this server needs.
// Call this before Serve. The platform displays the requested scopes
// to the user during MCP server registration; the user authorizes
// (or denies) each non-required scope. Required scopes cannot be
// unchecked — the user's only option is to skip adding the server.
//
// Common scope names:
//
//	patient/Patient.rs       — read/search the current patient
//	patient/Condition.rs     — read/search conditions for the patient
//	patient/Observation.rs   — read/search observations (labs, vitals)
//	patient/Encounter.rs     — read/search encounters
//	offline_access           — receive a refresh token for background work
func (s *Server) RequestFHIRScope(name string, required bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.fhirScopes = append(s.fhirScopes, FHIRScope{Name: name, Required: required})
}

// AddTool registers a tool. Safe to call before Serve.
func (s *Server) AddTool(t Tool, h ToolHandler) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tools = append(s.tools, t)
	s.handlers[t.Name] = h
}

// Tools returns a snapshot of registered tools (useful in tests).
func (s *Server) Tools() []Tool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Tool, len(s.tools))
	copy(out, s.tools)
	return out
}

// Serve reads JSON-RPC messages from r and writes responses to w. One
// message per line (LSP-style framing is not used — MCP allows the
// simpler newline-delimited form for stdio.) Returns when r reaches EOF
// or ctx is cancelled.
func (s *Server) Serve(ctx context.Context, r io.Reader, w io.Writer) error {
	scanner := bufio.NewScanner(r)
	// Large buffer for FHIR bundles or generated letters.
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)

	enc := json.NewEncoder(w)
	var writeMu sync.Mutex
	write := func(resp response) error {
		writeMu.Lock()
		defer writeMu.Unlock()
		return enc.Encode(resp)
	}

	for scanner.Scan() {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}

		var req request
		if err := json.Unmarshal(line, &req); err != nil {
			_ = write(response{
				JSONRPC: "2.0",
				Error:   &rpcError{Code: -32700, Message: "parse error: " + err.Error()},
			})
			continue
		}

		go func(req request) {
			resp := s.handle(ctx, req)
			if req.ID == nil {
				// Notification — no response.
				return
			}
			_ = write(resp)
		}(req)
	}
	return scanner.Err()
}

func (s *Server) handle(ctx context.Context, req request) response {
	resp := response{JSONRPC: "2.0", ID: req.ID}

	switch req.Method {
	case "initialize":
		s.mu.RLock()
		scopes := make([]FHIRScope, len(s.fhirScopes))
		copy(scopes, s.fhirScopes)
		s.mu.RUnlock()

		capabilities := map[string]any{
			"tools": map[string]any{},
		}
		// Only advertise the FHIR-context extension if the server has
		// requested scopes. Servers that don't need patient data won't
		// trigger Prompt Opinion's authorization flow.
		if len(scopes) > 0 {
			capabilities["extensions"] = map[string]any{
				"ai.promptopinion/fhir-context": map[string]any{
					"scopes": scopes,
				},
			}
		}
		result, _ := json.Marshal(map[string]any{
			"protocolVersion": "2024-11-05",
			"capabilities":    capabilities,
			"serverInfo": map[string]any{
				"name":    s.name,
				"version": s.version,
			},
		})
		resp.Result = result

	case "initialized", "notifications/initialized":
		// Notification, no result needed.

	case "tools/list":
		s.mu.RLock()
		toolsList := make([]Tool, len(s.tools))
		copy(toolsList, s.tools)
		s.mu.RUnlock()
		result, _ := json.Marshal(map[string]any{"tools": toolsList})
		resp.Result = result

	case "tools/call":
		result, err := s.callTool(ctx, req.Params)
		if err != nil {
			resp.Error = &rpcError{Code: -32000, Message: err.Error()}
		} else {
			resp.Result = result
		}

	default:
		resp.Error = &rpcError{Code: -32601, Message: "method not found: " + req.Method}
	}
	return resp
}

func (s *Server) callTool(ctx context.Context, params json.RawMessage) (json.RawMessage, error) {
	var p struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
		Meta      map[string]any  `json:"_meta,omitempty"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("invalid params: %w", err)
	}

	s.mu.RLock()
	h, ok := s.handlers[p.Name]
	s.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("unknown tool: %s", p.Name)
	}

	out, err := h(ctx, p.Arguments, p.Meta)
	if err != nil {
		// Return error as a tool-level error result rather than RPC error
		// so the LLM sees it. This matches MCP convention.
		errResult, _ := json.Marshal(map[string]any{
			"content": []map[string]any{
				{"type": "text", "text": "error: " + err.Error()},
			},
			"isError": true,
		})
		return errResult, nil
	}

	// Wrap successful results in MCP content array.
	body, err := json.Marshal(out)
	if err != nil {
		return nil, err
	}
	result, _ := json.Marshal(map[string]any{
		"content": []map[string]any{
			{"type": "text", "text": string(body)},
		},
	})
	return result, nil
}

// Errors callers can return from handlers to signal specific conditions.
var (
	ErrMissingSHARP = errors.New("missing SHARP context")
)
