// Package controller is the fleet's LLM-driven router. For each POST /dispatch a router
// agent splits the prompt into per-worker subtasks (each with its own system prompt), the
// controller sends them to live workers, and a synthesizer agent merges the results.
package controller

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/skinnyrad/agent-cluster/internal/agent"
	"github.com/skinnyrad/agent-cluster/internal/httputil"
	"github.com/skinnyrad/agent-cluster/internal/registry"
	"github.com/skinnyrad/agent-cluster/internal/schemas"
)

const (
	probeTimeout = 5 * time.Second
	// DefaultWorkerTimeout is how long a worker gets to answer one subtask.
	DefaultWorkerTimeout = 5 * time.Minute
)

// Config configures a controller.
type Config struct {
	WorkersPath      string        // path to workers.yaml
	RouterModel      string        // Ollama model for task decomposition
	SynthesizerModel string        // Ollama model for result merging
	PollInterval     time.Duration // time between worker health checks
	WorkerTimeout    time.Duration // per-subtask limit; zero means DefaultWorkerTimeout
	AgentClient      *agent.Client // nil uses the default Ollama client
}

type controller struct {
	workersPath      string
	routerModel      string
	synthesizerModel string
	agentClient      *agent.Client
	probeClient      *http.Client
	dispatchClient   *http.Client

	mu   sync.RWMutex
	live map[string]bool // worker_id -> passed its last health probe
}

// New probes the fleet once, starts background health polling until ctx is done, and
// returns the controller's HTTP handler.
func New(ctx context.Context, cfg Config) http.Handler {
	if cfg.WorkerTimeout <= 0 {
		cfg.WorkerTimeout = DefaultWorkerTimeout
	}
	c := &controller{
		workersPath:      cfg.WorkersPath,
		routerModel:      cfg.RouterModel,
		synthesizerModel: cfg.SynthesizerModel,
		agentClient:      cfg.AgentClient,
		probeClient:      &http.Client{Timeout: probeTimeout},
		dispatchClient:   &http.Client{Timeout: cfg.WorkerTimeout},
		live:             map[string]bool{},
	}
	c.refreshWorkerStatus(ctx)
	go c.pollHealth(ctx, cfg.PollInterval)
	return c.routes()
}

func (c *controller) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /workers", c.listWorkers)
	mux.HandleFunc("POST /dispatch", c.dispatch)
	return mux
}

func (c *controller) isAlive(workerID string) bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.live[workerID]
}

func (c *controller) markDead(workerID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.live[workerID] = false
}

// availableWorkers returns enabled workers that passed their last health probe.
func (c *controller) availableWorkers() ([]schemas.WorkerInfo, error) {
	workers, err := registry.Load(c.workersPath)
	if err != nil {
		return nil, err
	}
	var alive []schemas.WorkerInfo
	for _, w := range workers {
		if c.isAlive(w.WorkerID) {
			alive = append(alive, w)
		}
	}
	return alive, nil
}

// probeWorker reports whether the worker is reachable, says ok, isn't offline, and
// reports the worker_id the registry expects.
func (c *controller) probeWorker(ctx context.Context, w schemas.WorkerInfo) bool {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, w.URL+"/health", nil)
	if err != nil {
		return false
	}
	resp, err := c.probeClient.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return false
	}
	var h schemas.HealthResponse
	if err := json.NewDecoder(resp.Body).Decode(&h); err != nil || h.Validate() != nil {
		return false
	}
	return h.OK && h.WorkerID == w.WorkerID && h.Status != schemas.StatusOffline
}

// refreshWorkerStatus probes all enabled workers concurrently and replaces the live set.
func (c *controller) refreshWorkerStatus(ctx context.Context) {
	workers, err := registry.Load(c.workersPath)
	if err != nil {
		log.Printf("loading %s: %v", c.workersPath, err)
		return
	}

	results := make([]bool, len(workers))
	var wg sync.WaitGroup
	for i, w := range workers {
		wg.Go(func() { results[i] = c.probeWorker(ctx, w) })
	}
	wg.Wait()

	live := make(map[string]bool, len(workers))
	for i, w := range workers {
		live[w.WorkerID] = results[i]
	}
	c.mu.Lock()
	c.live = live
	c.mu.Unlock()
}

func (c *controller) pollHealth(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			c.refreshWorkerStatus(ctx)
		}
	}
}

