package agentkit

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"
)

func TestSessionStoresQueryMetadataAndPagination(t *testing.T) {
	constructors := map[string]func(*testing.T) SessionStore{
		"memory": func(*testing.T) SessionStore { return NewMemorySessionStore() },
		"file": func(t *testing.T) SessionStore {
			store, err := NewFileSessionStore(t.TempDir())
			if err != nil {
				t.Fatalf("NewFileSessionStore() error = %v", err)
			}
			return store
		},
	}
	for name, construct := range constructors {
		t.Run(name, func(t *testing.T) {
			testSessionStoreQueryMetadataAndPagination(t, construct(t))
		})
	}
}

func testSessionStoreQueryMetadataAndPagination(t *testing.T, store SessionStore) {
	t.Helper()
	ctx := context.Background()
	base := time.Date(2026, time.September, 5, 12, 0, 0, 0, time.UTC)
	sessions := []*Session{
		{
			SessionMetadata: SessionMetadata{Title: "Newest", OwnerID: "owner-1", Tags: []string{"prod", "blue"}},
			ID:              "a", CreatedAt: base, UpdatedAt: base,
		},
		{
			SessionMetadata: SessionMetadata{Title: "Archived", OwnerID: "owner-1", Tags: []string{"prod"}},
			ID:              "b", CreatedAt: base, UpdatedAt: base, Archived: true,
		},
		{
			SessionMetadata: SessionMetadata{Title: "Older", OwnerID: "owner-1", Tags: []string{"prod", "blue"}},
			ID:              "c", CreatedAt: base, UpdatedAt: base.Add(-time.Minute),
		},
		{
			SessionMetadata: SessionMetadata{Title: "Other owner", OwnerID: "owner-2", Tags: []string{"prod", "blue"}},
			ID:              "d", CreatedAt: base, UpdatedAt: base.Add(-2 * time.Minute),
		},
	}
	for _, session := range sessions {
		if err := store.Save(ctx, session); err != nil {
			t.Fatalf("Save(%q) error = %v", session.ID, err)
		}
	}

	notArchived := false
	query := SessionQuery{OwnerID: "owner-1", Tags: []string{"blue", "prod", "blue"}, Archived: &notArchived, Limit: 1}
	first, err := QuerySessions(ctx, store, query)
	if err != nil {
		t.Fatalf("QuerySessions(first) error = %v", err)
	}
	if len(first.Sessions) != 1 || first.Sessions[0].ID != "a" || first.Sessions[0].Title != "Newest" {
		t.Fatalf("first page = %#v", first)
	}
	if first.NextCursor == "" {
		t.Fatal("first page cursor is empty")
	}

	query.Cursor = first.NextCursor
	second, err := QuerySessions(ctx, store, query)
	if err != nil {
		t.Fatalf("QuerySessions(second) error = %v", err)
	}
	if len(second.Sessions) != 1 || second.Sessions[0].ID != "c" || second.NextCursor != "" {
		t.Fatalf("second page = %#v", second)
	}

	first.Sessions[0].Tags[0] = "mutated"
	loaded, err := store.Load(ctx, "a")
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if loaded.Tags[0] != "prod" {
		t.Fatalf("stored tags were mutated: %v", loaded.Tags)
	}

	archived := true
	archivedPage, err := QuerySessions(ctx, store, SessionQuery{OwnerID: "owner-1", Archived: &archived})
	if err != nil {
		t.Fatalf("QuerySessions(archived) error = %v", err)
	}
	if len(archivedPage.Sessions) != 1 || archivedPage.Sessions[0].ID != "b" {
		t.Fatalf("archived page = %#v", archivedPage)
	}
}

func TestQuerySessionsFallsBackToList(t *testing.T) {
	ctx := context.Background()
	backing := NewMemorySessionStore()
	if err := backing.Save(ctx, &Session{ID: "session", SessionMetadata: SessionMetadata{OwnerID: "owner"}}); err != nil {
		t.Fatal(err)
	}
	store := &listOnlySessionStore{store: backing}
	page, err := QuerySessions(ctx, store, SessionQuery{OwnerID: "owner"})
	if err != nil {
		t.Fatalf("QuerySessions() error = %v", err)
	}
	if len(page.Sessions) != 1 || page.Sessions[0].ID != "session" {
		t.Fatalf("page = %#v", page)
	}
}

