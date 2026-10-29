// Package worker is a generic, role-agnostic agent node. Each POST /run carries its own
// system prompt, so the controller decides the worker's role per request.
package worker

import (
	"net/http"

	"github.com/skinnyrad/agent-cluster/internal/agent"
	"github.com/skinnyrad/agent-cluster/internal/httputil"
	"github.com/skinnyrad/agent-cluster/internal/schemas"
)

// Config configures a worker.
type Config struct {
	WorkerID string
	ModelID  string
	Status   schemas.WorkerStatus // reported by /health
	Client   *agent.Client        // nil uses the default Ollama client
}

type worker struct {
	id     string
	model  string
	status schemas.WorkerStatus
	client *agent.Client
}

// New returns the worker's HTTP handler.
func New(cfg Config) http.Handler {
	wk := &worker{id: cfg.WorkerID, model: cfg.ModelID, status: cfg.Status, client: cfg.Client}
	return wk.routes()
}

func (wk *worker) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", wk.health)
	mux.HandleFunc("POST /run", wk.run)
	return mux
}

func (wk *worker) health(w http.ResponseWriter, r *http.Request) {
	httputil.WriteJSON(w, http.StatusOK, schemas.HealthResponse{
		OK:       true,
		WorkerID: wk.id,
		Status:   wk.status,
		ModelID:  wk.model,
	})
}

func (wk *worker) run(w http.ResponseWriter, r *http.Request) {
	var task schemas.AgentTask
	if !httputil.ReadJSON(w, r, &task) {
		return
	}

	a := &agent.Agent{
		Model:        wk.model,
		Instructions: []string{task.SystemPrompt},
		Markdown:     true,
		AddDatetime:  true,
		Client:       wk.client,
	}
	resp, err := a.Run(r.Context(), task.Prompt)
	if err != nil {
		msg := err.Error()
		httputil.WriteJSON(w, http.StatusOK, schemas.AgentResult{
			TaskID:   task.TaskID,
			WorkerID: wk.id,
			Success:  false,
			Metrics:  map[string]any{},
			Error:    &msg,
		})
		return
	}
	httputil.WriteJSON(w, http.StatusOK, schemas.AgentResult{
		TaskID:   task.TaskID,
		WorkerID: wk.id,
		Success:  true,
		Content:  resp.Content,
		Metrics:  resp.Metrics,
	})
}
