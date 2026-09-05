package agentkit

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/cloudwego/eino/schema"
)

func TestGoalRunnerCompletesAfterMultipleIterations(t *testing.T) {
	ctx := context.Background()
	sessions := NewMemorySessionStore()
	model := NewMockChatModel(
		MockModelText("implemented the first half"),
		MockModelText(`{"complete":false,"reason":"verification is missing","next_prompt":"run verification"}`),
		MockModelText("verification passed"),
		MockModelText(`{"complete":true,"reason":"the implementation and verification are complete","next_prompt":""}`),
	)
	agent, err := New(ctx, &Config{
		Name: "worker", Model: model,
		Session: &SessionConfig{ID: "goal-session", Store: sessions},
	})
	if err != nil {
		t.Fatalf("create agent: %v", err)
	}
	t.Cleanup(func() { _ = agent.Close() })
	runner, err := NewGoalRunner(agent, nil)
	if err != nil {
		t.Fatalf("create goal runner: %v", err)
	}

	result, err := runner.Start(ctx, GoalRequest{
		ID: "release", Objective: "prepare a release",
		SuccessCriteria: "implementation is complete and verification passes",
	})
	if err != nil {
		t.Fatalf("run goal: %v", err)
	}
	if result.Goal.Status != GoalStatusCompleted || result.Goal.Iteration != 2 {
		t.Fatalf("unexpected completed goal: %#v", result.Goal)
	}
	if result.LastRun == nil || result.LastRun.Text != "verification passed" {
		t.Fatalf("unexpected last run: %#v", result.LastRun)
	}
	if calls := model.Calls(); len(calls) != 4 {
		t.Fatalf("expected two work calls and two evaluation calls, got %d", len(calls))
	}
	history := agent.History()
	if len(history) != 4 {
		t.Fatalf("evaluation calls must not enter agent history, got %d messages", len(history))
	}
	if !strings.Contains(history[2].Content, "run verification") {
		t.Fatalf("continuation prompt did not include evaluator action: %q", history[2].Content)
	}
}

func TestGoalRunnerResumesPendingEvaluationAcrossAgentInstances(t *testing.T) {
	ctx := context.Background()
	sessions := NewMemorySessionStore()
	firstModel := NewMockChatModel(
		MockModelText("all checks passed"),
		MockModelError(errors.New("evaluator unavailable")),
	)
	firstAgent, err := New(ctx, &Config{
		Name: "worker", Model: firstModel,
		Session: &SessionConfig{ID: "resume-session", Store: sessions},
	})
	if err != nil {
		t.Fatalf("create first agent: %v", err)
	}
	firstRunner, err := NewGoalRunner(firstAgent, nil)
	if err != nil {
		t.Fatalf("create first goal runner: %v", err)
	}
	result, err := firstRunner.Start(ctx, GoalRequest{ID: "resume", Objective: "finish checks"})
	if err == nil {
		t.Fatal("expected evaluator error")
	}
	if result.Goal.Iteration != 1 || !result.Goal.PendingEvaluation {
		t.Fatalf("expected durable pending evaluation: %#v", result.Goal)
	}
	_ = firstAgent.Close()

	secondModel := NewMockChatModel(
		MockModelText(`{"complete":true,"reason":"all checks passed","next_prompt":""}`),
	)
	secondAgent, err := New(ctx, &Config{
		Name: "worker", Model: secondModel,
		Session: &SessionConfig{ID: "resume-session", Store: sessions},
	})
	if err != nil {
		t.Fatalf("create restored agent: %v", err)
	}
	t.Cleanup(func() { _ = secondAgent.Close() })
	secondRunner, err := NewGoalRunner(secondAgent, nil)
	if err != nil {
		t.Fatalf("create restored goal runner: %v", err)
	}
	result, err = secondRunner.Resume(ctx, "resume")
	if err != nil {
		t.Fatalf("resume goal: %v", err)
	}
	if result.Goal.Status != GoalStatusCompleted || result.Goal.Iteration != 1 {
		t.Fatalf("unexpected restored goal: %#v", result.Goal)
	}
	if len(secondModel.Calls()) != 1 {
		t.Fatalf("pending evaluation should not repeat agent work, got %d calls", len(secondModel.Calls()))
	}
}

