package agentkit

import (
	"context"
	"testing"
)

func TestGoalRunnerExposesStableOperationKeyToTools(t *testing.T) {
	ctx := context.Background()
	var captured GoalRunInfo
	var operationKey string
	tool := MustMockTool("publish", "publish release", func(ctx context.Context, _ string) (string, error) {
		var ok bool
		captured, ok = CurrentGoalRun(ctx)
		if !ok {
			t.Fatal("tool did not receive goal run context")
		}
		operationKey, ok = GoalOperationKey(ctx, "publish-release")
		if !ok {
			t.Fatal("tool did not receive a goal operation key")
		}
		return "published", nil
	})
	agent, err := New(ctx, &Config{
		Name: "publisher",
		Model: NewMockChatModel(
			MockModelToolCallWithID("publish-call", "publish", `""`),
			MockModelTextAfterToolResult("publish-call"),
		),
		Tools:   MockTools(tool),
		Session: &SessionConfig{ID: "release-session", Store: NewMemorySessionStore()},
	})
	if err != nil {
		t.Fatalf("create agent: %v", err)
	}
	defer agent.Close()
	runner, err := NewGoalRunner(agent, &GoalRunnerConfig{
		Evaluator: GoalEvaluatorFunc(func(context.Context, GoalEvaluation) (GoalDecision, error) {
			return GoalDecision{Complete: true, Reason: "published"}, nil
		}),
	})
	if err != nil {
		t.Fatalf("create goal runner: %v", err)
	}
	if _, err := runner.Start(ctx, GoalRequest{ID: "release", Objective: "publish"}); err != nil {
		t.Fatalf("start goal: %v", err)
	}
	if captured != (GoalRunInfo{GoalID: "release", SessionID: "release-session", Attempt: 1}) {
		t.Fatalf("goal run info = %#v", captured)
	}
	keyAgain, ok := GoalOperationKey(withGoalRunContext(ctx, &Goal{
		ID: "release", SessionID: "release-session", AttemptIteration: 1,
	}), "publish-release")
	if !ok || keyAgain != operationKey {
		t.Fatalf("operation key changed: first %q, second %q", operationKey, keyAgain)
	}
}

func TestGoalOperationKeyRequiresGoalAndOperation(t *testing.T) {
	if key, ok := GoalOperationKey(context.Background(), "publish"); ok || key != "" {
		t.Fatalf("non-goal key = %q, %v", key, ok)
	}
	ctx := withGoalRunContext(context.Background(), &Goal{
		ID: "goal", SessionID: "session", AttemptIteration: 1,
	})
	if key, ok := GoalOperationKey(ctx, " "); ok || key != "" {
		t.Fatalf("empty-operation key = %q, %v", key, ok)
	}
}

func TestGoalRetryReusesAttemptOperationKey(t *testing.T) {
	ctx := context.Background()
	sessions := NewMemorySessionStore()
	var retryKey string
	tool := MustMockTool("retry_publish", "retry publish", func(ctx context.Context, _ string) (string, error) {
		var ok bool
		retryKey, ok = GoalOperationKey(ctx, "publish-release")
		if !ok {
			t.Fatal("retry tool did not receive operation key")
		}
		return "published", nil
	})
	agent, err := New(ctx, &Config{
		Name: "publisher",
		Model: NewMockChatModel(
			MockModelToolCallWithID("retry-call", "retry_publish", `""`),
			MockModelTextAfterToolResult("retry-call"),
		),
		Tools:   MockTools(tool),
		Session: &SessionConfig{ID: "release-session", Store: sessions},
	})
	if err != nil {
		t.Fatalf("create agent: %v", err)
	}
	defer agent.Close()
	runner, err := NewGoalRunner(agent, &GoalRunnerConfig{
		Evaluator: GoalEvaluatorFunc(func(context.Context, GoalEvaluation) (GoalDecision, error) {
			return GoalDecision{Complete: true, Reason: "published"}, nil
		}),
	})
	if err != nil {
		t.Fatalf("create goal runner: %v", err)
	}
	goal := &Goal{
		ID: "release", SessionID: "release-session", Objective: "publish",
		Status: GoalStatusBlocked, MaxIterations: 2, InProgress: true, AttemptIteration: 1,
	}
	if err := sessions.GoalStore().Save(ctx, goal); err != nil {
		t.Fatalf("save uncertain goal: %v", err)
	}
	if _, err := runner.Retry(ctx, goal.ID); err != nil {
		t.Fatalf("retry goal: %v", err)
	}
	expected, ok := GoalOperationKey(withGoalRunContext(ctx, goal), "publish-release")
	if !ok || retryKey != expected {
		t.Fatalf("retry key = %q, want %q", retryKey, expected)
	}
}
