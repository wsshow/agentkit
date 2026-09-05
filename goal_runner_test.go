package agentkit

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cloudwego/eino/adk"
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

func TestGoalRunnerExclusivelyOwnsAgentBetweenSteps(t *testing.T) {
	ctx := context.Background()
	evaluationStarted := make(chan struct{})
	releaseEvaluation := make(chan struct{})
	model := NewMockChatModel(
		MockModelText("goal work finished"),
		MockModelText("ordinary work after goal"),
	)
	agent, err := New(ctx, &Config{
		Name: "worker", Model: model,
		Session: &SessionConfig{ID: "exclusive-goal-session", Store: NewMemorySessionStore()},
	})
	if err != nil {
		t.Fatalf("create agent: %v", err)
	}
	t.Cleanup(func() { _ = agent.Close() })
	runner, err := NewGoalRunner(agent, &GoalRunnerConfig{
		Evaluator: GoalEvaluatorFunc(func(ctx context.Context, _ GoalEvaluation) (GoalDecision, error) {
			close(evaluationStarted)
			select {
			case <-releaseEvaluation:
				return GoalDecision{Complete: true, Reason: "verified"}, nil
			case <-ctx.Done():
				return GoalDecision{}, ctx.Err()
			}
		}),
	})
	if err != nil {
		t.Fatalf("create goal runner: %v", err)
	}

	type outcome struct {
		result *GoalRunResult
		err    error
	}
	finished := make(chan outcome, 1)
	go func() {
		result, runErr := runner.Start(ctx, GoalRequest{ID: "exclusive", Objective: "finish goal work"})
		finished <- outcome{result: result, err: runErr}
	}()
	waitForTestSignal(t, evaluationStarted, "goal evaluation")
	if _, err := agent.Ask(ctx, "must not enter the goal conversation"); !errors.Is(err, ErrAgentRunning) {
		close(releaseEvaluation)
		t.Fatalf("Ask() during goal evaluation error = %v, want ErrAgentRunning", err)
	}
	close(releaseEvaluation)
	select {
	case got := <-finished:
		if got.err != nil || got.result == nil || got.result.Goal.Status != GoalStatusCompleted {
			t.Fatalf("goal outcome = %#v, %v", got.result, got.err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("goal did not finish")
	}
	result, err := agent.Ask(ctx, "ordinary work is allowed now")
	if err != nil || result.Text != "ordinary work after goal" {
		t.Fatalf("Ask() after goal = %#v, %v", result, err)
	}
}

func TestAgentAbortStopsGoalDuringEvaluation(t *testing.T) {
	ctx := context.Background()
	evaluationStarted := make(chan struct{})
	model := NewMockChatModel(
		MockModelText("goal work finished"),
		MockModelText("ordinary work after abort"),
	)
	agent, err := New(ctx, &Config{
		Name: "worker", Model: model,
		Session: &SessionConfig{ID: "abort-goal-session", Store: NewMemorySessionStore()},
	})
	if err != nil {
		t.Fatalf("create agent: %v", err)
	}
	t.Cleanup(func() { _ = agent.Close() })
	runner, err := NewGoalRunner(agent, &GoalRunnerConfig{
		Evaluator: GoalEvaluatorFunc(func(ctx context.Context, _ GoalEvaluation) (GoalDecision, error) {
			close(evaluationStarted)
			<-ctx.Done()
			return GoalDecision{}, ctx.Err()
		}),
	})
	if err != nil {
		t.Fatalf("create goal runner: %v", err)
	}

	finished := make(chan error, 1)
	go func() {
		_, runErr := runner.Start(ctx, GoalRequest{ID: "abort-evaluation", Objective: "finish goal work"})
		finished <- runErr
	}()
	waitForTestSignal(t, evaluationStarted, "goal evaluation")
	abortCtx, cancelAbort := context.WithTimeout(ctx, 2*time.Second)
	defer cancelAbort()
	if err := agent.AbortContext(abortCtx); err != nil {
		t.Fatalf("AbortContext() error = %v", err)
	}
	select {
	case err := <-finished:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("goal error = %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("goal did not stop after Agent abort")
	}
	result, err := agent.Ask(ctx, "ordinary work is allowed after abort")
	if err != nil || result.Text != "ordinary work after abort" {
		t.Fatalf("Ask() after abort = %#v, %v", result, err)
	}
}

func TestAgentCloseStopsGoalDuringEvaluation(t *testing.T) {
	ctx := context.Background()
	evaluationStarted := make(chan struct{})
	agent, err := New(ctx, &Config{
		Name: "worker", Model: NewMockChatModel(MockModelText("goal work finished")),
		Session: &SessionConfig{ID: "close-goal-session", Store: NewMemorySessionStore()},
	})
	if err != nil {
		t.Fatalf("create agent: %v", err)
	}
	runner, err := NewGoalRunner(agent, &GoalRunnerConfig{
		Evaluator: GoalEvaluatorFunc(func(ctx context.Context, _ GoalEvaluation) (GoalDecision, error) {
			close(evaluationStarted)
			<-ctx.Done()
			return GoalDecision{}, ctx.Err()
		}),
	})
	if err != nil {
		t.Fatalf("create goal runner: %v", err)
	}

	finished := make(chan error, 1)
	go func() {
		_, runErr := runner.Start(ctx, GoalRequest{ID: "close-evaluation", Objective: "finish goal work"})
		finished <- runErr
	}()
	waitForTestSignal(t, evaluationStarted, "goal evaluation")
	closeCtx, cancelClose := context.WithTimeout(ctx, 2*time.Second)
	defer cancelClose()
	if err := agent.CloseContext(closeCtx); err != nil {
		t.Fatalf("CloseContext() error = %v", err)
	}
	select {
	case err := <-finished:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("goal error = %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("goal did not stop after Agent close")
	}
}

func TestGoalRunnerEmitsPersistedGoalUpdates(t *testing.T) {
	ctx := context.Background()
	sessions := NewMemorySessionStore()
	model := NewMockChatModel(
		MockModelText("done"),
		MockModelText(`{"complete":true,"reason":"verified","next_prompt":""}`),
	)
	agent, err := New(ctx, &Config{
		Name: "worker", Model: model,
		Session: &SessionConfig{ID: "event-session", Store: sessions},
	})
	if err != nil {
		t.Fatalf("create agent: %v", err)
	}
	t.Cleanup(func() { _ = agent.Close() })
	var updates []*Goal
	agent.Subscribe(func(event Event) {
		if event.Type == EventGoalUpdate {
			updates = append(updates, event.Goal)
		}
	})
	runner, err := NewGoalRunner(agent, nil)
	if err != nil {
		t.Fatalf("create runner: %v", err)
	}

	result, err := runner.Start(ctx, GoalRequest{ID: "observed", Objective: "finish"})
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	if len(updates) != 4 {
		t.Fatalf("goal updates = %d, want 4", len(updates))
	}
	for index, update := range updates {
		wantRevision := uint64(index + 1)
		if update == nil || update.Revision != wantRevision {
			t.Fatalf("goal update %d = %#v, want revision %d", index, update, wantRevision)
		}
		if update.CreatedAt.IsZero() || update.UpdatedAt.Before(update.CreatedAt) {
			t.Fatalf("goal update %d timestamps = %#v", index, update)
		}
	}
	last := updates[len(updates)-1]
	if last.Status != GoalStatusCompleted || result.Goal.Revision != last.Revision {
		t.Fatalf("last goal update = %#v, result = %#v", last, result.Goal)
	}
	persisted, err := sessions.GoalStore().Load(ctx, "observed")
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if persisted.Revision != last.Revision || persisted.Status != last.Status {
		t.Fatalf("persisted goal = %#v, last update = %#v", persisted, last)
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

func TestGoalRunnerResumesPendingEvaluationAcrossFileStoreRestart(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	firstSessions, err := NewFileSessionStore(dir)
	if err != nil {
		t.Fatalf("create first file session store: %v", err)
	}
	firstModel := NewMockChatModel(
		MockModelText("all checks passed"),
		MockModelError(errors.New("evaluator unavailable")),
	)
	firstAgent, err := New(ctx, &Config{
		Name: "worker", Model: firstModel,
		Session: &SessionConfig{ID: "file-restart-session", Store: firstSessions},
	})
	if err != nil {
		t.Fatalf("create first agent: %v", err)
	}
	firstRunner, err := NewGoalRunner(firstAgent, nil)
	if err != nil {
		_ = firstAgent.Close()
		t.Fatalf("create first goal runner: %v", err)
	}
	result, err := firstRunner.Start(ctx, GoalRequest{ID: "file-restart", Objective: "finish checks"})
	if err == nil {
		_ = firstAgent.Close()
		t.Fatal("expected evaluator error")
	}
	if result == nil || result.Goal == nil || !result.Goal.PendingEvaluation {
		_ = firstAgent.Close()
		t.Fatalf("expected persisted pending evaluation: %#v", result)
	}
	if err := firstAgent.Close(); err != nil {
		t.Fatalf("close first agent: %v", err)
	}

	secondSessions, err := NewFileSessionStore(dir)
	if err != nil {
		t.Fatalf("reopen file session store: %v", err)
	}
	secondModel := NewMockChatModel(
		MockModelText(`{"complete":true,"reason":"all checks passed","next_prompt":""}`),
	)
	secondAgent, err := New(ctx, &Config{
		Name: "worker", Model: secondModel,
		Session: &SessionConfig{ID: "file-restart-session", Store: secondSessions},
	})
	if err != nil {
		t.Fatalf("create restored agent: %v", err)
	}
	t.Cleanup(func() { _ = secondAgent.Close() })
	secondRunner, err := NewGoalRunner(secondAgent, nil)
	if err != nil {
		t.Fatalf("create restored goal runner: %v", err)
	}
	run, err := secondRunner.ResumePendingAsync(ctx)
	if err != nil {
		t.Fatalf("resume pending goal asynchronously: %v", err)
	}
	result, err = run.Wait()
	if err != nil {
		t.Fatalf("wait for restored goal: %v", err)
	}
	if result.Goal.Status != GoalStatusCompleted || result.Goal.Iteration != 1 {
		t.Fatalf("unexpected restored goal: %#v", result.Goal)
	}
	if calls := secondModel.Calls(); len(calls) != 1 {
		t.Fatalf("restored model calls = %d, want only pending evaluation", len(calls))
	}
	persisted, err := secondSessions.GoalStore().Load(ctx, "file-restart")
	if err != nil {
		t.Fatalf("load restored goal: %v", err)
	}
	if persisted.Status != GoalStatusCompleted || persisted.PendingEvaluation {
		t.Fatalf("persisted restored goal = %#v", persisted)
	}
}

func TestGoalRunnerReportsRunAndErrorPersistenceFailures(t *testing.T) {
	ctx := context.Background()
	runErr := errors.New("model unavailable")
	persistErr := errors.New("goal database unavailable")
	store := &failNthGoalSaveStore{
		inner:  NewMemoryGoalStore(),
		failAt: 3,
		err:    persistErr,
	}
	agent, err := New(ctx, &Config{
		Name: "worker", Model: NewMockChatModel(MockModelError(runErr)),
		Session: &SessionConfig{ID: "joined-error-session", Store: NewMemorySessionStore()},
	})
	if err != nil {
		t.Fatalf("create agent: %v", err)
	}
	t.Cleanup(func() { _ = agent.Close() })
	runner, err := NewGoalRunner(agent, &GoalRunnerConfig{Store: store})
	if err != nil {
		t.Fatalf("create runner: %v", err)
	}

	result, err := runner.Start(ctx, GoalRequest{ID: "joined-error", Objective: "finish"})
	if !errors.Is(err, runErr) || !errors.Is(err, persistErr) {
		t.Fatalf("Start() error = %v, want run and persistence errors", err)
	}
	if result == nil || result.Goal == nil {
		t.Fatalf("Start() result = %#v, want attempted error snapshot", result)
	}
	if !strings.Contains(result.Goal.LastError, runErr.Error()) {
		t.Fatalf("Start() goal = %#v, want attempted error snapshot", result.Goal)
	}
}

func TestGoalRunnerStartPreparationErrorReleasesRunner(t *testing.T) {
	ctx := context.Background()
	persistErr := errors.New("goal database unavailable")
	store := &failNthGoalSaveStore{
		inner:  NewMemoryGoalStore(),
		failAt: 1,
		err:    persistErr,
	}
	agent, err := New(ctx, &Config{
		Model:   NewMockChatModel(MockModelText("finished")),
		Session: &SessionConfig{ID: "prepare-error", Store: NewMemorySessionStore()},
	})
	if err != nil {
		t.Fatalf("create agent: %v", err)
	}
	t.Cleanup(func() { _ = agent.Close() })
	runner, err := NewGoalRunner(agent, &GoalRunnerConfig{
		Store: store,
		Evaluator: GoalEvaluatorFunc(func(context.Context, GoalEvaluation) (GoalDecision, error) {
			return GoalDecision{Complete: true, Reason: "verified"}, nil
		}),
	})
	if err != nil {
		t.Fatalf("create runner: %v", err)
	}
	if _, err := runner.Start(ctx, GoalRequest{ID: "failed", Objective: "work"}); !errors.Is(err, persistErr) {
		t.Fatalf("first Start() error = %v, want persistence error", err)
	}
	result, err := runner.Start(ctx, GoalRequest{ID: "recovered", Objective: "work"})
	if err != nil || result.Goal.Status != GoalStatusCompleted {
		t.Fatalf("second Start() = %#v, %v, want released runner", result, err)
	}
}

func TestGoalRunnerGeneratesIDAndResumesOnlyPendingGoal(t *testing.T) {
	ctx := context.Background()
	sessions := NewMemorySessionStore()
	firstModel := NewMockChatModel(
		MockModelText("all checks passed"),
		MockModelError(errors.New("evaluator unavailable")),
	)
	firstAgent, err := New(ctx, &Config{
		Name: "worker", Model: firstModel,
		Session: &SessionConfig{ID: "auto-resume-session", Store: sessions},
	})
	if err != nil {
		t.Fatalf("create first agent: %v", err)
	}
	firstRunner, err := NewGoalRunner(firstAgent, nil)
	if err != nil {
		t.Fatalf("create first goal runner: %v", err)
	}
	result, err := firstRunner.Start(ctx, GoalRequest{Objective: "finish checks"})
	if err == nil {
		t.Fatal("expected evaluator error")
	}
	if result == nil || result.Goal == nil || result.Goal.ID == "" {
		t.Fatalf("generated goal result = %#v", result)
	}
	generatedID := result.Goal.ID
	_ = firstAgent.Close()

	secondAgent, err := New(ctx, &Config{
		Name: "worker", Model: NewMockChatModel(MockModelText(`{"complete":true,"reason":"verified","next_prompt":""}`)),
		Session: &SessionConfig{ID: "auto-resume-session", Store: sessions},
	})
	if err != nil {
		t.Fatalf("create restored agent: %v", err)
	}
	t.Cleanup(func() { _ = secondAgent.Close() })
	secondRunner, err := NewGoalRunner(secondAgent, nil)
	if err != nil {
		t.Fatalf("create restored goal runner: %v", err)
	}
	goals, err := secondRunner.List(ctx)
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(goals) != 1 || goals[0].ID != generatedID {
		t.Fatalf("List() = %#v, want generated goal %q", goals, generatedID)
	}
	result, err = secondRunner.ResumePending(ctx)
	if err != nil {
		t.Fatalf("ResumePending() error = %v", err)
	}
	if result.Goal.ID != generatedID || result.Goal.Status != GoalStatusCompleted {
		t.Fatalf("ResumePending() result = %#v", result.Goal)
	}
	if _, err := secondRunner.ResumePending(ctx); !errors.Is(err, ErrGoalNotFound) {
		t.Fatalf("ResumePending() completed error = %v, want ErrGoalNotFound", err)
	}
}

func TestGoalRunnerResumePendingRejectsAmbiguousGoalsAndFiltersSession(t *testing.T) {
	ctx := context.Background()
	sessions := NewMemorySessionStore()
	for _, goal := range []*Goal{
		{ID: "older", SessionID: "current", Objective: "one", Status: GoalStatusPaused, MaxIterations: 2},
		{ID: "newer", SessionID: "current", Objective: "two", Status: GoalStatusActive, MaxIterations: 2},
		{ID: "other", SessionID: "another", Objective: "three", Status: GoalStatusActive, MaxIterations: 2},
	} {
		if err := sessions.GoalStore().Save(ctx, goal); err != nil {
			t.Fatalf("save goal %q: %v", goal.ID, err)
		}
	}
	agent, err := New(ctx, &Config{
		Name: "worker", Model: NewMockChatModel(),
		Session: &SessionConfig{ID: "current", Store: sessions},
	})
	if err != nil {
		t.Fatalf("create agent: %v", err)
	}
	t.Cleanup(func() { _ = agent.Close() })
	runner, err := NewGoalRunner(agent, nil)
	if err != nil {
		t.Fatalf("create runner: %v", err)
	}

	goals, err := runner.List(ctx)
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(goals) != 2 {
		t.Fatalf("List() = %#v, want only two goals from current session", goals)
	}
	if _, err := runner.ResumePending(ctx); !errors.Is(err, ErrGoalResumeAmbiguous) {
		t.Fatalf("ResumePending() error = %v, want ErrGoalResumeAmbiguous", err)
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

func TestGoalRunnerBlocksRecoveryWhenSessionDiverged(t *testing.T) {
	ctx := context.Background()
	sessions := NewMemorySessionStore()
	if err := sessions.Save(ctx, &Session{
		ID: "diverged-session",
		Messages: []*schema.Message{
			schema.UserMessage("unrelated conversation"),
			schema.AssistantMessage("unrelated answer", nil),
		},
	}); err != nil {
		t.Fatalf("save diverged session: %v", err)
	}
	if err := sessions.GoalStore().Save(ctx, &Goal{
		ID: "diverged", SessionID: "diverged-session", Objective: "perform goal work",
		Status: GoalStatusActive, MaxIterations: 3, InProgress: true,
		AttemptIteration: 1, HistoryMessageCount: 0, PendingPrompt: "perform goal work",
	}); err != nil {
		t.Fatalf("save in-progress goal: %v", err)
	}
	model := NewMockChatModel()
	agent, err := New(ctx, &Config{
		Name: "worker", Model: model,
		Session: &SessionConfig{ID: "diverged-session", Store: sessions},
	})
	if err != nil {
		t.Fatalf("create restored agent: %v", err)
	}
	t.Cleanup(func() { _ = agent.Close() })
	var evaluations atomic.Int32
	runner, err := NewGoalRunner(agent, &GoalRunnerConfig{
		Evaluator: GoalEvaluatorFunc(func(context.Context, GoalEvaluation) (GoalDecision, error) {
			evaluations.Add(1)
			return GoalDecision{Complete: true, Reason: "must not evaluate unrelated work"}, nil
		}),
	})
	if err != nil {
		t.Fatalf("create restored runner: %v", err)
	}

	result, err := runner.Resume(ctx, "diverged")
	if !errors.Is(err, ErrGoalRecoveryRequired) {
		t.Fatalf("Resume() error = %v, want ErrGoalRecoveryRequired", err)
	}
	if result == nil || result.Goal == nil || result.Goal.Status != GoalStatusBlocked || !result.Goal.InProgress {
		t.Fatalf("Resume() result = %#v, want blocked in-progress goal", result)
	}
	if evaluations.Load() != 0 || len(model.Calls()) != 0 {
		t.Fatalf("diverged recovery evaluated %d times and called model %d times", evaluations.Load(), len(model.Calls()))
	}
	saved, err := sessions.GoalStore().Load(ctx, "diverged")
	if err != nil {
		t.Fatalf("load blocked goal: %v", err)
	}
	if saved.LastReason != "agent session diverged from the recorded goal attempt" || saved.LastError != ErrGoalRecoveryRequired.Error() {
		t.Fatalf("saved blocked goal = %#v", saved)
	}
}

func TestGoalRunnerContinuesAfterSavedToolResultWithoutRepeatingTool(t *testing.T) {
	ctx := context.Background()
	sessions := NewMemorySessionStore()
	const callID = "published-release"
	if err := sessions.Save(ctx, &Session{
		ID: "tool-result-session",
		Messages: []*schema.Message{
			schema.UserMessage("publish the release"),
			schema.AssistantMessage("", []schema.ToolCall{{
				ID: callID,
				Function: schema.FunctionCall{
					Name:      "publish_release",
					Arguments: `{"version":"v2"}`,
				},
			}}),
			schema.ToolMessage("release v2 published", callID, schema.WithToolName("publish_release")),
		},
	}); err != nil {
		t.Fatalf("save session with tool result: %v", err)
	}
	if err := sessions.GoalStore().Save(ctx, &Goal{
		ID: "tool-result", SessionID: "tool-result-session", Objective: "publish the release",
		Status: GoalStatusActive, MaxIterations: 3, InProgress: true,
		AttemptIteration: 1, HistoryMessageCount: 0, PendingPrompt: "publish the release",
	}); err != nil {
		t.Fatalf("save in-progress goal: %v", err)
	}
	var executions atomic.Int32
	publishTool := MustMockTool("publish_release", "publish a release", func(context.Context, string) (string, error) {
		executions.Add(1)
		return "unexpected duplicate publication", nil
	})
	model := NewMockChatModel(MockExpect(MockModelText("publication verified"), func(call MockModelCall) error {
		if result, ok := mockInputToolResult(call.Input, callID); !ok || result != "release v2 published" {
			return errors.New("saved tool result was not supplied to the model")
		}
		return nil
	}))
	agent, err := New(ctx, &Config{
		Name: "worker", Model: model, Tools: MockTools(publishTool),
		Session: &SessionConfig{ID: "tool-result-session", Store: sessions},
	})
	if err != nil {
		t.Fatalf("create restored agent: %v", err)
	}
	t.Cleanup(func() { _ = agent.Close() })
	runner, err := NewGoalRunner(agent, &GoalRunnerConfig{
		Evaluator: GoalEvaluatorFunc(func(_ context.Context, evaluation GoalEvaluation) (GoalDecision, error) {
			if evaluation.LastResponse != "publication verified" {
				t.Fatalf("recovered response = %q", evaluation.LastResponse)
			}
			return GoalDecision{Complete: true, Reason: "publication is verified"}, nil
		}),
	})
	if err != nil {
		t.Fatalf("create restored goal runner: %v", err)
	}

	result, err := runner.Resume(ctx, "tool-result")
	if err != nil {
		t.Fatalf("resume after saved tool result: %v", err)
	}
	if result.Goal.Status != GoalStatusCompleted || result.Goal.Iteration != 1 {
		t.Fatalf("unexpected restored goal: %#v", result.Goal)
	}
	if executions.Load() != 0 {
		t.Fatalf("saved side-effect tool executed %d extra times", executions.Load())
	}
	if calls := model.Calls(); len(calls) != 1 {
		t.Fatalf("model calls = %d, want one continuation", len(calls))
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
	current, err := runner.ResumeInterrupt(ctx, "hitl", nil)
	if err == nil || current == nil || current.Goal.Status != GoalStatusCompleted {
		t.Fatalf("second ResumeInterrupt() = %#v, %v, want current completed goal and error", current, err)
	}
}

func TestGoalRunnerDoesNotStartOverExistingSessionInterrupt(t *testing.T) {
	ctx := context.Background()
	const callID = "ordinary-approval"
	sessions := NewMemorySessionStore()
	model := NewMockChatModel(MockModelToolCallWithID(
		callID, "approve_action", `{"action":"ordinary"}`,
	))
	agent, err := New(ctx, &Config{
		Name:    "worker",
		Model:   model,
		Tools:   []Tool{newCheckpointApprovalTool(t)},
		Session: &SessionConfig{ID: "existing-interrupt-session", Store: sessions},
	})
	if err != nil {
		t.Fatalf("create agent: %v", err)
	}
	t.Cleanup(func() { _ = agent.Close() })
	result, err := agent.Ask(ctx, "ordinary interrupted work")
	if err != nil {
		t.Fatalf("create ordinary interrupt: %v", err)
	}
	if result == nil || !result.IsInterrupted() || len(agent.PendingInterrupts()) != 1 {
		t.Fatalf("ordinary run result = %#v, pending = %#v", result, agent.PendingInterrupts())
	}
	runner, err := NewGoalRunner(agent, nil)
	if err != nil {
		t.Fatalf("create goal runner: %v", err)
	}

	if _, err := runner.Start(ctx, GoalRequest{ID: "must-not-exist", Objective: "new goal"}); !errors.Is(err, ErrResumeRequired) {
		t.Fatalf("Start() error = %v, want ErrResumeRequired", err)
	}
	if _, err := sessions.GoalStore().Load(ctx, "must-not-exist"); !errors.Is(err, ErrGoalNotFound) {
		t.Fatalf("goal was persisted over an existing interrupt: %v", err)
	}
	if got := len(model.Calls()); got != 1 {
		t.Fatalf("model calls = %d, want only the ordinary interrupted run", got)
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

func TestGoalRunnerReusesAgentModelRetryForEvaluation(t *testing.T) {
	ctx := context.Background()
	transient := errors.New("temporary evaluator failure")
	model := NewMockChatModel(
		MockModelText("work completed"),
		MockModelError(transient),
		MockModelText(`{"complete":true,"reason":"verified","next_prompt":""}`),
	)
	agent, err := New(ctx, &Config{
		Name:  "worker",
		Model: model,
		ModelRetryConfig: &ModelRetryConfig{
			MaxRetries:  1,
			BackoffFunc: func(context.Context, int) time.Duration { return 0 },
		},
		Session: &SessionConfig{ID: "retry-evaluation", Store: NewMemorySessionStore()},
	})
	if err != nil {
		t.Fatalf("create agent: %v", err)
	}
	t.Cleanup(func() { _ = agent.Close() })
	runner, err := NewGoalRunner(agent, nil)
	if err != nil {
		t.Fatalf("create runner: %v", err)
	}

	result, err := runner.Start(ctx, GoalRequest{ID: "retry", Objective: "finish work"})
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	if result.Goal.Status != GoalStatusCompleted {
		t.Fatalf("goal = %#v, want completed", result.Goal)
	}
	if calls := model.Calls(); len(calls) != 3 {
		t.Fatalf("model calls = %d, want work plus two evaluation attempts", len(calls))
	}
}

func TestGoalRunnerEvaluationRetryHonorsShouldRetry(t *testing.T) {
	ctx := context.Background()
	model := NewMockChatModel(
		MockModelText("work completed"),
		MockModelText("retry this evaluation"),
		MockModelText(`{"complete":true,"reason":"verified","next_prompt":""}`),
	)
	agent, err := New(ctx, &Config{
		Name:  "worker",
		Model: model,
		ModelRetryConfig: &ModelRetryConfig{
			MaxRetries: 1,
			ShouldRetry: func(_ context.Context, retry *adk.RetryContext) *adk.RetryDecision {
				return &adk.RetryDecision{Retry: retry.OutputMessage != nil && retry.OutputMessage.Content == "retry this evaluation"}
			},
			BackoffFunc: func(context.Context, int) time.Duration { return 0 },
		},
		Session: &SessionConfig{ID: "retry-evaluation-output", Store: NewMemorySessionStore()},
	})
	if err != nil {
		t.Fatalf("create agent: %v", err)
	}
	t.Cleanup(func() { _ = agent.Close() })
	runner, err := NewGoalRunner(agent, nil)
	if err != nil {
		t.Fatalf("create runner: %v", err)
	}

	result, err := runner.Start(ctx, GoalRequest{ID: "retry-output", Objective: "finish work"})
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	if result.Goal.Status != GoalStatusCompleted {
		t.Fatalf("goal = %#v, want completed", result.Goal)
	}
}

func TestGoalRunnerEvaluationRetriesBeforeModelFailover(t *testing.T) {
	ctx := context.Background()
	primaryErr := errors.New("primary unavailable")
	primary := NewMockChatModel(
		MockModelText("work completed"),
		MockModelError(primaryErr),
		MockModelError(primaryErr),
	)
	backup := NewMockChatModel(
		MockModelText(`{"complete":true,"reason":"verified by backup","next_prompt":""}`),
	)
	var failoverCalls int
	agent, err := New(ctx, &Config{
		Name:  "worker",
		Model: primary,
		ModelRetryConfig: &ModelRetryConfig{
			MaxRetries:  1,
			BackoffFunc: func(context.Context, int) time.Duration { return 0 },
		},
		ModelFailoverConfig: &ModelFailoverConfig{
			MaxRetries: 1,
			ShouldFailover: func(_ context.Context, _ *schema.Message, err error) bool {
				var exhausted *adk.RetryExhaustedError
				return errors.As(err, &exhausted) && errors.Is(exhausted.LastErr, primaryErr)
			},
			GetFailoverModel: func(_ context.Context, failover *adk.FailoverContext[*schema.Message]) (ChatModel, []*schema.Message, error) {
				failoverCalls++
				if failover.FailoverAttempt != 1 {
					t.Fatalf("failover attempt = %d, want 1", failover.FailoverAttempt)
				}
				return backup, nil, nil
			},
		},
		Session: &SessionConfig{ID: "evaluation-failover", Store: NewMemorySessionStore()},
	})
	if err != nil {
		t.Fatalf("create agent: %v", err)
	}
	t.Cleanup(func() { _ = agent.Close() })
	runner, err := NewGoalRunner(agent, nil)
	if err != nil {
		t.Fatalf("create runner: %v", err)
	}

	result, err := runner.Start(ctx, GoalRequest{ID: "failover", Objective: "finish work"})
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	if result.Goal.Status != GoalStatusCompleted {
		t.Fatalf("goal = %#v, want completed", result.Goal)
	}
	if len(primary.Calls()) != 3 || len(backup.Calls()) != 1 || failoverCalls != 1 {
		t.Fatalf("model calls = primary %d, backup %d, failover %d; want 3, 1, 1",
			len(primary.Calls()), len(backup.Calls()), failoverCalls)
	}
}

func TestGoalRunnerRecoversFromEvaluatorPanic(t *testing.T) {
	ctx := context.Background()
	model := NewMockChatModel(MockModelText("work completed"))
	agent, err := New(ctx, &Config{
		Name:    "worker",
		Model:   model,
		Session: &SessionConfig{ID: "evaluation-panic", Store: NewMemorySessionStore()},
	})
	if err != nil {
		t.Fatalf("create agent: %v", err)
	}
	t.Cleanup(func() { _ = agent.Close() })
	runner, err := NewGoalRunner(agent, &GoalRunnerConfig{
		Evaluator: GoalEvaluatorFunc(func(context.Context, GoalEvaluation) (GoalDecision, error) {
			panic("broken evaluator")
		}),
	})
	if err != nil {
		t.Fatalf("create runner: %v", err)
	}

	result, err := runner.Start(ctx, GoalRequest{ID: "panic", Objective: "finish work"})
	if !errors.Is(err, ErrGoalEvaluatorPanic) {
		t.Fatalf("Start() error = %v, want ErrGoalEvaluatorPanic", err)
	}
	if result == nil || result.Goal == nil || !result.Goal.PendingEvaluation {
		t.Fatalf("Start() result = %#v, want pending evaluation", result)
	}
	saved, err := runner.Get(ctx, "panic")
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if !saved.PendingEvaluation || !strings.Contains(saved.LastError, ErrGoalEvaluatorPanic.Error()) {
		t.Fatalf("saved goal = %#v, want recoverable evaluator error", saved)
	}

	runner.evaluator = GoalEvaluatorFunc(func(context.Context, GoalEvaluation) (GoalDecision, error) {
		return GoalDecision{Complete: true, Reason: "verified"}, nil
	})
	resumed, err := runner.Resume(ctx, "panic")
	if err != nil {
		t.Fatalf("Resume() error = %v", err)
	}
	if resumed.Goal.Status != GoalStatusCompleted {
		t.Fatalf("resumed goal = %#v, want completed", resumed.Goal)
	}
	if got := len(model.Calls()); got != 1 {
		t.Fatalf("model calls = %d, want no repeated work after evaluator panic", got)
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

type failNthGoalSaveStore struct {
	inner  GoalStore
	failAt int
	err    error
	saves  int
}

func (s *failNthGoalSaveStore) Load(ctx context.Context, id string) (*Goal, error) {
	return s.inner.Load(ctx, id)
}

func (s *failNthGoalSaveStore) Save(ctx context.Context, goal *Goal) error {
	s.saves++
	if s.saves == s.failAt {
		return s.err
	}
	return s.inner.Save(ctx, goal)
}

func (s *failNthGoalSaveStore) Delete(ctx context.Context, id string) error {
	return s.inner.Delete(ctx, id)
}

func (s *failNthGoalSaveStore) List(ctx context.Context) ([]GoalInfo, error) {
	return s.inner.List(ctx)
}