func TestFileSessionStorePaginatesLegacySessionsWithoutTimestamps(t *testing.T) {
	ctx := context.Background()
	store, err := NewFileSessionStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	base := time.Date(2026, time.September, 5, 12, 0, 0, 0, time.UTC)
	for index, id := range []string{"newer", "older"} {
		path := store.path(id)
		data := []byte(`{"version":1,"session":{"id":"` + id + `","messages":[]}}`)
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatalf("write legacy session %q: %v", id, err)
		}
		modified := base.Add(-time.Duration(index) * time.Minute)
		if err := os.Chtimes(path, modified, modified); err != nil {
			t.Fatalf("set legacy session %q time: %v", id, err)
		}
	}

	first, err := QuerySessions(ctx, store, SessionQuery{Limit: 1})
	if err != nil {
		t.Fatalf("QuerySessions(first) error = %v", err)
	}
	if len(first.Sessions) != 1 || first.Sessions[0].ID != "newer" || first.NextCursor == "" {
		t.Fatalf("first page = %#v", first)
	}
	second, err := QuerySessions(ctx, store, SessionQuery{Limit: 1, Cursor: first.NextCursor})
	if err != nil {
		t.Fatalf("QuerySessions(second) error = %v", err)
	}
	if len(second.Sessions) != 1 || second.Sessions[0].ID != "older" || second.NextCursor != "" {
		t.Fatalf("second page = %#v", second)
	}
}

func TestQuerySessionsValidatesInputAndGuardsBackendPanics(t *testing.T) {
	ctx := context.Background()
	store := NewMemorySessionStore()
	tests := []struct {
		name  string
		ctx   context.Context
		store SessionStore
		query SessionQuery
		want  error
	}{
		{name: "nil store", ctx: ctx, want: errors.New("session store is required")},
		{name: "large limit", ctx: ctx, store: store, query: SessionQuery{Limit: MaxSessionPageSize + 1}, want: ErrInvalidSessionQuery},
		{name: "bad cursor", ctx: ctx, store: store, query: SessionQuery{Cursor: "not-a-cursor"}, want: ErrInvalidSessionCursor},
		{name: "blank tag", ctx: ctx, store: store, query: SessionQuery{Tags: []string{" "}}, want: ErrInvalidSessionQuery},
		{name: "backend panic", ctx: ctx, store: &panickingSessionQueryStore{SessionStore: store}, want: ErrPersistencePanic},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := QuerySessions(tt.ctx, tt.store, tt.query)
			if tt.want == ErrInvalidSessionQuery || tt.want == ErrInvalidSessionCursor || tt.want == ErrPersistencePanic {
				if !errors.Is(err, tt.want) {
					t.Fatalf("QuerySessions() error = %v, want %v", err, tt.want)
				}
				return
			}
			if err == nil || !errors.Is(err, tt.want) && err.Error() != "agentkit: "+tt.want.Error() {
				t.Fatalf("QuerySessions() error = %v, want containing %q", err, tt.want)
			}
		})
	}
}

func TestAgentPreservesSessionMetadataAndRejectsArchivedSession(t *testing.T) {
	ctx := context.Background()
	store := NewMemorySessionStore()
	if err := store.Save(ctx, &Session{
		ID:              "active",
		SessionMetadata: SessionMetadata{Title: "Support", OwnerID: "owner", Tags: []string{"prod"}},
	}); err != nil {
		t.Fatal(err)
	}
	agent, err := New(ctx, &Config{
		Name: "assistant", Model: NewMockChatModel(MockModelText("done")),
		Session: &SessionConfig{ID: "active", Store: store},
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if err := agent.Prompt(ctx, "hello"); err != nil {
		t.Fatalf("Prompt() error = %v", err)
	}
	if err := agent.Close(); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.Load(ctx, "active")
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Title != "Support" || loaded.OwnerID != "owner" || len(loaded.Tags) != 1 || loaded.Tags[0] != "prod" {
		t.Fatalf("metadata = %#v", loaded.SessionMetadata)
	}

	if err := store.Save(ctx, &Session{ID: "archived", Archived: true}); err != nil {
		t.Fatal(err)
	}
	_, err = New(ctx, &Config{
		Name: "assistant", Model: NewMockChatModel(),
		Session: &SessionConfig{ID: "archived", Store: store},
	})
	if !errors.Is(err, ErrSessionArchived) {
		t.Fatalf("New(archived) error = %v, want ErrSessionArchived", err)
	}
}

type listOnlySessionStore struct {
	store SessionStore
}

func (s *listOnlySessionStore) Load(ctx context.Context, id string) (*Session, error) {
	return s.store.Load(ctx, id)
}

func (s *listOnlySessionStore) Save(ctx context.Context, session *Session) error {
	return s.store.Save(ctx, session)
}

func (s *listOnlySessionStore) Delete(ctx context.Context, id string) error {
	return s.store.Delete(ctx, id)
}

func (s *listOnlySessionStore) List(ctx context.Context) ([]SessionInfo, error) {
	return s.store.List(ctx)
}

type panickingSessionQueryStore struct {
	SessionStore
}

func (*panickingSessionQueryStore) QuerySessions(context.Context, SessionQuery) (SessionPage, error) {
	panic("boom")
}
