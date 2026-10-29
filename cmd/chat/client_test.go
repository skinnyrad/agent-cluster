package main

import (
	"fmt"
	"testing"
)

func TestHistoryAugmentedPrompt(t *testing.T) {
	var h history
	if got := h.augmentedPrompt("hi"); got != "hi" {
		t.Errorf("empty history: got %q, want %q", got, "hi")
	}

	h.Add("user", "What is 2+2?")
	h.Add("assistant", "4")
	want := "[Conversation so far]\nUser: What is 2+2?\nAssistant: 4\n\nUser: And 3+3?"
	if got := h.augmentedPrompt("And 3+3?"); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestHistoryKeepsLastSixInteractions(t *testing.T) {
	var h history
	for i := range 10 {
		h.Add("user", fmt.Sprint("q", i))
		h.Add("assistant", fmt.Sprint("a", i))
	}
	if len(h.msgs) != historyMaxLen {
		t.Fatalf("got %d messages, want %d", len(h.msgs), historyMaxLen)
	}
	if h.msgs[0].Content != "q4" || h.msgs[len(h.msgs)-1].Content != "a9" {
		t.Errorf("window = %v..%v, want q4..a9", h.msgs[0].Content, h.msgs[len(h.msgs)-1].Content)
	}

	h.Clear()
	if len(h.msgs) != 0 {
		t.Error("Clear left messages behind")
	}
}
