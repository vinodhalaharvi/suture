package tools

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/vinodhalaharvi/suture/internal/fhir"
	"github.com/vinodhalaharvi/suture/internal/mcp"
	"github.com/vinodhalaharvi/suture/internal/sharp"
)

// fakeFHIRServer returns realistic FHIR responses for tool tests.
// Different from internal/fhir/fhir_test.go because here we use it to
// exercise composed tool arrows end-to-end.
func fakeFHIRServer(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/fhir+json")
		switch {
		case strings.HasPrefix(r.URL.Path, "/Patient/"):
			_, _ = w.Write([]byte(`{
				"resourceType":"Patient","id":"p1",
				"name":[{"text":"Jane Smith"}],
				"birthDate":"1950-01-01","gender":"female"
			}`))
		case r.URL.Path == "/Condition":
			_, _ = w.Write([]byte(`{
				"resourceType":"Bundle","type":"searchset","total":3,
				"entry":[
					{"resource":{"resourceType":"Condition","code":{
						"text":"Congestive heart failure",
						"coding":[{"system":"http://hl7.org/fhir/sid/icd-10-cm","code":"I50.9"}]}}},
					{"resource":{"resourceType":"Condition","code":{
						"text":"Hypertension",
						"coding":[{"system":"http://hl7.org/fhir/sid/icd-10-cm","code":"I10"}]}}},
					{"resource":{"resourceType":"Condition","code":{
						"text":"Type 2 diabetes",
						"coding":[{"system":"http://hl7.org/fhir/sid/icd-10-cm","code":"E11.9"}]}}}
				]
			}`))
		default:
			http.NotFound(w, r)
		}
	}))
}

func sharpCtx(base string) context.Context {
	return sharp.Inject(context.Background(), sharp.Context{
		PatientID: "p1",
		FHIRBase:  base,
		Token:     "tok",
	})
}

// === get_patient_summary ===

func TestPatientSummaryArrow(t *testing.T) {
	srv := fakeFHIRServer(t)
	defer srv.Close()

	arrow := PatientSummaryArrow(fhir.NewClient())
	out, err := arrow(sharpCtx(srv.URL), PatientSummaryIn{})
	if err != nil {
		t.Fatalf("arrow: %v", err)
	}
	if out.Name != "Jane Smith" {
		t.Errorf("name: got %q", out.Name)
	}
	if out.Gender != "female" {
		t.Errorf("gender: got %q", out.Gender)
	}
	if len(out.ActiveIssues) != 3 {
		t.Errorf("issues: got %d (%v)", len(out.ActiveIssues), out.ActiveIssues)
	}
}

// === CHA2DS2-VASc score ===

func TestCalculateScoreArrow(t *testing.T) {
	srv := fakeFHIRServer(t)
	defer srv.Close()

	arrow := CalculateScoreArrow(fhir.NewClient())
	result, err := arrow(sharpCtx(srv.URL), CHA2DS2VAScIn{})
	if err != nil {
		t.Fatalf("arrow: %v", err)
	}

	// Expected:
	//   CHF (I50)           +1
	//   Hypertension (I10)  +1
	//   Diabetes (E11)      +1
	//   Female              +1
	//   Age (b. 1950 ≥ 75)  +2
	//   = 6, high risk
	if result.Score != 6 {
		t.Errorf("score: got %d, want 6 (%+v)", result.Score, result.Components)
	}
	if result.RiskBand != "high" {
		t.Errorf("band: got %q, want high", result.RiskBand)
	}
	c := result.Components
	if !(c.CHF && c.Hypertension && c.Diabetes && c.Female) {
		t.Errorf("components mismatch: %+v", c)
	}
	if c.StrokeHx || c.Vascular {
		t.Errorf("false positives: %+v", c)
	}
}

func TestGetComponentsArrow(t *testing.T) {
	srv := fakeFHIRServer(t)
	defer srv.Close()

	arrow := GetComponentsArrow(fhir.NewClient())
	c, err := arrow(sharpCtx(srv.URL), CHA2DS2VAScIn{})
	if err != nil {
		t.Fatalf("arrow: %v", err)
	}
	if !c.CHF || !c.Hypertension || !c.Diabetes {
		t.Errorf("components: %+v", c)
	}
}

