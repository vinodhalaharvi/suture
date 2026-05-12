package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/vinodhalaharvi/suture/internal/fhir"
	"github.com/vinodhalaharvi/suture/internal/mcp"
	"github.com/vinodhalaharvi/suture/pkg/tools"
)

// promptOpinionClient is a small test client that mimics how Prompt
// Opinion's platform calls an MCP server over HTTP, including the
// FHIR context headers from their published spec:
//
//	X-FHIR-Server-URL
//	X-FHIR-Access-Token
//	X-Patient-ID
//
// If our server passes against this client, we know it'll work against
// the real platform — the wire shape, header set, and JSON-RPC frames
// are exactly what their docs prescribe.
type promptOpinionClient struct {
	baseURL string
	http    *http.Client
}

func newPromptOpinionClient(baseURL string) *promptOpinionClient {
	return &promptOpinionClient{baseURL: baseURL, http: &http.Client{}}
}

func (p *promptOpinionClient) initializeReq(t *testing.T) map[string]any {
	t.Helper()
	return p.post(t, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`, nil)
}

func (p *promptOpinionClient) listTools(t *testing.T) map[string]any {
	t.Helper()
	return p.post(t, `{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}`, nil)
}

func (p *promptOpinionClient) callTool(t *testing.T, name string, args any, fhirURL, token, patientID string) map[string]any {
	t.Helper()
	argBytes, _ := json.Marshal(args)
	body := fmt.Sprintf(
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":%q,"arguments":%s}}`,
		name, string(argBytes),
	)
	headers := map[string]string{}
	if fhirURL != "" {
		headers["X-FHIR-Server-URL"] = fhirURL
	}
	if token != "" {
		headers["X-FHIR-Access-Token"] = token
	}
	if patientID != "" {
		headers["X-Patient-ID"] = patientID
	}
	return p.post(t, body, headers)
}

func (p *promptOpinionClient) post(t *testing.T, body string, headers map[string]string) map[string]any {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, p.baseURL+"/mcp", strings.NewReader(body))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := p.http.Do(req)
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()
	bodyBytes, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("non-200 status %d: %s", resp.StatusCode, bodyBytes)
	}
	var out map[string]any
	if err := json.Unmarshal(bodyBytes, &out); err != nil {
		t.Fatalf("decode response: %v\nbody: %s", err, bodyBytes)
	}
	return out
}

// === Tests ===

// TestEndToEnd_InitializeAdvertisesFHIRContextExtension verifies that
// our initialize response includes the
//
//	capabilities.extensions["ai.promptopinion/fhir-context"]
//
// extension with the SMART scopes we declared during registration.
// This is what Prompt Opinion looks for to decide whether to display
// the FHIR-context authorization flow to the user.
func TestEndToEnd_InitializeAdvertisesFHIRContextExtension(t *testing.T) {
	srv := setupServerForTest(t)
	defer srv.Close()
	client := newPromptOpinionClient(srv.URL)

	resp := client.initializeReq(t)
	if resp["error"] != nil {
		t.Fatalf("initialize error: %+v", resp["error"])
	}
	result, ok := resp["result"].(map[string]any)
	if !ok {
		t.Fatalf("no result: %+v", resp)
	}
	caps, ok := result["capabilities"].(map[string]any)
	if !ok {
		t.Fatalf("no capabilities: %+v", result)
	}
	exts, ok := caps["extensions"].(map[string]any)
	if !ok {
		t.Fatalf("no extensions advertised: %+v", caps)
	}
	fhirExt, ok := exts["ai.promptopinion/fhir-context"].(map[string]any)
	if !ok {
		t.Fatalf("FHIR context extension not declared: %+v", exts)
	}
	scopes, ok := fhirExt["scopes"].([]any)
	if !ok || len(scopes) == 0 {
		t.Fatalf("no scopes declared: %+v", fhirExt)
	}
	// Verify the scopes we actually need are present.
	scopeNames := map[string]bool{}
	for _, s := range scopes {
		m, _ := s.(map[string]any)
		name, _ := m["name"].(string)
		scopeNames[name] = true
	}
	for _, required := range []string{
		"patient/Patient.rs",
		"patient/Condition.rs",
	} {
		if !scopeNames[required] {
			t.Errorf("missing required scope %q: got %v", required, scopeNames)
		}
	}
}

// TestEndToEnd_PatientSummaryFromHTTPHeaders is the headline test: it
// proves that when Prompt Opinion sends X-FHIR-Server-URL,
// X-FHIR-Access-Token, X-Patient-ID on a tools/call POST, our server
// (a) extracts them, (b) routes them through the FHIR context
// middleware, (c) the FHIR client uses them to make an authenticated
// request, and (d) the tool returns the expected output.
func TestEndToEnd_PatientSummaryFromHTTPHeaders(t *testing.T) {
	fhirSrv := newFakeFHIRServer(t)
	defer fhirSrv.Close()

	srv := setupServerForTest(t)
	defer srv.Close()
	client := newPromptOpinionClient(srv.URL)

	// First, list tools (sanity check).
	listResp := client.listTools(t)
	listResult, _ := listResp["result"].(map[string]any)
	toolsList, _ := listResult["tools"].([]any)
	if len(toolsList) < 3 {
		t.Errorf("expected ≥3 tools, got %d", len(toolsList))
	}

	// Now call get_patient_summary with the actual Prompt Opinion headers.
	resp := client.callTool(t, "get_patient_summary", struct{}{}, fhirSrv.URL, "test-tok", "p1")
	result, ok := resp["result"].(map[string]any)
	if !ok {
		t.Fatalf("no result: %+v", resp)
	}
	if result["isError"] == true {
		t.Fatalf("tool returned isError: %+v", result)
	}
	content, _ := result["content"].([]any)
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

	// Verify the FHIR server saw the Authorization header.
	if !fhirSrv.SawAuthHeader("Bearer test-tok") {
		t.Errorf("FHIR server did not receive bearer token; saw: %v", fhirSrv.AuthHeaders())
	}
}

