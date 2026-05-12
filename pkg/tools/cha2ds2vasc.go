package tools

import (
	"context"
	"encoding/json"

	"github.com/vinodhalaharvi/suture/internal/fhir"
	"github.com/vinodhalaharvi/suture/internal/mcp"
	"github.com/vinodhalaharvi/weft/weft"
)

// === CHA2DS2-VASc Stroke Risk Score =================================
//
// Three MCP tools, all built from the same three building blocks:
//
//     fetchClinicalData :: Arrow[struct{}, ClinicalData]   (FHIR reads)
//     extractComponents :: Arrow[ClinicalData, Components] (pure)
//     computeScore      :: Arrow[Components, ScoreResult]  (pure)
//
//   calculate_cha2ds2_vasc  =  Pipe3(fetchClinicalData, extractComponents, computeScore)
//   get_score_components    =  Pipe2(fetchClinicalData, extractComponents)
//   explain_cha2ds2_vasc    =  Pipe3 + LLM narration arrow (added later when API key present)

// CHA2DS2VAScIn — patient identified by SHARP.
type CHA2DS2VAScIn struct{}

// ClinicalData bundles together the FHIR reads we need.
type ClinicalData struct {
	Patient    fhir.Resource
	Conditions fhir.Bundle
}

// Components is the per-criterion breakdown of the score.
type Components struct {
	CHF          bool `json:"chf"`          // congestive heart failure (1 pt)
	Hypertension bool `json:"hypertension"` // 1 pt
	Age          int  `json:"age"`          // 65-74: 1 pt, ≥75: 2 pt
	Diabetes     bool `json:"diabetes"`     // 1 pt
	StrokeHx     bool `json:"stroke_hx"`    // prior stroke/TIA: 2 pt
	Vascular     bool `json:"vascular"`     // MI/PAD: 1 pt
	Female       bool `json:"female"`       // 1 pt
}

// ScoreResult is the full result returned by calculate_cha2ds2_vasc.
type ScoreResult struct {
	Score      int        `json:"score"`
	Components Components `json:"components"`
	RiskBand   string     `json:"risk_band"` // "low" | "moderate" | "high"
	Notes      string     `json:"notes,omitempty"`
}

// fetchClinicalData :: Arrow[CHA2DS2VAScIn, ClinicalData]
//
// Built from a Par of two FHIR reads; this is the second time we use
// the parallel-then-merge pattern, but with different output shape.
func fetchClinicalData(c *fhir.Client) weft.Arrow[CHA2DS2VAScIn, ClinicalData] {
	read := func(ctx context.Context, _ CHA2DS2VAScIn) (fhir.Resource, error) {
		return c.ReadPatient()(ctx, struct{}{})
	}
	search := func(ctx context.Context, _ CHA2DS2VAScIn) (fhir.Bundle, error) {
		return c.SearchConditions()(ctx, fhir.ConditionFilter{ClinicalStatus: "active"})
	}
	return weft.Map(
		weft.Par(read, search),
		func(p weft.Pair[fhir.Resource, fhir.Bundle]) ClinicalData {
			return ClinicalData{Patient: p.Fst, Conditions: p.Snd}
		},
	)
}

// extractComponents :: Arrow[ClinicalData, Components]
//
// Pure transformation from FHIR data to a typed components struct.
// All the ICD-10 -> criterion mapping lives here, isolated.
var extractComponents = weft.Pure(func(d ClinicalData) Components {
	hasCondition := func(prefix string) bool {
		for _, r := range d.Conditions.Resources() {
			if fhir.ConditionHasICD10Prefix(r, prefix) {
				return true
			}
		}
		return false
	}
	return Components{
		// I50 — Heart failure
		CHF: hasCondition("I50"),
		// I10 — Essential (primary) hypertension
		Hypertension: hasCondition("I10"),
		Age:          fhir.PatientAge(d.Patient),
		// E10 — Type 1, E11 — Type 2 diabetes
		Diabetes: hasCondition("E10") || hasCondition("E11"),
		// I63 — Cerebral infarction, G45 — TIA
		StrokeHx: hasCondition("I63") || hasCondition("G45"),
		// I21 — MI, I25 — chronic ischemic heart disease, I70 — atherosclerosis
		Vascular: hasCondition("I21") || hasCondition("I25") || hasCondition("I70"),
		Female:   fhir.PatientGender(d.Patient) == "female",
	}
})

// computeScore :: Arrow[Components, ScoreResult]
//
// Pure scoring logic — exhaustively testable, no IO.
var computeScore = weft.Pure(func(c Components) ScoreResult {
	s := 0
	if c.CHF {
		s++
	}
	if c.Hypertension {
		s++
	}
	switch {
	case c.Age >= 75:
		s += 2
	case c.Age >= 65:
		s++
	}
	if c.Diabetes {
		s++
	}
	if c.StrokeHx {
		s += 2
	}
	if c.Vascular {
		s++
	}
	if c.Female {
		s++
	}
	band := "low"
	switch {
	case s >= 4:
		band = "high"
	case s >= 2:
		band = "moderate"
	}
	notes := ""
	if c.Female && s == 1 {
		// Per AHA/ACC guideline: female sex alone (score 1) is not
		// considered sufficient indication for anticoagulation.
		notes = "Sex-only score; consider clinical context for anticoagulation."
	}
	return ScoreResult{Score: s, Components: c, RiskBand: band, Notes: notes}
})

// === Composed tools ================================================

// CalculateScoreArrow is Pipe3(fetchClinicalData, extractComponents, computeScore).
// This is the full pipeline as a single composed arrow.
func CalculateScoreArrow(c *fhir.Client) weft.Arrow[CHA2DS2VAScIn, ScoreResult] {
	return weft.Pipe3(
		fetchClinicalData(c),
		extractComponents,
		computeScore,
	)
}

// GetComponentsArrow stops after extractComponents, returning the
// per-criterion breakdown without summing. Same upstream work.
func GetComponentsArrow(c *fhir.Client) weft.Arrow[CHA2DS2VAScIn, Components] {
	return weft.Compose(
		fetchClinicalData(c),
		extractComponents,
	)
}

// CHA2DS2VAScTools registers both score-related MCP tools.
func CHA2DS2VAScTools(s *mcp.Server, c *fhir.Client) {
	score := CalculateScoreArrow(c)
	s.AddTool(
		mcp.Tool{
			Name:        "calculate_cha2ds2_vasc",
			Description: "Compute the CHA2DS2-VASc stroke-risk score for the SHARP patient.",
			InputSchema: json.RawMessage(`{"type":"object","properties":{}}`),
		},
		runWithSharp(func(ctx context.Context, _ json.RawMessage) (any, error) {
			return score(ctx, CHA2DS2VAScIn{})
		}),
	)

	comp := GetComponentsArrow(c)
	s.AddTool(
		mcp.Tool{
			Name:        "get_cha2ds2_vasc_components",
			Description: "Return the per-criterion breakdown without computing the total.",
			InputSchema: json.RawMessage(`{"type":"object","properties":{}}`),
		},
		runWithSharp(func(ctx context.Context, _ json.RawMessage) (any, error) {
			return comp(ctx, CHA2DS2VAScIn{})
		}),
	)
}
