package fhir

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/vinodhalaharvi/suture/internal/fhircontext"
)

// fakeServer returns an httptest.Server that responds to the FHIR
// endpoints we use with canned bundles. It also records the requests
// it received so tests can verify the bearer token was attached.
type fakeServer struct {
	*httptest.Server
	requests []recordedReq
}

type recordedReq struct {
	Path  string
	Query string
	Auth  string
}

func newFakeServer(t *testing.T) *fakeServer {
	t.Helper()
	fs := &fakeServer{}
	fs.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fs.requests = append(fs.requests, recordedReq{
			Path:  r.URL.Path,
			Query: r.URL.RawQuery,
			Auth:  r.Header.Get("Authorization"),
		})
		w.Header().Set("Content-Type", "application/fhir+json")
		switch {
		case strings.HasPrefix(r.URL.Path, "/Patient/"):
			_, _ = w.Write([]byte(`{
				"resourceType":"Patient","id":"p1",
				"name":[{"text":"Jane Doe","family":"Doe","given":["Jane"]}],
				"birthDate":"1955-04-01","gender":"female"
			}`))
		case r.URL.Path == "/Condition":
			_, _ = w.Write([]byte(`{
				"resourceType":"Bundle","type":"searchset","total":2,
				"entry":[
					{"resource":{"resourceType":"Condition","code":{"text":"Heart failure",
						"coding":[{"system":"http://hl7.org/fhir/sid/icd-10-cm","code":"I50.9"}]}}},
					{"resource":{"resourceType":"Condition","code":{"text":"Type 2 diabetes",
						"coding":[{"system":"http://hl7.org/fhir/sid/icd-10-cm","code":"E11.9"}]}}}
				]
			}`))
		case r.URL.Path == "/Observation":
			_, _ = w.Write([]byte(`{"resourceType":"Bundle","type":"searchset","total":0,"entry":[]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	return fs
}

func withSharp(ctx context.Context, base string) context.Context {
	return fhircontext.Inject(ctx, fhircontext.Context{
		PatientID: "p1",
		FHIRBase:  base,
		Token:     "test-token",
	})
}

func TestReadPatient(t *testing.T) {
	fs := newFakeServer(t)
	defer fs.Close()
	c := NewClient()
	ctx := withSharp(context.Background(), fs.URL)

	patient, err := c.ReadPatient()(ctx, struct{}{})
	if err != nil {
		t.Fatalf("ReadPatient: %v", err)
	}
	if PatientName(patient) != "Jane Doe" {
		t.Errorf("name: got %q", PatientName(patient))
	}
	if PatientBirthDate(patient) != "1955-04-01" {
		t.Errorf("dob: got %q", PatientBirthDate(patient))
	}
	if PatientGender(patient) != "female" {
		t.Errorf("gender: got %q", PatientGender(patient))
	}
	if age := PatientAge(patient); age < 60 || age > 120 {
		t.Errorf("age: implausible value %d", age)
	}

	// Verify token was sent.
	if len(fs.requests) != 1 {
		t.Fatalf("expected 1 request, got %d", len(fs.requests))
	}
	if fs.requests[0].Auth != "Bearer test-token" {
		t.Errorf("bad auth: %q", fs.requests[0].Auth)
	}
}

func TestSearchConditions(t *testing.T) {
	fs := newFakeServer(t)
	defer fs.Close()
	c := NewClient()
	ctx := withSharp(context.Background(), fs.URL)

	bundle, err := c.SearchConditions()(ctx, ConditionFilter{ClinicalStatus: "active"})
	if err != nil {
		t.Fatalf("SearchConditions: %v", err)
	}
	if bundle.Total != 2 {
		t.Errorf("total: got %d", bundle.Total)
	}
	if len(bundle.Resources()) != 2 {
		t.Errorf("resources: got %d", len(bundle.Resources()))
	}

	// Verify query had the clinical-status filter and patient ID.
	q := fs.requests[0].Query
	if !strings.Contains(q, "clinical-status=active") {
		t.Errorf("missing filter in query: %s", q)
	}
	if !strings.Contains(q, "patient=p1") {
		t.Errorf("missing patient in query: %s", q)
	}
}

func TestICD10Detection(t *testing.T) {
	c := Resource(map[string]any{
		"code": map[string]any{
			"coding": []any{
				map[string]any{
					"system": "http://hl7.org/fhir/sid/icd-10-cm",
					"code":   "I50.9",
				},
			},
		},
	})
	if !ConditionHasICD10Prefix(c, "I50") {
		t.Error("should detect I50 prefix")
	}
	if ConditionHasICD10Prefix(c, "E11") {
		t.Error("should not detect E11 prefix")
	}
}

func TestConditionDisplay(t *testing.T) {
	c := Resource(map[string]any{
		"code": map[string]any{"text": "Heart failure"},
	})
	if ConditionDisplay(c) != "Heart failure" {
		t.Errorf("bad display")
	}
}

func TestMissingSharp(t *testing.T) {
	c := NewClient()
	_, err := c.ReadPatient()(context.Background(), struct{}{})
	if err == nil {
		t.Error("expected error without SHARP context")
	}
}

func TestFHIRErrorStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"resourceType":"OperationOutcome"}`))
	}))
	defer srv.Close()

	c := NewClient()
	ctx := withSharp(context.Background(), srv.URL)
	_, err := c.ReadPatient()(ctx, struct{}{})
	if err == nil {
		t.Fatal("expected error on 401")
	}
	if !strings.Contains(err.Error(), "401") {
		t.Errorf("expected status in error, got: %v", err)
	}
}

func TestPatientAgeFromMissingBirthDate(t *testing.T) {
	if got := PatientAge(Resource{}); got != 0 {
		t.Errorf("expected 0, got %d", got)
	}
}

// Ensure JSON round-trips don't break (smoke).
func TestResourceJSON(t *testing.T) {
	r := Resource{"resourceType": "Patient", "id": "x"}
	b, _ := json.Marshal(r)
	var back Resource
	_ = json.Unmarshal(b, &back)
	if back["id"] != "x" {
		t.Error("roundtrip")
	}
}
