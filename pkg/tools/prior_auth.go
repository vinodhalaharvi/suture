package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/vinodhalaharvi/suture/internal/fhir"
	"github.com/vinodhalaharvi/suture/internal/mcp"
	"github.com/vinodhalaharvi/suture/pkg/agent"
	"github.com/vinodhalaharvi/weft/llm"
	"github.com/vinodhalaharvi/weft/weft"
)

// === prior_auth_assistant (Option-2 Agent) ===========================
//
// The whole agent is a single MCP tool from Prompt Opinion's
// perspective: takes a free-text clinician request, runs an LLM loop
// with our local superpowers as bindings, returns a draft letter +
// audit trail.

// PriorAuthIn is the public input.
type PriorAuthIn struct {
	Request string `json:"request"`
}

// PriorAuthOut is the public output.
type PriorAuthOut struct {
	Letter string `json:"letter"`
	Notes  string `json:"notes,omitempty"`
}

// PriorAuthAgentArrow constructs the agent. It requires ANTHROPIC_API_KEY
// in the environment at registration time; if missing, the registered
// tool returns a clear error.
//
// In production you'd configure the model via Prompt Opinion's
// platform-provided credentials. For the hackathon submission, env var
// is the lowest-friction path.
func PriorAuthAgentArrow(c *fhir.Client) weft.Arrow[PriorAuthIn, PriorAuthOut] {
	apiKey := os.Getenv("ANTHROPIC_API_KEY")

	model := llm.Claude("claude-opus-4-5-20250929", llm.WithAPIKey(apiKey))

	// Wrap each tool arrow as a binding the LLM can call.
	bindings := []agent.ToolBinding{
		agent.BindArrow(
			llm.ToolSpec{
				Name:        "get_patient_summary",
				Description: "Demographics + active problem list for the patient in SHARP context. Call this first to ground every decision in the actual patient.",
				InputSchema: json.RawMessage(`{"type":"object","properties":{}}`),
			},
			PatientSummaryArrow(c),
		),
		agent.BindArrow(
			llm.ToolSpec{
				Name:        "calculate_cha2ds2_vasc",
				Description: "Stroke-risk score for atrial fibrillation patients. Useful when assessing anticoagulation needs.",
				InputSchema: json.RawMessage(`{"type":"object","properties":{}}`),
			},
			CalculateScoreArrow(c),
		),
		agent.BindArrow(
			llm.ToolSpec{
				Name:        "summarize_recent_encounters",
				Description: "Returns a timeline of the patient's recent encounters. Use this when the prior-auth justification depends on visit history.",
				InputSchema: json.RawMessage(`{"type":"object","properties":{"limit":{"type":"integer"}}}`),
			},
			ChartReviewArrow(c),
		),
	}

	loop := agent.Loop(model, bindings, agent.WithMaxIter(8))

	// Convert the typed input/output to/from the LLM Prompt/Response shape.
	formatPrompt := weft.Pure(func(in PriorAuthIn) llm.Prompt {
		return llm.Prompt{
			System: priorAuthSystemPrompt,
			Messages: []llm.Message{
				llm.UserText(in.Request),
			},
			MaxTokens: 4096,
		}
	})
	parseResp := weft.Pure(func(r llm.Response) PriorAuthOut {
		return PriorAuthOut{
			Letter: r.Text(),
			Notes:  fmt.Sprintf("%d input tokens / %d output tokens", r.Usage.InputTokens, r.Usage.OutputTokens),
		}
	})

	return weft.Pipe3(formatPrompt, loop, parseResp)
}

const priorAuthSystemPrompt = `You are a clinical assistant helping a physician draft a prior authorization request.

Workflow:
  1. Use get_patient_summary to ground the request in the patient's actual demographics and problem list.
  2. Use additional tools (calculate_cha2ds2_vasc, summarize_recent_encounters) only when the requested medication or therapy depends on that data.
  3. Draft a concise, evidence-based prior authorization letter citing specifics from the tools you called.

Constraints:
  - Cite only data returned by your tool calls; do not invent labs, dates, or diagnoses.
  - If a tool fails, note the gap in your letter rather than fabricating.
  - Keep the letter under 400 words.`

// PriorAuthAgentTool registers the prior-auth agent as a single MCP tool.
func PriorAuthAgentTool(s *mcp.Server, c *fhir.Client) {
	arrow := PriorAuthAgentArrow(c)
	s.AddTool(
		mcp.Tool{
			Name:        "prior_auth_assistant",
			Description: "Drafts a prior authorization letter for a requested therapy by orchestrating patient lookup, risk scoring, and chart review.",
			InputSchema: json.RawMessage(`{
				"type":"object",
				"properties":{
					"request":{"type":"string","description":"Free-text clinician request, e.g. 'prior auth for apixaban for atrial fibrillation'"}
				},
				"required":["request"]
			}`),
		},
		runWithFHIRContext(func(ctx context.Context, args json.RawMessage) (any, error) {
			if os.Getenv("ANTHROPIC_API_KEY") == "" {
				return nil, fmt.Errorf("prior_auth_assistant: ANTHROPIC_API_KEY not set (this tool requires an LLM provider)")
			}
			var in PriorAuthIn
			if err := json.Unmarshal(args, &in); err != nil {
				return nil, fmt.Errorf("invalid args: %w", err)
			}
			if in.Request == "" {
				return nil, fmt.Errorf("request field is required")
			}
			return arrow(ctx, in)
		}),
	)
}
