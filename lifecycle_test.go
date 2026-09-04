package agentkit

import (
	"context"
	"testing"

	"github.com/cloudwego/eino/schema"
)

func TestAgentEndsTurnAfterLastToolResultEvent(t *testing.T) {
	agent := &Agent{
		name:      "assistant",
		state:     newState(),
		emtr:      newEmitter(),
		toolCalls: make(map[string]toolCallInfo),
	}
	agent.inTurn.Store(true)
	agent.recordToolCalls([]schema.ToolCall{{
		ID:       "call-1",
		Function: schema.FunctionCall{Name: "first"},
	}, {
		ID:       "call-2",
		Function: schema.FunctionCall{Name: "second"},
	}})

	events := newMockEventRecorder()
	agent.Subscribe(events.Record)
	agent.processMessage("assistant", schema.ToolMessage("first result", "call-1"))
	if got := events.Last(EventTurnEnd); got != nil {
		t.Fatalf("turn ended before all tool results: %#v", got)
	}

	agent.processMessage("assistant", schema.ToolMessage("second result", "call-2"))
	events.RequireTypes(t,
		EventToolEnd,
		EventMessageStart,
		EventMessageEnd,
		EventToolEnd,
		EventMessageStart,
		EventMessageEnd,
		EventTurnEnd,
	)
}

func TestToolCallInfoWaitsUntilToolStartEvent(t *testing.T) {
	agent := &Agent{
		name:      "assistant",
		emtr:      newEmitter(),
		toolCalls: make(map[string]toolCallInfo),
	}
	type result struct {
		name string
		args string
	}
	waiting := make(chan struct{})
	resultCh := make(chan result, 1)
	go func() {
		close(waiting)
		name, args, _ := agent.waitToolCallInfo(context.Background(), "call-1")
		resultCh <- result{name: name, args: args}
	}()
	<-waiting

	select {
	case got := <-resultCh:
		t.Fatalf("tool info returned before tool_start: %#v", got)
	default:
	}

	agent.emitToolStart("assistant", []schema.ToolCall{{
		ID: "call-1",
		Function: schema.FunctionCall{
			Name:      "echo",
			Arguments: `{"text":"hello"}`,
		},
	}})
	got := <-resultCh
	if got.name != "echo" || got.args != `{"text":"hello"}` {
		t.Fatalf("tool info = %#v", got)
	}
}

func TestToolBatchCompletesAfterToolResultSubscribers(t *testing.T) {
	agent := &Agent{
		name:      "assistant",
		state:     newState(),
		emtr:      newEmitter(),
		toolCalls: make(map[string]toolCallInfo),
	}
	call := schema.ToolCall{
		ID:       "call-1",
		Function: schema.FunctionCall{Name: "weather"},
	}
	agent.prepareToolBatch([]schema.ToolCall{call})
	agent.inTurn.Store(true)

	subscriberCalled := false
	agent.Subscribe(func(event Event) {
		if event.Type == EventToolEnd {
			subscriberCalled = true
			agent.Steer("new direction")
		}
	})

	agent.processMessage("assistant", schema.ToolMessage("sunny", "call-1"))
	if !subscriberCalled {
		t.Fatal("tool batch completed before tool_end subscriber")
	}
	if err := agent.waitToolBatch(context.Background()); err != nil {
		t.Fatalf("waitToolBatch() error = %v", err)
	}
	if !agent.hasSteering() {
		t.Fatal("steering message was not visible when tool batch completed")
	}
}
