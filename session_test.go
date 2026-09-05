package agentkit

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/cloudwego/eino/schema"
)

func TestMemorySessionStoreCopiesAndListsSessions(t *testing.T) {
	ctx := context.Background()
	store := &MemorySessionStore{}
	older := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	newer := older.Add(time.Hour)
	session := &Session{
		ID:        "older",
		CreatedAt: older,
		UpdatedAt: older,
		Messages:  []*schema.Message{schema.UserMessage("original")},
	}
	if err := store.Save(ctx, session); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	if err := store.Save(ctx, &Session{ID: "newer", CreatedAt: newer, UpdatedAt: newer}); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	session.Messages[0].Content = "changed by caller"
	loaded, err := store.Load(ctx, "older")
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if got := loaded.Messages[0].Content; got != "original" {
		t.Fatalf("loaded content = %q, want %q", got, "original")
	}
	if loaded.Revision != 1 {
		t.Fatalf("loaded revision = %d, want 1", loaded.Revision)
	}
	loaded.Messages[0].Content = "changed after load"
	reloaded, err := store.Load(ctx, "older")
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if got := reloaded.Messages[0].Content; got != "original" {
		t.Fatalf("reloaded content = %q, want %q", got, "original")
	}

	infos, err := store.List(ctx)
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if got := []string{infos[0].ID, infos[1].ID}; !slices.Equal(got, []string{"newer", "older"}) {
		t.Fatalf("session order = %v, want [newer older]", got)
	}
	if infos[1].MessageCount != 1 {
		t.Fatalf("message count = %d, want 1", infos[1].MessageCount)
	}
	if infos[1].Revision != 1 {
		t.Fatalf("session info revision = %d, want 1", infos[1].Revision)
	}

	if err := store.Delete(ctx, "older"); err != nil {
		t.Fatalf("Delete() error = %v", err)
	}
	if _, err := store.Load(ctx, "older"); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("Load() after delete error = %v, want ErrSessionNotFound", err)
	}
}

type sessionStoreWithResources interface {
	SessionStore
	CheckpointStoreProvider
	GoalStoreProvider
	ToolResultStoreProvider
}

func TestBuiltInSessionStoreDeleteCascadesResources(t *testing.T) {
	tests := map[string]func(*testing.T) sessionStoreWithResources{
		"memory": func(*testing.T) sessionStoreWithResources {
			return NewMemorySessionStore()
		},
		"file": func(t *testing.T) sessionStoreWithResources {
			store, err := NewFileSessionStore(t.TempDir())
			if err != nil {
				t.Fatalf("NewFileSessionStore() error = %v", err)
			}
			return store
		},
	}
	for name, newStore := range tests {
		t.Run(name, func(t *testing.T) {
			testSessionStoreDeleteCascadesResources(t, newStore(t))
		})
	}
}

