package agentkit

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestGoalRunnerStartAsyncPersistsBeforeReturningAndWaits(t *testing.T) {
	ctx := context.Background()
	started := make(chan struct{})
	release := make(chan struct{})
	tool := MustMockTool("wait", "wait for release", func(ctx context.Context, _ string) (string, error) {
		close(started)
		select {
		case <-release:
			return "released", nil
		case <-ctx.Done():
			return "", ctx.Err()
		}
	})
	agent, err := New(ctx, &Config{
		Name: "worker",
		Model: NewMockChatModel(
			MockModelToolCallWithID("wait-call", "wait", `""`),
			MockModelTextAfterToolResult("wait-call"),
		),
		Tools:   MockTools(tool),
		Session: &SessionConfig{ID: "async-goal", Store: NewMemorySessionStore()},
	})
	if err != nil {
		t.Fatalf("create agent: %v", err)
	}
	t.Cleanup(func() { _ = agent.Close() })
	runner, err := NewGoalRunner(agent, &GoalRunnerConfig{
		Evaluator: GoalEvaluatorFunc(func(context.Context, GoalEvaluation) (GoalDecision, error) {
			return GoalDecision{Complete: true, Reason: "verified"}, nil
		}),
	})
	if err != nil {
		t.Fatalf("create runner: %v", err)
	}

	run, err := runner.StartAsync(ctx, GoalRequest{ID: "async", Objective: "finish work"})
	if err != nil {
		t.Fatalf("StartAsync() error = %v", err)
	}
	if run.ID() != "async" {
		t.Fatalf("GoalRun.ID() = %q, want async", run.ID())
	}
	if saved, err := runner.Get(ctx, run.ID()); err != nil || saved.ID != "async" {
		t.Fatalf("Get() = %#v, %v, want persisted goal", saved, err)
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("background goal did not start")
	}

	waitCtx, cancelWait := context.WithTimeout(ctx, 20*time.Millisecond)
	result, err := run.WaitContext(waitCtx)
	cancelWait()
	if result != nil || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("WaitContext() = %#v, %v, want deadline", result, err)
	}
	close(release)
	result, err = run.Wait()
	if err != nil || result == nil || result.Goal.Status != GoalStatusCompleted || result.LastRun.Text != "released" {
		t.Fatalf("Wait() = %#v, %v, want completed", result, err)
	}
	result.Goal.Objective = "changed"
	again, err := run.Wait()
	if err != nil || again.Goal.Objective != "finish work" {
		t.Fatalf("second Wait() = %#v, %v, want isolated result", again, err)
	}
}

func TestGoalRunPausePersistsAndCancelsBackgroundWork(t *testing.T) {
	ctx := context.Background()
	started := make(chan struct{})
	tool := MustMockTool("wait", "wait for cancellation", func(ctx context.Context, _ string) (string, error) {
		close(started)
		<-ctx.Done()
		return "", ctx.Err()
	})
	agent, err := New(ctx, &Config{
		Name:    "worker",
		Model:   NewMockChatModel(MockModelToolCallWithID("wait-call", "wait", `""`)),
		Tools:   MockTools(tool),
		Session: &SessionConfig{ID: "pause-async-goal", Store: NewMemorySessionStore()},
	})
	if err != nil {
		t.Fatalf("create agent: %v", err)
	}
	t.Cleanup(func() { _ = agent.Close() })
	runner, err := NewGoalRunner(agent, &GoalRunnerConfig{
		Evaluator: GoalEvaluatorFunc(func(context.Context, GoalEvaluation) (GoalDecision, error) {
			return GoalDecision{Complete: true, Reason: "unused"}, nil
		}),
	})
	if err != nil {
		t.Fatalf("create runner: %v", err)
	}
	run, err := runner.StartAsync(ctx, GoalRequest{ID: "pause", Objective: "wait"})
	if err != nil {
		t.Fatalf("StartAsync() error = %v", err)
	}
	<-started
	if err := run.Pause(ctx); err != nil {
		t.Fatalf("Pause() error = %v", err)
	}
	if _, err := run.Wait(); !errors.Is(err, context.Canceled) {
		t.Fatalf("Wait() error = %v, want context.Canceled", err)
	}
	saved, err := runner.Get(ctx, "pause")
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if saved.Status != GoalStatusPaused || saved.LastReason != "paused by caller" {
		t.Fatalf("saved goal = %#v, want paused", saved)
	}
}

func TestGoalRunnerResumePendingAsyncContinuesSavedEvaluation(t *testing.T) {
	ctx := context.Background()
	evaluationErr := errors.New("evaluation temporarily unavailable")
	model := NewMockChatModel(MockModelText("work completed"))
	agent, err := New(ctx, &Config{
		Name:    "worker",
		Model:   model,
		Session: &SessionConfig{ID: "resume-async", Store: NewMemorySessionStore()},
	})
	if err != nil {
		t.Fatalf("create agent: %v", err)
	}
	t.Cleanup(func() { _ = agent.Close() })
	evaluator := GoalEvaluatorFunc(func(context.Context, GoalEvaluation) (GoalDecision, error) {
		return GoalDecision{}, evaluationErr
	})
	runner, err := NewGoalRunner(agent, &GoalRunnerConfig{Evaluator: evaluator})
	if err != nil {
		t.Fatalf("create runner: %v", err)
	}
	if _, err := runner.Start(ctx, GoalRequest{ID: "resume", Objective: "finish work"}); !errors.Is(err, evaluationErr) {
		t.Fatalf("Start() error = %v, want evaluation error", err)
	}
	runner.evaluator = GoalEvaluatorFunc(func(context.Context, GoalEvaluation) (GoalDecision, error) {
		return GoalDecision{Complete: true, Reason: "verified"}, nil
	})

	run, err := runner.ResumePendingAsync(ctx)
	if err != nil {
		t.Fatalf("ResumePendingAsync() error = %v", err)
	}
	if run.ID() != "resume" {
		t.Fatalf("GoalRun.ID() = %q, want resume", run.ID())
	}
	result, err := run.Wait()
	if err != nil || result.Goal.Status != GoalStatusCompleted {
		t.Fatalf("Wait() = %#v, %v, want completed", result, err)
	}
	if got := len(model.Calls()); got != 1 {
		t.Fatalf("model calls = %d, want saved work not to repeat", got)
	}
}