func TestGoalRunnerBlocksUncertainRecoveryUntilExplicitRetry(t *testing.T) {
	ctx := context.Background()
	sessions := NewMemorySessionStore()
	model := NewMockChatModel()
	agent, err := New(ctx, &Config{
		Name: "worker", Model: model,
		Session: &SessionConfig{ID: "uncertain-session", Store: sessions},
	})
	if err != nil {
		t.Fatalf("create agent: %v", err)
	}
	t.Cleanup(func() { _ = agent.Close() })
	store := sessions.GoalStore()
	if err := store.Save(ctx, &Goal{
		ID: "uncertain", SessionID: "uncertain-session", Objective: "deploy",
		Status: GoalStatusActive, MaxIterations: 3, InProgress: true,
		AttemptIteration: 1, PendingPrompt: "deploy",
	}); err != nil {
		t.Fatalf("save uncertain goal: %v", err)
	}
	runner, err := NewGoalRunner(agent, nil)
	if err != nil {
		t.Fatalf("create runner: %v", err)
	}

	result, err := runner.Resume(ctx, "uncertain")
	if !errors.Is(err, ErrGoalRecoveryRequired) {
		t.Fatalf("expected ErrGoalRecoveryRequired, got %v", err)
	}
	if result.Goal.Status != GoalStatusBlocked || !result.Goal.InProgress {
		t.Fatalf("unexpected blocked goal: %#v", result.Goal)
	}
	if len(model.Calls()) != 0 {
		t.Fatal("uncertain recovery must not call the model automatically")
	}

	model.AddResponses(MockModelText("deployed"))
	runner.evaluator = GoalEvaluatorFunc(func(context.Context, GoalEvaluation) (GoalDecision, error) {
		return GoalDecision{Complete: true, Reason: "deployment verified"}, nil
	})
	result, err = runner.Retry(ctx, "uncertain")
	if err != nil {
		t.Fatalf("retry goal: %v", err)
	}
	if result.Goal.Status != GoalStatusCompleted {
		t.Fatalf("expected completed goal after explicit retry: %#v", result.Goal)
	}
}

func TestGoalRunnerRecoversSavedAssistantResultWithoutRepeatingWork(t *testing.T) {
	ctx := context.Background()
	sessions := NewMemorySessionStore()
	if err := sessions.Save(ctx, &Session{
		ID: "saved-session",
		Messages: []*schema.Message{
			schema.UserMessage("perform work"),
			schema.AssistantMessage("work is complete", nil),
		},
	}); err != nil {
		t.Fatalf("save session: %v", err)
	}
	if err := sessions.GoalStore().Save(ctx, &Goal{
		ID: "saved", SessionID: "saved-session", Objective: "perform work",
		Status: GoalStatusActive, MaxIterations: 3, InProgress: true,
		AttemptIteration: 1, HistoryMessageCount: 0, PendingPrompt: "perform work",
	}); err != nil {
		t.Fatalf("save goal: %v", err)
	}
	model := NewMockChatModel()
	agent, err := New(ctx, &Config{
		Name: "worker", Model: model,
		Session: &SessionConfig{ID: "saved-session", Store: sessions},
	})
	if err != nil {
		t.Fatalf("create agent: %v", err)
	}
	t.Cleanup(func() { _ = agent.Close() })
	runner, err := NewGoalRunner(agent, &GoalRunnerConfig{
		Evaluator: GoalEvaluatorFunc(func(_ context.Context, evaluation GoalEvaluation) (GoalDecision, error) {
			if evaluation.LastResponse != "work is complete" {
				t.Fatalf("unexpected recovered response: %q", evaluation.LastResponse)
			}
			return GoalDecision{Complete: true, Reason: "work is complete"}, nil
		}),
	})
	if err != nil {
		t.Fatalf("create runner: %v", err)
	}

	result, err := runner.Resume(ctx, "saved")
	if err != nil {
		t.Fatalf("resume saved result: %v", err)
	}
	if result.Goal.Status != GoalStatusCompleted || result.Goal.Iteration != 1 {
		t.Fatalf("unexpected recovered goal: %#v", result.Goal)
	}
	if len(model.Calls()) != 0 {
		t.Fatal("saved assistant result must not repeat model work")
	}
}

