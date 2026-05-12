package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"time"

	"github.com/vinodhalaharvi/suture/internal/fhir"
	"github.com/vinodhalaharvi/suture/internal/fhircontext"
	"github.com/vinodhalaharvi/suture/internal/mcp"
	"github.com/vinodhalaharvi/weft/weft"
)

// === summarize_recent_encounters ====================================
//
// The Traverse-with-bounded-concurrency showcase. Lists last N
// encounters, then fans out per-encounter summarization in parallel
// with a partial-results policy so one failing encounter doesn't sink
// the whole tool.

// ChartReviewIn — tool input.
type ChartReviewIn struct {
	Limit int `json:"limit"` // default 5; max 20
}

// EncounterSummary — per-encounter row.
type EncounterSummary struct {
	EncounterID string `json:"encounter_id"`
	Date        string `json:"date"`
	Class       string `json:"class"`   // inpatient / ambulatory / etc
	Reason      string `json:"reason"`  // chief complaint or admission reason
	Summary     string `json:"summary"` // free-text summary
	Error       string `json:"error,omitempty"`
}

// ChartReviewOut — tool output.
type ChartReviewOut struct {
	Timeline []EncounterSummary `json:"timeline"`
	Skipped  int                `json:"skipped"`
}

// EncounterRef is the lightweight reference returned by the listing step.
type EncounterRef struct {
	ID    fhir.Resource // we keep the raw encounter so the summary step has its data
	Limit int           // kept for trace, not used downstream
}

// listEncounters :: Arrow[ChartReviewIn, []fhir.Resource]
//
// FHIR search for the most recent N encounters.
func listEncounters(c *fhir.Client) weft.Arrow[ChartReviewIn, []fhir.Resource] {
	return func(ctx context.Context, in ChartReviewIn) ([]fhir.Resource, error) {
		s, _ := fhircontext.From(ctx)
		limit := in.Limit
		if limit <= 0 {
			limit = 5
		}
		if limit > 20 {
			limit = 20
		}
		q := url.Values{}
		q.Set("patient", s.PatientID)
		q.Set("_count", fmt.Sprintf("%d", limit))
		q.Set("_sort", "-date")
		body, err := c.GetRaw(ctx, "/Encounter", q)
		if err != nil {
			return nil, err
		}
		var b fhir.Bundle
		if err := json.Unmarshal(body, &b); err != nil {
			return nil, err
		}
		return b.Resources(), nil
	}
}

// summarizeOne :: Arrow[fhir.Resource, EncounterSummary]
//
// Pure for now (no LLM call — adds risk for an end-to-end test). The
// shape of the arrow is what matters; swapping the body for an LLM
// arrow is a one-line change.
var summarizeOne weft.Arrow[fhir.Resource, EncounterSummary] = weft.Pure(
	func(e fhir.Resource) EncounterSummary {
		out := EncounterSummary{}
		out.EncounterID, _ = e["id"].(string)

		// period.start
		if period, ok := e["period"].(map[string]any); ok {
			out.Date, _ = period["start"].(string)
		}
		// class.code (e.g. "AMB", "IMP")
		if class, ok := e["class"].(map[string]any); ok {
			out.Class, _ = class["code"].(string)
		}
		// reasonCode[0].text or reasonCode[0].coding[0].display
		if reasons, ok := e["reasonCode"].([]any); ok && len(reasons) > 0 {
			if r, ok := reasons[0].(map[string]any); ok {
				if t, ok := r["text"].(string); ok {
					out.Reason = t
				} else if codings, ok := r["coding"].([]any); ok && len(codings) > 0 {
					if c, ok := codings[0].(map[string]any); ok {
						out.Reason, _ = c["display"].(string)
					}
				}
			}
		}
		out.Summary = fmt.Sprintf("%s encounter on %s%s",
			classDisplay(out.Class), out.Date,
			optional(": "+out.Reason, out.Reason != ""),
		)
		return out
	},
)

func classDisplay(code string) string {
	switch code {
	case "AMB":
		return "Ambulatory"
	case "IMP":
		return "Inpatient"
	case "EMER":
		return "Emergency"
	case "HH":
		return "Home health"
	case "VR":
		return "Virtual"
	case "":
		return "Encounter"
	default:
		return code
	}
}

func optional(s string, cond bool) string {
	if cond {
		return s
	}
	return ""
}

// ChartReviewArrow composes the listing with a Traverse-wrapped
// summarizer. The summarizer is hardened with retry + per-item timeout;
// the Traverse uses partial-results so individual failures don't sink
// the whole tool.
func ChartReviewArrow(c *fhir.Client) weft.Arrow[ChartReviewIn, ChartReviewOut] {
	robustSummarize := weft.Apply(summarizeOne,
		weft.WithRetry[fhir.Resource, EncounterSummary](2, weft.ExponentialBackoff(100*time.Millisecond)),
		weft.WithTimeout[fhir.Resource, EncounterSummary](10*time.Second),
	)
	fanout := weft.Traverse(robustSummarize,
		weft.WithConcurrency(4),
		weft.OnError(weft.PartialResults),
	)
	return weft.Map(
		weft.Compose(listEncounters(c), fanout),
		func(rows []EncounterSummary) ChartReviewOut {
			skipped := 0
			for _, r := range rows {
				if r.EncounterID == "" {
					skipped++
				}
			}
			return ChartReviewOut{Timeline: rows, Skipped: skipped}
		},
	)
}

// ChartReviewTool registers the encounter-summarization superpower.
func ChartReviewTool(s *mcp.Server, c *fhir.Client) {
	arrow := ChartReviewArrow(c)
	s.AddTool(
		mcp.Tool{
			Name:        "summarize_recent_encounters",
			Description: "Fetches the most recent N encounters in parallel and returns a timeline.",
			InputSchema: json.RawMessage(`{"type":"object","properties":{"limit":{"type":"integer","minimum":1,"maximum":20}}}`),
		},
		runWithFHIRContext(func(ctx context.Context, args json.RawMessage) (any, error) {
			var in ChartReviewIn
			if len(args) > 0 {
				_ = json.Unmarshal(args, &in)
			}
			return arrow(ctx, in)
		}),
	)
}
