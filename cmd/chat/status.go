package main

import (
	"fmt"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

var spinnerFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

var funWaitMessages = []string{
	"Reticulating splines...",
	"Herding digital cats...",
	"Crunching the crunchables...",
	"Noodling through the logic...",
	"Consulting the oracle...",
	"Locating the lost bits...",
	"Polishing the pixels...",
	"Calculating the meaning of life...",
	"Untangling the spaghetti code...",
	"Shuffling the deck...",
	"Brewing some fresh data...",
	"Aligning the stars...",
	"Harmonizing the sub-processors...",
	"Warming up the flux capacitor...",
	"Stretching the memory buffers...",
	"Skedaddling...",
	"Schlepping...",
	"Mulling...",
	"Frolicking...",
	"Pondering...",
	"Ruminating...",
	"Tinkering...",
}

// liveStatus is a spinner that redraws a single status line until stopped.
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
		line := fmt.Sprintf("  %s  %s", spinnerFrames[frame%len(spinnerFrames)], s.message)
		n := utf8.RuneCountInString(line)
		pad := max(s.lastLen-n, 0)
		s.lastLen = n
		s.mu.Unlock()
		fmt.Print("\r" + line + strings.Repeat(" ", pad))

		select {
		case <-s.stop:
			return
		case <-ticker.C:
		}
	}
}

func (s *liveStatus) Update(message string) {
	s.mu.Lock()
	s.message = message
	s.mu.Unlock()
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
	fmt.Print("\r" + strings.Repeat(" ", s.lastLen+4) + "\r")
}
