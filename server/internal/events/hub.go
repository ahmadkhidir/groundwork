package events

import (
	"sync"
)

// Hub is an in-process fan-out of live-run events (FEAT-023) keyed by run id.
//
// The orchestrator/executor publishes each transition to the run's subscribers; an SSE handler
// subscribes for the life of one open connection and forwards the buffered events verbatim, after
// first replaying a snapshot from persisted state so a mid-run subscriber still sees the current
// point.
//
// Delivery is at-most-once and lossless-in-spirit: sends are non-blocking into a per-subscriber
// buffer sized to swallow a run's transitions, and a full buffer drops rather than stalls the
// executor (the durable runs/steps rows and the SSE snapshot are the real source of truth; a dropped
// live event simply means the viewer reconciles the transition on its next step read). Publish never
// blocks the publisher.
type Hub struct {
	mu   sync.RWMutex
	subs map[string]map[chan Event]struct{}
}

// NewHub returns an empty Hub.
func NewHub() *Hub {
	return &Hub{subs: make(map[string]map[chan Event]struct{})}
}

// subBuffer is the per-subscriber event buffer. Generous because a run can transition bulk
// generate plan→execute→chart→narrate quickly, but bounded so a stalled subscriber cannot grow the
// process without limit.
const subBuffer = 256

// Subscribe opens a subscription to runID's events. The returned channel receives events published
// after the call; the returned func closes the subscription and MUST be called on teardown (the SSE
// handler calls it when the connection closes).
func (h *Hub) Subscribe(runID string) (<-chan Event, func()) {
	ch := make(chan Event, subBuffer)
	h.mu.Lock()
	if h.subs[runID] == nil {
		h.subs[runID] = make(map[chan Event]struct{})
	}
	h.subs[runID][ch] = struct{}{}
	h.mu.Unlock()

	return ch, func() {
		h.mu.Lock()
		delete(h.subs[runID], ch)
		if len(h.subs[runID]) == 0 {
			delete(h.subs, runID)
		}
		h.mu.Unlock()
	}
}

// SubscriberCount reports the number of live subscribers for runID — used by tests to pin the
// subscribe/unsubscribe contract.
func (h *Hub) SubscriberCount(runID string) int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.subs[runID])
}

// Publish fans ev out to every current subscriber of runID, never blocking the caller. Events for a
// run with no subscribers are dropped.
func (h *Hub) Publish(runID string, ev Event) {
	h.mu.RLock()
	subs := h.subs[runID]
	chans := make([]chan Event, 0, len(subs))
	for ch := range subs {
		chans = append(chans, ch)
	}
	h.mu.RUnlock()

	for _, ch := range chans {
		select {
		case ch <- ev:
		default:
			// Buffer full: drop rather than stall the publisher.
		}
	}
}
