package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"strings"
	"time"

	"github.com/skinnyrad/agent-cluster/internal/schemas"
)

// Each interaction is one user message plus one assistant message, so 12 entries keep
// the last 6 full interactions.
const historyMaxLen = 12

type chatMessage struct {
	Role    string
	Content string
}

// history is a rolling window of the most recent conversation messages.
type history struct {
	msgs []chatMessage
}

func (h *history) Add(role, content string) {
	h.msgs = append(h.msgs, chatMessage{Role: role, Content: content})
	if len(h.msgs) > historyMaxLen {
		h.msgs = h.msgs[len(h.msgs)-historyMaxLen:]
	}
}

func (h *history) Clear() { h.msgs = nil }

// augmentedPrompt prepends the conversation so far to prompt.
func (h *history) augmentedPrompt(prompt string) string {
	if len(h.msgs) == 0 {
		return prompt
	}
	lines := []string{"[Conversation so far]"}
	for _, m := range h.msgs {
		role := "Assistant"
		if m.Role == "user" {
			role = "User"
		}
		lines = append(lines, role+": "+m.Content)
	}
	lines = append(lines, "", "User: "+prompt)
	return strings.Join(lines, "\n")
}

// httpStatusError is returned when the controller answers with a non-2xx status.
type httpStatusError struct {
	Code int
	Body string
}

func (e *httpStatusError) Error() string { return fmt.Sprintf("HTTP %d: %s", e.Code, e.Body) }

func fetchWorkers(baseURL string) (*schemas.WorkersResponse, error) {
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Get(baseURL + "/workers")
	if err != nil {
		return nil, err
	}
	var out schemas.WorkersResponse
	if err := decode(resp, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// dispatchPrompt sends prompt plus conversation history to /dispatch, cycling the status
// line through the pipeline stages while it waits.
func dispatchPrompt(baseURL, prompt string, hist *history, status *liveStatus) (*schemas.DispatchResponse, error) {
	status.Update("Tasking fleet...")

	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		cycleStages(status, stop)
	}()
	defer func() {
		close(stop)
		<-done
	}()

	body, err := json.Marshal(map[string]string{"prompt": hist.augmentedPrompt(prompt)})
	if err != nil {
		return nil, err
	}
	client := &http.Client{Timeout: 300 * time.Second}
	resp, err := client.Post(baseURL+"/dispatch", "application/json", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	var out schemas.DispatchResponse
	if err := decode(resp, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func cycleStages(status *liveStatus, stop <-chan struct{}) {
	// Advance the stage label roughly every 3 seconds.
	for _, label := range []string{"Router decomposing task...", "Dispatching to workers...", "Waiting on fleet results..."} {
		select {
		case <-stop:
			return
		case <-time.After(3 * time.Second):
			status.Update(label)
		}
	}
	// Once we're waiting on workers, keep things lively with random updates.
	last := ""
	for {
		select {
		case <-stop:
			return
		case <-time.After(20 * time.Second):
			next := funWaitMessages[rand.IntN(len(funWaitMessages))]
			for next == last {
				next = funWaitMessages[rand.IntN(len(funWaitMessages))]
			}
			status.Update(next)
			last = next
		}
	}
}

func decode(resp *http.Response, v any) error {
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return &httpStatusError{Code: resp.StatusCode, Body: string(data)}
	}
	return json.Unmarshal(data, v)
}
