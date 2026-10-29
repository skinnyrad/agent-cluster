// Package agent runs LLM agents backed by Ollama's /api/chat endpoint. An agent sends its
// instructions as the system message, streams the model's reply, and, when it has tools,
// runs the tool calls the model asks for and feeds the results back until the model
// answers.
package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

const (
	defaultMaxSteps = 8
	// maxToolOutput caps what one tool call can add to the conversation.
	maxToolOutput = 16000
)

// Tool is something the model can ask the agent to run.
type Tool interface {
	Spec() ToolSpec
	Call(ctx context.Context, args map[string]any) (string, error)
}

// Event reports progress while an agent runs.
type Event struct {
	Type   string // "token", "tool_call" or "tool_result"
	Text   string
	Tool   string
	Args   map[string]any
	Output string
}

// Agent holds the configuration for one run. Create a new one per request.
type Agent struct {
	Model        string
	Instructions []string
	// Markdown asks the model to format its answer in markdown.
	Markdown bool
	// AddDatetime tells the model the current date and time.
	AddDatetime bool
	// Options sets model parameters such as context size and output length.
	Options *Options
	// Format, if set, is a JSON schema the reply must match.
	Format json.RawMessage
	// Tools the model may call. MaxSteps limits how many model calls one run makes.
	Tools    []Tool
	MaxSteps int
	// OnEvent, if set, receives tokens and tool activity as they happen.
	OnEvent func(Event)
	// Client defaults to an Ollama client pointed at OLLAMA_HOST.
	Client *Client
}

// Response is the result of Agent.Run.
type Response struct {
	Content string
	Metrics map[string]any
}

// SystemPrompt builds the system message from the agent's settings.
func (a *Agent) SystemPrompt() string {
	var b strings.Builder
	switch len(a.Instructions) {
	case 0:
	case 1:
		b.WriteString(a.Instructions[0])
	default:
		b.WriteString("<instructions>\n")
		for _, in := range a.Instructions {
			b.WriteString("- " + in + "\n")
		}
		b.WriteString("</instructions>")
	}

	var extra []string
	if a.Markdown {
		extra = append(extra, "Use markdown to format your answers.")
	}
	if a.AddDatetime {
		extra = append(extra, "The current time is "+time.Now().Format("2006-01-02 15:04:05 MST")+".")
	}
	if len(a.Tools) > 0 {
		extra = append(extra, "Use the available tools when they help. When you have what you need, answer without calling more tools.")
	}
	if len(extra) > 0 {
		if b.Len() > 0 {
			b.WriteString("\n\n")
		}
		b.WriteString("<additional_information>\n")
		for _, e := range extra {
			b.WriteString("- " + e + "\n")
		}
		b.WriteString("</additional_information>")
	}
	return b.String()
}

// Run sends prompt to the model, runs any tool calls it makes, and returns its answer.
func (a *Agent) Run(ctx context.Context, prompt string) (*Response, error) {
	c := a.Client
	if c == nil {
		c = DefaultClient()
	}
	maxSteps := a.MaxSteps
	if maxSteps <= 0 {
		maxSteps = defaultMaxSteps
	}

	var msgs []Message
	if sys := a.SystemPrompt(); sys != "" {
		msgs = append(msgs, Message{Role: "system", Content: sys})
	}
	msgs = append(msgs, Message{Role: "user", Content: prompt})

	tools := map[string]Tool{}
	var specs []ToolSpec
	for _, t := range a.Tools {
		spec := t.Spec()
		tools[spec.Function.Name] = t
		specs = append(specs, spec)
	}

	start := time.Now()
	var inputTokens, outputTokens, toolCalls int
	for step := 1; ; step++ {
		req := ChatRequest{Model: a.Model, Messages: msgs, Format: a.Format, Options: a.Options}
		// On the last allowed step, withhold tools so the model has to answer.
		lastStep := step >= maxSteps
		if !lastStep {
			req.Tools = specs
		}

		var onChunk func(*ChatResponse)
		if a.OnEvent != nil {
			onChunk = func(chunk *ChatResponse) {
				if chunk.Message.Content != "" {
					a.OnEvent(Event{Type: "token", Text: chunk.Message.Content})
				}
			}
		}
		resp, err := c.Chat(ctx, req, onChunk)
		if err != nil {
			return nil, err
		}
		inputTokens += resp.PromptEvalCount
		outputTokens += resp.EvalCount

		if len(resp.Message.ToolCalls) == 0 || lastStep {
			return &Response{
				Content: resp.Message.Content,
				Metrics: map[string]any{
					"input_tokens":  inputTokens,
					"output_tokens": outputTokens,
					"total_tokens":  inputTokens + outputTokens,
					"tool_calls":    toolCalls,
					"steps":         step,
					"duration":      time.Since(start).Seconds(),
				},
			}, nil
		}

		msgs = append(msgs, resp.Message)
		for _, call := range resp.Message.ToolCalls {
			toolCalls++
			name, args := call.Function.Name, call.Function.Arguments
			if args == nil {
				args = map[string]any{}
			}
			a.emit(Event{Type: "tool_call", Tool: name, Args: args})

			var output string
			if t, ok := tools[name]; !ok {
				output = fmt.Sprintf("error: unknown tool %q", name)
			} else if out, err := t.Call(ctx, args); err != nil {
				output = "error: " + err.Error()
			} else {
				output = out
			}
			if len(output) > maxToolOutput {
				output = output[:maxToolOutput] + "\n[output truncated]"
			}
			a.emit(Event{Type: "tool_result", Tool: name, Output: output})
			msgs = append(msgs, Message{Role: "tool", Content: output, ToolName: name})
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
	}
}

func (a *Agent) emit(e Event) {
	if a.OnEvent != nil {
		a.OnEvent(e)
	}
}

// ParamSchema builds a JSON schema for a tool whose parameters are all strings.
// required lists the names that must be present; descriptions maps each name to its help.
func ParamSchema(descriptions map[string]string, required ...string) json.RawMessage {
	props := map[string]any{}
	for name, desc := range descriptions {
		props[name] = map[string]string{"type": "string", "description": desc}
	}
	if required == nil {
		required = []string{}
	}
	data, _ := json.Marshal(map[string]any{"type": "object", "properties": props, "required": required})
	return data
}
