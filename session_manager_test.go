package agentkit

import (
	"context"
	"errors"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cloudwego/eino/schema"
)

func TestSessionManagerCreateOpenCloseAndRestore(t *testing.T) {
	ctx := context.Background()
	store := NewMemorySessionStore()
	manager, err := NewSessionManager(&SessionManagerConfig{
		Store:   store,
		OwnerID: "owner-1",
		AgentConfig: &Config{
			Name:  "assistant",
			Model: NewMockChatModel(MockModelText("remembered")),
		},
	})
	if err != nil {
		t.Fatalf("NewSessionManager() error = %v", err)
	}
	defer manager.Close()

	agent, err := manager.CreateWithOptions(ctx, CreateSessionOptions{
		ID: "session-1", Title: "First", Tags: []string{" blue ", "prod", "blue"},
	})
	if err != nil {
		t.Fatalf("CreateWithOptions() error = %v", err)
	}
	if got := manager.ActiveSessionIDs(); !slices.Equal(got, []string{"session-1"}) {
		t.Fatalf("ActiveSessionIDs() = %v", got)
	}
	snapshot := agent.Session()
	if snapshot.OwnerID != "owner-1" || snapshot.Title != "First" || !slices.Equal(snapshot.Tags, []string{"blue", "prod"}) {
		t.Fatalf("created metadata = %#v", snapshot.SessionMetadata)
	}

	same, err := manager.Open(ctx, "session-1")
	if err != nil {
		t.Fatalf("Open(active) error = %v", err)
	}
	if same != agent {
		t.Fatal("Open(active) returned a different Agent")
	}
	if err := agent.Prompt(ctx, "remember this"); err != nil {
		t.Fatalf("Prompt() error = %v", err)
	}
	if err := manager.CloseSession(ctx, "session-1"); err != nil {
		t.Fatalf("CloseSession() error = %v", err)
	}
	if len(manager.ActiveSessionIDs()) != 0 {
		t.Fatalf("active sessions after close = %v", manager.ActiveSessionIDs())
	}

	reopened, err := manager.Open(ctx, "session-1")
	if err != nil {
		t.Fatalf("Open(reconnect) error = %v", err)
	}
	if reopened == agent {
		t.Fatal("Open(reconnect) reused a closed Agent")
	}
	if got := len(reopened.History()); got != 2 {
		t.Fatalf("restored history length = %d, want 2", got)
	}

	if err := reopened.Close(); err != nil {
		t.Fatal(err)
	}
	reopenedAgain, err := manager.Open(ctx, "session-1")
	if err != nil {
		t.Fatalf("Open(after direct close) error = %v", err)
	}
	if reopenedAgain == reopened {
		t.Fatal("Open(after direct close) returned the closed Agent")
	}
}

func TestSessionManagerOpenOrCreate(t *testing.T) {
	ctx := context.Background()
	store := NewMemorySessionStore()
	manager := newTestSessionManager(t, store, "owner")
	defer manager.Close()

	createdAgent, created, err := manager.OpenOrCreate(ctx, CreateSessionOptions{ID: "stable", Title: "Created"})
	if err != nil {
		t.Fatalf("OpenOrCreate(create) error = %v", err)
	}
	if !created || createdAgent.Session().Title != "Created" {
		t.Fatalf("OpenOrCreate(create) = %#v, created %v", createdAgent.Session(), created)
	}
	openedAgent, created, err := manager.OpenOrCreate(ctx, CreateSessionOptions{ID: "stable", Title: "Ignored"})
	if err != nil {
		t.Fatalf("OpenOrCreate(open) error = %v", err)
	}
	if created || openedAgent != createdAgent || openedAgent.Session().Title != "Created" {
		t.Fatalf("OpenOrCreate(open) returned agent %p, created %v", openedAgent, created)
	}
	automatic, created, err := manager.OpenOrCreate(ctx, CreateSessionOptions{})
	if err != nil {
		t.Fatalf("OpenOrCreate(automatic ID) error = %v", err)
	}
	if !created || automatic.Session().ID == "" || automatic.Session().ID == "stable" {
		t.Fatalf("automatic session = %#v, created %v", automatic.Session(), created)
	}
}

