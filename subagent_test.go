package agentkit

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cloudwego/eino/schema"
)

func TestSubAgentDelegationIsObservableAndContextIsolated(t *testing.T) {
	ctx := context.Background()
	parentCall := MockModelToolCallWithID("research-call", "researcher", `{"request":"compare the APIs"}`)
	parentCall.Message.ResponseMeta = &schema.ResponseMeta{Usage: &schema.TokenUsage{TotalTokens: 3}}
	parentFinalMessage := MockModelText("child findings")
	parentFinalMessage.Message.ResponseMeta = &schema.ResponseMeta{Usage: &schema.TokenUsage{TotalTokens: 5}}
	parentFinal := MockModelAfterToolResult("research-call", parentFinalMessage)
	childReply := MockModelText("child findings")
	childReply.Message.ResponseMeta = &schema.ResponseMeta{Usage: &schema.TokenUsage{TotalTokens: 7}}
	childModel := NewMockChatModel(childReply)

	agent, err := New(ctx, &Config{
		Name:         "coordinator",
		SystemPrompt: "coordinate work",
		Model:        NewMockChatModel(parentCall, parentFinal),
		History:      []*schema.Message{schema.UserMessage("private previous message")},
		SubAgents: []SubAgentConfig{{
			Name:         "researcher",
			Description:  "Research a focused question",
			SystemPrompt: "research carefully",
			Model:        childModel,
		}},
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	t.Cleanup(func() { _ = agent.Close() })
	recorder := newMockEventRecorder()
	agent.Subscribe(recorder.Record)

	result, err := agent.Ask(ctx, "start the comparison")
	if err != nil {
		t.Fatalf("Ask() error = %v", err)
	}
	if result.Text != "child findings" {
		t.Fatalf("result text = %q, want child findings", result.Text)
	}
	if result.Usage == nil || result.Usage.TotalTokens != 15 {
		t.Fatalf("usage = %#v, want 15 total tokens", result.Usage)
	}

	childCalls := childModel.Calls()
	if len(childCalls) != 1 {
		t.Fatalf("child model calls = %d, want 1", len(childCalls))
	}
	var childInput strings.Builder
	for _, message := range childCalls[0].Input {
		if message != nil {
			childInput.WriteString(message.Content)
		}
	}
	if strings.Contains(childInput.String(), "private previous message") || strings.Contains(childInput.String(), "start the comparison") {
		t.Fatalf("isolated child input leaked parent history: %q", childInput.String())
	}
	if !strings.Contains(childInput.String(), "compare the APIs") {
		t.Fatalf("child input = %q, want delegated request", childInput.String())
	}

	stateMessages := agent.State().Messages()
	for _, message := range stateMessages {
		if message.Agent == "researcher" {
			t.Fatalf("child message leaked into parent state: %#v", message)
		}
	}
	history := agent.History()
	if len(history) != 5 {
		t.Fatalf("history length = %d, want previous + user + assistant/tool/assistant", len(history))
	}

	start := recorder.Last(EventDelegationStart)
	end := recorder.Last(EventDelegationEnd)
	if start == nil || end == nil || start.Delegation == nil || end.Delegation == nil {
		t.Fatalf("delegation events missing: start %#v, end %#v", start, end)
	}
	if start.Delegation.ID != "research-call" || end.Delegation.ID != start.Delegation.ID || end.Error != nil {
		t.Fatalf("delegation lifecycle = start %#v, end %#v", start, end)
	}
	if got := start.Delegation.Path; len(got) != 2 || got[0] != "coordinator" || got[1] != "researcher" {
		t.Fatalf("delegation path = %#v", got)
	}
	var foundChildMessage bool
	for _, event := range recorder.Events() {
		if event.Agent == "researcher" && event.Type == EventMessageEnd && event.Content == "child findings" {
			foundChildMessage = event.Delegation != nil && event.Delegation.ID == "research-call"
		}
	}
	if !foundChildMessage {
		t.Fatal("missing correlated child message event")
	}
}

func TestSubAgentOwnToolsEmitCorrelatedProgress(t *testing.T) {
	ctx := context.Background()
	lookup := MustMockTool("lookup", "look up a value", func(ctx context.Context, query string) (string, error) {
		EmitToolUpdate(ctx, "searching")
		return "found " + query, nil
	})
	lookupCall := lookup.Call("lookup-call", "agentkit")
	childModel := NewMockChatModel(
		MockModelCalls(lookupCall),
		MockModelTextAfterToolResult("lookup-call"),
	)
	agent, err := New(ctx, &Config{
		Name: "coordinator",
		Model: NewMockChatModel(
			MockModelToolCallWithID("delegate-call", "researcher", `{"request":"investigate"}`),
			MockModelTextAfterToolResult("delegate-call"),
		),
		SubAgents: []SubAgentConfig{{
			Name:        "researcher",
			Description: "Research a focused question",
			Model:       childModel,
			Tools:       MockTools(lookup),
		}},
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	t.Cleanup(func() { _ = agent.Close() })
	recorder := newMockEventRecorder()
	agent.Subscribe(recorder.Record)

	result, err := agent.Ask(ctx, "delegate")
	if err != nil || result.Text != "found agentkit" {
		t.Fatalf("Ask() = %#v, %v", result, err)
	}
	for _, event := range recorder.Events() {
		if event.Type == EventToolUpdate && event.Agent == "researcher" {
			if event.Content != "searching" || event.ToolCallID != "lookup-call" || event.ToolName != "lookup" {
				t.Fatalf("tool update = %#v", event)
			}
			if event.Delegation == nil || event.Delegation.ID != "delegate-call" {
				t.Fatalf("tool update delegation = %#v", event.Delegation)
			}
			return
		}
	}
	t.Fatal("missing child tool update")
}

func TestSubAgentRejectsOverlappingCallsToSameAgent(t *testing.T) {
	parent := &Agent{name: "coordinator", emtr: newEmitter()}
	runtime := newSubAgentRuntime(parent, parent.name, []SubAgentConfig{{Name: "worker"}}, nil)
	ctx, cancel, _, err := runtime.begin(context.Background(), "worker", "first")
	if err != nil || ctx == nil {
		t.Fatalf("first begin() = %v, %v", ctx, err)
	}
	defer cancel()
	_, secondCancel, _, err := runtime.begin(context.Background(), "worker", "second")
	secondCancel()
	if !errors.Is(err, ErrSubAgentBusy) {
		t.Fatalf("second begin() error = %v, want ErrSubAgentBusy", err)
	}
}

func TestSubAgentDelegationBudgetIsPerRun(t *testing.T) {
	ctx := context.Background()
	childModel := NewMockChatModel(MockModelText("first result"), MockModelText("next run result"))
	agent, err := New(ctx, &Config{
		Name: "coordinator",
		Model: NewMockChatModel(
			MockModelToolCallWithID("first", "worker", `{"request":"first"}`),
			MockModelTextAfterToolResult("first"),
			MockModelToolCallWithID("third", "worker", `{"request":"third"}`),
			MockModelTextAfterToolResult("third"),
		),
		SubAgents:      []SubAgentConfig{{Name: "worker", Description: "Do one job", Model: childModel}},
		SubAgentPolicy: &SubAgentPolicy{MaxDelegations: 1},
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	t.Cleanup(func() { _ = agent.Close() })

	first, err := agent.Ask(ctx, "first run")
	if err != nil || first.Text != "first result" {
		t.Fatalf("first Ask() = %#v, %v", first, err)
	}
	result, err := agent.Ask(ctx, "second run")
	if err != nil || result.Text != "next run result" {
		t.Fatalf("second Ask() = %#v, %v", result, err)
	}
	if got := len(childModel.Calls()); got != 2 {
		t.Fatalf("child calls = %d, want budget reset and 2 total", got)
	}
}

func TestSubAgentRejectsDelegationsBeyondRunBudget(t *testing.T) {
	parent := &Agent{name: "coordinator", emtr: newEmitter()}
	runtime := newSubAgentRuntime(parent, parent.name, []SubAgentConfig{{Name: "one"}, {Name: "two"}}, &SubAgentPolicy{
		MaxDelegations: 1,
	})
	_, cancel, _, err := runtime.begin(context.Background(), "one", "first")
	if err != nil {
		t.Fatalf("first begin() error = %v", err)
	}
	cancel()
	runtime.endInvocation("first", nil)
	runtime.finish("first")
	_, secondCancel, _, err := runtime.begin(context.Background(), "two", "second")
	secondCancel()
	if !errors.Is(err, ErrSubAgentBudgetExceeded) {
		t.Fatalf("second begin() error = %v, want ErrSubAgentBudgetExceeded", err)
	}
}

func TestSubAgentTimeoutAndModelPanicAreContained(t *testing.T) {
	t.Run("timeout", func(t *testing.T) {
		ctx := context.Background()
		blocking := &modelGuardStub{
			generate: func(ctx context.Context, _ []*schema.Message, _ ...ModelOption) (*schema.Message, error) {
				<-ctx.Done()
				return nil, ctx.Err()
			},
			stream: func(ctx context.Context, _ []*schema.Message, _ ...ModelOption) (*schema.StreamReader[*schema.Message], error) {
				<-ctx.Done()
				return nil, ctx.Err()
			},
		}
		agent, err := New(ctx, &Config{
			Name:           "coordinator",
			Model:          NewMockChatModel(MockModelToolCallWithID("slow-call", "slow", `{"request":"wait"}`)),
			SubAgents:      []SubAgentConfig{{Name: "slow", Description: "Wait", Model: blocking}},
			SubAgentPolicy: &SubAgentPolicy{Timeout: 20 * time.Millisecond},
		})
		if err != nil {
			t.Fatalf("New() error = %v", err)
		}
		t.Cleanup(func() { _ = agent.Close() })
		recorder := newMockEventRecorder()
		agent.Subscribe(recorder.Record)
		if _, err := agent.Ask(ctx, "wait"); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("Ask() error = %v, want deadline exceeded", err)
		}
		end := recorder.Last(EventDelegationEnd)
		if end == nil || !errors.Is(end.Error, context.DeadlineExceeded) {
			t.Fatalf("delegation end = %#v", end)
		}
	})

	t.Run("panic", func(t *testing.T) {
		ctx := context.Background()
		panicking := &modelGuardStub{
			generate: func(context.Context, []*schema.Message, ...ModelOption) (*schema.Message, error) {
				panic("child generate")
			},
			stream: func(context.Context, []*schema.Message, ...ModelOption) (*schema.StreamReader[*schema.Message], error) {
				panic("child stream")
			},
		}
		agent, err := New(ctx, &Config{
			Name:      "coordinator",
			Model:     NewMockChatModel(MockModelToolCallWithID("panic-call", "worker", `{"request":"panic"}`)),
			SubAgents: []SubAgentConfig{{Name: "worker", Description: "Panic safely", Model: panicking}},
		})
		if err != nil {
			t.Fatalf("New() error = %v", err)
		}
		t.Cleanup(func() { _ = agent.Close() })
		if _, err := agent.Ask(ctx, "panic"); !errors.Is(err, ErrModelPanic) {
			t.Fatalf("Ask() error = %v, want ErrModelPanic", err)
		}
	})
}

func TestSubAgentInheritsParentModel(t *testing.T) {
	ctx := context.Background()
	shared := NewMockChatModel(
		MockModelToolCallWithID("delegate", "worker", `{"request":"do it"}`),
		MockModelText("child result"),
		MockModelTextAfterToolResult("delegate"),
	)
	agent, err := New(ctx, &Config{
		Name:      "coordinator",
		Model:     shared,
		SubAgents: []SubAgentConfig{{Name: "worker", Description: "Do work"}},
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	t.Cleanup(func() { _ = agent.Close() })
	result, err := agent.Ask(ctx, "delegate")
	if err != nil || result.Text != "child result" || len(shared.Calls()) != 3 {
		t.Fatalf("Ask() = %#v, %v; model calls %d", result, err, len(shared.Calls()))
	}
}

func TestSubAgentCanExplicitlyReceiveParentHistory(t *testing.T) {
	ctx := context.Background()
	childModel := NewMockChatModel(MockModelText("child result"))
	agent, err := New(ctx, &Config{
		Name:    "coordinator",
		Model:   NewMockChatModel(MockModelToolCallWithID("delegate", "worker", `{"request":"do it"}`), MockModelTextAfterToolResult("delegate")),
		History: []*schema.Message{schema.UserMessage("shared background")},
		SubAgents: []SubAgentConfig{{
			Name:           "worker",
			Description:    "Do work with shared context",
			Model:          childModel,
			IncludeHistory: true,
		}},
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	t.Cleanup(func() { _ = agent.Close() })
	if _, err := agent.Ask(ctx, "current request"); err != nil {
		t.Fatalf("Ask() error = %v", err)
	}
	call, ok := childModel.LastCall()
	if !ok {
		t.Fatal("child model was not called")
	}
	var input strings.Builder
	for _, message := range call.Input {
		if message != nil {
			input.WriteString(message.Content)
		}
	}
	if !strings.Contains(input.String(), "shared background") || !strings.Contains(input.String(), "current request") {
		t.Fatalf("child input = %q, want explicit parent history", input.String())
	}
}

func TestSubAgentPreparationRepairsMissingDelegationID(t *testing.T) {
	parent := &Agent{name: "coordinator", emtr: newEmitter()}
	runtime := newSubAgentRuntime(parent, parent.name, []SubAgentConfig{{Name: "worker"}}, nil)
	calls := []schema.ToolCall{{Function: schema.FunctionCall{Name: "worker", Arguments: `{"request":"work"}`}}}
	infos := runtime.prepare(calls)
	if len(infos) != 1 || calls[0].ID == "" || infos[0].ID != calls[0].ID {
		t.Fatalf("prepare() = calls %#v, infos %#v", calls, infos)
	}
}

func TestSubAgentNestedToolIDsAreIsolatedByAgent(t *testing.T) {
	parent := &Agent{name: "coordinator", emtr: newEmitter()}
	runtime := newSubAgentRuntime(parent, parent.name, []SubAgentConfig{{Name: "one"}, {Name: "two"}}, nil)
	runtime.recordNestedTools("one", []schema.ToolCall{{ID: "same", Function: schema.FunctionCall{Name: "first-tool", Arguments: `{"one":1}`}}})
	runtime.recordNestedTools("two", []schema.ToolCall{{ID: "same", Function: schema.FunctionCall{Name: "second-tool", Arguments: `{"two":2}`}}})
	firstName, firstArgs := runtime.nestedToolInfo("one", "same")
	secondName, secondArgs := runtime.nestedToolInfo("two", "same")
	if firstName != "first-tool" || firstArgs != `{"one":1}` || secondName != "second-tool" || secondArgs != `{"two":2}` {
		t.Fatalf("nested tool info = (%q, %q), (%q, %q)", firstName, firstArgs, secondName, secondArgs)
	}
}

func TestSubAgentConfigurationValidation(t *testing.T) {
	ctx := context.Background()
	tests := []struct {
		name   string
		config Config
	}{
		{name: "missing description", config: Config{Model: NewMockChatModel(), SubAgents: []SubAgentConfig{{Name: "worker"}}}},
		{name: "duplicate", config: Config{Model: NewMockChatModel(), SubAgents: []SubAgentConfig{{Name: "worker", Description: "one"}, {Name: "worker", Description: "two"}}}},
		{name: "parent conflict", config: Config{Name: "same", Model: NewMockChatModel(), SubAgents: []SubAgentConfig{{Name: "same", Description: "same"}}}},
		{name: "negative iterations", config: Config{Model: NewMockChatModel(), SubAgents: []SubAgentConfig{{Name: "worker", Description: "work", MaxIterations: -1}}}},
		{name: "negative policy", config: Config{Model: NewMockChatModel(), SubAgentPolicy: &SubAgentPolicy{MaxParallel: -1}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := New(ctx, &test.config); err == nil {
				t.Fatal("New() error = nil")
			}
		})
	}
}

func TestSubAgentCancellationReleasesRun(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	started := make(chan struct{})
	var calls atomic.Int32
	blocking := &modelGuardStub{
		generate: func(ctx context.Context, _ []*schema.Message, _ ...ModelOption) (*schema.Message, error) {
			if calls.Add(1) == 1 {
				close(started)
			}
			<-ctx.Done()
			return nil, ctx.Err()
		},
		stream: func(ctx context.Context, _ []*schema.Message, _ ...ModelOption) (*schema.StreamReader[*schema.Message], error) {
			if calls.Add(1) == 1 {
				close(started)
			}
			<-ctx.Done()
			return nil, ctx.Err()
		},
	}
	agent, err := New(context.Background(), &Config{
		Name:      "coordinator",
		Model:     NewMockChatModel(MockModelToolCallWithID("blocking-call", "worker", `{"request":"wait"}`)),
		SubAgents: []SubAgentConfig{{Name: "worker", Description: "Wait", Model: blocking}},
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	t.Cleanup(func() { _ = agent.Close() })
	done := make(chan error, 1)
	go func() {
		_, runErr := agent.Ask(ctx, "wait")
		done <- runErr
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("child model did not start")
	}
	cancel()
	select {
	case runErr := <-done:
		if !errors.Is(runErr, context.Canceled) {
			t.Fatalf("Ask() error = %v, want canceled", runErr)
		}
	case <-time.After(time.Second):
		t.Fatal("Ask() did not stop after cancellation")
	}
}

func TestSubAgentHITLRestoresAcrossAgentRecreation(t *testing.T) {
	ctx := context.Background()
	store := NewMemorySessionStore()
	const (
		sessionID  = "sub-agent-hitl"
		delegateID = "delegate-approval"
		approvalID = "inner-approval"
	)
	newAgent := func(parentModel, childModel ChatModel) (*Agent, error) {
		return New(ctx, &Config{
			Name:  "coordinator",
			Model: parentModel,
			Session: &SessionConfig{
				ID:    sessionID,
				Store: store,
			},
			SubAgents: []SubAgentConfig{{
				Name:        "operator",
				Description: "Perform an approved operation",
				Model:       childModel,
				Tools:       []Tool{newCheckpointApprovalTool(t)},
			}},
		})
	}

	first, err := newAgent(
		NewMockChatModel(MockModelToolCallWithID(delegateID, "operator", `{"request":"clean up"}`)),
		NewMockChatModel(MockModelToolCallWithID(approvalID, "approve_action", `{"action":"cleanup"}`)),
	)
	if err != nil {
		t.Fatalf("first New() error = %v", err)
	}
	result, err := first.Ask(ctx, "delegate cleanup")
	if err != nil {
		t.Fatalf("first Ask() error = %v", err)
	}
	if result == nil || !result.IsInterrupted() || len(result.Interrupts) != 1 {
		t.Fatalf("first result = %#v, want one interrupt", result)
	}
	interruptID := result.Interrupts[0].ID
	if result.Interrupts[0].Info != "approve cleanup" {
		t.Fatalf("interrupt = %#v", result.Interrupts[0])
	}
	if err := first.Close(); err != nil {
		t.Fatalf("first Close() error = %v", err)
	}

	second, err := newAgent(
		NewMockChatModel(MockModelTextAfterToolResult(delegateID)),
		NewMockChatModel(MockModelTextAfterToolResult(approvalID)),
	)
	if err != nil {
		t.Fatalf("second New() error = %v", err)
	}
	t.Cleanup(func() { _ = second.Close() })
	recorder := newMockEventRecorder()
	second.Subscribe(recorder.Record)
	resumed, err := second.ResumeWithResult(ctx, map[string]any{interruptID: true})
	if err != nil {
		t.Fatalf("ResumeWithResult() error = %v", err)
	}
	if resumed == nil || resumed.Text != "approved" || resumed.IsInterrupted() {
		t.Fatalf("resumed result = %#v", resumed)
	}
	start, end := recorder.Last(EventDelegationStart), recorder.Last(EventDelegationEnd)
	if start == nil || end == nil || start.Delegation == nil || end.Delegation == nil {
		t.Fatalf("resumed delegation events = start %#v, end %#v", start, end)
	}
	if start.Delegation.ID != delegateID || end.Delegation.ID != delegateID || end.Error != nil {
		t.Fatalf("resumed delegation lifecycle = start %#v, end %#v", start, end)
	}
}

func TestSubAgentWorksInsideGoalRunner(t *testing.T) {
	ctx := context.Background()
	childModel := NewMockChatModel(MockModelText("verified evidence"))
	agent, err := New(ctx, &Config{
		Name: "coordinator",
		Model: NewMockChatModel(
			MockModelToolCallWithID("goal-delegation", "researcher", `{"request":"collect evidence"}`),
			MockModelTextAfterToolResult("goal-delegation"),
		),
		Session: &SessionConfig{ID: "sub-agent-goal", Store: NewMemorySessionStore()},
		SubAgents: []SubAgentConfig{{
			Name:        "researcher",
			Description: "Collect evidence",
			Model:       childModel,
		}},
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	t.Cleanup(func() { _ = agent.Close() })
	runner, err := NewGoalRunner(agent, &GoalRunnerConfig{
		Evaluator: GoalEvaluatorFunc(func(_ context.Context, evaluation GoalEvaluation) (GoalDecision, error) {
			return GoalDecision{Complete: evaluation.LastResponse == "verified evidence", Reason: "evidence checked"}, nil
		}),
	})
	if err != nil {
		t.Fatalf("NewGoalRunner() error = %v", err)
	}
	result, err := runner.Start(ctx, GoalRequest{ID: "research-goal", Objective: "research"})
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	if result == nil || result.Goal.Status != GoalStatusCompleted || result.LastRun.Text != "verified evidence" {
		t.Fatalf("goal result = %#v", result)
	}
	if got := len(childModel.Calls()); got != 1 {
		t.Fatalf("child calls = %d, want 1", got)
	}
}
