package controller_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/skinnyrad/agent-cluster/internal/controller"
	"github.com/skinnyrad/agent-cluster/internal/fakeollama"
	"github.com/skinnyrad/agent-cluster/internal/schemas"
	"github.com/skinnyrad/agent-cluster/internal/tools"
	"github.com/skinnyrad/agent-cluster/internal/worker"
)

type cluster struct {
	t       *testing.T
	ollama  *fakeollama.Server
	url     string
	token   string
	workers map[string]*httptest.Server
}

type workerSpec struct {
	id    string
	slots int
	tags  []string
	tools []string // tool sets
}

func w(id string) workerSpec { return workerSpec{id: id} }

// startCluster starts a controller and registers the given workers with it.
func startCluster(t *testing.T, f *fakeollama.Server, configure func(*controller.Config), specs ...workerSpec) *cluster {
	t.Helper()
	cfg := controller.Config{
		StorePath:        filepath.Join(t.TempDir(), "test.db"),
		RouterModel:      "router-model",
		SynthesizerModel: "synth-model",
		MaxRounds:        1,
		AgentClient:      f.Client,
	}
	if configure != nil {
		configure(&cfg)
	}
	ctl, err := controller.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(ctl.Handler())
	t.Cleanup(func() {
		srv.Close()
		ctl.Close()
	})

	c := &cluster{t: t, ollama: f, url: srv.URL, token: cfg.Token, workers: map[string]*httptest.Server{}}
	for _, s := range specs {
		c.addWorker(s)
	}
	return c
}

func (c *cluster) addWorker(s workerSpec) error {
	c.t.Helper()
	toolList, toolNames, err := tools.Build(s.tools, tools.Config{Workspace: c.t.TempDir()})
	if err != nil {
		c.t.Fatal(err)
	}
	var handler http.Handler
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { handler.ServeHTTP(w, r) }))
	c.t.Cleanup(srv.Close)
	wk := worker.New(worker.Config{
		WorkerID: s.id, ModelID: "model-" + s.id, Slots: s.slots, Tags: s.tags,
		Tools: toolList, ToolNames: toolNames,
		Token: c.token, Client: c.ollama.Client,
		ControllerURL: c.url, AdvertiseURL: srv.URL,
	})
	handler = wk.Handler()
	c.workers[s.id] = srv
	return wk.Register(context.Background())
}

func (c *cluster) request(method, path string, body any) *http.Response {
	c.t.Helper()
	var r *bytes.Reader
	if body != nil {
		data, _ := json.Marshal(body)
		r = bytes.NewReader(data)
	} else {
		r = bytes.NewReader(nil)
	}
	req, _ := http.NewRequest(method, c.url+path, r)
	req.Header.Set("Content-Type", "application/json")
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		c.t.Fatal(err)
	}
	return resp
}

func decode[T any](t *testing.T, resp *http.Response) T {
	t.Helper()
	defer resp.Body.Close()
	var out T
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	return out
}

func (c *cluster) dispatch(prompt, session string) (int, schemas.DispatchResponse) {
	c.t.Helper()
	resp := c.request(http.MethodPost, "/dispatch", schemas.JobRequest{Prompt: prompt, SessionID: session})
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return resp.StatusCode, schemas.DispatchResponse{}
	}
	return resp.StatusCode, decode[schemas.DispatchResponse](c.t, resp)
}

func (c *cluster) alive() map[string]bool {
	c.t.Helper()
	out := map[string]bool{}
	for _, w := range decode[schemas.WorkersResponse](c.t, c.request(http.MethodGet, "/workers", nil)).Workers {
		out[w.WorkerID] = w.Alive
	}
	return out
}

// events reads a job's event stream until it ends.
func (c *cluster) events(jobID string) []schemas.Event {
	c.t.Helper()
	resp := c.request(http.MethodGet, "/jobs/"+jobID+"/events", nil)
	defer resp.Body.Close()
	var out []schemas.Event
	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		if data, ok := strings.CutPrefix(scanner.Text(), "data: "); ok {
			var e schemas.Event
			if err := json.Unmarshal([]byte(data), &e); err != nil {
				c.t.Fatal(err)
			}
			out = append(out, e)
		}
	}
	return out
}

