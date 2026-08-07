package events

import (
	"testing"
	"time"
)

func TestMemberCountStartsAtZero(t *testing.T) {
	h := NewHub()
	if got := h.SubscriberCount("run-1"); got != 0 {
		t.Fatalf("SubscriberCount(run-1) = %d, want 0", got)
	}
}

func TestSubscribePublish(t *testing.T) {
	h := NewHub()
	ch, unsubscribe := h.Subscribe("run-1")
	defer unsubscribe()

	if got := h.SubscriberCount("run-1"); got != 1 {
		t.Fatalf("SubscriberCount(run-1) = %d, want 1", got)
	}

	h.Publish("run-1", Event{Type: TypeRunStatus, RunID: "run-1", Status: "running"})

	select {
	case ev := <-ch:
		if ev.Type != TypeRunStatus || ev.Status != "running" {
			t.Fatalf("received event = %+v, want run.status running", ev)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for published event")
	}
}

func TestUnsubscribeStopsDelivery(t *testing.T) {
	h := NewHub()
	ch, unsubscribe := h.Subscribe("run-1")
	unsubscribe()

	if got := h.SubscriberCount("run-1"); got != 0 {
		t.Fatalf("SubscriberCount after unsubscribe = %d, want 0", got)
	}

	h.Publish("run-1", Event{Type: TypeRunStatus, RunID: "run-1", Status: "running"})

	select {
	case ev := <-ch:
		t.Fatalf("received event after unsubscribe: %+v", ev)
	case <-time.After(50 * time.Millisecond):
		// Correct: nothing delivered after unsubscribe.
	}
}

func TestPublishFansOutToAllSubscribers(t *testing.T) {
	h := NewHub()
	c1, un1 := h.Subscribe("run-1")
	defer un1()
	c2, un2 := h.Subscribe("run-1")
	defer un2()

	h.Publish("run-1", Event{Type: TypeStepCompleted, RunID: "run-1", StepName: "execute"})

	for name, c := range map[string]<-chan Event{"c1": c1, "c2": c2} {
		select {
		case ev := <-c:
			if ev.Type != TypeStepCompleted {
				t.Fatalf("%s received %+v, want step.completed", name, ev)
			}
		case <-time.After(time.Second):
			t.Fatalf("%s did not receive the event", name)
		}
	}
}

func TestPublishDropsWhenSubscriberCannotKeepUp(t *testing.T) {
	// The hub is a best-effort fan-out: a subscriber that never drains is not allowed to stall the
	// publisher, so once the fixed buffer fills, further events are dropped rather than blocking.
	h := NewHub()
	_, unsubscribe := h.Subscribe("run-1")
	defer unsubscribe()

	for i := 0; i < subBuffer*2; i++ {
		h.Publish("run-1", Event{Type: TypeRunStatus, RunID: "run-1", Status: "running"})
	}

	// The publisher never blocks, which is the invariant under test. We cannot observe dropped events
	// without also draining, so this test only asserts the publisher completed without a deadlock.
	if got := h.SubscriberCount("run-1"); got != 1 {
		t.Fatalf("SubscriberCount = %d, want 1", got)
	}
}

func TestDifferentRunsAreIsolated(t *testing.T) {
	h := NewHub()
	ch, unsubscribe := h.Subscribe("run-1")
	defer unsubscribe()

	h.Publish("run-2", Event{Type: TypeRunStatus, RunID: "run-2", Status: "complete"})

	select {
	case ev := <-ch:
		t.Fatalf("run-2 event leaked to run-1 subscriber: %+v", ev)
	case <-time.After(50 * time.Millisecond):
	}
}
