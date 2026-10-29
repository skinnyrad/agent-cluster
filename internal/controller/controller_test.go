package controller

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/skinnyrad/agent-cluster/internal/agent"
	"github.com/skinnyrad/agent-cluster/internal/schemas"
	"github.com/skinnyrad/agent-cluster/internal/worker"
)

// fakeOllama answers /api/chat based on which agent is calling, identified by its system prompt.
type fakeOllama struct {
	routerReply string

	mu          sync.Mutex
	workerCalls []agent.ChatRequest
	synthPrompt string
}

func (f *fakeOllama) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var req agent.ChatRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	system, user := req.Messages[0].Content, req.Messages[len(req.Messages)-1].Content

	var reply string
	f.mu.Lock()
	switch {
	case strings.Contains(system, "task router"):
		reply = f.routerReply
	case strings.Contains(system, "synthesis agent"):
		f.synthPrompt = user
		reply = "FINAL ANSWER"
	default:
		f.workerCalls = append(f.workerCalls, req)
		reply = fmt.Sprintf("%s did: %s", req.Model, user)
	}
	f.mu.Unlock()

	json.NewEncoder(w).Encode(agent.ChatResponse{
		Model:           req.Model,
		Message:         agent.Message{Role: "assistant", Content: reply},
		Done:            true,
		PromptEvalCount: 10,
		EvalCount:       5,
	})
}

type cluster struct {
	ollama     *fakeOllama
	controller *httptest.Server
	workers    map[string]*httptest.Server
}

// startCluster starts a fake Ollama, the named workers and a controller that knows about them.
func startCluster(t *testing.T, routerReply string, workerIDs ...string) *cluster {
	t.Helper()
	ollama := &fakeOllama{routerReply: routerReply}
	ollamaSrv := httptest.NewServer(ollama)
	t.Cleanup(ollamaSrv.Close)
	client := &agent.Client{BaseURL: ollamaSrv.URL, HTTP: ollamaSrv.Client()}

	var yaml strings.Builder
	yaml.WriteString("workers:\n")
	workers := map[string]*httptest.Server{}
	for _, id := range workerIDs {
		srv := httptest.NewServer(worker.New(worker.Config{
			WorkerID: id, ModelID: "model-" + id, Status: schemas.StatusIdle, Client: client,
		}))
		t.Cleanup(srv.Close)
		workers[id] = srv
		fmt.Fprintf(&yaml, "  - worker_id: %s\n    url: %s\n    model_id: model-%s\n", id, srv.URL, id)
	}
	path := filepath.Join(t.TempDir(), "workers.yaml")
	if err := os.WriteFile(path, []byte(yaml.String()), 0o644); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	ctrl := httptest.NewServer(New(ctx, Config{
		WorkersPath:      path,
		RouterModel:      "router-model",
		SynthesizerModel: "synth-model",
		PollInterval:     time.Hour,
		AgentClient:      client,
	}))
	t.Cleanup(ctrl.Close)

	return &cluster{ollama: ollama, controller: ctrl, workers: workers}
}

func (c *cluster) dispatch(t *testing.T, prompt string) (int, schemas.DispatchResponse) {
	t.Helper()
	body, _ := json.Marshal(schemas.DispatchRequest{Prompt: prompt})
	resp, err := http.Post(c.controller.URL+"/dispatch", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out schemas.DispatchResponse
	if resp.StatusCode == http.StatusOK {
		if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
			t.Fatal(err)
		}
	}
	return resp.StatusCode, out
}

func (c *cluster) aliveMap(t *testing.T) map[string]bool {
	t.Helper()
	resp, err := http.Get(c.controller.URL + "/workers")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out schemas.WorkersResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	alive := map[string]bool{}
	for _, w := range out.Workers {
		alive[w.WorkerID] = w.Alive
	}
	return alive
}

