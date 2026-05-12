// Package agent implements an LLM tool-use loop as a weft Arrow.
//
// The whole agent loop is itself an Arrow[llm.Prompt, llm.Response],
// which means it composes with the rest of the weft algebra: you can
// wrap it in WithTimeout, Traverse a list of prompts through it,
// Compose it before or after other arrows, etc.
//
// We hand-roll this rather than depend on weft's `llm.Loop` because
// the published weft v0.1.0 doesn't expose Loop. Re-implementing it
// in ~100 lines is a feature, not a bug: it demonstrates that the
// agent loop is not a special construct, just another arrow.
package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/vinodhalaharvi/weft/llm"
	"github.com/vinodhalaharvi/weft/weft"
)

// ToolBinding pairs a tool spec (what the LLM sees) with a typed handler
// (what runs when the LLM invokes it).
type ToolBinding struct {
	Spec    llm.ToolSpec
	Handler func(ctx context.Context, args json.RawMessage) (string, error)
}

// LoopOption configures the loop.
type LoopOption func(*loopConfig)

type loopConfig struct {
	maxIter int
}

// WithMaxIter caps the number of (model call + tool calls) iterations.
func WithMaxIter(n int) LoopOption {
	return func(c *loopConfig) {
		if n > 0 {
			c.maxIter = n
		}
	}
}

// Loop builds an Arrow[llm.Prompt, llm.Response] that runs the
// model-call → tool-dispatch loop until the model stops or maxIter
// is reached.
//
// The loop semantics:
//  1. Add tool specs to the prompt.
//  2. Call the LLM arrow.
//  3. If the response has no tool_use blocks, return it.
//  4. Otherwise, dispatch each tool_use to its binding, append the
//     results as a user message, and go back to step 2.
func Loop(
	model weft.Arrow[llm.Prompt, llm.Response],
	bindings []ToolBinding,
	opts ...LoopOption,
) weft.Arrow[llm.Prompt, llm.Response] {
	cfg := loopConfig{maxIter: 8}
	for _, o := range opts {
		o(&cfg)
	}

	// Pre-build the tool specs slice and a handler lookup.
	specs := make([]llm.ToolSpec, len(bindings))
	handlers := make(map[string]func(ctx context.Context, args json.RawMessage) (string, error), len(bindings))
	for i, b := range bindings {
		specs[i] = b.Spec
		handlers[b.Spec.Name] = b.Handler
	}

	return func(ctx context.Context, in llm.Prompt) (llm.Response, error) {
		// Start with the caller's prompt + our tools.
		prompt := in
		prompt.Tools = append(append([]llm.ToolSpec{}, in.Tools...), specs...)

		var last llm.Response
		for iter := 0; iter < cfg.maxIter; iter++ {
			if ctx.Err() != nil {
				return last, ctx.Err()
			}
			resp, err := model(ctx, prompt)
			if err != nil {
				return last, fmt.Errorf("agent: model call %d: %w", iter, err)
			}
			last = resp

			calls := resp.ToolCalls()
			if len(calls) == 0 {
				// Model is done.
				return resp, nil
			}

			// Append assistant turn so the model sees its own tool_use.
			if len(resp.Messages) > 0 {
				prompt.Messages = append(prompt.Messages, resp.Messages...)
			}

			// Dispatch each tool_use; collect tool_result blocks for the next turn.
			resultBlocks := make([]llm.Block, 0, len(calls))
			for _, call := range calls {
				h, ok := handlers[call.ToolName]
				if !ok {
					resultBlocks = append(resultBlocks, llm.Block{
						Kind:         llm.BlockToolResult,
						ToolResultID: call.ToolUseID,
						ToolResult:   "error: unknown tool " + call.ToolName,
					})
					continue
				}
				out, herr := h(ctx, call.ToolInput)
				if herr != nil {
					out = "error: " + herr.Error()
				}
				resultBlocks = append(resultBlocks, llm.Block{
					Kind:         llm.BlockToolResult,
					ToolResultID: call.ToolUseID,
					ToolResult:   out,
				})
			}
			prompt.Messages = append(prompt.Messages, llm.Message{
				Role:    llm.RoleUser,
				Content: resultBlocks,
			})
		}

		return last, errors.New("agent: max iterations reached")
	}
}

// BindArrow lifts a typed weft.Arrow[In, Out] into a ToolBinding.
// JSON encoding happens at the boundary; the arrow stays fully typed.
func BindArrow[In, Out any](spec llm.ToolSpec, arrow weft.Arrow[In, Out]) ToolBinding {
	return ToolBinding{
		Spec: spec,
		Handler: func(ctx context.Context, args json.RawMessage) (string, error) {
			var in In
			if len(args) > 0 && string(args) != "null" {
				if err := json.Unmarshal(args, &in); err != nil {
					return "", fmt.Errorf("decode args: %w", err)
				}
			}
			out, err := arrow(ctx, in)
			if err != nil {
				return "", err
			}
			b, err := json.Marshal(out)
			if err != nil {
				return "", err
			}
			return string(b), nil
		},
	}
}
