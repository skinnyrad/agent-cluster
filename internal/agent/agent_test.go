package agent_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/skinnyrad/agent-cluster/internal/agent"
	"github.com/skinnyrad/agent-cluster/internal/fakeollama"
)

type echoTool struct{ calls int }

func (t *echoTool) Spec() agent.ToolSpec {
	return agent.ToolSpec{Type: "function", Function: agent.FunctionSpec{
		Name: "echo", Description: "echo", Parameters: agent.ParamSchema(map[string]string{"text": "text"}, "text"),
	}}
}

func (t *echoTool) Call(ctx context.Context, args map[string]any) (string, error) {
	t.calls++
	return "echo:" + args["text"].(string), nil
}

func TestToolLoop(t *testing.T) {
	f := fakeollama.Start(t)
	f.ToolModel, f.ToolName, f.ToolArgs = "m", "echo", map[string]any{"text": "hi"}

	tool := &echoTool{}
	var events []agent.Event
	a := &agent.Agent{
		Model: "m", Instructions: []string{"You are a worker."}, Tools: []agent.Tool{tool},
		Client: f.Client, OnEvent: func(e agent.Event) { events = append(events, e) },
	}
	resp, err := a.Run(context.Background(), "do it")
	if err != nil {
		t.Fatal(err)
	}
	if resp.Content != "tool said: echo:hi" || tool.calls != 1 {
		t.Fatalf("content = %q after %d tool calls", resp.Content, tool.calls)
	}
	if resp.Metrics["tool_calls"] != 1 || resp.Metrics["steps"] != 2 {
		t.Errorf("metrics = %v", resp.Metrics)
	}

	var kinds []string
	var text strings.Builder
	for _, e := range events {
		if e.Type == "token" {
			text.WriteString(e.Text)
		} else {
			kinds = append(kinds, e.Type)
		}
	}
	if strings.Join(kinds, ",") != "tool_call,tool_result" || text.String() != resp.Content {
		t.Errorf("events = %v, streamed text %q", kinds, text.String())
	}

	// The tool result goes back to the model as a "tool" message naming the tool.
	calls := f.Calls()
	last := calls[len(calls)-1].Messages
	if m := last[len(last)-1]; m.Role != "tool" || m.ToolName != "echo" || m.Content != "echo:hi" {
		t.Errorf("tool message = %+v", m)
	}
}

func TestMaxStepsForcesAnAnswer(t *testing.T) {
	f := fakeollama.Start(t)
	f.ToolModel, f.ToolName, f.ToolArgs = "m", "echo", map[string]any{"text": "again"}

	tool := &echoTool{}
	a := &agent.Agent{Model: "m", Tools: []agent.Tool{tool}, MaxSteps: 1, Client: f.Client}
	resp, err := a.Run(context.Background(), "loop forever")
	if err != nil {
		t.Fatal(err)
	}
	if tool.calls != 0 || !strings.HasPrefix(resp.Content, "m did:") {
		t.Fatalf("with MaxSteps 1 the model must answer without tools; got %q after %d calls", resp.Content, tool.calls)
	}
	if len(f.Calls()[0].Tools) != 0 {
		t.Error("tools were offered on the last step")
	}
}

func TestFormatAndOptionsAreSent(t *testing.T) {
	f := fakeollama.Start(t)
	schema := json.RawMessage(`{"type":"object"}`)
	a := &agent.Agent{Model: "m", Format: schema, Options: &agent.Options{NumCtx: 4096, NumPredict: 100}, Client: f.Client}
	if _, err := a.Run(context.Background(), "x"); err != nil {
		t.Fatal(err)
	}
	req := f.Calls()[0]
	if string(req.Format) != string(schema) || req.Options == nil || req.Options.NumCtx != 4096 || req.Options.NumPredict != 100 {
		t.Errorf("request format=%s options=%+v", req.Format, req.Options)
	}
}
