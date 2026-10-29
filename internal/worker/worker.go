// Package worker is a generic, role-agnostic agent node. Each POST /run carries its own
// system prompt, so the controller decides the worker's role per request. The worker runs
// at most Slots tasks at once (more wait their turn), streams progress back as
// newline-delimited JSON, and registers itself with the controller on a heartbeat.
package worker

import (
	"bytes"
	"context"
	"encoding/json"
	"log"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/skinnyrad/agent-cluster/internal/agent"
	"github.com/skinnyrad/agent-cluster/internal/httputil"
	"github.com/skinnyrad/agent-cluster/internal/schemas"
)

// Config configures a worker.
type Config struct {
	WorkerID string
	ModelID  string
	Options  *agent.Options // model parameters, e.g. context size
	MaxSteps int            // model calls per task when using tools

	Tools     []agent.Tool
	ToolNames []string
	Tags      []string
	Slots     int // tasks run at once; set it to what the Ollama server can run in parallel

	Token  string        // cluster token; required on requests when set
	Client *agent.Client // nil uses the default Ollama client

	// Registration. With no ControllerURL the worker doesn't register.
	ControllerURL string
	AdvertiseURL  string // URL the controller should use to reach this worker
	Heartbeat     time.Duration
}

// Worker serves tasks and keeps itself registered with the controller.
type Worker struct {
	cfg      Config
	slots    chan struct{}
	active   atomic.Int32
	draining atomic.Bool
	http     *http.Client
}

// New returns a worker for cfg.
func New(cfg Config) *Worker {
	if cfg.Slots <= 0 {
		cfg.Slots = 1
	}
	if cfg.Heartbeat <= 0 {
		cfg.Heartbeat = 5 * time.Second
	}
	if cfg.Client == nil {
		cfg.Client = agent.DefaultClient()
	}
	if cfg.Tags == nil {
		cfg.Tags = []string{}
	}
	if cfg.ToolNames == nil {
		cfg.ToolNames = []string{}
	}
	return &Worker{
		cfg:   cfg,
		slots: make(chan struct{}, cfg.Slots),
		http:  &http.Client{Timeout: 10 * time.Second},
	}
}

// Handler returns the worker's HTTP API.
func (wk *Worker) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", wk.health)
	mux.HandleFunc("POST /run", wk.run)
	return httputil.RequireToken(wk.cfg.Token, mux)
}

func (wk *Worker) status() schemas.WorkerStatus {
	switch {
	case wk.draining.Load():
		return schemas.StatusOffline
	case int(wk.active.Load()) >= wk.cfg.Slots:
		return schemas.StatusBusy
	}
	return schemas.StatusIdle
}

func (wk *Worker) health(w http.ResponseWriter, r *http.Request) {
	httputil.WriteJSON(w, http.StatusOK, schemas.HealthResponse{
		OK:       true,
		WorkerID: wk.cfg.WorkerID,
		Status:   wk.status(),
		ModelID:  wk.cfg.ModelID,
		Slots:    wk.cfg.Slots,
		Active:   int(wk.active.Load()),
		Tools:    wk.cfg.ToolNames,
		Tags:     wk.cfg.Tags,
	})
}

// run executes one task and streams WorkerEvents as newline-delimited JSON, ending with
// a "result" event.
func (wk *Worker) run(w http.ResponseWriter, r *http.Request) {
	var task schemas.AgentTask
	if !httputil.ReadJSON(w, r, &task) {
		return
	}
	ctx := r.Context()

	// Wait for a free slot.
	select {
	case wk.slots <- struct{}{}:
	case <-ctx.Done():
		return
	}
	wk.active.Add(1)
	defer func() {
		wk.active.Add(-1)
		<-wk.slots
	}()

	w.Header().Set("Content-Type", "application/x-ndjson")
	w.WriteHeader(http.StatusOK)
	flusher, _ := w.(http.Flusher)
	var mu sync.Mutex
	enc := json.NewEncoder(w)
	send := func(e schemas.WorkerEvent) {
		mu.Lock()
		defer mu.Unlock()
		if err := enc.Encode(e); err == nil && flusher != nil {
			flusher.Flush()
		}
	}

	a := &agent.Agent{
		Model:        wk.cfg.ModelID,
		Instructions: []string{task.SystemPrompt},
		Markdown:     true,
		AddDatetime:  true,
		Options:      wk.cfg.Options,
		Tools:        wk.cfg.Tools,
		MaxSteps:     wk.cfg.MaxSteps,
		Client:       wk.cfg.Client,
		OnEvent: func(e agent.Event) {
			send(schemas.WorkerEvent{Type: e.Type, Text: e.Text, Tool: e.Tool, Args: e.Args, Output: e.Output})
		},
	}
	result := schemas.AgentResult{TaskID: task.TaskID, WorkerID: wk.cfg.WorkerID, Metrics: map[string]any{}}
	if resp, err := a.Run(ctx, task.Prompt); err != nil {
		msg := err.Error()
		result.Error = &msg
	} else {
		result.Success = true
		result.Content = resp.Content
		result.Metrics = resp.Metrics
	}
	send(schemas.WorkerEvent{Type: schemas.WorkerEventResult, Result: &result})
}

// Registration describes this worker to the controller.
func (wk *Worker) Registration() schemas.WorkerRegistration {
	return schemas.WorkerRegistration{
		WorkerID:   wk.cfg.WorkerID,
		URL:        wk.cfg.AdvertiseURL,
		ModelID:    wk.cfg.ModelID,
		OllamaHost: wk.cfg.Client.BaseURL,
		Tags:       wk.cfg.Tags,
		Tools:      wk.cfg.ToolNames,
		Slots:      wk.cfg.Slots,
		Active:     int(wk.active.Load()),
	}
}

// Register sends one registration (or heartbeat) to the controller.
func (wk *Worker) Register(ctx context.Context) error {
	body, err := json.Marshal(wk.Registration())
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, wk.cfg.ControllerURL+"/workers/register", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	httputil.SetToken(req, wk.cfg.Token)
	resp, err := wk.http.Do(req)
	if err != nil {
		return err
	}
	return httputil.DecodeResponse(resp, nil)
}

// deregister tells the controller this worker is leaving.
func (wk *Worker) deregister() {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, wk.cfg.ControllerURL+"/workers/"+wk.cfg.WorkerID, nil)
	if err != nil {
		return
	}
	httputil.SetToken(req, wk.cfg.Token)
	if resp, err := wk.http.Do(req); err == nil {
		resp.Body.Close()
	}
}

// RunHeartbeat registers with the controller now and every Heartbeat until ctx is done,
// then deregisters. It does nothing without a ControllerURL.
func (wk *Worker) RunHeartbeat(ctx context.Context) {
	if wk.cfg.ControllerURL == "" {
		return
	}
	registered := false
	ticker := time.NewTicker(wk.cfg.Heartbeat)
	defer ticker.Stop()
	for {
		err := wk.Register(ctx)
		switch {
		case err == nil && !registered:
			log.Printf("registered with controller at %s", wk.cfg.ControllerURL)
			registered = true
		case err != nil && ctx.Err() == nil:
			if registered {
				log.Printf("lost controller at %s: %v", wk.cfg.ControllerURL, err)
			} else {
				log.Printf("can't register with controller at %s yet: %v", wk.cfg.ControllerURL, err)
			}
			registered = false
		}
		select {
		case <-ctx.Done():
			wk.draining.Store(true)
			wk.deregister()
			return
		case <-ticker.C:
		}
	}
}
