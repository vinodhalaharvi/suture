// Package tools exposes the four healthcare Superpowers as weft Arrows
// plus MCP registration helpers.
package tools

import (
	"context"
	"encoding/json"

	"github.com/vinodhalaharvi/suture/internal/fhir"
	"github.com/vinodhalaharvi/suture/internal/mcp"
	"github.com/vinodhalaharvi/suture/internal/sharp"
	"github.com/vinodhalaharvi/weft/weft"
)

// === get_patient_summary ===========================================

// PatientSummaryIn — empty: patient identified by SHARP context.
type PatientSummaryIn struct{}

// PatientSummaryOut is what the MCP tool returns.
type PatientSummaryOut struct {
	PatientID    string   `json:"patient_id"`
	Name         string   `json:"name"`
	DOB          string   `json:"dob"`
	Age          int      `json:"age"`
	Gender       string   `json:"gender"`
	ActiveIssues []string `json:"active_issues"`
}

// PatientSummaryArrow composes ReadPatient || SearchConditions(active)
// concurrently, then merges into PatientSummaryOut.
//
// This is the headline composition the README sketches: weft.Par lets
// us read two FHIR endpoints in parallel; weft.Map lets us shape the
// joined Pair into our output type without an explicit lambda for the
// glue.
func PatientSummaryArrow(c *fhir.Client) weft.Arrow[PatientSummaryIn, PatientSummaryOut] {
	read := func(ctx context.Context, _ PatientSummaryIn) (fhir.Resource, error) {
		return c.ReadPatient()(ctx, struct{}{})
	}
	search := func(ctx context.Context, _ PatientSummaryIn) (fhir.Bundle, error) {
		return c.SearchConditions()(ctx, fhir.ConditionFilter{ClinicalStatus: "active"})
	}
	parallel := weft.Par(read, search)

	return weft.Map(parallel, func(p weft.Pair[fhir.Resource, fhir.Bundle]) PatientSummaryOut {
		patient, bundle := p.Fst, p.Snd
		issues := make([]string, 0, len(bundle.Entry))
		for _, r := range bundle.Resources() {
			if d := fhir.ConditionDisplay(r); d != "" {
				issues = append(issues, d)
			}
		}
		id, _ := patient["id"].(string)
		return PatientSummaryOut{
			PatientID:    id,
			Name:         fhir.PatientName(patient),
			DOB:          fhir.PatientBirthDate(patient),
			Age:          fhir.PatientAge(patient),
			Gender:       fhir.PatientGender(patient),
			ActiveIssues: issues,
		}
	})
}

// PatientSummaryTool registers the patient-summary superpower on an MCP server.
func PatientSummaryTool(s *mcp.Server, c *fhir.Client) {
	arrow := PatientSummaryArrow(c)
	s.AddTool(
		mcp.Tool{
			Name:        "get_patient_summary",
			Description: "Returns demographics and active problem list for the patient in SHARP context.",
			InputSchema: json.RawMessage(`{"type":"object","properties":{}}`),
		},
		runWithSharp(func(ctx context.Context, _ json.RawMessage) (any, error) {
			return arrow(ctx, PatientSummaryIn{})
		}),
	)
}

// runWithSharp is the SHARP middleware. It extracts SHARP fields from
// the MCP request _meta, validates them, injects into context, and
// then runs the handler. Errors from missing SHARP come back as
// well-typed isError:true results.
func runWithSharp(
	h func(ctx context.Context, args json.RawMessage) (any, error),
) mcp.ToolHandler {
	return func(ctx context.Context, args json.RawMessage, meta map[string]any) (any, error) {
		s := sharp.FromMeta(meta)
		if err := s.Validate(); err != nil {
			return nil, err
		}
		return h(sharp.Inject(ctx, s), args)
	}
}
