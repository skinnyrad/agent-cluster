package chat

import (
	"strings"
	"testing"

	"github.com/skinnyrad/agent-cluster/internal/schemas"
)

func TestReadSSE(t *testing.T) {
	stream := strings.Join([]string{
		": keep-alive",
		"",
		"id: 1",
		"event: job.started",
		`data: {"seq":1,"type":"job.started"}`,
		"",
		"id: 2",
		"event: subtask.token",
		`data: {"seq":2,"type":"subtask.token","text":"hi"}`,
		"",
		"data: not json",
		"",
	}, "\n")

	var got []schemas.Event
	if err := readSSE(strings.NewReader(stream), func(e schemas.Event) { got = append(got, e) }); err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Type != schemas.EventJobStarted || got[1].Text != "hi" {
		t.Fatalf("events = %+v", got)
	}
}
