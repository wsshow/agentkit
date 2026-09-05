package agentkit

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestCanceledRunUsesBoundedContextToPersistSession(t *testing.T) {
	store := &blockingSessionStore{started: make(chan error, 1)}
	agent, err := New(context.Background(), &Config{
		Name:               "worker",
		Model:              NewMockChatModel(),
		PersistenceTimeout: 20 * time.Millisecond,
		Session:            &SessionConfig{ID: "bounded-session", Store: store},
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	t.Cleanup(func() { _ = agent.Close() })
	parent, cancel := context.WithCancel(context.Background())
	cancel()
	runErr := errors.New("run failed")
	started := time.Now()
	err = agent.persistSession(parent, runErr)
	if !errors.Is(err, runErr) || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("persistSession() error = %v, want run error and deadline", err)
	}
	if initialErr := <-store.started; initialErr != nil {
		t.Fatalf("persistence context was already canceled: %v", initialErr)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("persistSession() took %s, want bounded exit", elapsed)
	}
}

func TestGoalDetachedSaveUsesBoundedPersistenceContext(t *testing.T) {
	store := &blockingGoalStore{started: make(chan error, 1)}
	agent, err := New(context.Background(), &Config{
		Name:               "worker",
		Model:              NewMockChatModel(),
		PersistenceTimeout: 20 * time.Millisecond,
		Session:            &SessionConfig{ID: "bounded-goal-session", Store: NewMemorySessionStore()},
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	t.Cleanup(func() { _ = agent.Close() })
	runner, err := NewGoalRunner(agent, &GoalRunnerConfig{Store: store})
	if err != nil {
		t.Fatalf("NewGoalRunner() error = %v", err)
	}
	parent, cancel := context.WithCancel(context.Background())
	cancel()
	started := time.Now()
	err = runner.saveDetached(parent, &Goal{
		ID: "bounded", SessionID: "bounded-goal-session", Objective: "finish",
		Status: GoalStatusActive, MaxIterations: 1,
	})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("saveDetached() error = %v, want context deadline exceeded", err)
	}
	if initialErr := <-store.started; initialErr != nil {
		t.Fatalf("persistence context was already canceled: %v", initialErr)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("saveDetached() took %s, want bounded exit", elapsed)
	}
}

type blockingSessionStore struct {
	started chan error
}

func (s *blockingSessionStore) Load(context.Context, string) (*Session, error) {
	return nil, ErrSessionNotFound
}

func (s *blockingSessionStore) Save(ctx context.Context, _ *Session) error {
	s.started <- ctx.Err()
	<-ctx.Done()
	return ctx.Err()
}

func (s *blockingSessionStore) Delete(context.Context, string) error { return nil }

func (s *blockingSessionStore) List(context.Context) ([]SessionInfo, error) { return nil, nil }

type blockingGoalStore struct {
	started chan error
}

func (s *blockingGoalStore) Load(context.Context, string) (*Goal, error) {
	return nil, ErrGoalNotFound
}

func (s *blockingGoalStore) Save(ctx context.Context, _ *Goal) error {
	s.started <- ctx.Err()
	<-ctx.Done()
	return ctx.Err()
}

func (s *blockingGoalStore) Delete(context.Context, string) error { return nil }

func (s *blockingGoalStore) List(context.Context) ([]GoalInfo, error) { return nil, nil }