func TestDispatchEndToEnd(t *testing.T) {
	plan := "```json\n" + `[
		{"worker_id": "w1", "system_prompt": "You are a code reviewer.", "task_prompt": "Review bubble sort."},
		{"worker_id": "w2", "system_prompt": "You are a test engineer.", "task_prompt": "Write tests."}
	]` + "\n```"
	c := startCluster(t, plan, "w1", "w2")

	status, resp := c.dispatch(t, "Review and test bubble sort")
	if status != http.StatusOK {
		t.Fatalf("dispatch status = %d", status)
	}
	if resp.Synthesis != "FINAL ANSWER" || resp.OriginalPrompt != "Review and test bubble sort" || resp.RequestID == "" {
		t.Errorf("unexpected response: %+v", resp)
	}
	if len(resp.Subtasks) != 2 || len(resp.Results) != 2 {
		t.Fatalf("got %d subtasks / %d results, want 2 / 2", len(resp.Subtasks), len(resp.Results))
	}
	for i, want := range []string{"model-w1 did: Review bubble sort.", "model-w2 did: Write tests."} {
		r := resp.Results[i]
		if !r.Success || r.Content != want {
			t.Errorf("result %d = %+v, want success with %q", i, r, want)
		}
		if r.Metrics["total_tokens"] != float64(15) {
			t.Errorf("result %d metrics = %v, want total_tokens 15", i, r.Metrics)
		}
	}

	// The worker's system message carries the router-assigned role.
	if got := c.ollama.workerCalls[0].Messages[0].Content; !strings.HasPrefix(got, "You are a code reviewer.") {
		t.Errorf("worker system prompt = %q", got)
	}
	if !strings.Contains(c.ollama.synthPrompt, "### Worker w2\nmodel-w2 did: Write tests.") {
		t.Errorf("synthesizer prompt missing worker results:\n%s", c.ollama.synthPrompt)
	}
}

func TestDispatchFallsBackToBroadcast(t *testing.T) {
	c := startCluster(t, "Sorry, I can't produce JSON.", "w1", "w2")

	status, resp := c.dispatch(t, "Say hi")
	if status != http.StatusOK {
		t.Fatalf("dispatch status = %d", status)
	}
	if len(resp.Subtasks) != 2 {
		t.Fatalf("got %d subtasks, want one per worker", len(resp.Subtasks))
	}
	for _, st := range resp.Subtasks {
		if st.TaskPrompt != "Say hi" || st.SystemPrompt != fallbackSubtaskSystemPrompt {
			t.Errorf("fallback subtask = %+v", st)
		}
	}
}

func TestDeadWorkersAreExcluded(t *testing.T) {
	plan := `[{"worker_id": "w1", "system_prompt": "S", "task_prompt": "T"},
	          {"worker_id": "w2", "system_prompt": "S", "task_prompt": "T"}]`
	c := startCluster(t, plan, "w1", "w2")

	// w2 goes down after the startup probe, so the first dispatch still sends it work.
	c.workers["w2"].Close()
	_, resp := c.dispatch(t, "do it")
	if resp.Results[1].Success || resp.Results[1].Error == nil {
		t.Fatalf("expected w2 to fail, got %+v", resp.Results[1])
	}
	if alive := c.aliveMap(t); !alive["w1"] || alive["w2"] {
		t.Fatalf("alive = %v, want w1 alive and w2 dead", alive)
	}

	// Next dispatch only sees w1, so the router's w2 subtask is reassigned to it.
	_, resp = c.dispatch(t, "do it again")
	for _, r := range resp.Results {
		if r.WorkerID != "w1" || !r.Success {
			t.Errorf("result %+v, want success from w1", r)
		}
	}
}

func TestDispatchWithNoLiveWorkers(t *testing.T) {
	c := startCluster(t, "[]")
	if status, _ := c.dispatch(t, "anything"); status != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", status)
	}
}

func TestDispatchRejectsBadBody(t *testing.T) {
	c := startCluster(t, "[]", "w1")
	resp, err := http.Post(c.controller.URL+"/dispatch", "application/json", strings.NewReader(`{"nope": 1}`))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422", resp.StatusCode)
	}
}
