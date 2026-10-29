package controller

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/skinnyrad/agent-cluster/internal/agent"
	"github.com/skinnyrad/agent-cluster/internal/httputil"
	"github.com/skinnyrad/agent-cluster/internal/schemas"
)

const (
	// maxResultInPrompt caps how much of one result is passed to later prompts.
	maxResultInPrompt = 12000
	// maxHistoryInPrompt caps each earlier turn shown to the router.
	maxHistoryInPrompt = 2000
	subscriberBuffer   = 1024
)

// jobRun is a job in progress: its state, its event log and its live subscribers.
type jobRun struct {
	ctl    *Controller
	cancel context.CancelFunc
	done   chan struct{}

	mu     sync.Mutex
	job    schemas.Job
	seq    int
	events []schemas.Event // persistent events, for replay
	subs   map[chan schemas.Event]bool
}

func (jr *jobRun) snapshot() schemas.Job {
	jr.mu.Lock()
	defer jr.mu.Unlock()
	j := jr.job
	j.Subtasks = append([]schemas.SubTask(nil), j.Subtasks...)
	j.Results = append([]schemas.AgentResult(nil), j.Results...)
	return j
}

// emit records an event and sends it to subscribers. A subscriber too slow to keep up
// is disconnected rather than allowed to block the job.
func (jr *jobRun) emit(e schemas.Event) {
	jr.mu.Lock()
	defer jr.mu.Unlock()
	jr.seq++
	e.Seq = jr.seq
	e.JobID = jr.job.ID
	e.Time = time.Now().UTC()
	if e.Persistent() {
		jr.events = append(jr.events, e)
		if err := jr.ctl.store.addEvent(&e); err != nil {
			log.Printf("job %s: storing event: %v", jr.job.ID, err)
		}
	}
	for ch := range jr.subs {
		select {
		case ch <- e:
		default:
			delete(jr.subs, ch)
			close(ch)
		}
	}
}

// subscribe returns the events so far and a channel of the ones to come. The channel is
// nil if the job has already finished.
func (jr *jobRun) subscribe() ([]schemas.Event, chan schemas.Event) {
	jr.mu.Lock()
	defer jr.mu.Unlock()
	past := append([]schemas.Event(nil), jr.events...)
	if jr.job.Status.Finished() {
		return past, nil
	}
	ch := make(chan schemas.Event, subscriberBuffer)
	jr.subs[ch] = true
	return past, ch
}

func (jr *jobRun) unsubscribe(ch chan schemas.Event) {
	jr.mu.Lock()
	defer jr.mu.Unlock()
	if jr.subs[ch] {
		delete(jr.subs, ch)
		close(ch)
	}
}

func (jr *jobRun) update(f func(j *schemas.Job)) {
	jr.mu.Lock()
	f(&jr.job)
	j := jr.job
	jr.mu.Unlock()
	if err := jr.ctl.store.saveJob(&j); err != nil {
		log.Printf("job %s: saving: %v", j.ID, err)
	}
}

// finish records the final state, emits job.finished and closes every subscriber.
func (jr *jobRun) finish(status schemas.JobStatus, synthesis, errMsg string) {
	now := time.Now().UTC()
	jr.update(func(j *schemas.Job) {
		j.Status = status
		j.Synthesis = synthesis
		j.Error = errMsg
		j.FinishedAt = &now
	})
	jr.emit(schemas.Event{Type: schemas.EventJobFinished, Status: status, Synthesis: synthesis, Error: errMsg})
	jr.mu.Lock()
	for ch := range jr.subs {
		close(ch)
	}
	jr.subs = map[chan schemas.Event]bool{}
	jr.mu.Unlock()
}

