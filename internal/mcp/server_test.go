package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
)

// safeBuffer is a thread-safe wrapper around bytes.Buffer for tests
// that write from one goroutine and read snapshots from another.
type safeBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (s *safeBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.Write(p)
}

func (s *safeBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.String()
}

func (s *safeBuffer) Bytes() []byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]byte, s.buf.Len())
	copy(out, s.buf.Bytes())
	return out
}

// readerWith returns an io.Reader from the given lines.
func readerWith(lines ...string) io.Reader {
	return strings.NewReader(strings.Join(lines, "\n") + "\n")
}

func TestInitialize(t *testing.T) {
	s := NewServer("test", "0.1.0")
	in := readerWith(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`)
	out := &safeBuffer{}

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		_ = s.Serve(ctx, in, out)
	}()
	// Allow goroutine to run.
	waitForBuf(t, out, 1)
	cancel()

	var resp response
	if err := json.Unmarshal(out.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Error != nil {
		t.Fatalf("got error: %+v", resp.Error)
	}
	var r map[string]any
	if err := json.Unmarshal(resp.Result, &r); err != nil {
		t.Fatalf("decode result: %v", err)
	}
	if r["protocolVersion"] != "2024-11-05" {
		t.Errorf("bad protocolVersion: %v", r["protocolVersion"])
	}
}

func TestToolsListAndCall(t *testing.T) {
	s := NewServer("test", "0.1.0")
	called := false
	var sawMeta map[string]any
	s.AddTool(
		Tool{Name: "echo", Description: "echoes", InputSchema: json.RawMessage(`{"type":"object"}`)},
		func(ctx context.Context, args json.RawMessage, meta map[string]any) (any, error) {
			called = true
			sawMeta = meta
			var in map[string]string
			_ = json.Unmarshal(args, &in)
			return map[string]string{"echo": in["msg"]}, nil
		},
	)

	in := strings.NewReader(
		`{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}` + "\n" +
			`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"echo","arguments":{"msg":"hi"},"_meta":{"sharp.patient_id":"abc"}}}` + "\n",
	)
	out := &safeBuffer{}
	ctx, cancel := context.WithCancel(context.Background())
	go func() { _ = s.Serve(ctx, in, out) }()
	waitForBuf(t, out, 2)
	cancel()

	lines := splitLines(out.String())
	if len(lines) < 2 {
		t.Fatalf("expected 2 responses, got %d:\n%s", len(lines), out.String())
	}

	// Pull responses by ID since they may arrive out of order (goroutines).
	respByID := map[float64]response{}
	for _, l := range lines {
		var r response
		if err := json.Unmarshal([]byte(l), &r); err == nil {
			var id float64
			_ = json.Unmarshal(r.ID, &id)
			respByID[id] = r
		}
	}

	// tools/list result should contain our tool.
	var listResult struct {
		Tools []Tool `json:"tools"`
	}
	if err := json.Unmarshal(respByID[1].Result, &listResult); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	if len(listResult.Tools) != 1 || listResult.Tools[0].Name != "echo" {
		t.Errorf("expected echo tool, got %+v", listResult.Tools)
	}

	// tools/call should have invoked the handler.
	if !called {
		t.Error("handler not called")
	}
	if sawMeta["sharp.patient_id"] != "abc" {
		t.Errorf("meta not forwarded: %+v", sawMeta)
	}
}

func TestUnknownTool(t *testing.T) {
	s := NewServer("test", "0.1.0")
	in := strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"nope","arguments":{}}}` + "\n")
	out := &safeBuffer{}
	ctx, cancel := context.WithCancel(context.Background())
	go func() { _ = s.Serve(ctx, in, out) }()
	waitForBuf(t, out, 1)
	cancel()

	var resp response
	if err := json.Unmarshal(out.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Error == nil {
		t.Fatal("expected RPC error for unknown tool")
	}
}

func TestHandlerErrorBecomesIsError(t *testing.T) {
	s := NewServer("test", "0.1.0")
	s.AddTool(
		Tool{Name: "fail", Description: "fails", InputSchema: json.RawMessage(`{"type":"object"}`)},
		func(ctx context.Context, args json.RawMessage, meta map[string]any) (any, error) {
			return nil, errors.New("boom")
		},
	)
	in := strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"fail","arguments":{}}}` + "\n")
	out := &safeBuffer{}
	ctx, cancel := context.WithCancel(context.Background())
	go func() { _ = s.Serve(ctx, in, out) }()
	waitForBuf(t, out, 1)
	cancel()

	var resp response
	if err := json.Unmarshal(out.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	// Handler errors come back as a successful response with isError:true content.
	if resp.Error != nil {
		t.Fatalf("did not expect RPC error: %+v", resp.Error)
	}
	var r map[string]any
	if err := json.Unmarshal(resp.Result, &r); err != nil {
		t.Fatalf("decode result: %v", err)
	}
	if r["isError"] != true {
		t.Errorf("expected isError=true, got %+v", r)
	}
}

func TestBadJSON(t *testing.T) {
	s := NewServer("test", "0.1.0")
	in := strings.NewReader("not json\n")
	out := &safeBuffer{}
	ctx, cancel := context.WithCancel(context.Background())
	go func() { _ = s.Serve(ctx, in, out) }()
	waitForBuf(t, out, 1)
	cancel()

	var resp response
	if err := json.Unmarshal(out.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Error == nil || resp.Error.Code != -32700 {
		t.Errorf("expected parse error code -32700, got %+v", resp.Error)
	}
}

func TestRegistration(t *testing.T) {
	s := NewServer("t", "1")
	s.AddTool(Tool{Name: "a"}, func(ctx context.Context, _ json.RawMessage, _ map[string]any) (any, error) { return nil, nil })
	s.AddTool(Tool{Name: "b"}, func(ctx context.Context, _ json.RawMessage, _ map[string]any) (any, error) { return nil, nil })
	if got := len(s.Tools()); got != 2 {
		t.Errorf("expected 2 tools, got %d", got)
	}
}

// --- helpers ---

func splitLines(s string) []string {
	var out []string
	for _, line := range bytes.Split([]byte(s), []byte("\n")) {
		if len(line) > 0 {
			out = append(out, string(line))
		}
	}
	return out
}

func waitForBuf(t *testing.T, buf *safeBuffer, expectedLines int) {
	t.Helper()
	for i := 0; i < 200; i++ {
		if countLines(buf.String()) >= expectedLines {
			return
		}
		sleep1ms()
	}
}

func countLines(s string) int {
	n := 0
	for _, l := range bytes.Split([]byte(s), []byte("\n")) {
		if len(l) > 0 {
			n++
		}
	}
	return n
}
