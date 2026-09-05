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
		Status: GoalStatusPaused, MaxIterations: 20, Iteration: 3, UpdatedAt: newer,
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
	if len(infos) != 2 || infos[0].ID != "second" || infos[0].Iteration != 3 {
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