// startJob creates a job and runs it in the background.
func (c *Controller) startJob(req schemas.JobRequest) (*jobRun, error) {
	if req.SessionID == "" {
		req.SessionID = uuid.NewString()
	}
	ctx, cancel := context.WithCancel(c.ctx)
	jr := &jobRun{
		ctl:    c,
		cancel: cancel,
		done:   make(chan struct{}),
		subs:   map[chan schemas.Event]bool{},
		job: schemas.Job{
			ID:        uuid.NewString(),
			SessionID: req.SessionID,
			Prompt:    req.Prompt,
			Status:    schemas.JobRunning,
			Subtasks:  []schemas.SubTask{},
			Results:   []schemas.AgentResult{},
			CreatedAt: time.Now().UTC(),
		},
	}
	if err := c.store.saveJob(&jr.job); err != nil {
		cancel()
		return nil, err
	}

	c.mu.Lock()
	c.jobs[jr.job.ID] = jr
	c.mu.Unlock()

	go func() {
		defer close(jr.done)
		defer cancel()
		c.runJob(ctx, jr, req)
		// Finished jobs are served from the store from now on.
		c.mu.Lock()
		delete(c.jobs, jr.job.ID)
		c.mu.Unlock()
	}()
	return jr, nil
}

func (c *Controller) runJob(ctx context.Context, jr *jobRun, req schemas.JobRequest) {
	jr.emit(schemas.Event{Type: schemas.EventJobStarted})
	fail := func(err error) {
		if ctx.Err() != nil {
			jr.finish(schemas.JobCancelled, "", "cancelled")
			return
		}
		jr.finish(schemas.JobFailed, "", err.Error())
	}

	history, err := c.store.history(req.SessionID, c.cfg.HistoryTurns)
	if err != nil {
		log.Printf("job %s: loading history: %v", jr.job.ID, err)
	}
	workers := c.pool.live()
	if len(workers) == 0 {
		fail(errors.New("No live workers available"))
		return
	}

	subtasks, err := c.plan(ctx, req.Prompt, history, workers)
	if err != nil {
		fail(fmt.Errorf("router agent: %w", err))
		return
	}

	results := map[string]schemas.AgentResult{}
	var all []schemas.SubTask
	for round := 1; ; round++ {
		done := map[string]bool{}
		for id := range results {
			done[id] = true
		}
		subtasks = normalizePlan(subtasks, round, req.Prompt, done, c.pool.canSatisfy)
		all = append(all, subtasks...)
		jr.update(func(j *schemas.Job) { j.Subtasks = append([]schemas.SubTask(nil), all...) })
		jr.emit(schemas.Event{Type: schemas.EventPlan, Round: round, Subtasks: subtasks})

		c.runRound(ctx, jr, subtasks, all, results, req.Context)
		if ctx.Err() != nil {
			fail(ctx.Err())
			return
		}
		if round >= c.cfg.MaxRounds {
			break
		}

		rv, err := c.review(ctx, req.Prompt, history, all, results)
		if err != nil {
			if ctx.Err() != nil {
				fail(ctx.Err())
				return
			}
			log.Printf("job %s: review failed, finishing: %v", jr.job.ID, err)
			break
		}
		jr.emit(schemas.Event{Type: schemas.EventReview, Round: round, Text: rv.Reason, Subtasks: rv.Subtasks})
		if rv.Complete || len(rv.Subtasks) == 0 {
			break
		}
		subtasks = rv.Subtasks
	}

	jr.emit(schemas.Event{Type: schemas.EventSynthesisStarted})
	synthesis, err := c.synthesize(ctx, req.Prompt, history, all, results, func(tok string) {
		jr.emit(schemas.Event{Type: schemas.EventSynthesisToken, Text: tok})
	})
	if err != nil {
		fail(fmt.Errorf("synthesizer agent: %w", err))
		return
	}
	jr.finish(schemas.JobDone, synthesis, "")
}

