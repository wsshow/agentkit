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

	if err := store.Delete(ctx, "older"); err != nil {
		t.Fatalf("Delete() error = %v", err)
	}
	if _, err := store.Load(ctx, "older"); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("Load() after delete error = %v, want ErrSessionNotFound", err)
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
	}
	if err := store.Save(ctx, session); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	broken := cloneSession(session)
	broken.Messages[0].Content = "must not replace the valid session"
	broken.Messages[0].Extra = map[string]any{"unsupported": make(chan int)}
	if err := store.Save(ctx, broken); err == nil {
		t.Fatal("Save() with unsupported JSON value error = nil, want error")
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir() error = %v", err)
	}
	if len(entries) != 1 || filepath.Ext(entries[0].Name()) != ".json" {
		t.Fatalf("session files = %v, want one JSON file", entries)
	}
	if entries[0].Name() == "outside.json" {
		t.Fatalf("unsafe session file name = %q", entries[0].Name())
	}
	fileInfo, err := entries[0].Info()
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
	if loaded.ID != session.ID || len(loaded.Messages) != 2 || loaded.Messages[1].Content != "world" {
		t.Fatalf("loaded session = %#v", loaded)
	}
	if loaded.Messages[0].Content != "hello" {
		t.Fatalf("failed save replaced valid content with %q", loaded.Messages[0].Content)
	}
	infos, err := store.List(ctx)
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(infos) != 1 || infos[0].ID != session.ID || infos[0].MessageCount != 2 {
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
	if snapshot.CreatedAt.IsZero() || snapshot.UpdatedAt.Before(snapshot.CreatedAt) {
		t.Fatalf("session timestamps = created %v, updated %v", snapshot.CreatedAt, snapshot.UpdatedAt)
	}
	snapshot.Messages[0].Content = "changed snapshot"
	if got := second.History()[0].Content; got != "first prompt" {
		t.Fatalf("mutating Session() changed agent history to %q", got)
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
