package controller

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/skinnyrad/agent-cluster/internal/schemas"
)

const (
	defaultSubtaskSystemPrompt  = "You are a helpful assistant."
	fallbackSubtaskSystemPrompt = "You are a helpful assistant. Complete the assigned task concisely."
)

// subtaskSchema is the JSON schema for one planned subtask.
var subtaskSchema = map[string]any{
	"type": "object",
	"properties": map[string]any{
		"id":            map[string]any{"type": "string"},
		"title":         map[string]any{"type": "string"},
		"system_prompt": map[string]any{"type": "string"},
		"task_prompt":   map[string]any{"type": "string"},
		"depends_on":    map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
		"requires":      map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
		"worker_id":     map[string]any{"type": "string"},
	},
	"required": []string{"id", "title", "system_prompt", "task_prompt", "depends_on", "requires"},
}

// planSchema constrains the router's reply.
var planSchema = mustJSON(map[string]any{
	"type": "object",
	"properties": map[string]any{
		"subtasks": map[string]any{"type": "array", "minItems": 1, "items": subtaskSchema},
	},
	"required": []string{"subtasks"},
})

// reviewSchema constrains the reviewer's reply.
var reviewSchema = mustJSON(map[string]any{
	"type": "object",
	"properties": map[string]any{
		"complete": map[string]any{"type": "boolean"},
		"reason":   map[string]any{"type": "string"},
		"subtasks": map[string]any{"type": "array", "items": subtaskSchema},
	},
	"required": []string{"complete", "reason", "subtasks"},
})

func mustJSON(v any) json.RawMessage {
	data, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return data
}

// plannedSubtask uses pointers so a missing key can be told apart from an empty value.
type plannedSubtask struct {
	ID           *string  `json:"id"`
	Title        *string  `json:"title"`
	SystemPrompt *string  `json:"system_prompt"`
	TaskPrompt   *string  `json:"task_prompt"`
	DependsOn    []string `json:"depends_on"`
	Requires     []string `json:"requires"`
	WorkerID     *string  `json:"worker_id"`
}

func (p plannedSubtask) toSubTask() schemas.SubTask {
	return schemas.SubTask{
		ID:           strings.TrimSpace(deref(p.ID, "")),
		Title:        deref(p.Title, ""),
		SystemPrompt: deref(p.SystemPrompt, defaultSubtaskSystemPrompt),
		TaskPrompt:   deref(p.TaskPrompt, ""),
		DependsOn:    p.DependsOn,
		Requires:     p.Requires,
		WorkerID:     deref(p.WorkerID, ""),
	}
}

// parsePlan reads the router's reply. It accepts the schema's {"subtasks": [...]} object
// or a bare array, and tolerates prose or code fences around either, for models that
// ignore the format constraint. It returns nil if nothing usable comes back.
func parsePlan(text string) []schemas.SubTask {
	var obj struct {
		Subtasks []plannedSubtask `json:"subtasks"`
	}
	if span, ok := between(text, "{", "}"); ok && json.Unmarshal([]byte(span), &obj) == nil && len(obj.Subtasks) > 0 {
		return convert(obj.Subtasks)
	}
	var arr []plannedSubtask
	if span, ok := between(text, "[", "]"); ok && json.Unmarshal([]byte(span), &arr) == nil && len(arr) > 0 {
		return convert(arr)
	}
	return nil
}

func convert(items []plannedSubtask) []schemas.SubTask {
	out := make([]schemas.SubTask, 0, len(items))
	for _, it := range items {
		out = append(out, it.toSubTask())
	}
	return out
}

// review is the reviewer's verdict on a round's results.
type review struct {
	Complete bool
	Reason   string
	Subtasks []schemas.SubTask
}

