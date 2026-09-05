package agentkit

import (
	"context"
	"errors"
	"testing"
	"time"
)

type panickingSessionStore struct {
	*MemorySessionStore
	load bool
	save bool
	list bool
}

func (s *panickingSessionStore) Load(ctx context.Context, id string) (*Session, error) {
	if s.load {
		panic("broken session load")
	}
	return s.MemorySessionStore.Load(ctx, id)
}

func (s *panickingSessionStore) Save(ctx context.Context, session *Session) error {
	if s.save {
		panic("broken session save")
	}
	return s.MemorySessionStore.Save(ctx, session)
}

func (s *panickingSessionStore) List(ctx context.Context) ([]SessionInfo, error) {
	if s.list {
		panic("broken session list")
	}
	return s.MemorySessionStore.List(ctx)
}

type panickingGoalProviderSessionStore struct {
	*MemorySessionStore
}

func (s *panickingGoalProviderSessionStore) GoalStore() GoalStore {
	panic("broken goal provider")
}

type panickingGoalLoadStore struct {
	*MemoryGoalStore
}

func (s *panickingGoalLoadStore) Load(context.Context, string) (*Goal, error) {
	panic("broken goal load")
}

type panickingRenewGoalStore struct {
	*MemoryGoalStore
}

func (s *panickingRenewGoalStore) RenewGoalLease(
	context.Context,
	*GoalLease,
	time.Duration,
) (*GoalLease, error) {
	panic("broken lease renewal")
}

type panickingCheckpointStore struct{}

func (panickingCheckpointStore) Set(context.Context, string, []byte) error {
	panic("broken checkpoint save")
}

func (panickingCheckpointStore) Get(context.Context, string) ([]byte, bool, error) {
	panic("broken checkpoint load")
}

func (panickingCheckpointStore) Delete(context.Context, string) error {
	panic("broken checkpoint delete")
}

type panickingToolResultStore struct{}

func (panickingToolResultStore) Load(context.Context, string) (*StoredToolResult, error) {
	panic("broken tool result load")
}

func (panickingToolResultStore) Save(context.Context, *StoredToolResult) error {
	panic("broken tool result save")
}

func (panickingToolResultStore) Delete(context.Context, string) error {
	panic("broken tool result delete")
}

func (panickingToolResultStore) List(context.Context) ([]ToolResultInfo, error) {
	panic("broken tool result list")
}

