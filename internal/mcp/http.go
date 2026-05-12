package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"
)

// HTTPHeaders is the type used to pass HTTP headers from the transport
// layer through to tool handlers. It's a string map (case-canonical via
// http.CanonicalHeaderKey) rather than http.Header so the handler
// signature stays transport-agnostic — a future stdio transport that
// carries headers via some other mechanism could populate the same map.
type HTTPHeaders map[string]string

// httpHeadersCtxKey is the context.Context key under which we stash
// HTTP headers for the duration of a request. Tool handlers should not
// reach for this directly; they should read context via the higher-level
// fhircontext package which knows the specific header names.
type httpHeadersCtxKey struct{}

// WithHTTPHeaders puts request headers into a context so downstream
// middleware (e.g., fhircontext.FromHTTP) can extract what it needs.
func WithHTTPHeaders(ctx context.Context, h HTTPHeaders) context.Context {
	return context.WithValue(ctx, httpHeadersCtxKey{}, h)
}

// HTTPHeadersFrom retrieves request headers from a context. Returns
// an empty map if none were attached (e.g., the server was driven via
// stdio rather than HTTP).
func HTTPHeadersFrom(ctx context.Context) HTTPHeaders {
	h, _ := ctx.Value(httpHeadersCtxKey{}).(HTTPHeaders)
	if h == nil {
		return HTTPHeaders{}
	}
	return h
}

// ServeHTTP exposes the MCP server over a single HTTP endpoint as the
// MCP Streamable HTTP transport prescribes. Clients POST JSON-RPC
// messages to the endpoint and receive a single JSON-RPC response in
// the response body (we don't implement SSE streaming because all of
// Suture's tools are synchronous and complete within a single HTTP
// roundtrip).
//
// Each incoming POST request carries any HTTP headers Prompt Opinion
// has attached for FHIR context propagation (X-FHIR-Server-URL,
// X-FHIR-Access-Token, X-Patient-ID, etc.). We stash a snapshot of
// the relevant headers in the per-request context.Context so tool
// handlers can read them without depending on http.Request directly.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Allow browser-based clients (like the operator console demo) to
	// hit us from any origin. The Prompt Opinion platform speaks
	// server-to-server so it doesn't care about this header, but local
	// UIs do — and CORS only kicks in for response reads in browsers,
	// not for curl or server-side fetches.
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "POST, GET, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "*")

	switch r.Method {
	case http.MethodPost:
		s.serveHTTPPost(w, r)
	case http.MethodGet:
		// Some MCP clients GET the endpoint as a health check.
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{
			"server":    s.name,
			"version":   s.version,
			"transport": "http",
		})
	case http.MethodOptions:
		// CORS preflight — headers set above are sufficient.
		w.WriteHeader(http.StatusNoContent)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// serveHTTPPost reads a single JSON-RPC message from the request body,
// dispatches it through the standard handler, and writes the response.
func (s *Server) serveHTTPPost(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 4*1024*1024))
	if err != nil {
		writeRPCError(w, nil, -32700, "read body: "+err.Error())
		return
	}

	var req request
	if err := json.Unmarshal(body, &req); err != nil {
		writeRPCError(w, nil, -32700, "parse error: "+err.Error())
		return
	}

	// Stash headers (only the ones we care about, to keep the context
	// payload small) so middleware downstream can pull them out.
	headers := HTTPHeaders{}
	for _, k := range []string{
		"X-Fhir-Server-Url",
		"X-Fhir-Access-Token",
		"X-Fhir-Refresh-Token",
		"X-Fhir-Refresh-Url",
		"X-Patient-Id",
	} {
		if v := r.Header.Get(k); v != "" {
			headers[http.CanonicalHeaderKey(k)] = v
		}
	}
	ctx := WithHTTPHeaders(r.Context(), headers)

	resp := s.handle(ctx, req)
	if req.ID == nil {
		// Notification — no response body. MCP spec says 202.
		w.WriteHeader(http.StatusAccepted)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(resp); err != nil {
		// At this point headers are already flushed; just log via stderr
		// in the caller. Nothing useful to do for the client.
		fmt.Fprintf(io.Discard, "encode resp: %v", err)
	}
}

func writeRPCError(w http.ResponseWriter, id json.RawMessage, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK) // JSON-RPC errors travel inside the 200 envelope
	_ = json.NewEncoder(w).Encode(response{
		JSONRPC: "2.0",
		ID:      id,
		Error:   &rpcError{Code: code, Message: msg},
	})
}

// ListenAndServe starts an HTTP server bound to addr (e.g. ":8080") and
// serves the MCP endpoint at path (typically "/mcp"). Returns when the
// context is cancelled or the underlying http.Server errors.
func (s *Server) ListenAndServe(ctx context.Context, addr, path string) error {
	mux := http.NewServeMux()
	mux.Handle(path, s)
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})

	srv := &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}

	// Shutdown when context cancels.
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}()

	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		wg.Wait()
		return err
	}
	wg.Wait()
	return nil
}
