package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/vinodhalaharvi/suture/internal/fhir"
	"github.com/vinodhalaharvi/suture/internal/mcp"
	"github.com/vinodhalaharvi/suture/pkg/tools"
)

// safeBuffer is a thread-safe bytes.Buffer wrapper, identical in spirit
// to the one in internal/mcp tests. Used because Serve writes to the
// buffer from goroutines while the test inspects it from the main one.
type safeBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (s *safeBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.Write(p)
}

func (s *safeBuffer) Bytes() []byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]byte, s.buf.Len())
	copy(out, s.buf.Bytes())
	return out
}

func (s *safeBuffer) String() string {
	return string(s.Bytes())
}

// TestEndToEnd_PatientSummaryThroughMCP exercises the full stack:
// JSON-RPC on the wire → MCP server → SHARP middleware → weft arrow →
// fake FHIR server. If this passes, the integration story is real.
func TestEndToEnd_PatientSummaryThroughMCP(t *testing.T) {
	// 1. Fake FHIR server.
	fhirSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/fhir+json")
		if strings.HasPrefix(r.URL.Path, "/Patient/") {
			_, _ = w.Write([]byte(`{"resourceType":"Patient","id":"p1","name":[{"text":"Test User"}],"gender":"male","birthDate":"1970-01-01"}`))
			return
		}
		if r.URL.Path == "/Condition" {
			_, _ = w.Write([]byte(`{"resourceType":"Bundle","total":1,"entry":[{"resource":{"resourceType":"Condition","code":{"text":"Hypertension","coding":[{"system":"http://hl7.org/fhir/sid/icd-10-cm","code":"I10"}]}}}]}`))
			return
		}
		http.NotFound(w, r)
	}))
	defer fhirSrv.Close()

	// 2. Construct the MCP server, register tools.
	s := mcp.NewServer("test", "0.0.0")
	c := fhir.NewClient()
	tools.PatientSummaryTool(s, c)
	tools.CHA2DS2VAScTools(s, c)

	// 3. Send JSON-RPC frames through Serve.
	frames := []string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"get_patient_summary","arguments":{},"_meta":{"sharp.patient_id":"p1","sharp.fhir_base":"` + fhirSrv.URL + `","sharp.token":"tok"}}}`,
	}
	in := strings.NewReader(strings.Join(frames, "\n") + "\n")
	out := &safeBuffer{}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	done := make(chan struct{})
	go func() { _ = s.Serve(ctx, in, out); close(done) }()
	// Wait until 3 responses arrive.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if countNewlineDelimited(out.Bytes()) >= 3 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	<-done

	lines := splitNonEmpty(out.String())
	if len(lines) < 3 {
		t.Fatalf("expected 3 responses, got %d:\n%s", len(lines), out.String())
	}

	// Index by id.
	responses := map[float64]map[string]any{}
	for _, line := range lines {
		var r map[string]any
		if err := json.Unmarshal([]byte(line), &r); err != nil {
			continue
		}
		if id, ok := r["id"].(float64); ok {
			responses[id] = r
		}
	}

	// initialize: capabilities present.
	init := responses[1]
	if init == nil {
		t.Fatal("no response for initialize")
	}

	// tools/list: at least 3 tools (get_patient_summary + 2 cha2ds2 tools).
	list, _ := responses[2]["result"].(map[string]any)
	listed, _ := list["tools"].([]any)
	if len(listed) < 3 {
		t.Errorf("tools/list returned %d tools", len(listed))
	}

	// tools/call: should have content array with a JSON payload.
	callResult, _ := responses[3]["result"].(map[string]any)
	if callResult == nil {
		t.Fatalf("tools/call returned no result: %+v", responses[3])
	}
	if callResult["isError"] == true {
		t.Fatalf("tools/call returned isError: %+v", callResult)
	}
	content, _ := callResult["content"].([]any)
	if len(content) == 0 {
		t.Fatal("empty content")
	}
	first, _ := content[0].(map[string]any)
	text, _ := first["text"].(string)
	var payload tools.PatientSummaryOut
	if err := json.Unmarshal([]byte(text), &payload); err != nil {
		t.Fatalf("decode payload: %v\nraw: %s", err, text)
	}
	if payload.Name != "Test User" {
		t.Errorf("payload.Name = %q", payload.Name)
	}
	if len(payload.ActiveIssues) != 1 || payload.ActiveIssues[0] != "Hypertension" {
		t.Errorf("issues: %+v", payload.ActiveIssues)
	}
}

// TestEndToEnd_MissingSHARPReturnsToolError verifies the SHARP
// middleware surfaces a clean tool-level error (not an RPC error)
// when SHARP context is missing.
func TestEndToEnd_MissingSHARPReturnsToolError(t *testing.T) {
	s := mcp.NewServer("test", "0.0.0")
	tools.PatientSummaryTool(s, fhir.NewClient())

	frame := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"get_patient_summary","arguments":{}}}` + "\n"
	out := &safeBuffer{}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	go func() { _ = s.Serve(ctx, strings.NewReader(frame), out) }()
	deadline := time.Now().Add(1 * time.Second)
	for time.Now().Before(deadline) {
		if countNewlineDelimited(out.Bytes()) >= 1 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	cancel()

	var resp map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(out.Bytes()), &resp); err != nil {
		t.Fatalf("decode: %v\nraw: %s", err, out.String())
	}
	result, _ := resp["result"].(map[string]any)
	if result == nil || result["isError"] != true {
		t.Errorf("expected isError result, got %+v", resp)
	}
}

func countNewlineDelimited(b []byte) int {
	n := 0
	for _, l := range bytes.Split(b, []byte("\n")) {
		if len(bytes.TrimSpace(l)) > 0 {
			n++
		}
	}
	return n
}

func splitNonEmpty(s string) []string {
	var out []string
	for _, l := range strings.Split(s, "\n") {
		if strings.TrimSpace(l) != "" {
			out = append(out, l)
		}
	}
	return out
}
