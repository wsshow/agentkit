package agentkit

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestMemoryGoalStoreCopiesAndListsGoals(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryGoalStore()
	older := time.Now().UTC().Add(-time.Hour)
	newer := time.Now().UTC()
	first := &Goal{
		ID: "first", SessionID: "session-1", Objective: "ship",
		Status: GoalStatusActive, MaxIterations: 10, UpdatedAt: older,
	}
	second := &Goal{
		ID: "second", SessionID: "session-2", Objective: "verify",
		Status: GoalStatusPaused, MaxIterations: 20, Iteration: 3, AttemptIteration: 4,
		InProgress: true, AwaitingInterrupt: true, PendingEvaluation: true,
		LastReason: "waiting", LastError: "interrupted", UpdatedAt: newer,
	}
	if err := store.Save(ctx, first); err != nil {
		t.Fatalf("save first goal: %v", err)
	}
	if err := store.Save(ctx, second); err != nil {
		t.Fatalf("save second goal: %v", err)
	}
	first.Objective = "mutated"

	loaded, err := store.Load(ctx, "first")
	if err != nil {
		t.Fatalf("load first goal: %v", err)
	}
	if loaded.Objective != "ship" {
		t.Fatalf("stored goal was mutated: %q", loaded.Objective)
	}
	loaded.Objective = "changed again"
	loaded, err = store.Load(ctx, "first")
	if err != nil {
		t.Fatalf("reload first goal: %v", err)
	}
	if loaded.Objective != "ship" {
		t.Fatalf("loaded goal shares mutable state: %q", loaded.Objective)
	}

	infos, err := store.List(ctx)
	if err != nil {
		t.Fatalf("list goals: %v", err)
	}
	if len(infos) != 2 || infos[0].ID != "second" || infos[0].Objective != "verify" ||
		infos[0].Iteration != 3 || infos[0].MaxIterations != 20 || infos[0].AttemptIteration != 4 ||
		!infos[0].InProgress || !infos[0].AwaitingInterrupt || !infos[0].PendingEvaluation ||
		infos[0].LastReason != "waiting" || infos[0].LastError != "interrupted" {
		t.Fatalf("unexpected goal list: %#v", infos)
	}
	if err := store.Delete(ctx, "missing"); err != nil {
		t.Fatalf("delete missing goal: %v", err)
	}
}

func TestMemorySessionStoreProvidesGoalStore(t *testing.T) {
	ctx := context.Background()
	sessions := NewMemorySessionStore()
	goals := sessions.GoalStore()
	if goals != sessions.GoalStore() {
		t.Fatal("expected the session store to reuse its goal store")
	}
	goal := &Goal{
		ID: "goal", SessionID: "session", Objective: "finish",
		Status: GoalStatusActive, MaxIterations: 5,
	}
	if err := goals.Save(ctx, goal); err != nil {
		t.Fatalf("save goal: %v", err)
	}
	if _, err := sessions.GoalStore().Load(ctx, goal.ID); err != nil {
		t.Fatalf("load shared goal: %v", err)
	}
}

func TestGoalStoreValidation(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryGoalStore()
	tests := []struct {
		name string
		goal *Goal
	}{
		{name: "nil", goal: nil},
		{name: "missing ID", goal: &Goal{SessionID: "s", Objective: "o", Status: GoalStatusActive, MaxIterations: 1}},
		{name: "missing session", goal: &Goal{ID: "g", Objective: "o", Status: GoalStatusActive, MaxIterations: 1}},
		{name: "missing objective", goal: &Goal{ID: "g", SessionID: "s", Status: GoalStatusActive, MaxIterations: 1}},
		{name: "invalid status", goal: &Goal{ID: "g", SessionID: "s", Objective: "o", Status: "running", MaxIterations: 1}},
		{name: "invalid max", goal: &Goal{ID: "g", SessionID: "s", Objective: "o", Status: GoalStatusActive}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := store.Save(ctx, tt.goal); err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
	if _, err := store.Load(ctx, "missing"); !errors.Is(err, ErrGoalNotFound) {
		t.Fatalf("expected ErrGoalNotFound, got %v", err)
	}
}

func TestGoalStoresRejectAmbiguousDurableIDs(t *testing.T) {
	ctx := context.Background()
	fileStore, err := NewFileGoalStore(t.TempDir())
	if err != nil {
		t.Fatalf("create file goal store: %v", err)
	}
	stores := map[string]GoalStore{
		"memory": NewMemoryGoalStore(),
		"file":   fileStore,
	}
	for name, store := range stores {
		t.Run(name, func(t *testing.T) {
			if err := store.Save(ctx, &Goal{
				ID: " goal ", SessionID: "session", Objective: "finish",
				Status: GoalStatusActive, MaxIterations: 1,
			}); err == nil {
				t.Fatal("expected surrounding goal ID whitespace to be rejected")
			}
			if err := store.Save(ctx, &Goal{
				ID: "goal", SessionID: " session ", Objective: "finish",
				Status: GoalStatusActive, MaxIterations: 1,
			}); err == nil {
				t.Fatal("expected surrounding session ID whitespace to be rejected")
			}
			if _, err := store.Load(ctx, " goal "); err == nil {
				t.Fatal("expected surrounding goal ID whitespace to be rejected on load")
			}
		})
	}
}

func TestMemoryGoalStoreRejectsStaleRevision(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryGoalStore()
	goal := &Goal{
		ID: "goal", SessionID: "session", Objective: "finish",
		Status: GoalStatusActive, MaxIterations: 5,
	}
	if err := store.Save(ctx, goal); err != nil {
		t.Fatalf("create goal: %v", err)
	}
	first, err := store.Load(ctx, goal.ID)
	if err != nil {
		t.Fatalf("load first copy: %v", err)
	}
	stale, err := store.Load(ctx, goal.ID)
	if err != nil {
		t.Fatalf("load stale copy: %v", err)
	}
	first.Status = GoalStatusPaused
	if err := store.Save(ctx, first); err != nil {
		t.Fatalf("save first copy: %v", err)
	}
	stale.Status = GoalStatusCompleted
	if err := store.Save(ctx, stale); !errors.Is(err, ErrGoalConflict) {
		t.Fatalf("expected ErrGoalConflict, got %v", err)
	}
	loaded, err := store.Load(ctx, goal.ID)
	if err != nil {
		t.Fatalf("load final goal: %v", err)
	}
	if loaded.Status != GoalStatusPaused || loaded.Revision != 2 {
		t.Fatalf("stale update replaced current goal: %#v", loaded)
	}
}
