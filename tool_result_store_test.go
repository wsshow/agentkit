package agentkit

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestMemoryToolResultStoreIsImmutableAndCopiesResults(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryToolResultStore()
	older := time.Now().UTC().Add(-time.Hour)
	newer := time.Now().UTC()
	first := &StoredToolResult{ID: "first", SessionID: "session", Content: "complete output", CreatedAt: older}
	second := &StoredToolResult{ID: "second", Content: "new output", CreatedAt: newer}
	if err := store.Save(ctx, first); err != nil {
		t.Fatalf("save first result: %v", err)
	}
	if err := store.Save(ctx, second); err != nil {
		t.Fatalf("save second result: %v", err)
	}
	first.Content = "mutated"
	first.SessionID = "mutated"

	loaded, err := store.Load(ctx, "first")
	if err != nil {
		t.Fatalf("load first result: %v", err)
	}
	if loaded.Content != "complete output" || loaded.SessionID != "session" {
		t.Fatalf("stored result was mutated: %q", loaded.Content)
	}
	loaded.Content = "changed again"
	loaded, err = store.Load(ctx, "first")
	if err != nil {
		t.Fatalf("reload first result: %v", err)
	}
	if loaded.Content != "complete output" {
		t.Fatalf("loaded result shares mutable state: %q", loaded.Content)
	}

	if err := store.Save(ctx, &StoredToolResult{ID: "first", Content: "replacement"}); !errors.Is(err, ErrToolResultExists) {
		t.Fatalf("expected ErrToolResultExists, got %v", err)
	}
	infos, err := store.List(ctx)
	if err != nil {
		t.Fatalf("list results: %v", err)
	}
	if len(infos) != 2 || infos[0].ID != "second" || infos[1].SessionID != "session" || infos[1].Size != len("complete output") {
		t.Fatalf("unexpected result list: %#v", infos)
	}
	if err := store.Delete(ctx, "missing"); err != nil {
		t.Fatalf("delete missing result: %v", err)
	}
}

func TestMemorySessionStoreProvidesToolResultStore(t *testing.T) {
	ctx := context.Background()
	sessions := NewMemorySessionStore()
	results := sessions.ToolResultStore()
	if results != sessions.ToolResultStore() {
		t.Fatal("expected the session store to reuse its tool result store")
	}
	if err := results.Save(ctx, &StoredToolResult{ID: "result", Content: "saved"}); err != nil {
		t.Fatalf("save result: %v", err)
	}
	loaded, err := sessions.ToolResultStore().Load(ctx, "result")
	if err != nil {
		t.Fatalf("load shared result: %v", err)
	}
	if loaded.Content != "saved" {
		t.Fatalf("unexpected shared result: %#v", loaded)
	}
}

func TestToolResultStoreValidation(t *testing.T) {
	store := NewMemoryToolResultStore()
	ctx := context.Background()
	if err := store.Save(ctx, nil); err == nil {
		t.Fatal("expected nil result validation error")
	}
	if err := store.Save(ctx, &StoredToolResult{Content: "missing ID"}); err == nil {
		t.Fatal("expected missing ID validation error")
	}
	if _, err := store.Load(ctx, "missing"); !errors.Is(err, ErrToolResultNotFound) {
		t.Fatalf("expected ErrToolResultNotFound, got %v", err)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if err := store.Delete(canceled, "result"); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
}
