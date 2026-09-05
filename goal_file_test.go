package agentkit

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestFileGoalStorePersistsAcrossInstances(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	store, err := NewFileGoalStore(dir)
	if err != nil {
		t.Fatalf("create goal store: %v", err)
	}
	goal := &Goal{
		ID: "release/v1", SessionID: "session-1", Objective: "release",
		Status: GoalStatusActive, Iteration: 2, MaxIterations: 12,
		LastResponse: "tests pass", NextPrompt: "publish",
	}
	if err := store.Save(ctx, goal); err != nil {
		t.Fatalf("save goal: %v", err)
	}

	reopened, err := NewFileGoalStore(dir)
	if err != nil {
		t.Fatalf("reopen goal store: %v", err)
	}
	loaded, err := reopened.Load(ctx, goal.ID)
	if err != nil {
		t.Fatalf("load goal: %v", err)
	}
	if loaded.ID != goal.ID || loaded.Iteration != 2 || loaded.NextPrompt != "publish" {
		t.Fatalf("unexpected restored goal: %#v", loaded)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read goal directory: %v", err)
	}
	if len(entries) != 1 || filepath.Ext(entries[0].Name()) != ".json" {
		t.Fatalf("unexpected goal files: %#v", entries)
	}
	if err := reopened.Delete(ctx, goal.ID); err != nil {
		t.Fatalf("delete goal: %v", err)
	}
	if _, err := reopened.Load(ctx, goal.ID); !errors.Is(err, ErrGoalNotFound) {
		t.Fatalf("expected ErrGoalNotFound, got %v", err)
	}
}

func TestFileSessionStoreProvidesGoalStore(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	sessions, err := NewFileSessionStore(dir)
	if err != nil {
		t.Fatalf("create session store: %v", err)
	}
	goal := &Goal{
		ID: "goal", SessionID: "session", Objective: "finish",
		Status: GoalStatusPaused, MaxIterations: 5,
	}
	if err := sessions.GoalStore().Save(ctx, goal); err != nil {
		t.Fatalf("save goal: %v", err)
	}

	reopened, err := NewFileSessionStore(dir)
	if err != nil {
		t.Fatalf("reopen session store: %v", err)
	}
	loaded, err := reopened.GoalStore().Load(ctx, goal.ID)
	if err != nil {
		t.Fatalf("load goal: %v", err)
	}
	if loaded.Status != GoalStatusPaused {
		t.Fatalf("unexpected goal status: %q", loaded.Status)
	}
}
