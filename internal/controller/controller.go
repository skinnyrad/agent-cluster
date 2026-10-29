// Package controller is the cluster's LLM-driven coordinator. For each job, a router agent
// breaks the prompt into subtasks with dependencies, the controller schedules them onto
// registered workers as slots free up, a reviewer agent may ask for another round, and a
// synthesizer agent merges the results. Progress streams to clients as events, and jobs
// are stored in SQLite.
package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/skinnyrad/agent-cluster/internal/agent"
	"github.com/skinnyrad/agent-cluster/internal/httputil"
	"github.com/skinnyrad/agent-cluster/internal/registry"
	"github.com/skinnyrad/agent-cluster/internal/schemas"
)

// Defaults for Config fields left zero.
const (
	DefaultWorkerTimeout = 5 * time.Minute
	DefaultHeartbeatTTL  = 20 * time.Second
	DefaultMaxRounds     = 2
	DefaultHistoryTurns  = 6
)

// Config configures a controller.
type Config struct {
	Token            string         // cluster token; required on requests when set
	StorePath        string         // SQLite file for jobs and events
	AllowlistPath    string         // optional workers.yaml
	RouterModel      string         // Ollama model for planning and review
	SynthesizerModel string         // Ollama model for the final answer
	Options          *agent.Options // model parameters for the controller's own agents
	WorkerTimeout    time.Duration  // per-subtask limit
	HeartbeatTTL     time.Duration  // a worker silent this long is considered gone
	MaxRounds        int            // 1 disables review; each extra round allows one review
	HistoryTurns     int            // earlier exchanges shown to the router; zero means the default, negative none
	AgentClient      *agent.Client  // nil uses the default Ollama client
}

// Controller coordinates jobs across registered workers.
type Controller struct {
	cfg            Config
	store          *store
	pool           *pool
	dispatchClient *http.Client

	ctx    context.Context
	cancel context.CancelFunc

	mu   sync.Mutex
	jobs map[string]*jobRun // running jobs
}

// New opens the store and returns a controller. Call Close when done.
func New(cfg Config) (*Controller, error) {
	if cfg.WorkerTimeout <= 0 {
		cfg.WorkerTimeout = DefaultWorkerTimeout
	}
	if cfg.HeartbeatTTL <= 0 {
		cfg.HeartbeatTTL = DefaultHeartbeatTTL
	}
	if cfg.MaxRounds <= 0 {
		cfg.MaxRounds = DefaultMaxRounds
	}
	switch {
	case cfg.HistoryTurns == 0:
		cfg.HistoryTurns = DefaultHistoryTurns
	case cfg.HistoryTurns < 0:
		cfg.HistoryTurns = 0
	}
	if cfg.StorePath == "" {
		cfg.StorePath = "agent-cluster.db"
	}
	st, err := openStore(cfg.StorePath)
	if err != nil {
		return nil, fmt.Errorf("opening %s: %w", cfg.StorePath, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &Controller{
		cfg:            cfg,
		store:          st,
		pool:           newPool(cfg.HeartbeatTTL),
		dispatchClient: &http.Client{Timeout: cfg.WorkerTimeout},
		ctx:            ctx,
		cancel:         cancel,
		jobs:           map[string]*jobRun{},
	}, nil
}

// Close cancels running jobs, waits for them to record their state, and closes the store.
func (c *Controller) Close() error {
	c.cancel()
	c.mu.Lock()
	running := make([]*jobRun, 0, len(c.jobs))
	for _, jr := range c.jobs {
		running = append(running, jr)
	}
	c.mu.Unlock()
	for _, jr := range running {
		<-jr.done
	}
	return c.store.Close()
}

// Handler returns the controller's HTTP API.
func (c *Controller) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		httputil.WriteJSON(w, http.StatusOK, map[string]any{"ok": true, "live_workers": len(c.pool.live())})
	})
	mux.HandleFunc("GET /workers", c.listWorkers)
	mux.HandleFunc("POST /workers/register", c.registerWorker)
	mux.HandleFunc("DELETE /workers/{id}", c.deregisterWorker)
	mux.HandleFunc("POST /jobs", c.createJob)
	mux.HandleFunc("GET /jobs", c.listJobs)
	mux.HandleFunc("GET /jobs/{id}", c.getJob)
	mux.HandleFunc("DELETE /jobs/{id}", c.cancelJob)
	mux.HandleFunc("GET /jobs/{id}/events", c.jobEvents)
	mux.HandleFunc("POST /dispatch", c.dispatch)
	return httputil.RequireToken(c.cfg.Token, mux)
}

func (c *Controller) listWorkers(w http.ResponseWriter, r *http.Request) {
	httputil.WriteJSON(w, http.StatusOK, schemas.WorkersResponse{Workers: c.pool.list()})
}