func plan(subtasks ...string) string {
	return `{"subtasks": [` + strings.Join(subtasks, ",") + `]}`
}

func st(id, prompt string, extra ...string) string {
	fields := []string{fmt.Sprintf(`"id": %q, "title": "do %s", "system_prompt": "You are %s.", "task_prompt": %q`, id, id, id, prompt)}
	fields = append(fields, extra...)
	return "{" + strings.Join(fields, ", ") + "}"
}

func resultsByID(resp schemas.DispatchResponse) map[string]schemas.AgentResult {
	out := map[string]schemas.AgentResult{}
	for _, r := range resp.Results {
		out[r.SubtaskID] = r
	}
	return out
}

func TestDependenciesPassResultsAlong(t *testing.T) {
	f := fakeollama.Start(t)
	f.RouterReply = "```json\n" + plan(
		st("t1", "Write bubble sort."),
		st("t2", "Review it.", `"depends_on": ["t1"]`),
	) + "\n```"
	c := startCluster(t, f, nil, w("w1"), w("w2"))

	status, resp := c.dispatch("Write and review bubble sort", "")
	if status != http.StatusOK || resp.Status != schemas.JobDone {
		t.Fatalf("status %d, job %s: %s", status, resp.Status, resp.Error)
	}
	if resp.Synthesis != "FINAL ANSWER" || resp.SessionID == "" {
		t.Errorf("synthesis %q, session %q", resp.Synthesis, resp.SessionID)
	}
	results := resultsByID(resp)
	if len(results) != 2 || !results["t1"].Success || !results["t2"].Success {
		t.Fatalf("results = %+v", resp.Results)
	}
	// t2 ran after t1 and saw its output.
	if !strings.Contains(results["t2"].Content, "Results from the earlier subtasks") ||
		!strings.Contains(results["t2"].Content, "did: Write bubble sort.") {
		t.Errorf("t2 didn't get t1's result:\n%s", results["t2"].Content)
	}
	// The worker's system message carries the planned role.
	for _, call := range f.Calls() {
		if !strings.HasPrefix(call.Messages[0].Content, "You are t") {
			t.Errorf("system prompt = %q", call.Messages[0].Content)
		}
	}
	if !strings.Contains(f.SynthPrompts[0], "### t2: do t2") {
		t.Errorf("synthesizer prompt missing results:\n%s", f.SynthPrompts[0])
	}
}

func TestDependentSubtasksFollowTheirDependencysWorker(t *testing.T) {
	f := fakeollama.Start(t)
	// t2 isn't pinned, but t1 ran on w2, which may have files t2 needs.
	f.RouterReply = plan(st("t1", "write a file", `"worker_id": "w2"`), st("t2", "use it", `"depends_on": ["t1"]`))
	c := startCluster(t, f, nil, w("w1"), w("w2"))

	_, resp := c.dispatch("x", "")
	results := resultsByID(resp)
	if results["t1"].WorkerID != "w2" || results["t2"].WorkerID != "w2" {
		t.Errorf("t1 on %s, t2 on %s; want both on w2", results["t1"].WorkerID, results["t2"].WorkerID)
	}
}

func TestIndependentSubtasksRunConcurrently(t *testing.T) {
	f := fakeollama.Start(t)
	f.RouterReply = plan(st("t1", "one"), st("t2", "two"), st("t3", "three"))
	// Every worker call blocks until all three are in flight.
	f.Barrier = 3
	c := startCluster(t, f, nil, w("w1"), w("w2"), w("w3"))

	_, resp := c.dispatch("three things", "")
	for _, r := range resp.Results {
		if strings.Contains(r.Content, "TIMED OUT") {
			t.Fatalf("subtasks ran one at a time: %+v", resp.Results)
		}
	}
}

func TestSlotsLimitConcurrency(t *testing.T) {
	f := fakeollama.Start(t)
	f.RouterReply = plan(st("t1", "one"), st("t2", "two"), st("t3", "three"))
	c := startCluster(t, f, nil, workerSpec{id: "solo", slots: 1})

	_, resp := c.dispatch("three things", "")
	if len(resp.Results) != 3 {
		t.Fatalf("results = %+v", resp.Results)
	}
	if f.Max() != 1 {
		t.Errorf("a one-slot worker ran %d tasks at once", f.Max())
	}
}

