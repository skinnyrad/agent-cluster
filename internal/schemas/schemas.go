// Package schemas defines the JSON contracts shared by the controller, workers and chat client.
package schemas

import (
	"errors"
	"fmt"
)

// WorkerStatus is one of "idle", "busy" or "offline".
type WorkerStatus string

const (
	StatusIdle    WorkerStatus = "idle"
	StatusBusy    WorkerStatus = "busy"
	StatusOffline WorkerStatus = "offline"
)

func (s WorkerStatus) Valid() bool {
	switch s {
	case StatusIdle, StatusBusy, StatusOffline:
		return true
	}
	return false
}

// AgentTask is sent from the controller to a worker's POST /run.
type AgentTask struct {
	TaskID       string         `json:"task_id"`       // unique task identifier
	SystemPrompt string         `json:"system_prompt"` // dynamic role/instructions assigned to this worker for the task
	Prompt       string         `json:"prompt"`        // task prompt sent to the worker agent
	Context      map[string]any `json:"context"`       // extra task context
	SessionID    *string        `json:"session_id"`    // optional shared session identifier
	UserID       *string        `json:"user_id"`       // optional end-user identifier
}

func (t *AgentTask) Validate() error {
	if t.TaskID == "" {
		return errors.New("task_id is required")
	}
	if t.Context == nil {
		t.Context = map[string]any{}
	}
	return nil
}

// AgentResult is returned by a worker's POST /run.
type AgentResult struct {
	TaskID   string         `json:"task_id"`
	WorkerID string         `json:"worker_id"`
	Role     string         `json:"role"`
	Success  bool           `json:"success"`
	Content  string         `json:"content"`
	Metrics  map[string]any `json:"metrics"`
	Error    *string        `json:"error"`
}

// HealthResponse is returned by a worker's GET /health.
type HealthResponse struct {
	OK       bool         `json:"ok"`
	WorkerID string       `json:"worker_id"`
	Status   WorkerStatus `json:"status"`
	ModelID  string       `json:"model_id"`
}

func (h *HealthResponse) Validate() error {
	if h.WorkerID == "" {
		return errors.New("worker_id is required")
	}
	if !h.Status.Valid() {
		return fmt.Errorf("invalid status %q", h.Status)
	}
	return nil
}

// WorkerInfo is one entry from workers.yaml.
type WorkerInfo struct {
	WorkerID string   `json:"worker_id" yaml:"worker_id"`
	URL      string   `json:"url" yaml:"url"`
	ModelID  string   `json:"model_id" yaml:"model_id"`
	Enabled  bool     `json:"enabled" yaml:"enabled"`
	Tags     []string `json:"tags" yaml:"tags"`
}

// SubTask is one entry in the router's plan.
type SubTask struct {
	WorkerID     string `json:"worker_id"`     // ID of the worker assigned this subtask
	SystemPrompt string `json:"system_prompt"` // dynamic role/instructions for this worker
	TaskPrompt   string `json:"task_prompt"`   // specific task prompt for this worker
}

// DispatchRequest is the body of the controller's POST /dispatch.
type DispatchRequest struct {
	Prompt    string         `json:"prompt"` // high-level task prompt
	Context   map[string]any `json:"context"`
	SessionID *string        `json:"session_id"`
	UserID    *string        `json:"user_id"`
}

func (r *DispatchRequest) Validate() error {
	if r.Prompt == "" {
		return errors.New("prompt is required")
	}
	if r.Context == nil {
		r.Context = map[string]any{}
	}
	return nil
}

// DispatchResponse is returned by the controller's POST /dispatch.
type DispatchResponse struct {
	RequestID      string        `json:"request_id"`
	OriginalPrompt string        `json:"original_prompt"`
	Subtasks       []SubTask     `json:"subtasks"`
	Results        []AgentResult `json:"results"`
	Synthesis      string        `json:"synthesis"`
}

// WorkerStatusEntry is one row of the controller's GET /workers response.
type WorkerStatusEntry struct {
	WorkerInfo
	Alive bool `json:"alive"`
}

// WorkersResponse is the controller's GET /workers response.
type WorkersResponse struct {
	Workers []WorkerStatusEntry `json:"workers"`
}
