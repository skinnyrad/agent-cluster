package controller

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/skinnyrad/agent-cluster/internal/schemas"
)

func TestParsePlan(t *testing.T) {
	tests := []struct {
		name string
		text string
		ids  []string
	}{
		{"schema object", `{"subtasks":[{"id":"a","title":"A","system_prompt":"S","task_prompt":"T","depends_on":[],"requires":[]}]}`, []string{"a"}},
		{"object in prose and fences", "Plan:\n```json\n{\"subtasks\":[{\"id\":\"a\"},{\"id\":\"b\"}]}\n```", []string{"a", "b"}},
		{"bare array", `[{"id":"x","task_prompt":"T"}]`, []string{"x"}},
		{"invalid JSON", `{"subtasks": [oops]}`, nil},
		{"no JSON", "I can't do that.", nil},
		{"empty plan", `{"subtasks": []}`, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parsePlan(tt.text)
			var ids []string
			for _, st := range got {
				ids = append(ids, st.ID)
			}
			if !reflect.DeepEqual(ids, tt.ids) {
				t.Errorf("ids = %v, want %v", ids, tt.ids)
			}
		})
	}

	st := parsePlan(`[{"id":"a"}]`)[0]
	if st.SystemPrompt != defaultSubtaskSystemPrompt {
		t.Errorf("missing system_prompt should default, got %q", st.SystemPrompt)
	}
}

func TestParseReview(t *testing.T) {
	r := parseReview(`{"complete": false, "reason": "no tests", "subtasks": [{"id": "t9", "task_prompt": "write tests"}]}`)
	if r.Complete || r.Reason != "no tests" || len(r.Subtasks) != 1 || r.Subtasks[0].TaskPrompt != "write tests" {
		t.Errorf("review = %+v", r)
	}
	if r := parseReview("garbage"); !r.Complete {
		t.Error("an unreadable review must count as complete")
	}
}

func always([]string) bool { return true }

func TestNormalizePlan(t *testing.T) {
	plan := []schemas.SubTask{
		{ID: "a", TaskPrompt: "first"},
		{ID: "a", TaskPrompt: "duplicate id", DependsOn: []string{"a", "missing"}},
		{ID: "", DependsOn: []string{"a"}, Requires: []string{"gpu", "gpu", "laser"}},
	}
	canSatisfy := func(r []string) bool { return r[0] == "gpu" }
	got := normalizePlan(plan, 1, "the prompt", nil, canSatisfy)

	if got[0].ID != "a" || got[1].ID != "t2" || got[2].ID != "t3" {
		t.Fatalf("ids = %s, %s, %s", got[0].ID, got[1].ID, got[2].ID)
	}
	if !reflect.DeepEqual(got[1].DependsOn, []string{"a"}) {
		t.Errorf("unknown dependency should be dropped: %v", got[1].DependsOn)
	}
	if got[2].TaskPrompt != "the prompt" || got[2].Title != "t3" {
		t.Errorf("empty task prompt/title should default: %+v", got[2])
	}
	if !reflect.DeepEqual(got[2].Requires, []string{"gpu"}) {
		t.Errorf("requires = %v, want only satisfiable ones, deduplicated", got[2].Requires)
	}
	for _, st := range got {
		if st.Round != 1 {
			t.Errorf("%s round = %d", st.ID, st.Round)
		}
	}
}

func TestNormalizePlanLaterRound(t *testing.T) {
	done := map[string]bool{"t1": true}
	got := normalizePlan([]schemas.SubTask{{ID: "t1", DependsOn: []string{"t1"}}}, 2, "p", done, always)
	if got[0].ID != "r2-t1" {
		t.Errorf("id clashing with an earlier round = %q, want r2-t1", got[0].ID)
	}
	if !reflect.DeepEqual(got[0].DependsOn, []string{"t1"}) {
		t.Errorf("a follow-up may depend on an earlier round's subtask: %v", got[0].DependsOn)
	}
}

func TestNormalizePlanBreaksCycles(t *testing.T) {
	got := normalizePlan([]schemas.SubTask{
		{ID: "a", DependsOn: []string{"b"}},
		{ID: "b", DependsOn: []string{"a"}},
	}, 1, "p", nil, always)
	for _, st := range got {
		if len(st.DependsOn) != 0 {
			t.Errorf("%s still depends on %v", st.ID, st.DependsOn)
		}
	}
}

func reg(id string, slots int, tags ...string) schemas.WorkerRegistration {
	return schemas.WorkerRegistration{WorkerID: id, URL: "http://" + id, ModelID: "m", Slots: slots, Tags: tags}
}

func TestPoolAcquire(t *testing.T) {
	p := newPool(time.Minute)
	p.register(reg("a", 1))
	p.register(reg("b", 2, "gpu"))
	ctx := context.Background()

	// A pinned subtask gets its worker...
	if w, _ := p.acquire(ctx, schemas.SubTask{WorkerID: "a"}, "", nil); w.WorkerID != "a" {
		t.Fatalf("pin ignored, got %s", w.WorkerID)
	}
	// ...and waits for it when it's full, rather than moving to b.
	got := make(chan string)
	go func() {
		w, _ := p.acquire(ctx, schemas.SubTask{WorkerID: "a"}, "", nil)
		got <- w.WorkerID
	}()
	select {
	case id := <-got:
		t.Fatalf("pinned acquire returned %s while a was full", id)
	case <-time.After(100 * time.Millisecond):
	}
	p.release("a")
	if id := <-got; id != "a" {
		t.Fatalf("got %s after releasing a", id)
	}

	// A soft preference wins when that worker is free; requirements filter workers.
	if w, _ := p.acquire(ctx, schemas.SubTask{}, "b", nil); w.WorkerID != "b" {
		t.Fatalf("preference ignored, got %s", w.WorkerID)
	}
	if _, err := p.acquire(ctx, schemas.SubTask{Requires: []string{"laser"}}, "", nil); err == nil || !strings.Contains(err.Error(), "laser") {
		t.Fatalf("want a no-worker error naming the requirement, got %v", err)
	}

	// a is full and b has one slot left: take it, then the next acquire waits for a release.
	if w, _ := p.acquire(ctx, schemas.SubTask{Requires: []string{"gpu"}}, "", nil); w.WorkerID != "b" {
		t.Fatalf("got %s, want b", w.WorkerID)
	}
	go func() {
		w, _ := p.acquire(ctx, schemas.SubTask{}, "", nil)
		got <- w.WorkerID
	}()
	select {
	case id := <-got:
		t.Fatalf("acquire returned %s while every slot was taken", id)
	case <-time.After(100 * time.Millisecond):
	}
	p.release("b")
	if id := <-got; id != "b" {
		t.Fatalf("got %s after releasing b", id)
	}

	// A dead worker is skipped until it heartbeats again.
	p.markDead("b")
	if p.canSatisfy([]string{"gpu"}) {
		t.Error("dead worker still counts")
	}
	p.register(reg("b", 2, "gpu"))
	if !p.canSatisfy([]string{"gpu"}) {
		t.Error("heartbeat didn't revive the worker")
	}
}