func TestAgentConvertsSessionStorePanics(t *testing.T) {
	ctx := context.Background()
	_, err := New(ctx, &Config{
		Model:   NewMockChatModel(),
		Session: &SessionConfig{ID: "load", Store: &panickingSessionStore{MemorySessionStore: NewMemorySessionStore(), load: true}},
	})
	if !errors.Is(err, ErrPersistencePanic) {
		t.Fatalf("New() error = %v, want ErrPersistencePanic", err)
	}

	store := &panickingSessionStore{MemorySessionStore: NewMemorySessionStore(), save: true}
	agent, err := New(ctx, &Config{
		Model:   NewMockChatModel(),
		Session: &SessionConfig{ID: "save", Store: store},
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	t.Cleanup(func() { _ = agent.Close() })
	if err := agent.SaveSession(ctx); !errors.Is(err, ErrPersistencePanic) {
		t.Fatalf("SaveSession() error = %v, want ErrPersistencePanic", err)
	}
}

func TestPersistenceProvidersAndStoresConvertPanics(t *testing.T) {
	ctx := context.Background()
	sessions := &panickingGoalProviderSessionStore{MemorySessionStore: NewMemorySessionStore()}
	agent, err := New(ctx, &Config{
		Model:   NewMockChatModel(),
		Session: &SessionConfig{ID: "provider", Store: sessions},
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	t.Cleanup(func() { _ = agent.Close() })
	if _, err := NewGoalRunner(agent, nil); !errors.Is(err, ErrPersistencePanic) {
		t.Fatalf("NewGoalRunner() error = %v, want ErrPersistencePanic", err)
	}

	goalAgent, err := New(ctx, &Config{
		Model:   NewMockChatModel(),
		Session: &SessionConfig{ID: "goal", Store: NewMemorySessionStore()},
	})
	if err != nil {
		t.Fatalf("New() goal agent error = %v", err)
	}
	t.Cleanup(func() { _ = goalAgent.Close() })
	runner, err := NewGoalRunner(goalAgent, &GoalRunnerConfig{
		Store: &panickingGoalLoadStore{MemoryGoalStore: NewMemoryGoalStore()},
	})
	if err != nil {
		t.Fatalf("NewGoalRunner() error = %v", err)
	}
	if _, err := runner.Start(ctx, GoalRequest{ID: "broken", Objective: "work"}); !errors.Is(err, ErrPersistencePanic) {
		t.Fatalf("Start() error = %v, want ErrPersistencePanic", err)
	}

	checkpoint := guardCheckpointStore(panickingCheckpointStore{})
	if _, _, err := checkpoint.Get(ctx, "checkpoint"); !errors.Is(err, ErrPersistencePanic) {
		t.Fatalf("checkpoint Get() error = %v, want ErrPersistencePanic", err)
	}
	deleter, ok := checkpoint.(CheckpointDeleter)
	if !ok {
		t.Fatal("guarded checkpoint store lost CheckpointDeleter")
	}
	if err := deleter.Delete(ctx, "checkpoint"); !errors.Is(err, ErrPersistencePanic) {
		t.Fatalf("checkpoint Delete() error = %v, want ErrPersistencePanic", err)
	}

	if err := toolResultStoreSave(ctx, panickingToolResultStore{}, &StoredToolResult{ID: "result"}); !errors.Is(err, ErrPersistencePanic) {
		t.Fatalf("tool result Save() error = %v, want ErrPersistencePanic", err)
	}
	if _, err := PruneResources(ctx, &panickingSessionStore{
		MemorySessionStore: NewMemorySessionStore(), list: true,
	}, RetentionPolicy{SessionIdleTime: time.Hour}); !errors.Is(err, ErrPersistencePanic) {
		t.Fatalf("PruneResources() error = %v, want ErrPersistencePanic", err)
	}
}

func TestGoalRunnerLeaseRenewPanicStopsBackgroundRun(t *testing.T) {
	ctx := context.Background()
	started := make(chan struct{})
	tool := MustMockTool("wait", "wait for lease loss", func(ctx context.Context, _ string) (string, error) {
		close(started)
		<-ctx.Done()
		return "", ctx.Err()
	})
	agent, err := New(ctx, &Config{
		Model: NewMockChatModel(MockModelToolCallWithID("wait-call", "wait", `""`)),
		Tools: MockTools(tool),
		Session: &SessionConfig{
			ID: "lease-panic", Store: NewMemorySessionStore(),
		},
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	t.Cleanup(func() { _ = agent.Close() })
	runner, err := NewGoalRunner(agent, &GoalRunnerConfig{
		Store:         &panickingRenewGoalStore{MemoryGoalStore: NewMemoryGoalStore()},
		LeaseDuration: 30 * time.Millisecond,
		RequireLease:  true,
	})
	if err != nil {
		t.Fatalf("NewGoalRunner() error = %v", err)
	}
	run, err := runner.StartAsync(ctx, GoalRequest{ID: "lease", Objective: "wait"})
	if err != nil {
		t.Fatalf("StartAsync() error = %v", err)
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("goal tool did not start")
	}
	waitCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	_, err = run.WaitContext(waitCtx)
	if !errors.Is(err, ErrGoalLeaseLost) || !errors.Is(err, ErrPersistencePanic) {
		t.Fatalf("WaitContext() error = %v, want lease loss and persistence panic", err)
	}
}