// TestEndToEnd_MissingHeadersReturnsToolError verifies that a tools/call
// without the FHIR context headers fails cleanly (not by silently
// calling a wrong FHIR endpoint).
func TestEndToEnd_MissingHeadersReturnsToolError(t *testing.T) {
	srv := setupServerForTest(t)
	defer srv.Close()
	client := newPromptOpinionClient(srv.URL)

	resp := client.callTool(t, "get_patient_summary", struct{}{}, "", "", "")
	result, _ := resp["result"].(map[string]any)
	if result == nil || result["isError"] != true {
		t.Errorf("expected isError result, got %+v", resp)
	}
}

// TestEndToEnd_TokenOptional verifies that when no X-FHIR-Access-Token
// is provided (the spec says this is allowed for FHIR servers that
// don't require auth), the tool still runs and the FHIR call goes out
// without an Authorization header.
func TestEndToEnd_TokenOptional(t *testing.T) {
	fhirSrv := newFakeFHIRServer(t)
	defer fhirSrv.Close()

	srv := setupServerForTest(t)
	defer srv.Close()
	client := newPromptOpinionClient(srv.URL)

	// No token — should still work.
	resp := client.callTool(t, "get_patient_summary", struct{}{}, fhirSrv.URL, "", "p1")
	result, _ := resp["result"].(map[string]any)
	if result == nil {
		t.Fatalf("no result: %+v", resp)
	}
	if result["isError"] == true {
		t.Fatalf("token-optional path returned error: %+v", result)
	}
	// Verify no Authorization header was sent.
	for _, h := range fhirSrv.AuthHeaders() {
		if h != "" {
			t.Errorf("token was empty but FHIR request had Authorization: %q", h)
		}
	}
}

// TestEndToEnd_HealthCheck verifies the /healthz endpoint works (deploy
// platforms need this).
func TestEndToEnd_HealthCheck(t *testing.T) {
	srv := setupServerForTest(t)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/healthz")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("healthz status: %d", resp.StatusCode)
	}
}

// === Test scaffolding ===

// setupServerForTest stands up an HTTP MCP server with Suture's tools
// registered.
func setupServerForTest(t *testing.T) *httptest.Server {
	t.Helper()
	s := mcp.NewServer("suture-test", "0.0.0")
	c := fhir.NewClient()
	tools.PatientSummaryTool(s, c)
	tools.CHA2DS2VAScTools(s, c)
	tools.ChartReviewTool(s, c)

	s.RequestFHIRScope("patient/Patient.rs", true)
	s.RequestFHIRScope("patient/Condition.rs", true)
	s.RequestFHIRScope("patient/Encounter.rs", false)
	s.RequestFHIRScope("patient/Observation.rs", false)

	mux := http.NewServeMux()
	mux.Handle("/mcp", s)
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	return httptest.NewServer(mux)
}

// fakeFHIRServer records the Authorization headers it sees so tests
// can verify that token propagation works correctly.
type fakeFHIRServer struct {
	*httptest.Server
	authMu   sync.Mutex
	authHdrs []string
}

func newFakeFHIRServer(t *testing.T) *fakeFHIRServer {
	t.Helper()
	fs := &fakeFHIRServer{}
	fs.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fs.authMu.Lock()
		fs.authHdrs = append(fs.authHdrs, r.Header.Get("Authorization"))
		fs.authMu.Unlock()

		w.Header().Set("Content-Type", "application/fhir+json")
		switch {
		case strings.HasPrefix(r.URL.Path, "/Patient/"):
			_, _ = w.Write([]byte(`{"resourceType":"Patient","id":"p1","name":[{"text":"Test User"}],"gender":"male","birthDate":"1970-01-01"}`))
		case r.URL.Path == "/Condition":
			_, _ = w.Write([]byte(`{"resourceType":"Bundle","total":1,"entry":[{"resource":{"resourceType":"Condition","code":{"text":"Hypertension","coding":[{"system":"http://hl7.org/fhir/sid/icd-10-cm","code":"I10"}]}}}]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	return fs
}

func (f *fakeFHIRServer) SawAuthHeader(want string) bool {
	f.authMu.Lock()
	defer f.authMu.Unlock()
	for _, h := range f.authHdrs {
		if h == want {
			return true
		}
	}
	return false
}

func (f *fakeFHIRServer) AuthHeaders() []string {
	f.authMu.Lock()
	defer f.authMu.Unlock()
	out := make([]string, len(f.authHdrs))
	copy(out, f.authHdrs)
	return out
}

// Unused but kept available for explicit context if some future test needs it.
var _ = context.Background