func testSessionStoreDeleteCascadesResources(t *testing.T, store sessionStoreWithResources) {
	t.Helper()
	ctx := context.Background()
	const targetSession = "target-session"
	const keptSession = "kept-session"
	if err := store.Save(ctx, &Session{ID: targetSession, CheckpointID: "target-checkpoint"}); err != nil {
		t.Fatalf("save target session: %v", err)
	}
	if err := store.Save(ctx, &Session{ID: keptSession, CheckpointID: "kept-checkpoint"}); err != nil {
		t.Fatalf("save kept session: %v", err)
	}
	checkpoints := store.CheckpointStore()
	if err := checkpoints.Set(ctx, "target-checkpoint", []byte("target")); err != nil {
		t.Fatalf("save target checkpoint: %v", err)
	}
	if err := checkpoints.Set(ctx, "kept-checkpoint", []byte("kept")); err != nil {
		t.Fatalf("save kept checkpoint: %v", err)
	}
	goals := store.GoalStore()
	for _, goal := range []*Goal{
		{ID: "target-goal", SessionID: targetSession, Objective: "delete", Status: GoalStatusPaused, MaxIterations: 2},
		{ID: "kept-goal", SessionID: keptSession, Objective: "keep", Status: GoalStatusPaused, MaxIterations: 2},
	} {
		if err := goals.Save(ctx, goal); err != nil {
			t.Fatalf("save goal %q: %v", goal.ID, err)
		}
	}
	results := store.ToolResultStore()
	for _, result := range []*StoredToolResult{
		{ID: "target-result", SessionID: targetSession, Content: "delete"},
		{ID: "kept-result", SessionID: keptSession, Content: "keep"},
		{ID: "unscoped-result", Content: "keep"},
	} {
		if err := results.Save(ctx, result); err != nil {
			t.Fatalf("save tool result %q: %v", result.ID, err)
		}
	}

	if err := store.Delete(ctx, targetSession); err != nil {
		t.Fatalf("delete target session: %v", err)
	}
	if _, err := store.Load(ctx, targetSession); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("target session still exists: %v", err)
	}
	if _, existed, err := checkpoints.Get(ctx, "target-checkpoint"); err != nil || existed {
		t.Fatalf("target checkpoint exists = %v, error = %v", existed, err)
	}
	if _, err := goals.Load(ctx, "target-goal"); !errors.Is(err, ErrGoalNotFound) {
		t.Fatalf("target goal still exists: %v", err)
	}
	if _, err := results.Load(ctx, "target-result"); !errors.Is(err, ErrToolResultNotFound) {
		t.Fatalf("target tool result still exists: %v", err)
	}
	if _, err := store.Load(ctx, keptSession); err != nil {
		t.Fatalf("kept session was deleted: %v", err)
	}
	if _, existed, err := checkpoints.Get(ctx, "kept-checkpoint"); err != nil || !existed {
		t.Fatalf("kept checkpoint exists = %v, error = %v", existed, err)
	}
	if _, err := goals.Load(ctx, "kept-goal"); err != nil {
		t.Fatalf("kept goal was deleted: %v", err)
	}
	for _, id := range []string{"kept-result", "unscoped-result"} {
		if _, err := results.Load(ctx, id); err != nil {
			t.Fatalf("tool result %q was deleted: %v", id, err)
		}
	}

	if err := goals.Save(ctx, &Goal{
		ID: "orphan-goal", SessionID: targetSession, Objective: "delete", Status: GoalStatusPaused, MaxIterations: 2,
	}); err != nil {
		t.Fatalf("save orphan goal: %v", err)
	}
	if err := results.Save(ctx, &StoredToolResult{
		ID: "orphan-result", SessionID: targetSession, Content: "delete",
	}); err != nil {
		t.Fatalf("save orphan result: %v", err)
	}
	if err := store.Delete(ctx, targetSession); err != nil {
		t.Fatalf("repeat delete target session: %v", err)
	}
	if _, err := goals.Load(ctx, "orphan-goal"); !errors.Is(err, ErrGoalNotFound) {
		t.Fatalf("orphan goal still exists: %v", err)
	}
	if _, err := results.Load(ctx, "orphan-result"); !errors.Is(err, ErrToolResultNotFound) {
		t.Fatalf("orphan result still exists: %v", err)
	}
}

func TestFileSessionStoreKeepsSessionWhenResourceCleanupFails(t *testing.T) {
	ctx := context.Background()
	store, err := NewFileSessionStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewFileSessionStore() error = %v", err)
	}
	if err := store.Save(ctx, &Session{ID: "session"}); err != nil {
		t.Fatalf("save session: %v", err)
	}
	brokenGoal := filepath.Join(store.goals.dir, "broken.json")
	if err := os.WriteFile(brokenGoal, []byte("not JSON"), 0o600); err != nil {
		t.Fatalf("write broken goal: %v", err)
	}
	if err := store.Delete(ctx, "session"); err == nil {
		t.Fatal("delete with broken child error = nil, want error")
	}
	if _, err := store.Load(ctx, "session"); err != nil {
		t.Fatalf("session was deleted after cleanup failure: %v", err)
	}
	if err := os.Remove(brokenGoal); err != nil {
		t.Fatalf("remove broken goal: %v", err)
	}
	if err := store.Delete(ctx, "session"); err != nil {
		t.Fatalf("retry delete session: %v", err)
	}
	if _, err := store.Load(ctx, "session"); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("session still exists after retry: %v", err)
	}
}