// === Pure-function unit tests for the scoring rules ===

func TestComputeScore_LowRisk(t *testing.T) {
	r, _ := computeScore(context.Background(), Components{})
	if r.Score != 0 || r.RiskBand != "low" {
		t.Errorf("zero components should be low/0, got %+v", r)
	}
}

func TestComputeScore_AgeStratification(t *testing.T) {
	r1, _ := computeScore(context.Background(), Components{Age: 60})
	if r1.Score != 0 {
		t.Errorf("age 60: %d", r1.Score)
	}
	r2, _ := computeScore(context.Background(), Components{Age: 70})
	if r2.Score != 1 {
		t.Errorf("age 70: %d", r2.Score)
	}
	r3, _ := computeScore(context.Background(), Components{Age: 80})
	if r3.Score != 2 {
		t.Errorf("age 80: %d", r3.Score)
	}
}

func TestComputeScore_StrokeWorthTwo(t *testing.T) {
	r, _ := computeScore(context.Background(), Components{StrokeHx: true})
	if r.Score != 2 {
		t.Errorf("stroke alone should be 2, got %d", r.Score)
	}
}

func TestComputeScore_FemaleOnlyNote(t *testing.T) {
	r, _ := computeScore(context.Background(), Components{Female: true})
	if r.Score != 1 {
		t.Errorf("expected 1, got %d", r.Score)
	}
	if r.Notes == "" {
		t.Error("expected guideline note on sex-only score")
	}
}

func TestComputeScore_RiskBands(t *testing.T) {
	cases := []struct {
		c    Components
		want string
	}{
		{Components{}, "low"},
		{Components{Female: true}, "low"},                                            // 1
		{Components{CHF: true, Hypertension: true}, "moderate"},                      // 2
		{Components{CHF: true, Hypertension: true, Diabetes: true, Age: 70}, "high"}, // 4
	}
	for _, tc := range cases {
		r, _ := computeScore(context.Background(), tc.c)
		if r.RiskBand != tc.want {
			t.Errorf("for %+v: band %q, want %q (score=%d)", tc.c, r.RiskBand, tc.want, r.Score)
		}
	}
}

// === SHARP middleware tests ===

func TestRunWithSharp_MissingContext(t *testing.T) {
	handler := runWithSharp(func(ctx context.Context, _ json.RawMessage) (any, error) {
		return "ok", nil
	})
	_, err := handler(context.Background(), nil, nil)
	if err == nil {
		t.Error("expected error on missing SHARP context")
	}
}

func TestRunWithSharp_InjectsContext(t *testing.T) {
	var sawCtx sharp.Context
	handler := runWithSharp(func(ctx context.Context, _ json.RawMessage) (any, error) {
		s, ok := sharp.From(ctx)
		if !ok {
			t.Error("SHARP not injected")
		}
		sawCtx = s
		return "ok", nil
	})
	meta := map[string]any{
		"sharp.patient_id": "p99",
		"sharp.fhir_base":  "https://x",
		"sharp.token":      "t99",
	}
	_, err := handler(context.Background(), nil, meta)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	if sawCtx.PatientID != "p99" {
		t.Errorf("PatientID not injected: %+v", sawCtx)
	}
}

// === Tool registration smoke test ===

func TestRegisterAllTools(t *testing.T) {
	s := mcp.NewServer("test", "0.1.0")
	c := fhir.NewClient()
	PatientSummaryTool(s, c)
	CHA2DS2VAScTools(s, c)
	tools := s.Tools()
	want := map[string]bool{
		"get_patient_summary":         false,
		"calculate_cha2ds2_vasc":      false,
		"get_cha2ds2_vasc_components": false,
	}
	for _, tl := range tools {
		if _, ok := want[tl.Name]; ok {
			want[tl.Name] = true
		}
	}
	for name, seen := range want {
		if !seen {
			t.Errorf("tool %s not registered", name)
		}
	}
}