func TestGoalRunnerStopsAtIterationLimit(t *testing.T) {
	ctx := context.Background()
	sessions := NewMemorySessionStore()
	model := NewMockChatModel(MockModelText("one"), MockModelText("two"))
	agent, err := New(ctx, &Config{
		Name: "worker", Model: model,
		Session: &SessionConfig{ID: "limit-session", Store: sessions},
	})
	if err != nil {
		t.Fatalf("create agent: %v", err)
	}
	t.Cleanup(func() { _ = agent.Close() })
	runner, err := NewGoalRunner(agent, &GoalRunnerConfig{
		Evaluator: GoalEvaluatorFunc(func(_ context.Context, evaluation GoalEvaluation) (GoalDecision, error) {
			return GoalDecision{Complete: false, Reason: "not yet", NextPrompt: "continue"}, nil
		}),
	})
	if err != nil {
		t.Fatalf("create runner: %v", err)
	}
	result, err := runner.Start(ctx, GoalRequest{
		ID: "limited", Objective: "never finish", MaxIterations: 2,
	})
	if !errors.Is(err, ErrGoalBlocked) {
		t.Fatalf("expected ErrGoalBlocked, got %v", err)
	}
	if result.Goal.Status != GoalStatusBlocked || result.Goal.Iteration != 2 {
		t.Fatalf("unexpected limited goal: %#v", result.Goal)
	}
	if len(model.Calls()) != 2 {
		t.Fatalf("expected exactly two work calls, got %d", len(model.Calls()))
	}
}

func TestGoalRunnerResumesHITLWithoutStartingANewIteration(t *testing.T) {
	ctx := context.Background()
	sessions := NewMemorySessionStore()
	const callID = "goal-approval"
	model := NewMockChatModel(
		MockModelToolCallWithID(callID, "approve_action", `{"action":"release"}`),
		MockModelTextAfterToolResult(callID),
		MockModelText(`{"complete":true,"reason":"release was approved","next_prompt":""}`),
	)
	agent, err := New(ctx, &Config{
		Name: "worker", Model: model,
		Tools:   []Tool{newCheckpointApprovalTool(t)},
		Session: &SessionConfig{ID: "hitl-goal-session", Store: sessions},
	})
	if err != nil {
		t.Fatalf("create agent: %v", err)
	}
	t.Cleanup(func() { _ = agent.Close() })
	runner, err := NewGoalRunner(agent, nil)
	if err != nil {
		t.Fatalf("create runner: %v", err)
	}

	result, err := runner.Start(ctx, GoalRequest{ID: "hitl", Objective: "release"})
	if err != nil {
		t.Fatalf("start interrupted goal: %v", err)
	}
	if result.Goal.Status != GoalStatusPaused || !result.Goal.AwaitingInterrupt || result.Goal.Iteration != 1 {
		t.Fatalf("unexpected interrupted goal: %#v", result.Goal)
	}
	pending := agent.PendingInterrupts()
	if len(pending) != 1 {
		t.Fatalf("expected one pending interrupt, got %#v", pending)
	}
	if _, err := runner.Resume(ctx, "hitl"); !errors.Is(err, ErrGoalInterruptRequired) {
		t.Fatalf("expected ErrGoalInterruptRequired, got %v", err)
	}

	result, err = runner.ResumeInterrupt(ctx, "hitl", map[string]any{pending[0].ID: true})
	if err != nil {
		t.Fatalf("resume interrupt: %v", err)
	}
	if result.Goal.Status != GoalStatusCompleted || result.Goal.Iteration != 1 {
		t.Fatalf("unexpected completed goal: %#v", result.Goal)
	}
	if result.LastRun == nil || result.LastRun.Text != "approved" {
		t.Fatalf("unexpected resumed result: %#v", result.LastRun)
	}
}

func TestModelGoalEvaluatorParsesFencedJSON(t *testing.T) {
	ctx := context.Background()
	model := NewMockChatModel(MockModelText("preface\n```json\n{\"complete\":true,\"reason\":\"verified\",\"next_prompt\":\"ignored\"}\n```"))
	evaluator, err := NewModelGoalEvaluator(model)
	if err != nil {
		t.Fatalf("create evaluator: %v", err)
	}
	decision, err := evaluator.Evaluate(ctx, GoalEvaluation{Objective: "test", LastResponse: "done"})
	if err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if !decision.Complete || decision.Reason != "verified" || decision.NextPrompt != "" {
		t.Fatalf("unexpected decision: %#v", decision)
	}
}

func TestNewGoalRunnerRequiresSession(t *testing.T) {
	ctx := context.Background()
	agent, err := New(ctx, &Config{Name: "worker", Model: NewMockChatModel()})
	if err != nil {
		t.Fatalf("create agent: %v", err)
	}
	t.Cleanup(func() { _ = agent.Close() })
	if _, err := NewGoalRunner(agent, nil); !errors.Is(err, ErrSessionDisabled) {
		t.Fatalf("expected ErrSessionDisabled, got %v", err)
	}
}
