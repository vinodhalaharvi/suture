package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/vinodhalaharvi/weft/llm"
	"github.com/vinodhalaharvi/weft/weft"
)

// stubModel returns a programmable Arrow[Prompt, Response] for tests.
// turn[i] is returned on the (i+1)th call.
func stubModel(turns ...llm.Response) (weft.Arrow[llm.Prompt, llm.Response], *int32) {
	var calls int32
	return func(ctx context.Context, p llm.Prompt) (llm.Response, error) {
		i := int(atomic.AddInt32(&calls, 1)) - 1
		if i >= len(turns) {
			return llm.Response{}, errors.New("stub exhausted")
		}
		return turns[i], nil
	}, &calls
}

func textResp(s string) llm.Response {
	return llm.Response{
		Messages:   []llm.Message{llm.AssistantText(s)},
		StopReason: llm.StopEndTurn,
	}
}

func toolUseResp(id, name, args string) llm.Response {
	return llm.Response{
		Messages: []llm.Message{{
			Role: llm.RoleAssistant,
			Content: []llm.Block{{
				Kind:      llm.BlockToolUse,
				ToolUseID: id,
				ToolName:  name,
				ToolInput: json.RawMessage(args),
			}},
		}},
		StopReason: llm.StopToolUse,
	}
}

func TestLoop_NoToolUse_ReturnsImmediately(t *testing.T) {
	model, calls := stubModel(textResp("done"))
	agent := Loop(model, nil)
	resp, err := agent(context.Background(), llm.Prompt{
		Messages: []llm.Message{llm.UserText("hi")},
	})
	if err != nil {
		t.Fatalf("agent: %v", err)
	}
	if resp.Text() != "done" {
		t.Errorf("text: %q", resp.Text())
	}
	if *calls != 1 {
		t.Errorf("expected 1 model call, got %d", *calls)
	}
}

func TestLoop_DispatchesToolThenReturns(t *testing.T) {
	var sawArgs string
	binding := ToolBinding{
		Spec: llm.ToolSpec{
			Name:        "echo",
			Description: "echo",
			InputSchema: json.RawMessage(`{"type":"object"}`),
		},
		Handler: func(ctx context.Context, args json.RawMessage) (string, error) {
			sawArgs = string(args)
			return "echoed", nil
		},
	}

	model, calls := stubModel(
		toolUseResp("u1", "echo", `{"msg":"hi"}`),
		textResp("final"),
	)
	agent := Loop(model, []ToolBinding{binding})
	resp, err := agent(context.Background(), llm.Prompt{
		Messages: []llm.Message{llm.UserText("call the tool")},
	})
	if err != nil {
		t.Fatalf("agent: %v", err)
	}
	if resp.Text() != "final" {
		t.Errorf("final text: %q", resp.Text())
	}
	if *calls != 2 {
		t.Errorf("expected 2 model calls, got %d", *calls)
	}
	if !strings.Contains(sawArgs, "hi") {
		t.Errorf("tool didn't see args: %s", sawArgs)
	}
}

func TestLoop_UnknownToolReturnsError(t *testing.T) {
	model, _ := stubModel(
		toolUseResp("u1", "nonexistent", `{}`),
		textResp("recovered"),
	)
	agent := Loop(model, nil)
	resp, err := agent(context.Background(), llm.Prompt{
		Messages: []llm.Message{llm.UserText("?")},
	})
	if err != nil {
		t.Fatalf("agent: %v", err)
	}
	// Should have recovered to second turn after returning error to LLM.
	if resp.Text() != "recovered" {
		t.Errorf("recovery: %q", resp.Text())
	}
}

func TestLoop_HandlerErrorPropagatesAsString(t *testing.T) {
	binding := ToolBinding{
		Spec: llm.ToolSpec{Name: "fail", InputSchema: json.RawMessage(`{}`)},
		Handler: func(ctx context.Context, args json.RawMessage) (string, error) {
			return "", errors.New("kaboom")
		},
	}
	model, _ := stubModel(
		toolUseResp("u1", "fail", `{}`),
		textResp("ok"),
	)
	agent := Loop(model, []ToolBinding{binding})
	_, err := agent(context.Background(), llm.Prompt{
		Messages: []llm.Message{llm.UserText("try")},
	})
	if err != nil {
		t.Fatalf("agent: %v", err)
	}
}

func TestLoop_MaxIterReached(t *testing.T) {
	binding := ToolBinding{
		Spec: llm.ToolSpec{Name: "loop", InputSchema: json.RawMessage(`{}`)},
		Handler: func(ctx context.Context, args json.RawMessage) (string, error) {
			return "ok", nil
		},
	}
	// Model always asks for the tool — should hit maxIter.
	model := func(ctx context.Context, p llm.Prompt) (llm.Response, error) {
		return toolUseResp("u", "loop", `{}`), nil
	}
	agent := Loop(model, []ToolBinding{binding}, WithMaxIter(3))
	_, err := agent(context.Background(), llm.Prompt{
		Messages: []llm.Message{llm.UserText("?")},
	})
	if err == nil || !strings.Contains(err.Error(), "max iterations") {
		t.Errorf("expected max iter error, got %v", err)
	}
}

func TestLoop_ContextCancellation(t *testing.T) {
	model := func(ctx context.Context, p llm.Prompt) (llm.Response, error) {
		return textResp("never"), nil
	}
	agent := Loop(model, nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := agent(ctx, llm.Prompt{Messages: []llm.Message{llm.UserText("hi")}})
	if err == nil {
		t.Error("expected cancellation error")
	}
}

func TestBindArrow_TypedRoundtrip(t *testing.T) {
	type Add struct{ A, B int }
	type Sum struct{ Result int }

	arrow := weft.Pure(func(in Add) Sum {
		return Sum{Result: in.A + in.B}
	})

	binding := BindArrow(llm.ToolSpec{
		Name:        "add",
		InputSchema: json.RawMessage(`{"type":"object"}`),
	}, arrow)

	out, err := binding.Handler(context.Background(), json.RawMessage(`{"A":2,"B":3}`))
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	var result Sum
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if result.Result != 5 {
		t.Errorf("expected 5, got %d", result.Result)
	}
}

func TestBindArrow_DecodeError(t *testing.T) {
	type In struct{ X int }
	arrow := weft.Pure(func(_ In) string { return "ok" })
	binding := BindArrow(llm.ToolSpec{Name: "t"}, arrow)
	_, err := binding.Handler(context.Background(), json.RawMessage(`not json`))
	if err == nil {
		t.Error("expected decode error")
	}
}

func TestWithMaxIter_RejectsZero(t *testing.T) {
	cfg := &loopConfig{maxIter: 8}
	WithMaxIter(0)(cfg)
	if cfg.maxIter != 8 {
		t.Errorf("zero should be ignored, got %d", cfg.maxIter)
	}
	WithMaxIter(20)(cfg)
	if cfg.maxIter != 20 {
		t.Errorf("got %d", cfg.maxIter)
	}
}
