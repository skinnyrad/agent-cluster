package controller

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/skinnyrad/agent-cluster/internal/schemas"
)

// pool tracks registered workers and hands out their slots.
type pool struct {
	ttl time.Duration // a worker without a heartbeat for this long is not alive

	mu      sync.Mutex
	workers map[string]*poolEntry
	changed chan struct{} // closed and replaced whenever a slot may have freed up
}

type poolEntry struct {
	reg      schemas.WorkerRegistration
	lastSeen time.Time // zero once marked dead, until the next heartbeat
	inFlight int
	removed  bool // deregistered; dropped once its in-flight subtasks finish
}

func newPool(ttl time.Duration) *pool {
	return &pool{ttl: ttl, workers: map[string]*poolEntry{}, changed: make(chan struct{})}
}

// notify wakes everyone waiting for a slot. Callers hold p.mu.
func (p *pool) notify() {
	close(p.changed)
	p.changed = make(chan struct{})
}

func (p *pool) aliveLocked(e *poolEntry) bool {
	return !e.lastSeen.IsZero() && time.Since(e.lastSeen) < p.ttl
}

func (p *pool) register(reg schemas.WorkerRegistration) {
	p.mu.Lock()
	defer p.mu.Unlock()
	e, ok := p.workers[reg.WorkerID]
	if !ok {
		e = &poolEntry{}
		p.workers[reg.WorkerID] = e
	}
	e.reg = reg
	e.lastSeen = time.Now()
	e.removed = false
	p.notify()
}

func (p *pool) remove(id string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if e, ok := p.workers[id]; ok {
		e.lastSeen = time.Time{}
		e.removed = true
		if e.inFlight == 0 {
			delete(p.workers, id)
		}
	}
	p.notify()
}

// markDead excludes a worker until its next heartbeat.
func (p *pool) markDead(id string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if e, ok := p.workers[id]; ok {
		e.lastSeen = time.Time{}
	}
}

func (p *pool) list() []schemas.WorkerInfo {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]schemas.WorkerInfo, 0, len(p.workers))
	for _, e := range p.workers {
		out = append(out, schemas.WorkerInfo{
			WorkerRegistration: e.reg,
			Alive:              p.aliveLocked(e),
			InFlight:           e.inFlight,
			LastSeen:           e.lastSeen,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].WorkerID < out[j].WorkerID })
	return out
}

func (p *pool) live() []schemas.WorkerInfo {
	var out []schemas.WorkerInfo
	for _, w := range p.list() {
		if w.Alive {
			out = append(out, w)
		}
	}
	return out
}

// capabilities are what a subtask's "requires" can name: tags, tool names, and
// "model:<id>".
func capabilities(reg schemas.WorkerRegistration) map[string]bool {
	caps := map[string]bool{"model:" + reg.ModelID: true}
	for _, t := range reg.Tags {
		caps[t] = true
	}
	for _, t := range reg.Tools {
		caps[t] = true
	}
	return caps
}

func satisfies(reg schemas.WorkerRegistration, requires []string) bool {
	caps := capabilities(reg)
	for _, r := range requires {
		if !caps[r] {
			return false
		}
	}
	return true
}

// canSatisfy reports whether any live worker meets requires.
func (p *pool) canSatisfy(requires []string) bool {
	for _, w := range p.live() {
		if satisfies(w.WorkerRegistration, requires) {
			return true
		}
	}
	return false
}

var errNoWorker = errors.New("no live worker")

// acquire waits for a free slot on a live worker that meets the subtask's requirements,
// preferring the worker it names and otherwise the least loaded one, and reserves it.
// exclude lists workers not to use (for retries).
func (p *pool) acquire(ctx context.Context, st schemas.SubTask, exclude map[string]bool) (schemas.WorkerRegistration, error) {
	for {
		p.mu.Lock()
		var best *poolEntry
		eligible := false
		for _, e := range p.workers {
			if exclude[e.reg.WorkerID] || !p.aliveLocked(e) || !satisfies(e.reg, st.Requires) {
				continue
			}
			eligible = true
			if e.inFlight >= e.reg.Slots {
				continue
			}
			if e.reg.WorkerID == st.WorkerID {
				best = e
				break
			}
			if best == nil || load(e) < load(best) || (load(e) == load(best) && e.reg.WorkerID < best.reg.WorkerID) {
				best = e
			}
		}
		if best != nil {
			best.inFlight++
			reg := best.reg
			p.mu.Unlock()
			return reg, nil
		}
		changed := p.changed
		p.mu.Unlock()

		if !eligible {
			if len(st.Requires) > 0 {
				return schemas.WorkerRegistration{}, fmt.Errorf("%w with %s", errNoWorker, strings.Join(st.Requires, ", "))
			}
			return schemas.WorkerRegistration{}, errNoWorker
		}
		// Every eligible worker is busy. Wait for a slot, a new worker, or a heartbeat
		// timeout to change things.
		select {
		case <-ctx.Done():
			return schemas.WorkerRegistration{}, ctx.Err()
		case <-changed:
		case <-time.After(time.Second):
		}
	}
}

func load(e *poolEntry) float64 { return float64(e.inFlight) / float64(e.reg.Slots) }

func (p *pool) release(id string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if e, ok := p.workers[id]; ok {
		e.inFlight--
		if e.inFlight == 0 && e.removed {
			delete(p.workers, id)
		}
	}
	p.notify()
}