// runRound runs a round's subtasks, each as soon as its dependencies have finished.
func (c *Controller) runRound(ctx context.Context, jr *jobRun, subtasks, all []schemas.SubTask, results map[string]schemas.AgentResult, taskCtx map[string]any) {
	byID := map[string]schemas.SubTask{}
	for _, st := range all {
		byID[st.ID] = st
	}
	pending := append([]schemas.SubTask(nil), subtasks...)
	finished := make(chan schemas.AgentResult)
	running := 0

	for len(pending) > 0 || running > 0 {
		var waiting []schemas.SubTask
		for _, st := range pending {
			ready := true
			for _, d := range st.DependsOn {
				if _, ok := results[d]; !ok {
					ready = false
				}
			}
			if !ready {
				waiting = append(waiting, st)
				continue
			}
			deps := make([]schemas.AgentResult, 0, len(st.DependsOn))
			for _, d := range st.DependsOn {
				deps = append(deps, results[d])
			}
			running++
			go func(st schemas.SubTask) {
				finished <- c.runSubtask(ctx, jr, st, sameWorker(deps), depContext(byID, deps), taskCtx)
			}(st)
		}
		pending = waiting
		if running == 0 {
			// Only possible if dependencies can never be met; normalizePlan prevents it.
			for _, st := range pending {
				r := failedResult("", "", "dependencies could not be met")
				r.SubtaskID = st.ID
				results[st.ID] = r
			}
			return
		}
		r := <-finished
		running--
		results[r.SubtaskID] = r
		jr.update(func(j *schemas.Job) { j.Results = append(j.Results, r) })
	}
}

// sameWorker returns the worker that ran every one of deps, or "" if they ran on
// different workers. A subtask runs best there, next to any files its dependencies wrote.
func sameWorker(deps []schemas.AgentResult) string {
	worker := ""
	for i, r := range deps {
		if i > 0 && r.WorkerID != worker {
			return ""
		}
		worker = r.WorkerID
	}
	return worker
}

// depContext formats the results a subtask depends on.
func depContext(byID map[string]schemas.SubTask, deps []schemas.AgentResult) string {
	if len(deps) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n\nResults from the earlier subtasks this one builds on:\n")
	for _, r := range deps {
		fmt.Fprintf(&b, "\n### %s: %s\n%s\n", r.SubtaskID, byID[r.SubtaskID].Title, resultText(r))
	}
	return b.String()
}

func resultText(r schemas.AgentResult) string {
	if !r.Success {
		return "ERROR: " + deref(r.Error, "")
	}
	return truncate(r.Content, maxResultInPrompt)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "\n[truncated]"
}

// runSubtask runs one subtask on a worker, preferring prefer, and retries once on
// another worker if the first can't be reached.
func (c *Controller) runSubtask(ctx context.Context, jr *jobRun, st schemas.SubTask, prefer, deps string, taskCtx map[string]any) schemas.AgentResult {
	task := schemas.AgentTask{
		TaskID:       uuid.NewString(),
		SystemPrompt: st.SystemPrompt,
		Prompt:       st.TaskPrompt + deps,
		Context:      taskCtx,
	}
	tried := map[string]bool{}
	var result schemas.AgentResult
	for attempt := 1; attempt <= 2; attempt++ {
		reg, err := c.pool.acquire(ctx, st, prefer, tried)
		if err != nil {
			if attempt > 1 && errors.Is(err, errNoWorker) {
				break // keep the first attempt's error
			}
			result = failedResult(task.TaskID, "", err.Error())
			break
		}
		tried[reg.WorkerID] = true
		jr.emit(schemas.Event{Type: schemas.EventSubtaskStarted, SubtaskID: st.ID, WorkerID: reg.WorkerID})

		res, err := c.runOnWorker(ctx, reg, task, func(e schemas.WorkerEvent) {
			ev := schemas.Event{SubtaskID: st.ID, WorkerID: reg.WorkerID, Text: e.Text, Tool: e.Tool, Args: e.Args, Output: e.Output}
			switch e.Type {
			case schemas.WorkerEventToken:
				ev.Type = schemas.EventSubtaskToken
			case schemas.WorkerEventToolCall:
				ev.Type = schemas.EventSubtaskToolCall
			case schemas.WorkerEventToolResult:
				ev.Type = schemas.EventSubtaskToolResult
			default:
				return
			}
			jr.emit(ev)
		})
		c.pool.release(reg.WorkerID)
		if err == nil {
			result = *res
			break
		}
		result = failedResult(task.TaskID, reg.WorkerID, err.Error())
		// A worker that timed out is slow, not down, and a cancelled job says nothing
		// about the worker. Anything else (refused connection, bad status) marks it
		// dead until its next heartbeat, and the subtask gets one more try elsewhere.
		if isTimeout(err) || ctx.Err() != nil {
			break
		}
		c.pool.markDead(reg.WorkerID)
	}
	result.SubtaskID = st.ID
	jr.emit(schemas.Event{Type: schemas.EventSubtaskFinished, SubtaskID: st.ID, WorkerID: result.WorkerID, Result: &result})
	return result
}