func TestFileSessionStoreRoundTripAndSafeFileName(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	store, err := NewFileSessionStore(dir)
	if err != nil {
		t.Fatalf("NewFileSessionStore() error = %v", err)
	}
	session := &Session{
		ID:        "../../outside",
		CreatedAt: time.Now().UTC().Add(-time.Minute),
		UpdatedAt: time.Now().UTC(),
		Messages: []*schema.Message{
			schema.UserMessage("hello"),
			schema.AssistantMessage("world", nil),
		},
		Context: []*schema.Message{schema.UserMessage("summary")},
	}
	if err := store.Save(ctx, session); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	broken := cloneSession(session)
	broken.Revision = 1
	broken.Messages[0].Content = "must not replace the valid session"
	broken.Messages[0].Extra = map[string]any{"unsupported": make(chan int)}
	if err := store.Save(ctx, broken); err == nil {
		t.Fatal("Save() with unsupported JSON value error = nil, want error")
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir() error = %v", err)
	}
	var sessionEntries []os.DirEntry
	for _, entry := range entries {
		if !entry.IsDir() && filepath.Ext(entry.Name()) == ".json" {
			sessionEntries = append(sessionEntries, entry)
		}
	}
	if len(sessionEntries) != 1 {
		t.Fatalf("session files = %v, want one JSON file", entries)
	}
	if sessionEntries[0].Name() == "outside.json" {
		t.Fatalf("unsafe session file name = %q", sessionEntries[0].Name())
	}
	fileInfo, err := sessionEntries[0].Info()
	if err != nil {
		t.Fatalf("session file Info() error = %v", err)
	}
	if got := fileInfo.Mode().Perm(); got != 0o600 {
		t.Fatalf("session file permissions = %o, want 600", got)
	}

	loaded, err := store.Load(ctx, session.ID)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if loaded.ID != session.ID || len(loaded.Messages) != 2 || loaded.Messages[1].Content != "world" || len(loaded.Context) != 1 {
		t.Fatalf("loaded session = %#v", loaded)
	}
	if loaded.Revision != 1 {
		t.Fatalf("loaded revision = %d, want 1", loaded.Revision)
	}
	if loaded.Messages[0].Content != "hello" {
		t.Fatalf("failed save replaced valid content with %q", loaded.Messages[0].Content)
	}
	infos, err := store.List(ctx)
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(infos) != 1 || infos[0].ID != session.ID || infos[0].MessageCount != 2 || infos[0].ContextMessageCount != 1 {
		t.Fatalf("session infos = %#v", infos)
	}

	if err := store.Delete(ctx, session.ID); err != nil {
		t.Fatalf("Delete() error = %v", err)
	}
	if _, err := store.Load(ctx, session.ID); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("Load() after delete error = %v, want ErrSessionNotFound", err)
	}
}

