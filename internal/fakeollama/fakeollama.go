// Package fakeollama is a scriptable stand-in for Ollama's /api/chat, for tests. It
// recognises the controller's agents by their system prompts and answers workers with
// "<model> did: <prompt>", optionally calling a tool first.
package fakeollama

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/skinnyrad/agent-cluster/internal/agent"
)

// Server is a fake Ollama server.
type Server struct {
	URL    string
	Client *agent.Client

	// RouterReply is the router's reply. ReviewReplies are used in order; once they run
	// out, the reviewer says the work is complete.
	RouterReply   string
	ReviewReplies []string
	// SynthesisReply is streamed in two chunks.
	SynthesisReply string

	// ToolModel's first reply calls ToolName with ToolArgs; its second reports what the
	// tool returned.
	ToolModel string
	ToolName  string
	ToolArgs  map[string]any

	// SlowModel's worker calls take SlowDelay, or until the caller gives up.
	SlowModel string
	SlowDelay time.Duration

	// Barrier makes each worker call wait until this many are in flight at once, or reply
	// "TIMED OUT" after two seconds.
	Barrier int

	mu            sync.Mutex
	WorkerCalls   []agent.ChatRequest
	RouterPrompts []string
	ReviewPrompts []string
	SynthPrompts  []string
	inFlight      int
	MaxInFlight   int
	allArrived    chan struct{}
	reviews       int
}

// Start starts a fake server, closed when the test ends.
func Start(t *testing.T) *Server {
	t.Helper()
	f := &Server{SynthesisReply: "FINAL ANSWER", ToolArgs: map[string]any{}}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	f.URL = srv.URL
	f.Client = &agent.Client{BaseURL: srv.URL, HTTP: srv.Client()}
	return f
}

func (f *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var req agent.ChatRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	system, user := req.Messages[0].Content, req.Messages[len(req.Messages)-1].Content

	switch {
	case strings.Contains(system, "task router"):
		f.mu.Lock()
		f.RouterPrompts = append(f.RouterPrompts, user)
		reply := f.RouterReply
		f.mu.Unlock()
		f.reply(w, req, agent.Message{Role: "assistant", Content: reply})
	case strings.Contains(system, "review the work"):
		f.mu.Lock()
		f.ReviewPrompts = append(f.ReviewPrompts, user)
		reply := `{"complete": true, "reason": "looks complete", "subtasks": []}`
		if f.reviews < len(f.ReviewReplies) {
			reply = f.ReviewReplies[f.reviews]
		}
		f.reviews++
		f.mu.Unlock()
		f.reply(w, req, agent.Message{Role: "assistant", Content: reply})
	case strings.Contains(system, "synthesis agent"):
		f.mu.Lock()
		f.SynthPrompts = append(f.SynthPrompts, user)
		f.mu.Unlock()
		f.reply(w, req, agent.Message{Role: "assistant", Content: f.SynthesisReply})
	default:
		f.worker(w, r, req, user)
	}
}

func (f *Server) worker(w http.ResponseWriter, r *http.Request, req agent.ChatRequest, last string) {
	f.mu.Lock()
	f.WorkerCalls = append(f.WorkerCalls, req)
	f.inFlight++
	f.MaxInFlight = max(f.MaxInFlight, f.inFlight)
	barrier := f.Barrier
	var arrived chan struct{}
	if barrier > 0 {
		if f.allArrived == nil {
			f.allArrived = make(chan struct{})
		}
		if f.inFlight == barrier {
			close(f.allArrived)
		}
		arrived = f.allArrived
	}
	f.mu.Unlock()
	defer func() {
		f.mu.Lock()
		f.inFlight--
		f.mu.Unlock()
	}()

	reply := fmt.Sprintf("%s did: %s", req.Model, firstUserMessage(req))
	if arrived != nil {
		select {
		case <-arrived:
		case <-time.After(2 * time.Second):
			reply = "TIMED OUT"
		}
	}
	if f.SlowModel != "" && req.Model == f.SlowModel {
		select {
		case <-time.After(f.SlowDelay):
		case <-r.Context().Done():
			return
		}
	}

	if f.ToolModel != "" && req.Model == f.ToolModel && len(req.Tools) > 0 {
		lastMsg := req.Messages[len(req.Messages)-1]
		if lastMsg.Role != "tool" {
			var call agent.ToolCall
			call.Function.Name = f.ToolName
			call.Function.Arguments = f.ToolArgs
			f.reply(w, req, agent.Message{Role: "assistant", ToolCalls: []agent.ToolCall{call}})
			return
		}
		reply = "tool said: " + last
	}
	f.reply(w, req, agent.Message{Role: "assistant", Content: reply})
}

func firstUserMessage(req agent.ChatRequest) string {
	for _, m := range req.Messages {
		if m.Role == "user" {
			return m.Content
		}
	}
	return ""
}

// reply answers as one JSON object, or as a stream of two content chunks plus a final
// chunk when the request asks to stream.
func (f *Server) reply(w http.ResponseWriter, req agent.ChatRequest, msg agent.Message) {
	final := agent.ChatResponse{Model: req.Model, Done: true, PromptEvalCount: 10, EvalCount: 5}
	if !req.Stream {
		final.Message = msg
		json.NewEncoder(w).Encode(final)
		return
	}
	w.Header().Set("Content-Type", "application/x-ndjson")
	enc := json.NewEncoder(w)
	half := len(msg.Content) / 2
	for _, part := range []string{msg.Content[:half], msg.Content[half:]} {
		if part != "" {
			enc.Encode(agent.ChatResponse{Model: req.Model, Message: agent.Message{Role: "assistant", Content: part}})
		}
	}
	final.Message = agent.Message{Role: "assistant", ToolCalls: msg.ToolCalls}
	enc.Encode(final)
}

// Calls returns a copy of the worker calls so far.
func (f *Server) Calls() []agent.ChatRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]agent.ChatRequest(nil), f.WorkerCalls...)
}

// Max returns the most worker calls that were in flight at once.
func (f *Server) Max() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.MaxInFlight
}