// runOnWorker posts task to the worker's /run endpoint and reads its event stream.
func (c *Controller) runOnWorker(ctx context.Context, reg schemas.WorkerRegistration, task schemas.AgentTask, onEvent func(schemas.WorkerEvent)) (*schemas.AgentResult, error) {
	body, err := json.Marshal(task)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, reg.URL+"/run", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	httputil.SetToken(req, c.cfg.Token)

	resp, err := c.dispatchClient.Do(req)
	if err != nil {
		return nil, c.wrapWorkerErr(reg, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, fmt.Errorf("worker %s returned HTTP %d", reg.WorkerID, resp.StatusCode)
	}

	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		var e schemas.WorkerEvent
		if err := json.Unmarshal(line, &e); err != nil {
			return nil, fmt.Errorf("decoding event from %s: %w", reg.WorkerID, err)
		}
		if e.Type == schemas.WorkerEventResult && e.Result != nil {
			if e.Result.Metrics == nil {
				e.Result.Metrics = map[string]any{}
			}
			return e.Result, nil
		}
		onEvent(e)
	}
	if err := scanner.Err(); err != nil {
		return nil, c.wrapWorkerErr(reg, err)
	}
	return nil, fmt.Errorf("worker %s ended its response without a result", reg.WorkerID)
}

func (c *Controller) wrapWorkerErr(reg schemas.WorkerRegistration, err error) error {
	if isTimeout(err) {
		return fmt.Errorf("worker %s did not finish within %s: %w", reg.WorkerID, c.dispatchClient.Timeout, err)
	}
	return err
}

// isTimeout reports whether err is a deadline or client timeout.
func isTimeout(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var ne net.Error
	return errors.As(err, &ne) && ne.Timeout()
}

func failedResult(taskID, workerID, msg string) schemas.AgentResult {
	return schemas.AgentResult{
		TaskID:   taskID,
		WorkerID: workerID,
		Success:  false,
		Metrics:  map[string]any{},
		Error:    &msg,
	}
}

func (c *Controller) newAgent(model string, instructions []string) *agent.Agent {
	return &agent.Agent{Model: model, Instructions: instructions, Options: c.cfg.Options, Client: c.cfg.AgentClient}
}

// plan asks the router agent to break the prompt into subtasks.
func (c *Controller) plan(ctx context.Context, prompt string, history []turn, workers []schemas.WorkerInfo) ([]schemas.SubTask, error) {
	var desc strings.Builder
	for _, w := range workers {
		fmt.Fprintf(&desc, "- worker_id: %s, model: %s, slots: %d, tags: [%s], tools: [%s]\n",
			w.WorkerID, w.ModelID, w.Slots, strings.Join(w.Tags, ", "), strings.Join(w.Tools, ", "))
	}

	router := c.newAgent(c.cfg.RouterModel, []string{
		"You are the task router for a cluster of AI worker agents.",
		"Break the user's task into focused subtasks, each done by one worker. Simple tasks need only one subtask; use more only when the work genuinely splits.",
		"Give each subtask a short unique id (t1, t2, ...), a short title, a system_prompt that sets the worker's role, and a task_prompt with the specific work to do.",
		"Workers do not see the conversation or each other's work, so every task_prompt must include all the context it needs.",
		"Subtasks run in parallel. If a subtask needs another's output, list that subtask's id in depends_on; its results will be added to the task_prompt automatically.",
		"If a subtask needs a tool or capability (for example read_file, run_command, fetch_url, or a tag), list it in requires. Only use names that appear in the worker list. Workers with tools can read and write files in their own workspace, run commands, or fetch web pages, depending on the tools listed.",
		"Each worker has its own private workspace: files one worker writes are invisible to the others. Give subtasks that share files the same worker_id. Otherwise leave worker_id empty and the scheduler balances load.",
	})
	router.Format = planSchema

	resp, err := router.Run(ctx, fmt.Sprintf("%sTask: %s\n\nAvailable workers:\n%s\nPlan the subtasks.",
		formatHistory(history), prompt, desc.String()))
	if err != nil {
		return nil, err
	}
	if subtasks := parsePlan(resp.Content); len(subtasks) > 0 {
		return subtasks, nil
	}
	log.Printf("router reply was unusable, sending the whole task to one worker: %q", truncate(resp.Content, 300))
	return fallbackPlan(prompt), nil
}