func TestAgentSessionAutomaticallyPersistsAndRestores(t *testing.T) {
	ctx := context.Background()
	store := NewMemorySessionStore()
	first, err := New(ctx, &Config{
		Name:  "assistant",
		Model: NewMockChatModel(MockModelText("first response")),
		Session: &SessionConfig{
			ID:    "conversation-1",
			Store: store,
		},
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if err := first.Prompt(ctx, "first prompt"); err != nil {
		t.Fatalf("Prompt() error = %v", err)
	}
	_ = first.Close()

	persisted, err := store.Load(ctx, "conversation-1")
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if len(persisted.Messages) != 2 {
		t.Fatalf("persisted messages = %d, want 2", len(persisted.Messages))
	}

	secondModel := NewMockChatModel(MockExpect(MockModelText("second response"), func(call MockModelCall) error {
		contents := schemaMessageContents(call.Input)
		want := []string{"first prompt", "first response", "second prompt"}
		if !slices.Equal(contents, want) {
			return fmt.Errorf("model input = %v, want %v", contents, want)
		}
		return nil
	}))
	second, err := New(ctx, &Config{
		Name:  "assistant",
		Model: secondModel,
		Session: &SessionConfig{
			ID:    "conversation-1",
			Store: store,
		},
	})
	if err != nil {
		t.Fatalf("New() restore error = %v", err)
	}
	defer second.Close()
	if got := len(second.History()); got != 2 {
		t.Fatalf("restored history length = %d, want 2", got)
	}
	if err := second.Prompt(ctx, "second prompt"); err != nil {
		t.Fatalf("second Prompt() error = %v", err)
	}

	snapshot := second.Session()
	if snapshot == nil || snapshot.ID != "conversation-1" || len(snapshot.Messages) != 4 {
		t.Fatalf("Session() = %#v", snapshot)
	}
	if snapshot.Revision != 2 {
		t.Fatalf("session revision = %d, want 2", snapshot.Revision)
	}
	if snapshot.CreatedAt.IsZero() || snapshot.UpdatedAt.Before(snapshot.CreatedAt) {
		t.Fatalf("session timestamps = created %v, updated %v", snapshot.CreatedAt, snapshot.UpdatedAt)
	}
	snapshot.Messages[0].Content = "changed snapshot"
	if got := second.History()[0].Content; got != "first prompt" {
		t.Fatalf("mutating Session() changed agent history to %q", got)
	}
}

func TestSaveSessionRejectsPartialRunningSnapshot(t *testing.T) {
	ctx := context.Background()
	store := NewMemorySessionStore()
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
		Name: "assistant",
		Model: NewMockChatModel(
			MockModelToolCallWithID("wait-call", "wait", `""`),
			MockModelText("done"),
		),
		Tools:   MockTools(tool),
		Session: &SessionConfig{ID: "session", Store: store},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer agent.Close()
	runDone := make(chan error, 1)
	go func() { runDone <- agent.Prompt(ctx, "start") }()
	<-started

	if err := agent.SaveSession(ctx); !errors.Is(err, ErrAgentRunning) {
		t.Fatalf("SaveSession() error = %v, want ErrAgentRunning", err)
	}
	if _, err := store.Load(ctx, "session"); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("running snapshot was persisted: %v", err)
	}

	close(release)
	if err := <-runDone; err != nil {
		t.Fatalf("Prompt() error = %v", err)
	}
	saved, err := store.Load(ctx, "session")
	if err != nil {
		t.Fatal(err)
	}
	if got := schemaMessageContents(saved.Messages); !slices.Equal(got, []string{"start", "", "released", "done"}) {
		t.Fatalf("saved history = %v", got)
	}
}

func TestMemorySessionStoreRejectsStaleRevision(t *testing.T) {
	ctx := context.Background()
	store := NewMemorySessionStore()
	if err := store.Save(ctx, &Session{ID: "shared", Messages: []*schema.Message{schema.UserMessage("initial")}}); err != nil {
		t.Fatalf("initial Save() error = %v", err)
	}

	first, err := store.Load(ctx, "shared")
	if err != nil {
		t.Fatalf("first Load() error = %v", err)
	}
	stale, err := store.Load(ctx, "shared")
	if err != nil {
		t.Fatalf("second Load() error = %v", err)
	}
	first.Messages[0].Content = "first writer"
	if err := store.Save(ctx, first); err != nil {
		t.Fatalf("first update Save() error = %v", err)
	}
	stale.Messages[0].Content = "stale writer"
	if err := store.Save(ctx, stale); !errors.Is(err, ErrSessionConflict) {
		t.Fatalf("stale Save() error = %v, want ErrSessionConflict", err)
	}

	persisted, err := store.Load(ctx, "shared")
	if err != nil {
		t.Fatalf("final Load() error = %v", err)
	}
	if persisted.Revision != 2 || persisted.Messages[0].Content != "first writer" {
		t.Fatalf("persisted session = %#v, want revision 2 from first writer", persisted)
	}
}

func TestFileSessionStoreRejectsStaleRevisionAfterReopen(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	firstStore, err := NewFileSessionStore(dir)
	if err != nil {
		t.Fatalf("NewFileSessionStore() error = %v", err)
	}
	if err := firstStore.Save(ctx, &Session{ID: "shared", Messages: []*schema.Message{schema.UserMessage("initial")}}); err != nil {
		t.Fatalf("initial Save() error = %v", err)
	}
	stale, err := firstStore.Load(ctx, "shared")
	if err != nil {
		t.Fatalf("stale Load() error = %v", err)
	}

	secondStore, err := NewFileSessionStore(dir)
	if err != nil {
		t.Fatalf("reopen NewFileSessionStore() error = %v", err)
	}
	current, err := secondStore.Load(ctx, "shared")
	if err != nil {
		t.Fatalf("current Load() error = %v", err)
	}
	current.Messages[0].Content = "second store"
	if err := secondStore.Save(ctx, current); err != nil {
		t.Fatalf("current Save() error = %v", err)
	}
	stale.Messages[0].Content = "stale first store"
	if err := firstStore.Save(ctx, stale); !errors.Is(err, ErrSessionConflict) {
		t.Fatalf("stale Save() error = %v, want ErrSessionConflict", err)
	}
}

func TestFileSessionStoreUpgradesLegacyRevisionOnSave(t *testing.T) {
	ctx := context.Background()
	store, err := NewFileSessionStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewFileSessionStore() error = %v", err)
	}
	const legacy = `{"version":1,"session":{"id":"legacy","messages":[]}}`
	if err := os.WriteFile(store.path("legacy"), []byte(legacy), 0o600); err != nil {
		t.Fatalf("write legacy session error = %v", err)
	}

	loaded, err := store.Load(ctx, "legacy")
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if loaded.Revision != 0 {
		t.Fatalf("legacy revision = %d, want 0", loaded.Revision)
	}
	if err := store.Save(ctx, loaded); err != nil {
		t.Fatalf("Save() legacy session error = %v", err)
	}
	upgraded, err := store.Load(ctx, "legacy")
	if err != nil {
		t.Fatalf("Load() upgraded session error = %v", err)
	}
	if upgraded.Revision != 1 {
		t.Fatalf("upgraded revision = %d, want 1", upgraded.Revision)
	}
}

