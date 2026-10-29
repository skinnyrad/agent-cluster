package chat

import (
	"fmt"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

var spinnerFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

// liveStatus is a spinner that redraws a single status line until stopped. Println
// prints a line above it without garbling the spinner.
type liveStatus struct {
	mu      sync.Mutex
	message string
	lastLen int
	stop    chan struct{}
	done    chan struct{}
}

func startStatus(message string) *liveStatus {
	s := &liveStatus{message: message, stop: make(chan struct{}), done: make(chan struct{})}
	go s.render()
	return s
}

func (s *liveStatus) render() {
	defer close(s.done)
	ticker := time.NewTicker(80 * time.Millisecond)
	defer ticker.Stop()
	for frame := 0; ; frame++ {
		s.mu.Lock()
		s.drawLocked(frame)
		s.mu.Unlock()
		select {
		case <-s.stop:
			return
		case <-ticker.C:
		}
	}
}

func (s *liveStatus) drawLocked(frame int) {
	line := fmt.Sprintf("  %s  %s", spinnerFrames[frame%len(spinnerFrames)], s.message)
	if w := termWidth() - 1; utf8.RuneCountInString(line) > w {
		line = string([]rune(line)[:w-1]) + "…"
	}
	n := utf8.RuneCountInString(line)
	pad := max(s.lastLen-n, 0)
	s.lastLen = n
	fmt.Print("\r" + line + strings.Repeat(" ", pad))
}

func (s *liveStatus) clearLocked() {
	fmt.Print("\r" + strings.Repeat(" ", s.lastLen) + "\r")
	s.lastLen = 0
}

func (s *liveStatus) Update(message string) {
	s.mu.Lock()
	s.message = message
	s.mu.Unlock()
}

// Println prints a line above the spinner.
func (s *liveStatus) Println(line string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.clearLocked()
	fmt.Println(line)
}

// Stop halts the spinner and clears its line. It is safe to call more than once.
func (s *liveStatus) Stop() {
	select {
	case <-s.stop:
		return
	default:
		close(s.stop)
	}
	<-s.done
	s.mu.Lock()
	s.clearLocked()
	s.mu.Unlock()
}