// formatHistory shows earlier exchanges of the conversation, so the agents can resolve
// follow-up questions like "what is its population?".
func formatHistory(history []turn) string {
	if len(history) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("Conversation so far (oldest first):\n")
	for _, t := range history {
		fmt.Fprintf(&b, "User: %s\nAssistant: %s\n\n", truncate(t.Prompt, maxHistoryInPrompt), truncate(t.Synthesis, maxHistoryInPrompt))
	}
	return b.String()
}

// review asks whether the results so far answer the task, and for follow-up subtasks if not.
func (c *Controller) review(ctx context.Context, prompt string, history []turn, all []schemas.SubTask, results map[string]schemas.AgentResult) (review, error) {
	reviewer := c.newAgent(c.cfg.RouterModel, []string{
		"You review the work of a cluster of AI worker agents.",
		"Decide whether the subtask results together fully answer the original task. Read the task in the light of the conversation so far, if any.",
		"Set complete to true unless something important is missing or wrong AND more work by the workers could fix it. Do not ask for polish.",
		"If not complete, explain why in reason and add at most 3 follow-up subtasks. Follow-ups may list earlier subtask ids in depends_on to receive their results. Each task_prompt must include all the context it needs.",
	})
	reviewer.Format = reviewSchema
	resp, err := reviewer.Run(ctx, fmt.Sprintf("%sOriginal task: %s\n\nSubtask results:\n%s",
		formatHistory(history), prompt, formatResults(all, results)))
	if err != nil {
		return review{}, err
	}
	return parseReview(resp.Content), nil
}

// synthesize merges the results into the final answer, streaming its tokens.
func (c *Controller) synthesize(ctx context.Context, prompt string, history []turn, all []schemas.SubTask, results map[string]schemas.AgentResult, onToken func(string)) (string, error) {
	synth := c.newAgent(c.cfg.SynthesizerModel, []string{
		"You are a synthesis agent.",
		"You receive the results from multiple specialist worker agents and combine them into a single coherent final answer.",
		"Be concise and structured. Directly address the original task, read in the light of the conversation so far, if any.",
	})
	synth.Markdown = true
	synth.OnEvent = func(e agent.Event) {
		if e.Type == "token" {
			onToken(e.Text)
		}
	}
	resp, err := synth.Run(ctx, fmt.Sprintf("%sOriginal task: %s\n\nWorker results:\n%s\n\nSynthesize a final answer.",
		formatHistory(history), prompt, formatResults(all, results)))
	if err != nil {
		return "", err
	}
	return resp.Content, nil
}

func formatResults(all []schemas.SubTask, results map[string]schemas.AgentResult) string {
	var sections []string
	for _, st := range all {
		r, ok := results[st.ID]
		if !ok {
			continue
		}
		sections = append(sections, fmt.Sprintf("### %s: %s (worker %s)\n%s", st.ID, st.Title, r.WorkerID, resultText(r)))
	}
	return strings.Join(sections, "\n\n")
}
