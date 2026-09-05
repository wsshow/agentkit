package agentkit

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

type observedGoalLeaseStore struct {
	*MemoryGoalStore
	renewed   chan struct{}
	once      sync.Once
	lose      bool
	loseAfter <-chan struct{}
}

func (s *observedGoalLeaseStore) RenewGoalLease(
	ctx context.Context,
	lease *GoalLease,
	duration time.Duration,
) (*GoalLease, error) {
	if s.lose {
		if s.loseAfter != nil {
			select {
			case <-s.loseAfter:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		s.once.Do(func() { close(s.renewed) })
		return nil, ErrGoalLeaseLost
	}
	renewed, err := s.MemoryGoalStore.RenewGoalLease(ctx, lease, duration)
	if err == nil {
		s.once.Do(func() { close(s.renewed) })
	}
	return renewed, err
}

type goalStoreWithoutLease struct {
	store GoalStore
}

type malformedGoalLeaseStore struct {
	*MemoryGoalStore
	acquired *GoalLease
	renewed  *GoalLease
}

func (s *malformedGoalLeaseStore) AcquireGoalLease(
	context.Context,
	string,
	string,
	time.Duration,
) (*GoalLease, error) {
	return s.acquired, nil
}

func (s *malformedGoalLeaseStore) RenewGoalLease(
	context.Context,
	*GoalLease,
	time.Duration,
) (*GoalLease, error) {
	return s.renewed, nil
}

func (s *goalStoreWithoutLease) Load(ctx context.Context, id string) (*Goal, error) {
	return s.store.Load(ctx, id)
}

func (s *goalStoreWithoutLease) Save(ctx context.Context, goal *Goal) error {
	return s.store.Save(ctx, goal)
}

func (s *goalStoreWithoutLease) Delete(ctx context.Context, id string) error {
	return s.store.Delete(ctx, id)
}

func (s *goalStoreWithoutLease) List(ctx context.Context) ([]GoalInfo, error) {
	return s.store.List(ctx)
}

func TestGoalRunnerRenewsLeaseAndRejectsConcurrentWorker(t *testing.T) {
	sessions := NewMemorySessionStore()
	store := &observedGoalLeaseStore{
		MemoryGoalStore: NewMemoryGoalStore(),
		renewed:         make(chan struct{}),
	}
	started := make(chan struct{})
	unblock := make(chan struct{})
	var startOnce sync.Once
	blockingTool := MustMockTool("blocking_work", "wait until work is released", func(ctx context.Context, _ string) (string, error) {
		startOnce.Do(func() { close(started) })
		select {
		case <-unblock:
			return "finished", nil
		case <-ctx.Done():
			return "", ctx.Err()
		}
	})
	firstAgent, err := New(context.Background(), &Config{
		Name: "worker", Model: NewMockChatModel(
			MockModelToolCallWithID("blocking-call", "blocking_work", `""`),
			MockModelTextAfterToolResult("blocking-call"),
		),
		Tools:   MockTools(blockingTool),
		Session: &SessionConfig{ID: "shared-session", Store: sessions},
	})
	if err != nil {
		t.Fatalf("create first agent: %v", err)
	}
	t.Cleanup(func() { _ = firstAgent.Close() })
	firstRunner, err := NewGoalRunner(firstAgent, &GoalRunnerConfig{
		Store: store, WorkerID: "worker-1", LeaseDuration: 300 * time.Millisecond,
		RequireLease: true,
		Evaluator: GoalEvaluatorFunc(func(context.Context, GoalEvaluation) (GoalDecision, error) {
			return GoalDecision{Complete: true, Reason: "work finished"}, nil
		}),
	})
	if err != nil {
		t.Fatalf("create first runner: %v", err)
	}

	runCtx, cancelRun := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancelRun()
	type runOutcome struct {
		result *GoalRunResult
		err    error
	}
	finished := make(chan runOutcome, 1)
	go func() {
		result, runErr := firstRunner.Start(runCtx, GoalRequest{ID: "shared-goal", Objective: "finish work"})
		finished <- runOutcome{result: result, err: runErr}
	}()
	waitForTestSignal(t, started, "blocking tool start")
	waitForTestSignal(t, store.renewed, "goal lease renewal")

	secondAgent, err := New(context.Background(), &Config{
		Name: "worker", Model: NewMockChatModel(),
		Session: &SessionConfig{ID: "shared-session", Store: sessions},
	})
	if err != nil {
		close(unblock)
		t.Fatalf("create second agent: %v", err)
	}
	t.Cleanup(func() { _ = secondAgent.Close() })
	secondRunner, err := NewGoalRunner(secondAgent, &GoalRunnerConfig{
		Store: store, WorkerID: "worker-2", LeaseDuration: 300 * time.Millisecond, RequireLease: true,
	})
	if err != nil {
		close(unblock)
		t.Fatalf("create second runner: %v", err)
	}
	if _, err := secondRunner.Resume(context.Background(), "shared-goal"); !errors.Is(err, ErrGoalLeaseHeld) {
		close(unblock)
		t.Fatalf("expected ErrGoalLeaseHeld, got %v", err)
	}
	if err := secondRunner.Pause(context.Background(), "shared-goal"); !errors.Is(err, ErrGoalLeaseHeld) {
		close(unblock)
		t.Fatalf("expected leased goal pause to fail with ErrGoalLeaseHeld, got %v", err)
	}
	if err := secondRunner.Clear(context.Background(), "shared-goal"); !errors.Is(err, ErrGoalLeaseHeld) {
		close(unblock)
		t.Fatalf("expected leased goal clear to fail with ErrGoalLeaseHeld, got %v", err)
	}

	close(unblock)
	select {
	case outcome := <-finished:
		if outcome.err != nil {
			t.Fatalf("first runner failed: %v", outcome.err)
		}
		if outcome.result == nil || outcome.result.Goal.Status != GoalStatusCompleted {
			t.Fatalf("unexpected first result: %#v", outcome.result)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("first runner did not finish")
	}
	if err := secondRunner.Clear(context.Background(), "shared-goal"); err != nil {
		t.Fatalf("clear released goal: %v", err)
	}
	if _, err := store.Load(context.Background(), "shared-goal"); !errors.Is(err, ErrGoalNotFound) {
		t.Fatalf("expected cleared goal to be missing, got %v", err)
	}
}

func TestGoalRunnerRejectsMalformedAcquiredLease(t *testing.T) {
	ctx := context.Background()
	tests := []struct {
		name  string
		lease *GoalLease
	}{
		{name: "nil"},
		{name: "wrong goal", lease: &GoalLease{
			GoalID: "other", WorkerID: "worker", Token: "token", ExpiresAt: time.Now().Add(time.Minute),
		}},
		{name: "wrong worker", lease: &GoalLease{
			GoalID: "goal", WorkerID: "other", Token: "token", ExpiresAt: time.Now().Add(time.Minute),
		}},
		{name: "missing token", lease: &GoalLease{
			GoalID: "goal", WorkerID: "worker", ExpiresAt: time.Now().Add(time.Minute),
		}},
		{name: "expired", lease: &GoalLease{
			GoalID: "goal", WorkerID: "worker", Token: "token", ExpiresAt: time.Now().Add(-time.Minute),
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := &malformedGoalLeaseStore{MemoryGoalStore: NewMemoryGoalStore(), acquired: tt.lease}
			agent, err := New(ctx, &Config{
				Name: "worker", Model: NewMockChatModel(),
				Session: &SessionConfig{ID: "session", Store: NewMemorySessionStore()},
			})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = agent.Close() })
			runner, err := NewGoalRunner(agent, &GoalRunnerConfig{
				Store: store, WorkerID: "worker", RequireLease: true,
			})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := runner.Start(ctx, GoalRequest{ID: "goal", Objective: "finish"}); !errors.Is(err, ErrInvalidPersistenceData) {
				t.Fatalf("Start() error = %v, want ErrInvalidPersistenceData", err)
			}
			if _, err := store.Load(ctx, "goal"); !errors.Is(err, ErrGoalNotFound) {
				t.Fatalf("malformed lease allowed a goal save: %v", err)
			}
		})
	}
}

func TestRenewGoalLeaseRejectsMalformedBackendData(t *testing.T) {
	ctx := context.Background()
	lease := &GoalLease{
		GoalID: "goal", WorkerID: "worker", Token: "token", ExpiresAt: time.Now().Add(time.Minute),
	}
	store := &malformedGoalLeaseStore{
		MemoryGoalStore: NewMemoryGoalStore(),
		renewed: &GoalLease{
			GoalID: "goal", WorkerID: "other", Token: "token", ExpiresAt: lease.ExpiresAt.Add(time.Minute),
		},
	}
	if _, err := renewGoalLease(ctx, store, lease, time.Minute); !errors.Is(err, ErrInvalidPersistenceData) {
		t.Fatalf("renewGoalLease() error = %v, want ErrInvalidPersistenceData", err)
	}
}

func TestGoalRunnerPausesItsActiveLeasedGoal(t *testing.T) {
	ctx := context.Background()
	sessions := NewMemorySessionStore()
	store := NewMemoryGoalStore()
	started := make(chan struct{})
	var startOnce sync.Once
	blockingTool := MustMockTool("pausable_work", "wait for pause", func(ctx context.Context, _ string) (string, error) {
		startOnce.Do(func() { close(started) })
		<-ctx.Done()
		return "", ctx.Err()
	})
	agent, err := New(ctx, &Config{
		Name: "worker", Model: NewMockChatModel(
			MockModelToolCallWithID("pausable-call", "pausable_work", `""`),
		),
		Tools:   MockTools(blockingTool),
		Session: &SessionConfig{ID: "pausable-session", Store: sessions},
	})
	if err != nil {
		t.Fatalf("create agent: %v", err)
	}
	t.Cleanup(func() { _ = agent.Close() })
	runner, err := NewGoalRunner(agent, &GoalRunnerConfig{
		Store: store, WorkerID: "worker", LeaseDuration: time.Second, RequireLease: true,
	})
	if err != nil {
		t.Fatalf("create goal runner: %v", err)
	}

	finished := make(chan error, 1)
	go func() {
		_, runErr := runner.Start(context.Background(), GoalRequest{ID: "pausable-goal", Objective: "wait"})
		finished <- runErr
	}()
	waitForTestSignal(t, started, "pausable tool start")
	if err := runner.Pause(ctx, "pausable-goal"); err != nil {
		t.Fatalf("pause active goal: %v", err)
	}
	select {
	case err := <-finished:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("paused run error = %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("paused goal did not stop")
	}
	goal, err := store.Load(ctx, "pausable-goal")
	if err != nil {
		t.Fatalf("load paused goal: %v", err)
	}
	if goal.Status != GoalStatusPaused {
		t.Fatalf("paused goal status = %q, want %q", goal.Status, GoalStatusPaused)
	}
}

func TestGoalRunnerCancelsWorkWhenLeaseIsLost(t *testing.T) {
	sessions := NewMemorySessionStore()
	started := make(chan struct{})
	store := &observedGoalLeaseStore{
		MemoryGoalStore: NewMemoryGoalStore(),
		renewed:         make(chan struct{}),
		lose:            true,
		loseAfter:       started,
	}
	var startOnce sync.Once
	blockingTool := MustMockTool("lease_sensitive_work", "wait for lease cancellation", func(ctx context.Context, _ string) (string, error) {
		startOnce.Do(func() { close(started) })
		<-ctx.Done()
		return "", ctx.Err()
	})
	agent, err := New(context.Background(), &Config{
		Name: "worker", Model: NewMockChatModel(
			MockModelToolCallWithID("lease-call", "lease_sensitive_work", `""`),
		),
		Tools:   MockTools(blockingTool),
		Session: &SessionConfig{ID: "lease-session", Store: sessions},
	})
	if err != nil {
		t.Fatalf("create agent: %v", err)
	}
	t.Cleanup(func() { _ = agent.Close() })
	runner, err := NewGoalRunner(agent, &GoalRunnerConfig{
		Store: store, WorkerID: "worker-1", LeaseDuration: 300 * time.Millisecond, RequireLease: true,
		Evaluator: GoalEvaluatorFunc(func(context.Context, GoalEvaluation) (GoalDecision, error) {
			return GoalDecision{Complete: true, Reason: "unexpected"}, nil
		}),
	})
	if err != nil {
		t.Fatalf("create runner: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	result, err := runner.Start(ctx, GoalRequest{ID: "lease-loss", Objective: "wait"})
	if !errors.Is(err, ErrGoalLeaseLost) {
		t.Fatalf("expected ErrGoalLeaseLost, got result=%#v error=%v", result, err)
	}
	waitForTestSignal(t, started, "lease-sensitive tool start")
	waitForTestSignal(t, store.renewed, "failed lease renewal")
}

func TestGoalRunnerCanRequireLeaseCapability(t *testing.T) {
	sessions := NewMemorySessionStore()
	agent, err := New(context.Background(), &Config{
		Name: "worker", Model: NewMockChatModel(),
		Session: &SessionConfig{ID: "session", Store: sessions},
	})
	if err != nil {
		t.Fatalf("create agent: %v", err)
	}
	t.Cleanup(func() { _ = agent.Close() })
	legacy := &goalStoreWithoutLease{store: NewMemoryGoalStore()}
	if _, err := NewGoalRunner(agent, &GoalRunnerConfig{Store: legacy, RequireLease: true}); err == nil {
		t.Fatal("expected missing lease capability error")
	}
	runner, err := NewGoalRunner(agent, &GoalRunnerConfig{Store: legacy})
	if err != nil {
		t.Fatalf("legacy goal store should remain supported: %v", err)
	}
	if runner.leaseStore != nil {
		t.Fatalf("legacy runner lease store = %T, want nil", runner.leaseStore)
	}
	if _, err := NewGoalRunner(agent, &GoalRunnerConfig{WorkerID: " worker "}); err == nil {
		t.Fatal("expected worker ID validation error")
	}
	if _, err := NewGoalRunner(agent, &GoalRunnerConfig{LeaseDuration: -time.Second}); err == nil {
		t.Fatal("expected lease duration validation error")
	}
}

func waitForTestSignal(t *testing.T, signal <-chan struct{}, name string) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for %s", name)
	}
}
