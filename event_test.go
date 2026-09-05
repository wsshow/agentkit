package agentkit

import (
	"errors"
	"reflect"
	"testing"
)

func TestEmitterCallsSubscribersInSubscriptionOrder(t *testing.T) {
	emitter := newEmitter()
	var calls []int
	emitter.Subscribe(func(Event) { calls = append(calls, 1) })
	unsubscribe := emitter.Subscribe(func(Event) { calls = append(calls, 2) })
	emitter.Subscribe(func(Event) { calls = append(calls, 3) })

	for range 100 {
		calls = calls[:0]
		emitter.Emit(Event{Type: EventAgentStart})
		if want := []int{1, 2, 3}; !reflect.DeepEqual(calls, want) {
			t.Fatalf("subscriber order = %v, want %v", calls, want)
		}
	}

	unsubscribe()
	calls = calls[:0]
	emitter.Emit(Event{Type: EventAgentEnd})
	if want := []int{1, 3}; !reflect.DeepEqual(calls, want) {
		t.Fatalf("subscriber order after unsubscribe = %v, want %v", calls, want)
	}
}

func TestEmitterIsolatesMutableEventFields(t *testing.T) {
	emitter := newEmitter()
	index := 1
	event := Event{
		Type: EventToolStart,
		ToolCalls: []ToolCall{{
			Index: &index,
			ID:    "call-1",
			Extra: map[string]any{"nested": map[string]any{"value": "original"}},
		}},
		ResponseMeta: &ResponseMeta{Usage: &TokenUsage{TotalTokens: 42}},
		Interrupt:    []InterruptPoint{{ID: "interrupt-1"}},
		Compaction:   &CompactionInfo{MessagesBefore: 10, MessagesAfter: 4},
		Goal:         &Goal{ID: "goal-1", Objective: "original"},
		Delegation:   &DelegationInfo{ID: "delegation-1", Path: []string{"parent", "child"}},
	}

	emitter.Subscribe(func(event Event) {
		*event.ToolCalls[0].Index = 99
		event.ToolCalls[0].Extra["nested"].(map[string]any)["value"] = "mutated"
		event.ResponseMeta.Usage.TotalTokens = 99
		event.Interrupt[0].ID = "mutated"
		event.Compaction.MessagesAfter = 99
		event.Goal.Objective = "mutated"
		event.Delegation.Path[0] = "mutated"
	})
	emitter.Subscribe(func(event Event) {
		assertOriginalEvent(t, event)
	})

	emitter.Emit(event)
	assertOriginalEvent(t, event)
}

func TestEmitterIgnoresNilSubscriber(t *testing.T) {
	emitter := newEmitter()
	unsubscribe := emitter.Subscribe(nil)
	emitter.Emit(Event{Type: EventAgentStart})
	unsubscribe()
}

func TestEmitterIsolatesSubscriberPanics(t *testing.T) {
	emitter := newEmitter()
	var first, second []Event
	emitter.Subscribe(func(event Event) {
		first = append(first, event)
	})
	emitter.Subscribe(func(Event) {
		panic("observer failed")
	})
	emitter.Subscribe(func(event Event) {
		second = append(second, event)
	})

	emitter.Emit(Event{Type: EventAgentStart, Agent: "assistant"})
	for name, events := range map[string][]Event{"first": first, "second": second} {
		if len(events) != 2 || events[0].Type != EventAgentStart || events[1].Type != EventError {
			t.Fatalf("%s subscriber events = %#v", name, events)
		}
		if events[1].Agent != "assistant" || !errors.Is(events[1].Error, ErrSubscriberPanic) {
			t.Fatalf("%s subscriber diagnostic = %#v", name, events[1])
		}
	}
}

func assertOriginalEvent(t *testing.T, event Event) {
	t.Helper()
	if got := *event.ToolCalls[0].Index; got != 1 {
		t.Fatalf("tool index = %d, want 1", got)
	}
	if got := event.ToolCalls[0].Extra["nested"].(map[string]any)["value"]; got != "original" {
		t.Fatalf("nested tool metadata = %v, want original", got)
	}
	if got := event.ResponseMeta.Usage.TotalTokens; got != 42 {
		t.Fatalf("total tokens = %d, want 42", got)
	}
	if got := event.Interrupt[0].ID; got != "interrupt-1" {
		t.Fatalf("interrupt ID = %q, want interrupt-1", got)
	}
	if got := event.Compaction.MessagesAfter; got != 4 {
		t.Fatalf("messages after compaction = %d, want 4", got)
	}
	if got := event.Goal.Objective; got != "original" {
		t.Fatalf("goal objective = %q, want original", got)
	}
	if got := event.Delegation.Path[0]; got != "parent" {
		t.Fatalf("delegation path = %#v, want parent first", event.Delegation.Path)
	}
}
