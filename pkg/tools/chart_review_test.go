package tools

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/vinodhalaharvi/suture/internal/fhir"
	"github.com/vinodhalaharvi/suture/internal/sharp"
)

func fakeEncounterServer(t *testing.T, count int) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/fhir+json")
		if r.URL.Path != "/Encounter" {
			http.NotFound(w, r)
			return
		}
		entries := make([]map[string]any, 0, count)
		for i := 0; i < count; i++ {
			id := encID(i)
			class := "AMB"
			if i%2 == 1 {
				class = "IMP"
			}
			entries = append(entries, map[string]any{
				"resource": map[string]any{
					"resourceType": "Encounter",
					"id":           id,
					"class":        map[string]any{"code": class},
					"period":       map[string]any{"start": "2026-0" + string(rune('1'+i%9)) + "-15"},
					"reasonCode": []any{
						map[string]any{"text": "Visit reason " + id},
					},
				},
			})
		}
		bundle := map[string]any{
			"resourceType": "Bundle",
			"type":         "searchset",
			"total":        count,
			"entry":        entries,
		}
		_ = json.NewEncoder(w).Encode(bundle)
	}))
}

func encID(i int) string {
	return "enc-" + string(rune('a'+i))
}

func TestChartReviewArrow_HappyPath(t *testing.T) {
	srv := fakeEncounterServer(t, 5)
	defer srv.Close()

	arrow := ChartReviewArrow(fhir.NewClient())
	out, err := arrow(sharpCtx(srv.URL), ChartReviewIn{Limit: 5})
	if err != nil {
		t.Fatalf("arrow: %v", err)
	}
	if len(out.Timeline) != 5 {
		t.Fatalf("timeline length: %d, want 5", len(out.Timeline))
	}
	for _, row := range out.Timeline {
		if row.EncounterID == "" {
			t.Errorf("empty encounter id: %+v", row)
		}
		if row.Summary == "" {
			t.Errorf("empty summary: %+v", row)
		}
	}
}

func TestChartReviewArrow_DefaultLimit(t *testing.T) {
	srv := fakeEncounterServer(t, 3)
	defer srv.Close()
	arrow := ChartReviewArrow(fhir.NewClient())
	out, err := arrow(sharpCtx(srv.URL), ChartReviewIn{}) // no limit
	if err != nil {
		t.Fatalf("arrow: %v", err)
	}
	if len(out.Timeline) != 3 {
		t.Errorf("expected 3, got %d", len(out.Timeline))
	}
}

func TestChartReviewArrow_NoEncounters(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"resourceType":"Bundle","type":"searchset","total":0,"entry":[]}`))
	}))
	defer srv.Close()
	arrow := ChartReviewArrow(fhir.NewClient())
	out, err := arrow(sharpCtx(srv.URL), ChartReviewIn{Limit: 5})
	if err != nil {
		t.Fatalf("arrow: %v", err)
	}
	if len(out.Timeline) != 0 {
		t.Errorf("expected empty timeline, got %d", len(out.Timeline))
	}
}

// TestChartReviewArrow_SummarizeOnePure ensures the per-item arrow
// produces the right shape and is deterministic.
func TestSummarizeOnePure(t *testing.T) {
	e := fhir.Resource{
		"id":     "enc-1",
		"period": map[string]any{"start": "2026-03-15"},
		"class":  map[string]any{"code": "IMP"},
		"reasonCode": []any{
			map[string]any{"text": "Chest pain"},
		},
	}
	out, err := summarizeOne(context.Background(), e)
	if err != nil {
		t.Fatalf("summarizeOne: %v", err)
	}
	if out.EncounterID != "enc-1" {
		t.Errorf("id: %s", out.EncounterID)
	}
	if out.Class != "IMP" {
		t.Errorf("class: %s", out.Class)
	}
	if out.Reason != "Chest pain" {
		t.Errorf("reason: %s", out.Reason)
	}
	if out.Summary == "" {
		t.Errorf("empty summary")
	}
}

// Ensure listEncounters honors the SHARP token (sanity check that
// we're not bypassing the FHIR client's auth path).
func TestListEncounters_AuthHeader(t *testing.T) {
	var sawAuth atomic.Value
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawAuth.Store(r.Header.Get("Authorization"))
		_, _ = w.Write([]byte(`{"resourceType":"Bundle","entry":[]}`))
	}))
	defer srv.Close()

	ctx := sharp.Inject(context.Background(), sharp.Context{
		PatientID: "p", FHIRBase: srv.URL, Token: "secret",
	})
	_, err := listEncounters(fhir.NewClient())(ctx, ChartReviewIn{Limit: 1})
	if err != nil {
		t.Fatalf("listEncounters: %v", err)
	}
	if got, _ := sawAuth.Load().(string); got != "Bearer secret" {
		t.Errorf("auth header: %q", got)
	}
}

func TestClassDisplay(t *testing.T) {
	cases := map[string]string{
		"AMB":  "Ambulatory",
		"IMP":  "Inpatient",
		"EMER": "Emergency",
		"":     "Encounter",
		"XYZ":  "XYZ",
	}
	for in, want := range cases {
		if got := classDisplay(in); got != want {
			t.Errorf("classDisplay(%q) = %q, want %q", in, got, want)
		}
	}
}