func (c *Controller) registerWorker(w http.ResponseWriter, r *http.Request) {
	var reg schemas.WorkerRegistration
	if !httputil.ReadJSON(w, r, &reg) {
		return
	}
	allow, err := registry.Load(c.cfg.AllowlistPath)
	if err != nil {
		httputil.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	extraTags, err := allow.Check(reg.WorkerID)
	if err != nil {
		httputil.WriteError(w, http.StatusForbidden, err.Error())
		return
	}
	reg.Tags = dedupe(append(reg.Tags, extraTags...))
	c.pool.register(reg)
	httputil.WriteJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (c *Controller) deregisterWorker(w http.ResponseWriter, r *http.Request) {
	c.pool.remove(r.PathValue("id"))
	w.WriteHeader(http.StatusNoContent)
}

func (c *Controller) createJob(w http.ResponseWriter, r *http.Request) {
	var req schemas.JobRequest
	if !httputil.ReadJSON(w, r, &req) {
		return
	}
	jr, err := c.startJob(req)
	if err != nil {
		httputil.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	httputil.WriteJSON(w, http.StatusAccepted, schemas.JobCreated{JobID: jr.job.ID, SessionID: jr.job.SessionID})
}

func (c *Controller) running(id string) *jobRun {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.jobs[id]
}

func (c *Controller) listJobs(w http.ResponseWriter, r *http.Request) {
	limit := 20
	if n, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil && n > 0 && n <= 200 {
		limit = n
	}
	jobs, err := c.store.jobs(r.URL.Query().Get("session_id"), limit)
	if err != nil {
		httputil.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	httputil.WriteJSON(w, http.StatusOK, schemas.JobsResponse{Jobs: jobs})
}

func (c *Controller) getJob(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if jr := c.running(id); jr != nil {
		httputil.WriteJSON(w, http.StatusOK, jr.snapshot())
		return
	}
	j, err := c.store.job(id)
	switch {
	case err != nil:
		httputil.WriteError(w, http.StatusInternalServerError, err.Error())
	case j == nil:
		httputil.WriteError(w, http.StatusNotFound, "job not found")
	default:
		httputil.WriteJSON(w, http.StatusOK, j)
	}
}

func (c *Controller) cancelJob(w http.ResponseWriter, r *http.Request) {
	jr := c.running(r.PathValue("id"))
	if jr == nil {
		httputil.WriteError(w, http.StatusNotFound, "no running job with that id")
		return
	}
	jr.cancel()
	<-jr.done
	httputil.WriteJSON(w, http.StatusOK, jr.snapshot())
}

// jobEvents streams a job's events as server-sent events: everything so far, then live
// events until the job finishes.
func (c *Controller) jobEvents(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var past []schemas.Event
	var live chan schemas.Event
	if jr := c.running(id); jr != nil {
		past, live = jr.subscribe()
		if live != nil {
			defer jr.unsubscribe(live)
		}
	} else {
		j, err := c.store.job(id)
		if err == nil && j == nil {
			httputil.WriteError(w, http.StatusNotFound, "job not found")
			return
		}
		if err == nil {
			past, err = c.store.events(id)
		}
		if err != nil {
			httputil.WriteError(w, http.StatusInternalServerError, err.Error())
			return
		}
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	flusher, _ := w.(http.Flusher)
	write := func(e schemas.Event) bool {
		data, _ := json.Marshal(e)
		if _, err := fmt.Fprintf(w, "id: %d\nevent: %s\ndata: %s\n\n", e.Seq, e.Type, data); err != nil {
			return false
		}
		if flusher != nil {
			flusher.Flush()
		}
		return true
	}
	for _, e := range past {
		if !write(e) {
			return
		}
	}
	if live == nil {
		return
	}

	keepAlive := time.NewTicker(15 * time.Second)
	defer keepAlive.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case e, ok := <-live:
			if !ok || !write(e) {
				return
			}
		case <-keepAlive.C:
			if _, err := fmt.Fprint(w, ": keep-alive\n\n"); err != nil {
				return
			}
			if flusher != nil {
				flusher.Flush()
			}
		}
	}
}

// dispatch runs a job and waits for it to finish. If the client goes away first, the job
// is cancelled.
func (c *Controller) dispatch(w http.ResponseWriter, r *http.Request) {
	var req schemas.JobRequest
	if !httputil.ReadJSON(w, r, &req) {
		return
	}
	jr, err := c.startJob(req)
	if err != nil {
		httputil.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	select {
	case <-jr.done:
	case <-r.Context().Done():
		jr.cancel()
		<-jr.done
		return
	}

	j := jr.snapshot()
	if j.Status == schemas.JobFailed && j.Error == "No live workers available" {
		httputil.WriteError(w, http.StatusServiceUnavailable, j.Error)
		return
	}
	if j.Status != schemas.JobDone {
		log.Printf("job %s %s: %s", j.ID, j.Status, j.Error)
	}
	httputil.WriteJSON(w, http.StatusOK, schemas.DispatchResponse{
		RequestID:      j.ID,
		SessionID:      j.SessionID,
		OriginalPrompt: j.Prompt,
		Status:         j.Status,
		Subtasks:       j.Subtasks,
		Results:        j.Results,
		Synthesis:      j.Synthesis,
		Error:          j.Error,
	})
}