func TestRequirementsPickTheWorker(t *testing.T) {
	f := fakeollama.Start(t)
	f.RouterReply = plan(
		st("t1", "needs a gpu", `"requires": ["gpu"]`),
		st("t2", "needs files", `"requires": ["read_file"]`),
		st("t3", "needs a laser", `"requires": ["laser"]`), // nobody has one; requirement dropped
	)
	c := startCluster(t, f, nil,
		w("plain"),
		workerSpec{id: "gpu-box", tags: []string{"gpu"}},
		workerSpec{id: "files-box", tools: []string{"files"}},
	)
	_, resp := c.dispatch("go", "")
	results := resultsByID(resp)
	if results["t1"].WorkerID != "gpu-box" || results["t2"].WorkerID != "files-box" {
		t.Errorf("t1 on %s, t2 on %s", results["t1"].WorkerID, results["t2"].WorkerID)
	}
	if !results["t3"].Success {
		t.Errorf("t3 should run anywhere once its unmeetable requirement is dropped: %+v", results["t3"])
	}
}

func TestWorkerToolsRun(t *testing.T) {
	f := fakeollama.Start(t)
	f.RouterReply = plan(st("t1", "list the files", `"requires": ["list_files"]`))
	f.ToolModel, f.ToolName, f.ToolArgs = "model-w1", "list_files", map[string]any{"path": "."}
	c := startCluster(t, f, nil, workerSpec{id: "w1", tools: []string{"files"}})

	created := decode[schemas.JobCreated](t, c.request(http.MethodPost, "/jobs", schemas.JobRequest{Prompt: "list"}))
	events := c.events(created.JobID)

	var sawCall, sawResult, sawToken bool
	var final schemas.Event
	for _, e := range events {
		switch e.Type {
		case schemas.EventSubtaskToolCall:
			sawCall = e.Tool == "list_files" && e.Args["path"] == "."
		case schemas.EventSubtaskToolResult:
			sawResult = e.Output == "(empty)"
		case schemas.EventSubtaskToken:
			sawToken = true
		case schemas.EventJobFinished:
			final = e
		}
	}
	if !sawCall || !sawResult || !sawToken {
		t.Errorf("tool call %v, tool result %v, streamed tokens %v", sawCall, sawResult, sawToken)
	}
	if final.Status != schemas.JobDone {
		t.Fatalf("job ended %s: %s", final.Status, final.Error)
	}

	job := decode[schemas.Job](t, c.request(http.MethodGet, "/jobs/"+created.JobID, nil))
	if r := job.Results[0]; r.Content != "tool said: (empty)" || r.Metrics["tool_calls"] != float64(1) {
		t.Errorf("result = %+v", r)
	}
}

func TestEventsReplayAfterTheJobFinishes(t *testing.T) {
	f := fakeollama.Start(t)
	f.RouterReply = plan(st("t1", "x"))
	c := startCluster(t, f, nil, w("w1"))

	_, resp := c.dispatch("x", "")
	var types []string
	for _, e := range c.events(resp.RequestID) {
		types = append(types, e.Type)
	}
	want := []string{
		schemas.EventJobStarted, schemas.EventPlan, schemas.EventSubtaskStarted,
		schemas.EventSubtaskFinished, schemas.EventSynthesisStarted, schemas.EventJobFinished,
	}
	if strings.Join(types, ",") != strings.Join(want, ",") {
		t.Errorf("replayed events = %v\nwant %v (token events are not stored)", types, want)
	}
}

func TestReviewerAddsARound(t *testing.T) {
	f := fakeollama.Start(t)
	f.RouterReply = plan(st("t1", "write code"))
	f.ReviewReplies = []string{
		`{"complete": false, "reason": "no tests", "subtasks": [{"id": "t1", "title": "tests", "system_prompt": "S", "task_prompt": "write tests", "depends_on": ["t1"], "requires": []}]}`,
	}
	c := startCluster(t, f, func(cfg *controller.Config) { cfg.MaxRounds = 3 }, w("w1"))

	_, resp := c.dispatch("code with tests", "")
	if len(resp.Subtasks) != 2 || resp.Subtasks[1].ID != "r2-t1" || resp.Subtasks[1].Round != 2 {
		t.Fatalf("subtasks = %+v", resp.Subtasks)
	}
	r2 := resultsByID(resp)["r2-t1"]
	if !strings.Contains(r2.Content, "did: write code") {
		t.Errorf("follow-up didn't get round 1's result:\n%s", r2.Content)
	}
	// Round 2 was reviewed too (and found complete), so there's no round 3.
	if len(f.ReviewPrompts) != 2 {
		t.Errorf("reviews = %d, want 2", len(f.ReviewPrompts))
	}
}

