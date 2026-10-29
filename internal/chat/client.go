package chat

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/skinnyrad/agent-cluster/internal/httputil"
	"github.com/skinnyrad/agent-cluster/internal/schemas"
)

// client talks to the controller.
type client struct {
	baseURL string
	token   string
	http    *http.Client // for short requests
	stream  *http.Client // for event streams, which stay open
}

func newClient(baseURL, token string) *client {
	return &client{
		baseURL: strings.TrimRight(baseURL, "/"),
		token:   token,
		http:    &http.Client{Timeout: 15 * time.Second},
		stream:  &http.Client{},
	}
}

func (c *client) do(ctx context.Context, method, path string, body, out any) error {
	var r io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return err
		}
		r = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, r)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	httputil.SetToken(req, c.token)
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	return httputil.DecodeResponse(resp, out)
}

func (c *client) workers(ctx context.Context) (*schemas.WorkersResponse, error) {
	var out schemas.WorkersResponse
	return &out, c.do(ctx, http.MethodGet, "/workers", nil, &out)
}

func (c *client) createJob(ctx context.Context, req schemas.JobRequest) (*schemas.JobCreated, error) {
	var out schemas.JobCreated
	return &out, c.do(ctx, http.MethodPost, "/jobs", req, &out)
}

func (c *client) job(ctx context.Context, id string) (*schemas.Job, error) {
	var out schemas.Job
	return &out, c.do(ctx, http.MethodGet, "/jobs/"+url.PathEscape(id), nil, &out)
}

func (c *client) cancelJob(ctx context.Context, id string) error {
	return c.do(ctx, http.MethodDelete, "/jobs/"+url.PathEscape(id), nil, nil)
}

func (c *client) jobs(ctx context.Context, sessionID string) (*schemas.JobsResponse, error) {
	var out schemas.JobsResponse
	return &out, c.do(ctx, http.MethodGet, "/jobs?limit=10&session_id="+url.QueryEscape(sessionID), nil, &out)
}

// events streams a job's events to onEvent until the stream ends.
func (c *client) events(ctx context.Context, jobID string, onEvent func(schemas.Event)) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/jobs/"+url.PathEscape(jobID)+"/events", nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "text/event-stream")
	httputil.SetToken(req, c.token)
	resp, err := c.stream.Do(req)
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		return httputil.DecodeResponse(resp, nil)
	}
	defer resp.Body.Close()
	return readSSE(resp.Body, onEvent)
}

// readSSE parses a server-sent event stream whose data lines are JSON events.
func readSSE(r io.Reader, onEvent func(schemas.Event)) error {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	var data strings.Builder
	for scanner.Scan() {
		line := scanner.Text()
		switch {
		case line == "":
			if data.Len() > 0 {
				var e schemas.Event
				if err := json.Unmarshal([]byte(data.String()), &e); err == nil {
					onEvent(e)
				}
				data.Reset()
			}
		case strings.HasPrefix(line, "data:"):
			if data.Len() > 0 {
				data.WriteString("\n")
			}
			data.WriteString(strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " "))
		}
		// "id:", "event:" and ":" comment lines carry nothing the event doesn't.
	}
	return scanner.Err()
}
