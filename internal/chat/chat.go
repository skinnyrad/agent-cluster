// Package chat is an interactive terminal client for the controller. Each message starts
// a job; the client follows the job's live events and prints the plan, tool calls and
// results as they happen, then the final answer.
package chat

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/signal"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/google/uuid"
	"github.com/peterh/liner"

	"github.com/skinnyrad/agent-cluster/internal/httputil"
	"github.com/skinnyrad/agent-cluster/internal/schemas"
)

// Config configures the chat client.
type Config struct {
	ControllerURL string
	Token         string
	Timeout       time.Duration // how long to follow one job
}

type session struct {
	cfg Config
	c   *client
	id  string // conversation ID; the controller keeps the history
}

// Run starts the interactive chat and returns when the user quits.
func Run(cfg Config) error {
	initMarkdownStyle()
	s := &session{cfg: cfg, c: newClient(cfg.ControllerURL, cfg.Token), id: uuid.NewString()}

	// liner provides up/down input recall and left/right cursor movement.
	line := liner.NewLiner()
	defer line.Close()
	line.SetCtrlCAborts(true)

	fmt.Println()
	fmt.Println(panel("",
		bold.Inherit(cyan).Render("Agent Cluster Chat")+"\n"+
			dim.Render("Controller:")+" "+yellow.Render(s.c.baseURL)+"\n"+
			dim.Render("Type a message to send it to the cluster.  Use ")+cyan.Render("/help")+dim.Render(" for commands."),
		lipgloss.Color("6")))
	fmt.Println()

	for {
		input, err := line.Prompt("You › ")
		if err != nil { // Ctrl-C, Ctrl-D or a closed terminal
			fmt.Println("\n" + dim.Render("Bye!"))
			return nil
		}
		input = strings.TrimSpace(input)
		if input == "" {
			continue
		}
		line.AppendHistory(input)

		if strings.HasPrefix(input, "/") {
			cmd := strings.ToLower(strings.Fields(input)[0])
			switch cmd {
			case "/quit", "/exit", "/q":
				fmt.Println(dim.Render("Bye!"))
				return nil
			case "/clear":
				s.id = uuid.NewString()
				fmt.Println(green.Render("Started a new conversation."))
			case "/workers":
				s.showWorkers()
			case "/jobs":
				s.showJobs()
			case "/help":
				printHelp()
			default:
				fmt.Printf("%s %s  (type %s for list)\n", yellow.Render("Unknown command:"), cmd, cyan.Render("/help"))
			}
			continue
		}
		s.runJob(input)
	}
}

func (s *session) showWorkers() {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	data, err := s.c.workers(ctx)
	if err != nil {
		printError(s.c.baseURL, err)
		return
	}
	if len(data.Workers) == 0 {
		fmt.Println(yellow.Render("No workers have registered yet."))
		return
	}
	fmt.Println(bold.Render("Workers"))
	fmt.Println(workersTable(data.Workers))
}

func (s *session) showJobs() {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	data, err := s.c.jobs(ctx, s.id)
	if err != nil {
		printError(s.c.baseURL, err)
		return
	}
	if len(data.Jobs) == 0 {
		fmt.Println(dim.Render("No jobs in this conversation yet."))
		return
	}
	fmt.Println(jobsTable(data.Jobs))
}

// runJob sends prompt as a job and follows it to the end.
func (s *session) runJob(prompt string) {
	// Ctrl-C while a job runs cancels the job rather than quitting.
	interrupted, stopSignals := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stopSignals()
	ctx, cancel := context.WithTimeout(interrupted, s.cfg.Timeout)
	defer cancel()

	status := startStatus("Sending to the controller...")
	defer status.Stop()

	created, err := s.c.createJob(ctx, schemas.JobRequest{Prompt: prompt, SessionID: s.id})
	if err != nil {
		status.Stop()
		printError(s.c.baseURL, err)
		return
	}
	s.id = created.SessionID

	view := newJobView(status)
	stopRefresh := view.startRefresh()
	defer stopRefresh()

	var final *schemas.Event
	lastSeq, failures := 0, 0
	for final == nil && ctx.Err() == nil {
		err := s.c.events(ctx, created.JobID, func(e schemas.Event) {
			if e.Seq <= lastSeq {
				return // replayed after a reconnect
			}
			lastSeq = e.Seq
			view.handle(e)
			if e.Type == schemas.EventJobFinished {
				final = &e
			}
		})
		if final != nil || ctx.Err() != nil {
			break
		}
		// The stream ended early (a dropped connection, or we fell behind). Reconnect;
		// events we've already seen are skipped.
		if failures++; failures > 5 {
			status.Stop()
			printError(s.c.baseURL, fmt.Errorf("lost the event stream: %v", err))
			return
		}
		time.Sleep(time.Duration(failures) * 500 * time.Millisecond)
	}
	stopRefresh()
	status.Stop()

	switch {
	case final != nil:
		printFinal(final)
	case interrupted.Err() != nil:
		cctx, ccancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer ccancel()
		if err := s.c.cancelJob(cctx, created.JobID); err != nil {
			fmt.Println(yellow.Render("Couldn't cancel the job: ") + err.Error())
			return
		}
		fmt.Println(yellow.Render("Cancelled."))
	default:
		fmt.Printf("%s The job keeps running on the controller; see %s (job %s).\n",
			yellow.Render(fmt.Sprintf("Stopped waiting after %s.", s.cfg.Timeout)), cyan.Render("/jobs"), created.JobID)
	}
}