func TestSessionHistoryReachesTheRouter(t *testing.T) {
	f := fakeollama.Start(t)
	f.RouterReply = plan(st("t1", "x"))
	f.SynthesisReply = "Paris"
	c := startCluster(t, f, func(cfg *controller.Config) { cfg.MaxRounds = 2 }, w("w1"))

	_, first := c.dispatch("What is the capital of France?", "")
	c.dispatch("And its population?", first.SessionID)
	c.dispatch("Unrelated", "other-session")

	// Every agent that reads the task needs the history to resolve "its".
	const exchange = "User: What is the capital of France?\nAssistant: Paris"
	for name, prompt := range map[string]string{
		"router": f.RouterPrompts[1], "reviewer": f.ReviewPrompts[1], "synthesizer": f.SynthPrompts[1],
	} {
		if !strings.Contains(prompt, exchange) {
			t.Errorf("second %s prompt lacks the first exchange:\n%s", name, prompt)
		}
	}
	if strings.Contains(f.RouterPrompts[2], "France") {
		t.Errorf("another session's history leaked:\n%s", f.RouterPrompts[2])
	}

	jobs := decode[schemas.JobsResponse](t, c.request(http.MethodGet, "/jobs?session_id="+first.SessionID, nil))
	if len(jobs.Jobs) != 2 || jobs.Jobs[0].Prompt != "And its population?" {
		t.Errorf("session jobs = %+v", jobs.Jobs)
	}
}

func TestUnusableRouterReplyFallsBack(t *testing.T) {
	f := fakeollama.Start(t)
	f.RouterReply = "Sorry, I can't produce JSON."
	c := startCluster(t, f, nil, w("w1"), w("w2"))

	_, resp := c.dispatch("Say hi", "")
	if len(resp.Subtasks) != 1 || resp.Subtasks[0].TaskPrompt != "Say hi" {
		t.Fatalf("fallback plan = %+v", resp.Subtasks)
	}
	if !resp.Results[0].Success {
		t.Errorf("fallback subtask failed: %+v", resp.Results[0])
	}
}

func TestUnreachableWorkerIsRetriedElsewhere(t *testing.T) {
	f := fakeollama.Start(t)
	f.RouterReply = plan(st("t1", "x", `"worker_id": "down"`))
	c := startCluster(t, f, nil, w("down"), w("up"))
	c.workers["down"].Close()

	_, resp := c.dispatch("x", "")
	if r := resp.Results[0]; !r.Success || r.WorkerID != "up" {
		t.Fatalf("result = %+v, want success on the other worker", r)
	}
	if alive := c.alive(); alive["down"] || !alive["up"] {
		t.Errorf("alive = %v", alive)
	}
}

func TestSlowWorkersTimeOutButStayAlive(t *testing.T) {
	f := fakeollama.Start(t)
	f.RouterReply = plan(st("t1", "fast", `"worker_id": "fast"`), st("t2", "slow", `"worker_id": "slow"`))
	f.SlowModel, f.SlowDelay = "model-slow", 5*time.Second
	c := startCluster(t, f, func(cfg *controller.Config) { cfg.WorkerTimeout = 300 * time.Millisecond },
		w("fast"), w("slow"))

	_, resp := c.dispatch("one fast, one slow", "")
	results := resultsByID(resp)
	if !results["t1"].Success {
		t.Errorf("fast worker failed: %+v", results["t1"])
	}
	if r := results["t2"]; r.Success || r.Error == nil || !strings.Contains(*r.Error, "did not finish within 300ms") {
		t.Fatalf("slow result = %+v, want a timeout error", r)
	}
	if alive := c.alive(); !alive["fast"] || !alive["slow"] {
		t.Errorf("alive = %v, want both still alive", alive)
	}
}

