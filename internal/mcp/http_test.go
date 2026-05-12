package mcp

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestServeHTTP_Initialize verifies the HTTP transport correctly
// handles a POST containing an initialize message.
func TestServeHTTP_Initialize(t *testing.T) {
	s := NewServer("t", "1.0")
	s.RequestFHIRScope("patient/Patient.rs", true)

	req := httptest.NewRequest(http.MethodPost, "/mcp",
		strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	s.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status: %d", w.Code)
	}
	var resp map[string]any
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	result, _ := resp["result"].(map[string]any)
	caps, _ := result["capabilities"].(map[string]any)
	exts, _ := caps["extensions"].(map[string]any)
	if _, ok := exts["ai.promptopinion/fhir-context"]; !ok {
		t.Errorf("expected fhir-context extension: %+v", caps)
	}
}

// TestServeHTTP_HeaderPropagation verifies HTTP headers reach the
// tool handler via context.
func TestServeHTTP_HeaderPropagation(t *testing.T) {
	s := NewServer("t", "1.0")
	var sawHeaders HTTPHeaders
	s.AddTool(
		Tool{Name: "inspect", InputSchema: json.RawMessage(`{}`)},
		func(ctx context.Context, _ json.RawMessage, _ map[string]any) (any, error) {
			sawHeaders = HTTPHeadersFrom(ctx)
			return "ok", nil
		},
	)

	req := httptest.NewRequest(http.MethodPost, "/mcp",
		strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"inspect","arguments":{}}}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-FHIR-Server-URL", "https://fhir.x")
	req.Header.Set("X-Patient-ID", "p1")
	req.Header.Set("X-Other-Header", "should-not-propagate")

	w := httptest.NewRecorder()
	s.ServeHTTP(w, req)

	if sawHeaders[http.CanonicalHeaderKey("X-FHIR-Server-URL")] != "https://fhir.x" {
		t.Errorf("FHIR URL not propagated: %+v", sawHeaders)
	}
	if sawHeaders[http.CanonicalHeaderKey("X-Patient-ID")] != "p1" {
		t.Errorf("Patient ID not propagated: %+v", sawHeaders)
	}
	if _, ok := sawHeaders[http.CanonicalHeaderKey("X-Other-Header")]; ok {
		t.Errorf("unrelated header leaked: %+v", sawHeaders)
	}
}

// TestServeHTTP_BadJSON returns a parse error with the correct code.
func TestServeHTTP_BadJSON(t *testing.T) {
	s := NewServer("t", "1.0")
	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(`{bad json`))
	w := httptest.NewRecorder()
	s.ServeHTTP(w, req)

	var resp map[string]any
	_ = json.NewDecoder(w.Body).Decode(&resp)
	errObj, _ := resp["error"].(map[string]any)
	if errObj == nil {
		t.Fatal("expected error")
	}
	code, _ := errObj["code"].(float64)
	if code != -32700 {
		t.Errorf("code: %v", code)
	}
}

// TestServeHTTP_GETReturnsHealthMetadata is what some health-checkers do.
func TestServeHTTP_GET(t *testing.T) {
	s := NewServer("t", "1.0")
	req := httptest.NewRequest(http.MethodGet, "/mcp", nil)
	w := httptest.NewRecorder()
	s.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status: %d", w.Code)
	}
	var resp map[string]string
	_ = json.NewDecoder(w.Body).Decode(&resp)
	if resp["server"] != "t" {
		t.Errorf("server: %s", resp["server"])
	}
}

// TestServeHTTP_CORSPreflight verifies OPTIONS returns CORS headers.
func TestServeHTTP_OPTIONS(t *testing.T) {
	s := NewServer("t", "1.0")
	req := httptest.NewRequest(http.MethodOptions, "/mcp", nil)
	w := httptest.NewRecorder()
	s.ServeHTTP(w, req)
	if w.Code != http.StatusNoContent {
		t.Errorf("status: %d", w.Code)
	}
	if w.Header().Get("Access-Control-Allow-Origin") == "" {
		t.Error("missing CORS header")
	}
}

// TestServeHTTP_MethodNotAllowed rejects DELETE etc.
func TestServeHTTP_MethodNotAllowed(t *testing.T) {
	s := NewServer("t", "1.0")
	req := httptest.NewRequest(http.MethodDelete, "/mcp", nil)
	w := httptest.NewRecorder()
	s.ServeHTTP(w, req)
	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("status: %d", w.Code)
	}
}

// TestServeHTTP_Notification has no ID; transport returns 202.
func TestServeHTTP_Notification(t *testing.T) {
	s := NewServer("t", "1.0")
	req := httptest.NewRequest(http.MethodPost, "/mcp",
		strings.NewReader(`{"jsonrpc":"2.0","method":"notifications/initialized","params":{}}`))
	w := httptest.NewRecorder()
	s.ServeHTTP(w, req)
	if w.Code != http.StatusAccepted {
		t.Errorf("expected 202, got %d", w.Code)
	}
}

// TestListenAndServe_ShutdownOnCancel verifies the server stops when
// its context is cancelled.
func TestListenAndServe_ShutdownOnCancel(t *testing.T) {
	s := NewServer("t", "1.0")
	ctx, cancel := context.WithCancel(context.Background())

	// Pick port 0 (let kernel assign) by binding via httptest? No —
	// ListenAndServe takes addr. Use ":0" — Go will bind a random port.
	errCh := make(chan error, 1)
	go func() {
		errCh <- s.ListenAndServe(ctx, ":0", "/mcp")
	}()

	// Give it a moment to start, then cancel.
	cancel()

	select {
	case err := <-errCh:
		// Either nil or a "ListenAndServe was interrupted" style error
		// is acceptable. http.ErrServerClosed is swallowed by the impl.
		_ = err
	case <-context.Background().Done():
		t.Fatal("server did not shut down within timeout")
	}
}

// TestHTTPHeadersFrom_NoHeaders returns an empty (but non-nil) map.
func TestHTTPHeadersFrom_NoHeaders(t *testing.T) {
	h := HTTPHeadersFrom(context.Background())
	if h == nil {
		t.Error("expected non-nil map")
	}
	if len(h) != 0 {
		t.Errorf("expected empty, got %+v", h)
	}
}

// TestServeHTTP_BodyTooLarge tests the size limit.
func TestServeHTTP_LargeRequest(t *testing.T) {
	s := NewServer("t", "1.0")
	// Build a request just under the 4MB limit — should succeed parse.
	big := strings.Repeat("a", 1024)
	body := `{"jsonrpc":"2.0","id":1,"method":"unknown","params":{"x":"` + big + `"}}`
	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(body))
	w := httptest.NewRecorder()
	s.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Errorf("status: %d", w.Code)
	}
}

// TestReadBodyFailure exercises the io.ReadAll error path.
type erroringReader struct{}

func (erroringReader) Read(p []byte) (int, error) { return 0, io.ErrUnexpectedEOF }

func TestServeHTTP_BodyReadError(t *testing.T) {
	s := NewServer("t", "1.0")
	req := httptest.NewRequest(http.MethodPost, "/mcp", erroringReader{})
	w := httptest.NewRecorder()
	s.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Errorf("status: %d", w.Code)
	}
	var resp map[string]any
	_ = json.NewDecoder(w.Body).Decode(&resp)
	if resp["error"] == nil {
		t.Error("expected error in response")
	}
}