func (c *controller) listWorkers(w http.ResponseWriter, r *http.Request) {
	workers, err := registry.Load(c.workersPath)
	if err != nil {
		httputil.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	resp := schemas.WorkersResponse{Workers: []schemas.WorkerStatusEntry{}}
	for _, wk := range workers {
		resp.Workers = append(resp.Workers, schemas.WorkerStatusEntry{WorkerInfo: wk, Alive: c.isAlive(wk.WorkerID)})
	}
	httputil.WriteJSON(w, http.StatusOK, resp)
}

func (c *controller) dispatch(w http.ResponseWriter, r *http.Request) {
	var req schemas.DispatchRequest
	if !httputil.ReadJSON(w, r, &req) {
		return
	}

	workers, err := c.availableWorkers()
	if err != nil {
		httputil.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if len(workers) == 0 {
		httputil.WriteError(w, http.StatusServiceUnavailable, "No live workers available")
		return
	}

	requestID := uuid.NewString()
	ctx := r.Context()

	// Router agent: decompose the user prompt into per-worker subtasks.
	var desc strings.Builder
	for _, wk := range workers {
		model := wk.ModelID
		if model == "" {
			model = "unknown"
		}
		fmt.Fprintf(&desc, "- worker_id: %s, model: %s, tags: %v\n", wk.WorkerID, model, wk.Tags)
	}

	router := &agent.Agent{
		Model: c.routerModel,
		Instructions: []string{
			"You are a task router for a distributed agent system.",
			"Given a user task and a list of available worker agents, decompose the task into focused subtasks.",
			"Assign each subtask to a specific worker. Use as many or as few workers as the task requires.",
			"For each subtask, write a system_prompt that defines that worker's role/persona, and a task_prompt with the specific work to do.",
			"You MUST respond with ONLY a valid JSON array and nothing else — no explanation, no markdown fences.",
			`Format: [{"worker_id": "...", "system_prompt": "...", "task_prompt": "..."}, ...]`,
		},
		Client: c.agentClient,
	}
	routerPrompt := fmt.Sprintf(
		"Task: %s\n\nAvailable workers:\n%s\nDecompose this task into subtasks. Return JSON only.",
		req.Prompt, desc.String(),
	)
	routerResp, err := router.Run(ctx, routerPrompt)
	if err != nil {
		httputil.WriteError(w, http.StatusInternalServerError, "router agent: "+err.Error())
		return
	}
	subtasks := parseRouterPlan(routerResp.Content, workers)

	// Fill any empty task prompts (the fallback case) with the original prompt.
	for i := range subtasks {
		if subtasks[i].TaskPrompt == "" {
			subtasks[i].TaskPrompt = req.Prompt
		}
	}

	workerMap := make(map[string]schemas.WorkerInfo, len(workers))
	for _, wk := range workers {
		workerMap[wk.WorkerID] = wk
	}

	// Dispatch all subtasks concurrently. Each goroutine writes only its own slot, so
	// results stay in plan order.
	results := make([]schemas.AgentResult, len(subtasks))
	var wg sync.WaitGroup
	for i, st := range subtasks {
		wk, ok := workerMap[st.WorkerID]
		if !ok {
			results[i] = failedResult(requestID, st.WorkerID,
				fmt.Sprintf("Worker '%s' not found in registry", st.WorkerID))
			continue
		}

		task := schemas.AgentTask{
			TaskID:       uuid.NewString(),
			SystemPrompt: st.SystemPrompt,
			Prompt:       st.TaskPrompt,
			Context:      req.Context,
			SessionID:    req.SessionID,
			UserID:       req.UserID,
		}
		wg.Go(func() {
			result, err := c.runOnWorker(ctx, wk, task)
			if err != nil {
				// A worker that timed out is slow, not down, and a cancelled dispatch says
				// nothing about the worker. Anything else (refused connection, bad status)
				// marks it dead so it's excluded until its next successful health probe.
				if !isTimeout(err) && ctx.Err() == nil {
					c.markDead(wk.WorkerID)
				}
				results[i] = failedResult(task.TaskID, wk.WorkerID, err.Error())
				return
			}
			results[i] = *result
		})
	}
	wg.Wait()

	// Synthesizer agent: merge all worker results into a final answer.
	synthesizer := &agent.Agent{
		Model: c.synthesizerModel,
		Instructions: []string{
			"You are a synthesis agent.",
			"You receive the results from multiple specialist worker agents and combine them into a single coherent final answer.",
			"Be concise and structured. Directly address the original task.",
		},
		Markdown: true,
		Client:   c.agentClient,
	}
	sections := make([]string, 0, len(results))
	for _, res := range results {
		body := res.Content
		if !res.Success {
			body = "ERROR: " + deref(res.Error, "")
		}
		sections = append(sections, fmt.Sprintf("### Worker %s\n%s", res.WorkerID, body))
	}
	synthResp, err := synthesizer.Run(ctx, fmt.Sprintf(
		"Original task: %s\n\nWorker results:\n%s\n\nSynthesize a final answer.",
		req.Prompt, strings.Join(sections, "\n\n"),
	))
	if err != nil {
		httputil.WriteError(w, http.StatusInternalServerError, "synthesizer agent: "+err.Error())
		return
	}

	httputil.WriteJSON(w, http.StatusOK, schemas.DispatchResponse{
		RequestID:      requestID,
		OriginalPrompt: req.Prompt,
		Subtasks:       subtasks,
		Results:        results,
		Synthesis:      synthResp.Content,
	})
}

// runOnWorker posts task to the worker's /run endpoint.
func (c *controller) runOnWorker(ctx context.Context, wk schemas.WorkerInfo, task schemas.AgentTask) (*schemas.AgentResult, error) {
	body, err := json.Marshal(task)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, wk.URL+"/run", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.dispatchClient.Do(req)
	if isTimeout(err) {
		return nil, fmt.Errorf("worker %s did not finish within %s: %w", wk.WorkerID, c.dispatchClient.Timeout, err)
	}
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, fmt.Errorf("worker %s returned HTTP %d", wk.WorkerID, resp.StatusCode)
	}

	var result schemas.AgentResult
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decoding result from %s: %w", wk.WorkerID, err)
	}
	if result.Metrics == nil {
		result.Metrics = map[string]any{}
	}
	return &result, nil
}

// isTimeout reports whether err is a deadline or client timeout.
func isTimeout(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var ne net.Error
	return errors.As(err, &ne) && ne.Timeout()
}

func failedResult(taskID, workerID, msg string) schemas.AgentResult {
	return schemas.AgentResult{
		TaskID:   taskID,
		WorkerID: workerID,
		Success:  false,
		Metrics:  map[string]any{},
		Error:    &msg,
	}
}