func TestCancelJob(t *testing.T) {
	f := fakeollama.Start(t)
	f.RouterReply = plan(st("t1", "slow"))
	f.SlowModel, f.SlowDelay = "model-w1", time.Minute
	c := startCluster(t, f, nil, w("w1"))

	created := decode[schemas.JobCreated](t, c.request(http.MethodPost, "/jobs", schemas.JobRequest{Prompt: "x"}))
	deadline := time.Now().Add(5 * time.Second)
	for len(f.Calls()) == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	job := decode[schemas.Job](t, c.request(http.MethodDelete, "/jobs/"+created.JobID, nil))
	if job.Status != schemas.JobCancelled {
		t.Fatalf("status = %s", job.Status)
	}
	if alive := c.alive(); !alive["w1"] {
		t.Error("cancelling a job marked its worker dead")
	}
}

func TestNoLiveWorkers(t *testing.T) {
	c := startCluster(t, fakeollama.Start(t), nil)
	if status, _ := c.dispatch("anything", ""); status != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", status)
	}
}

func TestBadBody(t *testing.T) {
	c := startCluster(t, fakeollama.Start(t), nil, w("w1"))
	resp := c.request(http.MethodPost, "/jobs", map[string]int{"nope": 1})
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422", resp.StatusCode)
	}
}

func TestTokenIsRequired(t *testing.T) {
	f := fakeollama.Start(t)
	f.RouterReply = plan(st("t1", "x"))
	c := startCluster(t, f, func(cfg *controller.Config) { cfg.Token = "s3cret" }, w("w1"))

	resp, err := http.Get(c.url + "/workers")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("no token: status %d, want 401", resp.StatusCode)
	}
	// The worker registered and runs tasks with the token.
	if _, out := c.dispatch("x", ""); out.Status != schemas.JobDone {
		t.Fatalf("job with token: %s %s", out.Status, out.Error)
	}
}

func TestAllowlist(t *testing.T) {
	path := filepath.Join(t.TempDir(), "workers.yaml")
	os.WriteFile(path, []byte("workers:\n  - worker_id: ok\n    tags: [blessed]\n  - worker_id: off\n    enabled: false\n"), 0o644)
	c := startCluster(t, fakeollama.Start(t), func(cfg *controller.Config) { cfg.AllowlistPath = path })

	if err := c.addWorker(w("ok")); err != nil {
		t.Fatalf("allowed worker refused: %v", err)
	}
	for _, id := range []string{"off", "stranger"} {
		if err := c.addWorker(w(id)); err == nil || !strings.Contains(err.Error(), "403") {
			t.Errorf("%s: err = %v, want 403", id, err)
		}
	}
	workers := decode[schemas.WorkersResponse](t, c.request(http.MethodGet, "/workers", nil)).Workers
	if len(workers) != 1 || workers[0].Tags[0] != "blessed" {
		t.Errorf("workers = %+v", workers)
	}
}

func TestDeregister(t *testing.T) {
	c := startCluster(t, fakeollama.Start(t), nil, w("w1"))
	resp := c.request(http.MethodDelete, "/workers/w1", nil)
	resp.Body.Close()
	if alive := c.alive(); len(alive) != 0 {
		t.Errorf("workers after deregistering = %v", alive)
	}
}

// Many jobs at once share the workers without losing results.
func TestConcurrentJobs(t *testing.T) {
	f := fakeollama.Start(t)
	f.RouterReply = plan(st("t1", "a"), st("t2", "b"))
	c := startCluster(t, f, nil, workerSpec{id: "w1", slots: 2}, workerSpec{id: "w2", slots: 2})

	var wg sync.WaitGroup
	for i := range 8 {
		wg.Go(func() {
			_, resp := c.dispatch(fmt.Sprint("job ", i), "")
			if resp.Status != schemas.JobDone || len(resp.Results) != 2 {
				t.Errorf("job %d: %s, %d results", i, resp.Status, len(resp.Results))
			}
		})
	}
	wg.Wait()
	if f.Max() > 4 {
		t.Errorf("%d tasks ran at once on 4 slots", f.Max())
	}
}