// parseReview reads the reviewer's reply. Anything unreadable counts as complete, so a
// confused reviewer ends the job rather than looping.
func parseReview(text string) review {
	var r struct {
		Complete *bool            `json:"complete"`
		Reason   string           `json:"reason"`
		Subtasks []plannedSubtask `json:"subtasks"`
	}
	span, ok := between(text, "{", "}")
	if !ok || json.Unmarshal([]byte(span), &r) != nil || r.Complete == nil {
		return review{Complete: true, Reason: "reviewer reply was unreadable"}
	}
	return review{Complete: *r.Complete, Reason: r.Reason, Subtasks: convert(r.Subtasks)}
}

// between returns the text from the first open to the last close, inclusive.
func between(text, open, close string) (string, bool) {
	start := strings.Index(text, open)
	end := strings.LastIndex(text, close) + 1
	if start == -1 || end <= start {
		return "", false
	}
	return text[start:end], true
}

// fallbackPlan sends the whole prompt to one worker.
func fallbackPlan(prompt string) []schemas.SubTask {
	return []schemas.SubTask{{
		ID:           "t1",
		Title:        "Answer the request",
		SystemPrompt: fallbackSubtaskSystemPrompt,
		TaskPrompt:   prompt,
	}}
}

// normalizePlan makes a round's subtasks safe to run: every subtask gets a unique ID
// (not clashing with done, the IDs of earlier rounds), a task prompt and non-nil lists;
// dependencies on unknown IDs are dropped, as are requirements no live worker can meet;
// and if the dependencies contain a cycle, they are all dropped so the subtasks run
// independently.
func normalizePlan(subtasks []schemas.SubTask, round int, prompt string, done map[string]bool, canSatisfy func([]string) bool) []schemas.SubTask {
	used := map[string]bool{}
	for id := range done {
		used[id] = true
	}
	prefix := "t"
	if round > 1 {
		prefix = fmt.Sprintf("r%d-t", round)
	}
	renamed := map[string]string{} // planner's ID -> final ID, for this round
	for i := range subtasks {
		st := &subtasks[i]
		st.Round = round
		orig := st.ID
		if st.ID == "" || used[st.ID] {
			for n := i + 1; ; n++ {
				if id := fmt.Sprintf("%s%d", prefix, n); !used[id] {
					st.ID = id
					break
				}
			}
		}
		used[st.ID] = true
		if orig != "" {
			if _, seen := renamed[orig]; !seen {
				renamed[orig] = st.ID
			}
		}
		if st.TaskPrompt == "" {
			st.TaskPrompt = prompt
		}
		if st.Title == "" {
			st.Title = st.ID
		}
	}

	for i := range subtasks {
		st := &subtasks[i]
		deps := []string{}
		for _, d := range st.DependsOn {
			if id, ok := renamed[d]; ok && id != st.ID {
				deps = append(deps, id)
			} else if done[d] {
				deps = append(deps, d)
			}
		}
		st.DependsOn = dedupe(deps)

		reqs := []string{}
		for _, r := range dedupe(st.Requires) {
			if canSatisfy([]string{r}) {
				reqs = append(reqs, r)
			}
		}
		st.Requires = reqs
	}

	if hasCycle(subtasks) {
		for i := range subtasks {
			subtasks[i].DependsOn = []string{}
		}
	}
	return subtasks
}

func dedupe(in []string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, s := range in {
		if s = strings.TrimSpace(s); s != "" && !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

// hasCycle reports whether the subtasks' dependencies within this round form a cycle.
func hasCycle(subtasks []schemas.SubTask) bool {
	deps := map[string][]string{}
	for _, st := range subtasks {
		deps[st.ID] = st.DependsOn
	}
	const (
		visiting = 1
		visited  = 2
	)
	state := map[string]int{}
	var visit func(id string) bool
	visit = func(id string) bool {
		switch state[id] {
		case visiting:
			return true
		case visited:
			return false
		}
		state[id] = visiting
		for _, d := range deps[id] {
			if _, inRound := deps[d]; inRound && visit(d) {
				return true
			}
		}
		state[id] = visited
		return false
	}
	for _, st := range subtasks {
		if visit(st.ID) {
			return true
		}
	}
	return false
}

func deref(s *string, def string) string {
	if s == nil {
		return def
	}
	return *s
}
