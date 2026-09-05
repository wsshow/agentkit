package agentkit

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/cloudwego/eino/schema"
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

type retainingCheckpointStore struct {
	value []byte
}

func (s *retainingCheckpointStore) Set(_ context.Context, _ string, value []byte) error {
	s.value = value
	return nil
}

func (s *retainingCheckpointStore) Get(context.Context, string) ([]byte, bool, error) {
	return s.value, true, nil
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

type fixedSessionLoadStore struct {
	*MemorySessionStore
	loaded *Session
}

func (s *fixedSessionLoadStore) Load(context.Context, string) (*Session, error) {
	return s.loaded, nil
}

type fixedGoalLoadStore struct {
	*MemoryGoalStore
	loaded *Goal
}

func (s *fixedGoalLoadStore) Load(context.Context, string) (*Goal, error) {
	return s.loaded, nil
}

type fixedToolResultLoadStore struct {
	*MemoryToolResultStore
	loaded *StoredToolResult
}

type fixedGoalListStore struct {
	*MemoryGoalStore
	infos []GoalInfo
}

func (s *fixedGoalListStore) List(context.Context) ([]GoalInfo, error) {
	return s.infos, nil
}

type fixedToolResultListStore struct {
	*MemoryToolResultStore
	infos []ToolResultInfo
}

func (s *fixedToolResultListStore) List(context.Context) ([]ToolResultInfo, error) {
	return s.infos, nil
}

func (s *fixedToolResultLoadStore) Load(context.Context, string) (*StoredToolResult, error) {
	return s.loaded, nil
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

func TestPersistenceLoadsRejectInvalidBackendData(t *testing.T) {
	ctx := context.Background()
	tests := []struct {
		name string
		load func() error
	}{
		{
			name: "nil session",
			load: func() error {
				_, err := sessionStoreLoad(ctx, &fixedSessionLoadStore{MemorySessionStore: NewMemorySessionStore()}, "session")
				return err
			},
		},
		{
			name: "mismatched session",
			load: func() error {
				_, err := sessionStoreLoad(ctx, &fixedSessionLoadStore{
					MemorySessionStore: NewMemorySessionStore(), loaded: &Session{ID: "other"},
				}, "session")
				return err
			},
		},
		{
			name: "nil goal",
			load: func() error {
				_, err := goalStoreLoad(ctx, &fixedGoalLoadStore{MemoryGoalStore: NewMemoryGoalStore()}, "goal")
				return err
			},
		},
		{
			name: "mismatched goal",
			load: func() error {
				_, err := goalStoreLoad(ctx, &fixedGoalLoadStore{
					MemoryGoalStore: NewMemoryGoalStore(), loaded: validStoredGoal("other"),
				}, "goal")
				return err
			},
		},
		{
			name: "invalid goal",
			load: func() error {
				goal := validStoredGoal("goal")
				goal.Status = "corrupt"
				_, err := goalStoreLoad(ctx, &fixedGoalLoadStore{
					MemoryGoalStore: NewMemoryGoalStore(), loaded: goal,
				}, "goal")
				return err
			},
		},
		{
			name: "nil tool result",
			load: func() error {
				_, err := toolResultStoreLoad(ctx, &fixedToolResultLoadStore{
					MemoryToolResultStore: NewMemoryToolResultStore(),
				}, "result")
				return err
			},
		},
		{
			name: "mismatched tool result",
			load: func() error {
				_, err := toolResultStoreLoad(ctx, &fixedToolResultLoadStore{
					MemoryToolResultStore: NewMemoryToolResultStore(), loaded: &StoredToolResult{ID: "other"},
				}, "result")
				return err
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.load(); !errors.Is(err, ErrInvalidPersistenceData) {
				t.Fatalf("load error = %v, want ErrInvalidPersistenceData", err)
			}
		})
	}
}

func TestPersistenceLoadsCloneBackendSnapshots(t *testing.T) {
	ctx := context.Background()
	sessionSource := &Session{ID: "session", Messages: []*schema.Message{schema.UserMessage("original")}}
	session, err := sessionStoreLoad(ctx, &fixedSessionLoadStore{
		MemorySessionStore: NewMemorySessionStore(), loaded: sessionSource,
	}, "session")
	if err != nil {
		t.Fatal(err)
	}
	session.Messages[0].Content = "changed"
	if sessionSource.Messages[0].Content != "original" {
		t.Fatal("session load retained backend-owned message data")
	}

	goalSource := validStoredGoal("goal")
	goal, err := goalStoreLoad(ctx, &fixedGoalLoadStore{
		MemoryGoalStore: NewMemoryGoalStore(), loaded: goalSource,
	}, "goal")
	if err != nil {
		t.Fatal(err)
	}
	goal.Objective = "changed"
	if goalSource.Objective != "work" {
		t.Fatal("goal load retained backend-owned goal data")
	}
}

func TestGuardedCheckpointStoreIsolatesBackendBytes(t *testing.T) {
	ctx := context.Background()
	backend := &retainingCheckpointStore{}
	store := guardCheckpointStore(backend)
	input := []byte("checkpoint")
	if err := store.Set(ctx, "id", input); err != nil {
		t.Fatal(err)
	}
	input[0] = 'X'
	if string(backend.value) != "checkpoint" {
		t.Fatalf("checkpoint backend retained caller bytes: %q", backend.value)
	}
	loaded, existed, err := store.Get(ctx, "id")
	if err != nil || !existed {
		t.Fatalf("checkpoint Get() = %q, %v, %v", loaded, existed, err)
	}
	loaded[0] = 'Y'
	if string(backend.value) != "checkpoint" {
		t.Fatalf("checkpoint load exposed backend bytes: %q", backend.value)
	}
}

func TestPersistenceListsValidateAndCopyBackendData(t *testing.T) {
	ctx := context.Background()
	now := time.Now().UTC()
	goalSource := []GoalInfo{{
		ID: "goal", SessionID: "session", Objective: "work", Status: GoalStatusActive,
		MaxIterations: 2, UpdatedAt: now,
	}}
	goals := &fixedGoalListStore{MemoryGoalStore: NewMemoryGoalStore(), infos: goalSource}
	listedGoals, err := goalStoreList(ctx, goals)
	if err != nil {
		t.Fatal(err)
	}
	listedGoals[0].Objective = "mutated"
	if goalSource[0].Objective != "work" {
		t.Fatal("goal list retained backend-owned data")
	}
	goals.infos = []GoalInfo{{
		ID: "goal", SessionID: "session", Objective: "work", Status: "invalid",
		MaxIterations: 2, UpdatedAt: now,
	}}
	if _, err := goalStoreList(ctx, goals); !errors.Is(err, ErrInvalidPersistenceData) {
		t.Fatalf("goalStoreList() error = %v, want ErrInvalidPersistenceData", err)
	}

	resultSource := []ToolResultInfo{{ID: "result", Size: 10, CreatedAt: now}}
	results := &fixedToolResultListStore{
		MemoryToolResultStore: NewMemoryToolResultStore(), infos: resultSource,
	}
	listedResults, err := toolResultStoreList(ctx, results)
	if err != nil {
		t.Fatal(err)
	}
	listedResults[0].ID = "mutated"
	if resultSource[0].ID != "result" {
		t.Fatal("tool result list retained backend-owned data")
	}
	results.infos = []ToolResultInfo{{ID: "result", Size: -1, CreatedAt: now}}
	if _, err := toolResultStoreList(ctx, results); !errors.Is(err, ErrInvalidPersistenceData) {
		t.Fatalf("toolResultStoreList() error = %v, want ErrInvalidPersistenceData", err)
	}
}

func validStoredGoal(id string) *Goal {
	return &Goal{
		ID: id, SessionID: "session", Objective: "work", Status: GoalStatusActive, MaxIterations: 1,
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