func TestSessionManagerMetadataArchiveListAndDelete(t *testing.T) {
	ctx := context.Background()
	store := NewMemorySessionStore()
	manager := newTestSessionManager(t, store, "owner")
	defer manager.Close()

	agent, err := manager.CreateWithOptions(ctx, CreateSessionOptions{ID: "session", Title: "Before"})
	if err != nil {
		t.Fatal(err)
	}
	updated, err := manager.UpdateMetadata(ctx, "session", SessionMetadata{
		Title: " After ", Tags: []string{"support", " support "},
	})
	if err != nil {
		t.Fatalf("UpdateMetadata() error = %v", err)
	}
	if updated.Title != "After" || updated.OwnerID != "owner" || !slices.Equal(updated.Tags, []string{"support"}) {
		t.Fatalf("updated metadata = %#v", updated.SessionMetadata)
	}
	if agent.Session().Title != "After" {
		t.Fatalf("active Agent metadata = %#v", agent.Session().SessionMetadata)
	}
	if err := manager.Unarchive(ctx, "session"); err != nil {
		t.Fatalf("Unarchive(active non-archived) error = %v", err)
	}
	if opened, err := manager.Open(ctx, "session"); err != nil || opened != agent {
		t.Fatalf("Unarchive(active non-archived) replaced Agent: %p, %v", opened, err)
	}

	notArchived := false
	page, err := manager.List(ctx, SessionQuery{Archived: &notArchived})
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(page.Sessions) != 1 || page.Sessions[0].ID != "session" || page.Sessions[0].Title != "After" {
		t.Fatalf("page = %#v", page)
	}

	if err := manager.Archive(ctx, "session"); err != nil {
		t.Fatalf("Archive() error = %v", err)
	}
	if len(manager.ActiveSessionIDs()) != 0 {
		t.Fatalf("active sessions after archive = %v", manager.ActiveSessionIDs())
	}
	if _, err := manager.Open(ctx, "session"); !errors.Is(err, ErrSessionArchived) {
		t.Fatalf("Open(archived) error = %v, want ErrSessionArchived", err)
	}
	archived := true
	page, err = manager.List(ctx, SessionQuery{Archived: &archived})
	if err != nil || len(page.Sessions) != 1 || !page.Sessions[0].Archived {
		t.Fatalf("archived page = %#v, error = %v", page, err)
	}

	if err := manager.Unarchive(ctx, "session"); err != nil {
		t.Fatalf("Unarchive() error = %v", err)
	}
	if _, err := manager.Open(ctx, "session"); err != nil {
		t.Fatalf("Open(unarchived) error = %v", err)
	}
	if err := manager.CloseSession(ctx, "session"); err != nil {
		t.Fatalf("CloseSession(before inactive update) error = %v", err)
	}
	inactive, err := manager.UpdateMetadata(ctx, "session", SessionMetadata{Title: "Inactive update"})
	if err != nil {
		t.Fatalf("UpdateMetadata(inactive) error = %v", err)
	}
	if inactive.Title != "Inactive update" || inactive.OwnerID != "owner" {
		t.Fatalf("inactive metadata = %#v", inactive.SessionMetadata)
	}
	if err := manager.Delete(ctx, "session"); err != nil {
		t.Fatalf("Delete() error = %v", err)
	}
	if _, err := store.Load(ctx, "session"); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("Load(deleted) error = %v, want ErrSessionNotFound", err)
	}
	if err := manager.Delete(ctx, "session"); err != nil {
		t.Fatalf("Delete(missing) error = %v", err)
	}
}

func TestSessionManagerOwnerIsolation(t *testing.T) {
	ctx := context.Background()
	store := NewMemorySessionStore()
	if err := store.Save(ctx, &Session{ID: "other", SessionMetadata: SessionMetadata{OwnerID: "owner-2"}}); err != nil {
		t.Fatal(err)
	}
	manager := newTestSessionManager(t, store, "owner-1")
	defer manager.Close()

	if _, err := manager.Open(ctx, "other"); !errors.Is(err, ErrSessionAccessDenied) {
		t.Fatalf("Open(other owner) error = %v", err)
	}
	if _, err := manager.Get(ctx, "other"); !errors.Is(err, ErrSessionAccessDenied) {
		t.Fatalf("Get(other owner) error = %v", err)
	}
	if _, err := manager.UpdateMetadata(ctx, "other", SessionMetadata{}); !errors.Is(err, ErrSessionAccessDenied) {
		t.Fatalf("UpdateMetadata(other owner) error = %v", err)
	}
	if err := manager.Archive(ctx, "other"); !errors.Is(err, ErrSessionAccessDenied) {
		t.Fatalf("Archive(other owner) error = %v", err)
	}
	if err := manager.Delete(ctx, "other"); !errors.Is(err, ErrSessionAccessDenied) {
		t.Fatalf("Delete(other owner) error = %v", err)
	}
	if _, err := manager.List(ctx, SessionQuery{OwnerID: "owner-2"}); !errors.Is(err, ErrSessionAccessDenied) {
		t.Fatalf("List(other owner) error = %v", err)
	}
	if _, err := manager.CreateWithOptions(ctx, CreateSessionOptions{ID: "bad", OwnerID: "owner-2"}); !errors.Is(err, ErrSessionAccessDenied) {
		t.Fatalf("Create(other owner) error = %v", err)
	}
	if _, err := store.Load(ctx, "other"); err != nil {
		t.Fatalf("other owner's session was changed: %v", err)
	}
}