func TestAgentsReportConcurrentSessionConflict(t *testing.T) {
	ctx := context.Background()
	store := NewMemorySessionStore()
	newAgent := func(response string) *Agent {
		agent, err := New(ctx, &Config{
			Name:  "assistant",
			Model: NewMockChatModel(MockModelText(response)),
			Session: &SessionConfig{
				ID:    "shared",
				Store: store,
			},
		})
		if err != nil {
			t.Fatalf("New() error = %v", err)
		}
		return agent
	}

	first := newAgent("first response")
	defer first.Close()
	stale := newAgent("stale response")
	defer stale.Close()
	if err := first.Prompt(ctx, "first prompt"); err != nil {
		t.Fatalf("first Prompt() error = %v", err)
	}
	if err := stale.Prompt(ctx, "stale prompt"); !errors.Is(err, ErrSessionConflict) {
		t.Fatalf("stale Prompt() error = %v, want ErrSessionConflict", err)
	}

	persisted, err := store.Load(ctx, "shared")
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if got := schemaMessageContents(persisted.Messages); !slices.Equal(got, []string{"first prompt", "first response"}) {
		t.Fatalf("persisted messages = %v, want first Agent history", got)
	}
}

func TestSaveSessionWithoutConfiguration(t *testing.T) {
	agent, err := New(context.Background(), &Config{
		Name:  "assistant",
		Model: NewMockChatModel(),
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer agent.Close()
	if err := agent.SaveSession(context.Background()); !errors.Is(err, ErrSessionDisabled) {
		t.Fatalf("SaveSession() error = %v, want ErrSessionDisabled", err)
	}
}

func TestAgentSessionPersistsFailedRunAndReportsSaveFailure(t *testing.T) {
	ctx := context.Background()
	runErr := errors.New("model failed")
	store := &failingSessionStore{saveErr: errors.New("disk full")}
	agent, err := New(ctx, &Config{
		Name:  "assistant",
		Model: NewMockChatModel(MockModelError(runErr)),
		Session: &SessionConfig{
			ID:    "conversation-1",
			Store: store,
		},
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer agent.Close()

	events := newMockEventRecorder()
	agent.Subscribe(events.Record)
	err = agent.Prompt(ctx, "keep this")
	if !errors.Is(err, runErr) || !errors.Is(err, store.saveErr) {
		t.Fatalf("Prompt() error = %v, want joined model and save errors", err)
	}
	if len(store.savedMessages) != 1 || store.savedMessages[0].Content != "keep this" {
		t.Fatalf("save attempt messages = %#v", store.savedMessages)
	}
	errorEvent := events.Last(EventError)
	if errorEvent == nil || !errors.Is(errorEvent.Error, store.saveErr) {
		t.Fatalf("last error event = %#v, want save error", errorEvent)
	}
}

type failingSessionStore struct {
	saveErr       error
	savedMessages []*schema.Message
}

func (s *failingSessionStore) Load(context.Context, string) (*Session, error) {
	return nil, ErrSessionNotFound
}

func (s *failingSessionStore) Save(_ context.Context, session *Session) error {
	s.savedMessages = cloneHistoryMessages(session.Messages)
	return s.saveErr
}

func (s *failingSessionStore) Delete(context.Context, string) error { return nil }

func (s *failingSessionStore) List(context.Context) ([]SessionInfo, error) { return nil, nil }

func schemaMessageContents(messages []*schema.Message) []string {
	contents := make([]string, 0, len(messages))
	for _, message := range messages {
		if message.Role != schema.System {
			contents = append(contents, message.Content)
		}
	}
	return contents
}
