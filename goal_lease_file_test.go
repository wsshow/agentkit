package agentkit

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestFileGoalLeasePersistsAndRejectsStaleWorker(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	now := time.Date(2026, time.September, 5, 12, 0, 0, 0, time.UTC)
	firstStore, err := NewFileGoalStore(dir)
	if err != nil {
		t.Fatalf("create first goal store: %v", err)
	}
	firstStore.now = func() time.Time { return now }
	first, err := firstStore.AcquireGoalLease(ctx, "release/v2", "worker-1", time.Minute)
	if err != nil {
		t.Fatalf("acquire first lease: %v", err)
	}
	goal := &Goal{
		ID: "release/v2", SessionID: "session", Objective: "release",
		Status: GoalStatusActive, MaxIterations: 3,
	}
	if err := firstStore.SaveGoalWithLease(ctx, goal, first); err != nil {
		t.Fatalf("save goal with first lease: %v", err)
	}

	secondStore, err := NewFileGoalStore(dir)
	if err != nil {
		t.Fatalf("reopen goal store: %v", err)
	}
	secondStore.now = func() time.Time { return now }
	if _, err := secondStore.AcquireGoalLease(ctx, goal.ID, "worker-2", time.Minute); !errors.Is(err, ErrGoalLeaseHeld) {
		t.Fatalf("expected persisted ErrGoalLeaseHeld, got %v", err)
	}
	now = now.Add(2 * time.Minute)
	second, err := secondStore.AcquireGoalLease(ctx, goal.ID, "worker-2", time.Minute)
	if err != nil {
		t.Fatalf("take over expired lease: %v", err)
	}
	loaded, err := firstStore.Load(ctx, goal.ID)
	if err != nil {
		t.Fatalf("load goal: %v", err)
	}
	loaded.LastReason = "stale"
	if err := firstStore.SaveGoalWithLease(ctx, loaded, first); !errors.Is(err, ErrGoalLeaseLost) {
		t.Fatalf("expected stale file save to fail with ErrGoalLeaseLost, got %v", err)
	}
	loaded.LastReason = "current"
	if err := secondStore.SaveGoalWithLease(ctx, loaded, second); err != nil {
		t.Fatalf("save with current file lease: %v", err)
	}
	if err := secondStore.DeleteGoalWithLease(ctx, goal.ID, second); err != nil {
		t.Fatalf("delete with current file lease: %v", err)
	}
	if _, err := secondStore.Load(ctx, goal.ID); !errors.Is(err, ErrGoalNotFound) {
		t.Fatalf("expected deleted goal to be missing, got %v", err)
	}
	if err := secondStore.ReleaseGoalLease(ctx, second); err != nil {
		t.Fatalf("release file lease: %v", err)
	}
}

func TestFileGoalListIgnoresLeaseFiles(t *testing.T) {
	ctx := context.Background()
	store, err := NewFileGoalStore(t.TempDir())
	if err != nil {
		t.Fatalf("create goal store: %v", err)
	}
	lease, err := store.AcquireGoalLease(ctx, "leased", "worker", time.Minute)
	if err != nil {
		t.Fatalf("acquire lease: %v", err)
	}
	infos, err := store.List(ctx)
	if err != nil {
		t.Fatalf("list goals: %v", err)
	}
	if len(infos) != 0 {
		t.Fatalf("lease file appeared as a goal: %#v", infos)
	}
	entries, err := os.ReadDir(store.dir)
	if err != nil {
		t.Fatalf("read goal directory: %v", err)
	}
	if len(entries) != 1 || entries[0].Name() != filepath.Base(store.leasePath("leased")) {
		t.Fatalf("unexpected lease files: %#v", entries)
	}
	if err := store.ReleaseGoalLease(ctx, lease); err != nil {
		t.Fatalf("release lease: %v", err)
	}
}

func TestFileGoalDeleteRemovesLeaseFile(t *testing.T) {
	ctx := context.Background()
	store, err := NewFileGoalStore(t.TempDir())
	if err != nil {
		t.Fatalf("create goal store: %v", err)
	}
	lease, err := store.AcquireGoalLease(ctx, "obsolete", "worker", time.Minute)
	if err != nil {
		t.Fatalf("acquire lease: %v", err)
	}
	if err := store.Delete(ctx, lease.GoalID); err != nil {
		t.Fatalf("delete goal: %v", err)
	}
	if _, err := os.Stat(store.leasePath(lease.GoalID)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("lease file still exists: %v", err)
	}
}