func TestSessionManagerRejectsUnauthorizedFactoryAgent(t *testing.T) {
	ctx := context.Background()
	authoritative := NewMemorySessionStore()
	foreign := NewMemorySessionStore()
	if err := authoritative.Save(ctx, &Session{ID: "same-id", SessionMetadata: SessionMetadata{OwnerID: "owner-1"}}); err != nil {
		t.Fatal(err)
	}
	if err := foreign.Save(ctx, &Session{ID: "same-id", SessionMetadata: SessionMetadata{OwnerID: "owner-2"}}); err != nil {
		t.Fatal(err)
	}
	var returned *Agent
	manager, err := NewSessionManager(&SessionManagerConfig{
		Store:   authoritative,
		OwnerID: "owner-1",
		AgentFactory: func(ctx context.Context, session SessionConfig) (*Agent, error) {
			var err error
			returned, err = New(ctx, &Config{
				Name: "assistant", Model: NewMockChatModel(),
				Session: &SessionConfig{ID: session.ID, Store: foreign},
			})
			return returned, err
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	if _, err := manager.Open(ctx, "same-id"); !errors.Is(err, ErrSessionAccessDenied) {
		t.Fatalf("Open() error = %v, want ErrSessionAccessDenied", err)
	}
	if returned == nil || !agentCloseStarted(returned) {
		t.Fatal("unauthorized factory Agent was not closed")
	}
	if len(manager.ActiveSessionIDs()) != 0 {
		t.Fatalf("unauthorized Agent became active: %v", manager.ActiveSessionIDs())
	}
}

func TestSessionManagerForkCopiesConversationWithoutOperationalState(t *testing.T) {
	ctx := context.Background()
	store := NewMemorySessionStore()
	manager := newTestSessionManager(t, store, "owner")
	defer manager.Close()

	source, err := manager.CreateWithOptions(ctx, CreateSessionOptions{ID: "source", Title: "Source", Tags: []string{"old"}})
	if err != nil {
		t.Fatal(err)
	}
	source.SetHistory([]*schema.Message{schema.UserMessage("hello"), schema.AssistantMessage("world", nil)})
	if err := source.SaveSession(ctx); err != nil {
		t.Fatal(err)
	}
	sourceSnapshot := source.Session()

	target, err := manager.Fork(ctx, "source", CreateSessionOptions{ID: "target", Title: "Branch", Tags: []string{"new"}})
	if err != nil {
		t.Fatalf("Fork() error = %v", err)
	}
	targetSnapshot := target.Session()
	if targetSnapshot.ID != "target" || targetSnapshot.Title != "Branch" || targetSnapshot.OwnerID != "owner" {
		t.Fatalf("target metadata = %#v", targetSnapshot)
	}
	if got := schemaMessageContents(targetSnapshot.Messages); !slices.Equal(got, []string{"hello", "world"}) {
		t.Fatalf("target messages = %v", got)
	}
	if targetSnapshot.CheckpointID == "" || targetSnapshot.CheckpointID == sourceSnapshot.CheckpointID {
		t.Fatalf("target checkpoint = %q, source = %q", targetSnapshot.CheckpointID, sourceSnapshot.CheckpointID)
	}
	if len(targetSnapshot.PendingInterrupts) != 0 || targetSnapshot.Archived {
		t.Fatalf("target operational state = %#v", targetSnapshot)
	}

	target.SetHistory([]*schema.Message{schema.UserMessage("changed")})
	if got := schemaMessageContents(source.History()); !slices.Equal(got, []string{"hello", "world"}) {
		t.Fatalf("source history changed through fork: %v", got)
	}
}

func TestSessionManagerForkWaitsForSourceRunToSettle(t *testing.T) {
	ctx := context.Background()
	store := NewMemorySessionStore()
	started := make(chan struct{})
	release := make(chan struct{})
	waitTool := MustMockTool("wait", "wait for release", func(ctx context.Context, _ string) (string, error) {
		close(started)
		select {
		case <-release:
			return "released", nil
		case <-ctx.Done():
			return "", ctx.Err()
		}
	})
	manager, err := NewSessionManager(&SessionManagerConfig{
		Store: store,
		AgentConfig: &Config{
			Name: "assistant",
			Model: NewMockChatModel(
				MockModelToolCallWithID("wait-call", "wait", `""`),
				MockModelText("done"),
			),
			Tools: MockTools(waitTool),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	source, err := manager.CreateWithOptions(ctx, CreateSessionOptions{ID: "source"})
	if err != nil {
		t.Fatal(err)
	}
	runDone := make(chan error, 1)
	go func() { runDone <- source.Prompt(ctx, "start") }()
	<-started

	forkCtx, cancelFork := context.WithTimeout(ctx, 20*time.Millisecond)
	_, err = manager.Fork(forkCtx, "source", CreateSessionOptions{ID: "too-early"})
	cancelFork()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Fork(running source) error = %v, want deadline exceeded", err)
	}
	if _, err := store.Load(ctx, "too-early"); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("timed-out Fork created a target: %v", err)
	}

	close(release)
	if err := <-runDone; err != nil {
		t.Fatalf("Prompt() error = %v", err)
	}
	target, err := manager.Fork(ctx, "source", CreateSessionOptions{ID: "settled"})
	if err != nil {
		t.Fatalf("Fork(settled source) error = %v", err)
	}
	contents := schemaMessageContents(target.History())
	if !slices.Equal(contents, []string{"start", "", "released", "done"}) {
		t.Fatalf("forked history = %v", contents)
	}
}

func TestSessionManagerSerializesConcurrentOpen(t *testing.T) {
	ctx := context.Background()
	store := NewMemorySessionStore()
	if err := store.Save(ctx, &Session{ID: "session"}); err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	manager, err := NewSessionManager(&SessionManagerConfig{
		Store: store,
		AgentFactory: func(ctx context.Context, session SessionConfig) (*Agent, error) {
			calls.Add(1)
			time.Sleep(20 * time.Millisecond)
			return New(ctx, &Config{Name: "assistant", Model: NewMockChatModel(), Session: &session})
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()

	const workers = 16
	agents := make([]*Agent, workers)
	errs := make([]error, workers)
	var wait sync.WaitGroup
	wait.Add(workers)
	for index := range workers {
		go func() {
			defer wait.Done()
			agents[index], errs[index] = manager.Open(ctx, "session")
		}()
	}
	wait.Wait()
	for index, err := range errs {
		if err != nil {
			t.Fatalf("Open(%d) error = %v", index, err)
		}
		if agents[index] != agents[0] {
			t.Fatalf("Open(%d) returned a different Agent", index)
		}
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("factory calls = %d, want 1", got)
	}
}

func TestSessionManagerWaitingOpenHonorsContext(t *testing.T) {
	ctx := context.Background()
	store := NewMemorySessionStore()
	if err := store.Save(ctx, &Session{ID: "session"}); err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{})
	proceed := make(chan struct{})
	manager, err := NewSessionManager(&SessionManagerConfig{
		Store: store,
		AgentFactory: func(ctx context.Context, session SessionConfig) (*Agent, error) {
			close(started)
			<-proceed
			return New(ctx, &Config{Name: "assistant", Model: NewMockChatModel(), Session: &session})
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	firstDone := make(chan error, 1)
	go func() {
		_, openErr := manager.Open(ctx, "session")
		firstDone <- openErr
	}()
	<-started

	waitCtx, cancel := context.WithTimeout(ctx, 20*time.Millisecond)
	defer cancel()
	if _, err := manager.Open(waitCtx, "session"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("waiting Open() error = %v, want deadline exceeded", err)
	}
	close(proceed)
	if err := <-firstDone; err != nil {
		t.Fatalf("first Open() error = %v", err)
	}
}

func TestSessionManagerFactoryFailureKeepsCreatedSession(t *testing.T) {
	ctx := context.Background()
	store := NewMemorySessionStore()
	want := errors.New("model unavailable")
	manager, err := NewSessionManager(&SessionManagerConfig{
		Store: store,
		AgentFactory: func(context.Context, SessionConfig) (*Agent, error) {
			return nil, want
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	if _, err := manager.CreateWithOptions(ctx, CreateSessionOptions{ID: "created"}); !errors.Is(err, want) {
		t.Fatalf("CreateWithOptions() error = %v, want %v", err, want)
	}
	if _, err := store.Load(ctx, "created"); err != nil {
		t.Fatalf("created session was not retained: %v", err)
	}
	if _, err := manager.CreateWithOptions(ctx, CreateSessionOptions{ID: "created"}); !errors.Is(err, ErrSessionAlreadyExists) {
		t.Fatalf("duplicate CreateWithOptions() error = %v", err)
	}
	_, created, err := manager.OpenOrCreate(ctx, CreateSessionOptions{ID: "another"})
	if !created || !errors.Is(err, want) {
		t.Fatalf("OpenOrCreate(factory failure) created = %v, error = %v", created, err)
	}
}

func TestSessionManagerCloseClosesAgentsAndRejectsOperations(t *testing.T) {
	ctx := context.Background()
	manager := newTestSessionManager(t, NewMemorySessionStore(), "")
	agent, err := manager.CreateWithOptions(ctx, CreateSessionOptions{ID: "session"})
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if !agentCloseStarted(agent) {
		t.Fatal("active Agent was not closed")
	}
	if err := manager.Close(); err != nil {
		t.Fatalf("second Close() error = %v", err)
	}
	if _, err := manager.Open(ctx, "session"); !errors.Is(err, ErrSessionManagerClosed) {
		t.Fatalf("Open(closed manager) error = %v", err)
	}
	if _, err := manager.List(ctx, SessionQuery{}); !errors.Is(err, ErrSessionManagerClosed) {
		t.Fatalf("List(closed manager) error = %v", err)
	}
}

func TestSessionManagerValidatesConfigurationAndCallbacks(t *testing.T) {
	store := NewMemorySessionStore()
	model := NewMockChatModel()
	tests := []struct {
		name string
		cfg  *SessionManagerConfig
	}{
		{name: "nil config"},
		{name: "nil store", cfg: &SessionManagerConfig{AgentConfig: &Config{Model: model}}},
		{name: "missing builder", cfg: &SessionManagerConfig{Store: store}},
		{name: "multiple builders", cfg: &SessionManagerConfig{Store: store, AgentConfig: &Config{Model: model}, AgentFactory: func(context.Context, SessionConfig) (*Agent, error) { return nil, nil }}},
		{name: "template session", cfg: &SessionManagerConfig{Store: store, AgentConfig: &Config{Model: model, Session: &SessionConfig{ID: "x", Store: store}}}},
		{name: "template history", cfg: &SessionManagerConfig{Store: store, AgentConfig: &Config{Model: model, History: []*schema.Message{}}}},
		{name: "invalid template", cfg: &SessionManagerConfig{Store: store, AgentConfig: &Config{}}},
		{name: "owner whitespace", cfg: &SessionManagerConfig{Store: store, OwnerID: " owner ", AgentConfig: &Config{Model: model}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if manager, err := NewSessionManager(tt.cfg); err == nil || manager != nil {
				t.Fatalf("NewSessionManager() = %#v, %v, want error", manager, err)
			}
		})
	}

	panicFactory, err := NewSessionManager(&SessionManagerConfig{
		Store: store,
		AgentFactory: func(context.Context, SessionConfig) (*Agent, error) {
			panic("boom")
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer panicFactory.Close()
	if _, err := panicFactory.CreateWithOptions(context.Background(), CreateSessionOptions{ID: "panic"}); !errors.Is(err, ErrSessionFactoryPanic) {
		t.Fatalf("panic factory error = %v", err)
	}

	panicID, err := NewSessionManager(&SessionManagerConfig{
		Store: store, AgentConfig: &Config{Model: model}, IDGenerator: func() string { panic("boom") },
	})
	if err != nil {
		t.Fatal(err)
	}
	defer panicID.Close()
	if _, err := panicID.Create(context.Background()); !errors.Is(err, ErrSessionIDGeneratorPanic) {
		t.Fatalf("panic ID generator error = %v", err)
	}
}

func newTestSessionManager(t *testing.T, store SessionStore, ownerID string) *SessionManager {
	t.Helper()
	manager, err := NewSessionManager(&SessionManagerConfig{
		Store: store, OwnerID: ownerID,
		AgentConfig: &Config{Name: "assistant", Model: NewMockChatModel()},
	})
	if err != nil {
		t.Fatalf("NewSessionManager() error = %v", err)
	}
	return manager
}
