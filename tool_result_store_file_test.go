package agentkit

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestFileToolResultStorePersistsAndUsesSafeFileName(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	store, err := NewFileToolResultStore(dir)
	if err != nil {
		t.Fatalf("create tool result store: %v", err)
	}
	id := "../../outside/result"
	if err := store.Save(ctx, &StoredToolResult{ID: id, SessionID: "session", Content: "complete output"}); err != nil {
		t.Fatalf("save result: %v", err)
	}
	if err := store.Save(ctx, &StoredToolResult{ID: id, Content: "replacement"}); !errors.Is(err, ErrToolResultExists) {
		t.Fatalf("expected ErrToolResultExists, got %v", err)
	}

	reopened, err := NewFileToolResultStore(dir)
	if err != nil {
		t.Fatalf("reopen tool result store: %v", err)
	}
	loaded, err := reopened.Load(ctx, id)
	if err != nil {
		t.Fatalf("load result: %v", err)
	}
	if loaded.SessionID != "session" || loaded.Content != "complete output" || loaded.CreatedAt.IsZero() {
		t.Fatalf("unexpected restored result: %#v", loaded)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read result directory: %v", err)
	}
	if len(entries) != 1 || entries[0].Name() != sessionStorageKey(id)+".json" {
		t.Fatalf("unexpected result files: %#v", entries)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(dir), "outside")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unsafe path was created: %v", err)
	}
	if err := reopened.Delete(ctx, id); err != nil {
		t.Fatalf("delete result: %v", err)
	}
	if _, err := reopened.Load(ctx, id); !errors.Is(err, ErrToolResultNotFound) {
		t.Fatalf("expected ErrToolResultNotFound, got %v", err)
	}
}

func TestFileSessionStoreProvidesPersistentToolResultStore(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	first, err := NewFileSessionStore(dir)
	if err != nil {
		t.Fatalf("create first session store: %v", err)
	}
	if err := first.ToolResultStore().Save(ctx, &StoredToolResult{ID: "result", Content: "saved"}); err != nil {
		t.Fatalf("save result: %v", err)
	}

	second, err := NewFileSessionStore(dir)
	if err != nil {
		t.Fatalf("create second session store: %v", err)
	}
	loaded, err := second.ToolResultStore().Load(ctx, "result")
	if err != nil {
		t.Fatalf("load persisted result: %v", err)
	}
	if loaded.Content != "saved" {
		t.Fatalf("unexpected persisted result: %#v", loaded)
	}
}
