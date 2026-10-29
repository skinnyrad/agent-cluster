// Package schemas defines the JSON contracts shared by the controller, workers and chat client.
package schemas

import (
	"errors"
	"time"
)

// WorkerStatus is one of "idle", "busy" or "offline".
type WorkerStatus string

const (
	StatusIdle    WorkerStatus = "idle"
	StatusBusy    WorkerStatus = "busy"
	StatusOffline WorkerStatus = "offline"
)

// WorkerRegistration is what a worker sends to the controller's POST /workers/register,
// both when it starts and as a heartbeat.
type WorkerRegistration struct {
	WorkerID   string   `json:"worker_id"`
	URL        string   `json:"url"`         // where the controller can reach the worker
	ModelID    string   `json:"model_id"`    // Ollama model the worker runs
	OllamaHost string   `json:"ollama_host"` // Ollama server the worker uses
	Tags       []string `json:"tags"`        // free-form capabilities, e.g. "gpu", "code"
	Tools      []string `json:"tools"`       // tool names the worker can run
	Slots      int      `json:"slots"`       // subtasks it runs at once
	Active     int      `json:"active"`      // subtasks running right now
}

func (r *WorkerRegistration) Validate() error {
	if r.WorkerID == "" {
		return errors.New("worker_id is required")
	}
	if r.URL == "" {
		return errors.New("url is required")
	}
	if r.Slots <= 0 {
		r.Slots = 1
	}
	if r.Tags == nil {
		r.Tags = []string{}
	}
	if r.Tools == nil {
		r.Tools = []string{}
	}
	return nil
}

// HealthResponse is returned by a worker's GET /health.
type HealthResponse struct {
	OK       bool         `json:"ok"`
	WorkerID string       `json:"worker_id"`
	Status   WorkerStatus `json:"status"`
	ModelID  string       `json:"model_id"`
	Slots    int          `json:"slots"`
	Active   int          `json:"active"`
	Tools    []string     `json:"tools"`
	Tags     []string     `json:"tags"`
}

// WorkerInfo is one row of the controller's GET /workers response.
type WorkerInfo struct {
	WorkerRegistration
	Alive    bool      `json:"alive"`
	InFlight int       `json:"in_flight"` // subtasks the controller has sent and not finished
	LastSeen time.Time `json:"last_seen"`
}

// WorkersResponse is the controller's GET /workers response.
type WorkersResponse struct {
	Workers []WorkerInfo `json:"workers"`
}

