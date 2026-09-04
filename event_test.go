package agentkit

import (
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
	}

	emitter.Subscribe(func(event Event) {
		*event.ToolCalls[0].Index = 99
		event.ToolCalls[0].Extra["nested"].(map[string]any)["value"] = "mutated"
		event.ResponseMeta.Usage.TotalTokens = 99
		event.Interrupt[0].ID = "mutated"
		event.Compaction.MessagesAfter = 99
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
}
