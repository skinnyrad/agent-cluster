package controller

import (
	"testing"

	"github.com/skinnyrad/agent-cluster/internal/schemas"
)

var testWorkers = []schemas.WorkerInfo{
	{WorkerID: "worker-1", URL: "http://w1"},
	{WorkerID: "worker-2", URL: "http://w2"},
}

func TestParseRouterPlan(t *testing.T) {
	tests := []struct {
		name string
		text string
		want []schemas.SubTask
	}{
		{
			name: "clean array",
			text: `[{"worker_id":"worker-2","system_prompt":"You review code.","task_prompt":"Review it."}]`,
			want: []schemas.SubTask{{WorkerID: "worker-2", SystemPrompt: "You review code.", TaskPrompt: "Review it."}},
		},
		{
			name: "array wrapped in prose and fences",
			text: "Here is the plan:\n```json\n[{\"worker_id\":\"worker-1\",\"system_prompt\":\"S\",\"task_prompt\":\"T\"}]\n```",
			want: []schemas.SubTask{{WorkerID: "worker-1", SystemPrompt: "S", TaskPrompt: "T"}},
		},
		{
			name: "unknown worker goes to the first worker, missing fields get defaults",
			text: `[{"worker_id":"nope"}]`,
			want: []schemas.SubTask{{WorkerID: "worker-1", SystemPrompt: defaultSubtaskSystemPrompt, TaskPrompt: ""}},
		},
		{
			name: "explicit empty system prompt is kept",
			text: `[{"worker_id":"worker-1","system_prompt":"","task_prompt":"T"}]`,
			want: []schemas.SubTask{{WorkerID: "worker-1", SystemPrompt: "", TaskPrompt: "T"}},
		},
		{
			name: "invalid JSON falls back to broadcast",
			text: `[{"worker_id": oops}]`,
			want: broadcast(),
		},
		{
			name: "no array falls back to broadcast",
			text: "I can't do that.",
			want: broadcast(),
		},
		{
			name: "empty array falls back to broadcast",
			text: "[]",
			want: broadcast(),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseRouterPlan(tt.text, testWorkers)
			if len(got) != len(tt.want) {
				t.Fatalf("got %d subtasks %+v, want %d %+v", len(got), got, len(tt.want), tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("subtask %d = %+v, want %+v", i, got[i], tt.want[i])
				}
			}
		})
	}
}

func broadcast() []schemas.SubTask {
	return []schemas.SubTask{
		{WorkerID: "worker-1", SystemPrompt: fallbackSubtaskSystemPrompt},
		{WorkerID: "worker-2", SystemPrompt: fallbackSubtaskSystemPrompt},
	}
}
