package controller

import (
	"encoding/json"
	"strings"

	"github.com/skinnyrad/agent-cluster/internal/schemas"
)

const (
	defaultSubtaskSystemPrompt  = "You are a helpful assistant."
	fallbackSubtaskSystemPrompt = "You are a helpful assistant. Complete the assigned task concisely."
)

// planItem uses pointers so a missing key can be told apart from an empty string.
type planItem struct {
	WorkerID     *string `json:"worker_id"`
	SystemPrompt *string `json:"system_prompt"`
	TaskPrompt   *string `json:"task_prompt"`
}

// parseRouterPlan parses the router agent's JSON output into subtasks. It takes the text
// between the first '[' and the last ']', so prose or code fences around the array are
// ignored. Subtasks naming an unknown worker go to the first worker. If nothing usable
// comes back, every worker gets the original prompt with a generic system prompt; those
// subtasks have an empty TaskPrompt for the caller to fill in.
func parseRouterPlan(text string, workers []schemas.WorkerInfo) []schemas.SubTask {
	if subtasks := tryParsePlan(text, workers); len(subtasks) > 0 {
		return subtasks
	}

	subtasks := make([]schemas.SubTask, 0, len(workers))
	for _, w := range workers {
		subtasks = append(subtasks, schemas.SubTask{
			WorkerID:     w.WorkerID,
			SystemPrompt: fallbackSubtaskSystemPrompt,
		})
	}
	return subtasks
}

func tryParsePlan(text string, workers []schemas.WorkerInfo) []schemas.SubTask {
	start := strings.Index(text, "[")
	end := strings.LastIndex(text, "]") + 1
	if start == -1 || end <= start {
		return nil
	}

	var items []planItem
	if err := json.Unmarshal([]byte(text[start:end]), &items); err != nil {
		return nil
	}

	known := make(map[string]bool, len(workers))
	for _, w := range workers {
		known[w.WorkerID] = true
	}

	subtasks := make([]schemas.SubTask, 0, len(items))
	for _, item := range items {
		wid := deref(item.WorkerID, "")
		if !known[wid] {
			wid = workers[0].WorkerID
		}
		subtasks = append(subtasks, schemas.SubTask{
			WorkerID:     wid,
			SystemPrompt: deref(item.SystemPrompt, defaultSubtaskSystemPrompt),
			TaskPrompt:   deref(item.TaskPrompt, ""),
		})
	}
	return subtasks
}

func deref(s *string, def string) string {
	if s == nil {
		return def
	}
	return *s
}
