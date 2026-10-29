// Package agent is a minimal single-turn LLM agent backed by Ollama's /api/chat endpoint.
// It replaces the small slice of Agno the Python version used: instructions become the
// system message, and the user prompt is sent as a single user message.
package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

// Agent holds the configuration for one LLM call. Create a new one per request.
type Agent struct {
	Model        string
	Instructions []string
	// Markdown asks the model to format its answer in markdown.
	Markdown bool
	// AddDatetime tells the model the current date and time.
	AddDatetime bool
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

// Run sends prompt to the model and returns its reply.
func (a *Agent) Run(ctx context.Context, prompt string) (*Response, error) {
	c := a.Client
	if c == nil {
		c = DefaultClient()
	}
	var msgs []Message
	if sys := a.SystemPrompt(); sys != "" {
		msgs = append(msgs, Message{Role: "system", Content: sys})
	}
	msgs = append(msgs, Message{Role: "user", Content: prompt})

	start := time.Now()
	resp, err := c.Chat(ctx, ChatRequest{Model: a.Model, Messages: msgs})
	if err != nil {
		return nil, err
	}
	return &Response{
		Content: resp.Message.Content,
		Metrics: map[string]any{
			"input_tokens":  resp.PromptEvalCount,
			"output_tokens": resp.EvalCount,
			"total_tokens":  resp.PromptEvalCount + resp.EvalCount,
			"duration":      time.Since(start).Seconds(),
		},
	}, nil
}

// Message is one chat message.
type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// ChatRequest is the body of Ollama's POST /api/chat.
type ChatRequest struct {
	Model    string    `json:"model"`
	Messages []Message `json:"messages"`
	Stream   bool      `json:"stream"`
}

// ChatResponse is the non-streaming response from Ollama's POST /api/chat.
type ChatResponse struct {
	Model           string  `json:"model"`
	Message         Message `json:"message"`
	Done            bool    `json:"done"`
	PromptEvalCount int     `json:"prompt_eval_count"`
	EvalCount       int     `json:"eval_count"`
}

// Client talks to an Ollama server.
type Client struct {
	BaseURL string
	HTTP    *http.Client
}

// DefaultClient returns a client for OLLAMA_HOST, or http://localhost:11434 if unset.
func DefaultClient() *Client {
	return &Client{BaseURL: hostFromEnv(), HTTP: http.DefaultClient}
}

func hostFromEnv() string {
	host := strings.TrimSpace(os.Getenv("OLLAMA_HOST"))
	if host == "" {
		return "http://localhost:11434"
	}
	if !strings.Contains(host, "://") {
		host = "http://" + host
	}
	return strings.TrimRight(host, "/")
}

// Chat sends a non-streaming chat request.
func (c *Client) Chat(ctx context.Context, req ChatRequest) (*ChatResponse, error) {
	req.Stream = false
	body, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+"/api/chat", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := c.HTTP.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("ollama request failed: %w", err)
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		var e struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(data, &e) == nil && e.Error != "" {
			return nil, fmt.Errorf("ollama: %s (status code: %d)", e.Error, resp.StatusCode)
		}
		return nil, fmt.Errorf("ollama: status code %d: %s", resp.StatusCode, strings.TrimSpace(string(data)))
	}

	var out ChatResponse
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, fmt.Errorf("ollama: decoding response: %w", err)
	}
	return &out, nil
}