// AgentTask is sent from the controller to a worker's POST /run.
type AgentTask struct {
	TaskID       string         `json:"task_id"`       // unique task identifier
	SystemPrompt string         `json:"system_prompt"` // role/instructions for this task
	Prompt       string         `json:"prompt"`        // the work to do
	Context      map[string]any `json:"context"`       // extra task context
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

// AgentResult is the outcome of one subtask.
type AgentResult struct {
	TaskID    string         `json:"task_id"`
	SubtaskID string         `json:"subtask_id,omitempty"` // set by the controller
	WorkerID  string         `json:"worker_id"`
	Success   bool           `json:"success"`
	Content   string         `json:"content"`
	Metrics   map[string]any `json:"metrics"`
	Error     *string        `json:"error"`
}

// Worker /run responses are a stream of newline-delimited WorkerEvents, ending with one
// of type "result".
const (
	WorkerEventToken      = "token"
	WorkerEventToolCall   = "tool_call"
	WorkerEventToolResult = "tool_result"
	WorkerEventResult     = "result"
)

// WorkerEvent is one line of a worker's /run response.
type WorkerEvent struct {
	Type   string         `json:"type"`
	Text   string         `json:"text,omitempty"`   // token
	Tool   string         `json:"tool,omitempty"`   // tool_call, tool_result
	Args   map[string]any `json:"args,omitempty"`   // tool_call
	Output string         `json:"output,omitempty"` // tool_result
	Result *AgentResult   `json:"result,omitempty"` // result
}

// SubTask is one step of a plan.
type SubTask struct {
	ID           string   `json:"id"`
	Title        string   `json:"title,omitempty"`
	SystemPrompt string   `json:"system_prompt"`
	TaskPrompt   string   `json:"task_prompt"`
	DependsOn    []string `json:"depends_on"` // subtask IDs whose results this one needs
	Requires     []string `json:"requires"`   // tags or tools the worker must have
	WorkerID     string   `json:"worker_id,omitempty"`
	Round        int      `json:"round"`
}

// JobRequest is the body of the controller's POST /jobs and POST /dispatch.
type JobRequest struct {
	Prompt    string         `json:"prompt"`
	SessionID string         `json:"session_id,omitempty"` // conversation to continue
	Context   map[string]any `json:"context"`
}

func (r *JobRequest) Validate() error {
	if r.Prompt == "" {
		return errors.New("prompt is required")
	}
	if r.Context == nil {
		r.Context = map[string]any{}
	}
	return nil
}

// JobStatus is the lifecycle state of a job.
type JobStatus string

const (
	JobRunning   JobStatus = "running"
	JobDone      JobStatus = "done"
	JobFailed    JobStatus = "failed"
	JobCancelled JobStatus = "cancelled"
)

func (s JobStatus) Finished() bool { return s != JobRunning }

// Job is the state of one request to the cluster.
type Job struct {
	ID         string        `json:"id"`
	SessionID  string        `json:"session_id"`
	Prompt     string        `json:"prompt"`
	Status     JobStatus     `json:"status"`
	Subtasks   []SubTask     `json:"subtasks"`
	Results    []AgentResult `json:"results"`
	Synthesis  string        `json:"synthesis"`
	Error      string        `json:"error,omitempty"`
	CreatedAt  time.Time     `json:"created_at"`
	FinishedAt *time.Time    `json:"finished_at,omitempty"`
}

// JobCreated is the response to POST /jobs.
type JobCreated struct {
	JobID     string `json:"job_id"`
	SessionID string `json:"session_id"`
}

// JobsResponse is the controller's GET /jobs response.
type JobsResponse struct {
	Jobs []Job `json:"jobs"`
}

// Job event types, streamed from GET /jobs/{id}/events.
const (
	EventJobStarted        = "job.started"
	EventPlan              = "plan"
	EventSubtaskStarted    = "subtask.started"
	EventSubtaskToken      = "subtask.token"
	EventSubtaskToolCall   = "subtask.tool_call"
	EventSubtaskToolResult = "subtask.tool_result"
	EventSubtaskFinished   = "subtask.finished"
	EventReview            = "review"
	EventSynthesisStarted  = "synthesis.started"
	EventSynthesisToken    = "synthesis.token"
	EventJobFinished       = "job.finished"
)

// Event is one step of a job's progress. Only the fields relevant to its type are set.
type Event struct {
	Seq       int            `json:"seq"`
	JobID     string         `json:"job_id"`
	Type      string         `json:"type"`
	Time      time.Time      `json:"time"`
	Round     int            `json:"round,omitempty"`
	SubtaskID string         `json:"subtask_id,omitempty"`
	WorkerID  string         `json:"worker_id,omitempty"`
	Subtasks  []SubTask      `json:"subtasks,omitempty"` // plan, review
	Text      string         `json:"text,omitempty"`     // tokens, review reason
	Tool      string         `json:"tool,omitempty"`
	Args      map[string]any `json:"args,omitempty"`
	Output    string         `json:"output,omitempty"`
	Result    *AgentResult   `json:"result,omitempty"`
	Status    JobStatus      `json:"status,omitempty"` // job.finished
	Synthesis string         `json:"synthesis,omitempty"`
	Error     string         `json:"error,omitempty"`
}

// Persistent reports whether the event is stored and replayed. Token events are only
// streamed live.
func (e *Event) Persistent() bool {
	return e.Type != EventSubtaskToken && e.Type != EventSynthesisToken
}

// DispatchResponse is returned by the synchronous POST /dispatch.
type DispatchResponse struct {
	RequestID      string        `json:"request_id"`
	SessionID      string        `json:"session_id"`
	OriginalPrompt string        `json:"original_prompt"`
	Status         JobStatus     `json:"status"`
	Subtasks       []SubTask     `json:"subtasks"`
	Results        []AgentResult `json:"results"`
	Synthesis      string        `json:"synthesis"`
	Error          string        `json:"error,omitempty"`
}
