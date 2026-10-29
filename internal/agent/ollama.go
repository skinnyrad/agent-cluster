package agent

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
)

// Message is one chat message.
type Message struct {
	Role      string     `json:"role"`
	Content   string     `json:"content"`
	ToolCalls []ToolCall `json:"tool_calls,omitempty"`
	ToolName  string     `json:"tool_name,omitempty"` // on role "tool" messages
}

// ToolCall is a model's request to run a tool.
type ToolCall struct {
	Function struct {
		Name      string         `json:"name"`
		Arguments map[string]any `json:"arguments"`
	} `json:"function"`
}

// ToolSpec describes a tool to the model.
type ToolSpec struct {
	Type     string       `json:"type"` // always "function"
	Function FunctionSpec `json:"function"`
}

// FunctionSpec is the callable part of a ToolSpec.
type FunctionSpec struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
}

// Options are Ollama model parameters. Zero values are left to Ollama's defaults.
type Options struct {
	NumCtx     int `json:"num_ctx,omitempty"`     // context window, in tokens
	NumPredict int `json:"num_predict,omitempty"` // maximum tokens to generate
}

// ChatRequest is the body of Ollama's POST /api/chat.
type ChatRequest struct {
	Model    string          `json:"model"`
	Messages []Message       `json:"messages"`
	Stream   bool            `json:"stream"`
	Tools    []ToolSpec      `json:"tools,omitempty"`
	Format   json.RawMessage `json:"format,omitempty"`
	Options  *Options        `json:"options,omitempty"`
}

// ChatResponse is Ollama's response, or one streamed chunk of it.
type ChatResponse struct {
	Model           string  `json:"model"`
	Message         Message `json:"message"`
	Done            bool    `json:"done"`
	PromptEvalCount int     `json:"prompt_eval_count"`
	EvalCount       int     `json:"eval_count"`
	Error           string  `json:"error,omitempty"`
}

// Client talks to an Ollama server.
type Client struct {
	BaseURL string
	HTTP    *http.Client
}

// DefaultClient returns a client for OLLAMA_HOST, or http://localhost:11434 if unset.
func DefaultClient() *Client {
	return &Client{BaseURL: HostFromEnv(), HTTP: http.DefaultClient}
}

// HostFromEnv returns the Ollama base URL from OLLAMA_HOST.
func HostFromEnv() string {
	host := strings.TrimSpace(os.Getenv("OLLAMA_HOST"))
	if host == "" {
		return "http://localhost:11434"
	}
	if !strings.Contains(host, "://") {
		host = "http://" + host
	}
	return strings.TrimRight(host, "/")
}

// Chat sends a chat request. If onChunk is set the reply is streamed and onChunk sees
// each chunk; either way the returned response holds the whole message.
func (c *Client) Chat(ctx context.Context, req ChatRequest, onChunk func(*ChatResponse)) (*ChatResponse, error) {
	req.Stream = onChunk != nil
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

	if resp.StatusCode != http.StatusOK {
		data, _ := io.ReadAll(resp.Body)
		var e struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(data, &e) == nil && e.Error != "" {
			return nil, fmt.Errorf("ollama: %s (status code: %d)", e.Error, resp.StatusCode)
		}
		return nil, fmt.Errorf("ollama: status code %d: %s", resp.StatusCode, strings.TrimSpace(string(data)))
	}

	if !req.Stream {
		var out ChatResponse
		if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
			return nil, fmt.Errorf("ollama: decoding response: %w", err)
		}
		return &out, nil
	}

	// Streamed: one JSON object per line. Content arrives in pieces; tool calls and
	// token counts arrive whole.
	var out ChatResponse
	var content strings.Builder
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		var chunk ChatResponse
		if err := json.Unmarshal(line, &chunk); err != nil {
			return nil, fmt.Errorf("ollama: decoding stream: %w", err)
		}
		if chunk.Error != "" {
			return nil, fmt.Errorf("ollama: %s", chunk.Error)
		}
		onChunk(&chunk)
		content.WriteString(chunk.Message.Content)
		out.Message.ToolCalls = append(out.Message.ToolCalls, chunk.Message.ToolCalls...)
		if chunk.Done {
			out.Model = chunk.Model
			out.Done = true
			out.PromptEvalCount = chunk.PromptEvalCount
			out.EvalCount = chunk.EvalCount
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("ollama: reading stream: %w", err)
	}
	if !out.Done {
		return nil, fmt.Errorf("ollama: stream ended early")
	}
	out.Message.Role = "assistant"
	out.Message.Content = content.String()
	return &out, nil
}