func printFinal(e *schemas.Event) {
	fmt.Println()
	switch e.Status {
	case schemas.JobDone:
		fmt.Println(rule(bold.Inherit(blue).Render("Answer")))
		if e.Synthesis == "" {
			fmt.Println(yellow.Render("(empty answer)"))
		} else {
			fmt.Print(renderMarkdown(e.Synthesis))
		}
	case schemas.JobCancelled:
		fmt.Println(yellow.Render("The job was cancelled."))
	default:
		fmt.Println(bold.Inherit(red).Render("The job failed: ") + e.Error)
	}
	fmt.Println()
}

func printError(baseURL string, err error) {
	errRed := bold.Inherit(red)
	var statusErr *httputil.StatusError
	var opErr *net.OpError
	switch {
	case errors.As(err, &statusErr) && statusErr.Code == 401:
		fmt.Printf("%s Set CLUSTER_TOKEN (or --token) to the controller's token.\n", errRed.Render("Unauthorized."))
	case errors.As(err, &statusErr):
		fmt.Printf("%s %s\n", errRed.Render(fmt.Sprintf("HTTP %d:", statusErr.Code)), statusErr.Body)
	case errors.As(err, &opErr) && opErr.Op == "dial":
		fmt.Printf("%s Is the controller running at %s?\n", errRed.Render("Connection refused."), yellow.Render(baseURL))
	default:
		fmt.Printf("%s %v\n", errRed.Render("Error:"), err)
	}
}

// jobView turns job events into printed lines and a status line.
type jobView struct {
	status *liveStatus
	start  time.Time

	mu          sync.Mutex
	phase       string
	total, done int
	running     map[string]string // subtask ID -> worker ID
	synthChunks int
}

func newJobView(status *liveStatus) *jobView {
	return &jobView{status: status, start: time.Now(), phase: "Planning", running: map[string]string{}}
}

// startRefresh keeps the elapsed time on the status line current.
func (v *jobView) startRefresh() (stop func()) {
	done := make(chan struct{})
	var once sync.Once
	go func() {
		t := time.NewTicker(500 * time.Millisecond)
		defer t.Stop()
		for {
			select {
			case <-done:
				return
			case <-t.C:
				v.refresh()
			}
		}
	}()
	return func() { once.Do(func() { close(done) }) }
}

func (v *jobView) refresh() {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.refreshLocked()
}

func (v *jobView) refreshLocked() {
	elapsed := time.Since(v.start).Round(time.Second)
	var parts []string
	switch v.phase {
	case "Running":
		parts = append(parts, fmt.Sprintf("%d/%d subtasks done", v.done, v.total))
		if len(v.running) > 0 {
			ids := make([]string, 0, len(v.running))
			for id, w := range v.running {
				ids = append(ids, id+"@"+w)
			}
			sort.Strings(ids)
			parts = append(parts, "running "+strings.Join(ids, ", "))
		}
	case "Synthesizing":
		parts = append(parts, fmt.Sprintf("Writing the answer (%d chunks)", v.synthChunks))
	default:
		parts = append(parts, v.phase+"...")
	}
	parts = append(parts, elapsed.String())
	v.status.Update(strings.Join(parts, " · "))
}

func (v *jobView) handle(e schemas.Event) {
	v.mu.Lock()
	defer v.mu.Unlock()
	switch e.Type {
	case schemas.EventPlan:
		v.phase = "Running"
		v.total += len(e.Subtasks)
		title := "Plan"
		if e.Round > 1 {
			title = fmt.Sprintf("Round %d", e.Round)
		}
		lines := []string{bold.Render(fmt.Sprintf("%s: %d subtask(s)", title, len(e.Subtasks)))}
		for _, st := range e.Subtasks {
			lines = append(lines, subtaskLine(st))
		}
		v.status.Println(strings.Join(lines, "\n"))
	case schemas.EventSubtaskStarted:
		v.running[e.SubtaskID] = e.WorkerID
		v.status.Println(dim.Render(fmt.Sprintf("  ▶ %s started on %s", e.SubtaskID, e.WorkerID)))
	case schemas.EventSubtaskToolCall:
		v.status.Println(toolCallLine(e))
	case schemas.EventSubtaskFinished:
		delete(v.running, e.SubtaskID)
		v.done++
		if e.Result != nil {
			v.status.Println(resultPanel(e.Result))
		}
	case schemas.EventReview:
		if len(e.Subtasks) > 0 {
			v.status.Println(yellow.Render("Reviewer: ") + e.Text)
		} else {
			v.status.Println(dim.Render("Reviewer: " + e.Text))
		}
		v.phase = "Reviewing"
	case schemas.EventSynthesisStarted:
		v.phase = "Synthesizing"
	case schemas.EventSynthesisToken:
		v.synthChunks++
	}
	v.refreshLocked()
}
