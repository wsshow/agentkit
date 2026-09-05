package agentkit

import (
	"context"
	"errors"
	"testing"
	"time"
)

type sessionStoreWithoutResourceProviders struct {
	SessionStore
}

func TestPruneResourcesKeepsRecoverableState(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, time.September, 5, 12, 0, 0, 0, time.UTC)
	old := now.Add(-48 * time.Hour)
	recent := now.Add(-time.Hour)
	store := NewMemorySessionStore()
	for _, session := range []*Session{
		{ID: "old-session", CreatedAt: old, UpdatedAt: old},
		{ID: "recent-session", CreatedAt: recent, UpdatedAt: recent},
	} {
		if err := store.Save(ctx, session); err != nil {
			t.Fatalf("save session %q: %v", session.ID, err)
		}
	}
	goals := store.GoalStore()
	for _, goal := range []*Goal{
		{ID: "old-session-goal", SessionID: "old-session", Objective: "delete with session", Status: GoalStatusPaused, MaxIterations: 2, CreatedAt: old, UpdatedAt: old},
		{ID: "old-completed", SessionID: "recent-session", Objective: "delete", Status: GoalStatusCompleted, MaxIterations: 2, CreatedAt: old, UpdatedAt: old},
		{ID: "recent-completed", SessionID: "recent-session", Objective: "keep", Status: GoalStatusCompleted, MaxIterations: 2, CreatedAt: recent, UpdatedAt: recent},
		{ID: "old-paused", SessionID: "recent-session", Objective: "keep", Status: GoalStatusPaused, MaxIterations: 2, CreatedAt: old, UpdatedAt: old},
		{ID: "old-blocked", SessionID: "recent-session", Objective: "keep", Status: GoalStatusBlocked, MaxIterations: 2, CreatedAt: old, UpdatedAt: old},
	} {
		if err := goals.Save(ctx, goal); err != nil {
			t.Fatalf("save goal %q: %v", goal.ID, err)
		}
	}
	results := store.ToolResultStore()
	for _, result := range []*StoredToolResult{
		{ID: "old-session-result", SessionID: "old-session", Content: "delete with session", CreatedAt: old},
		{ID: "old-detached", Content: "delete", CreatedAt: old},
		{ID: "recent-detached", Content: "keep", CreatedAt: recent},
		{ID: "old-owned", SessionID: "recent-session", Content: "keep", CreatedAt: old},
	} {
		if err := results.Save(ctx, result); err != nil {
			t.Fatalf("save tool result %q: %v", result.ID, err)
		}
	}

	report, err := pruneResourcesAt(ctx, store, RetentionPolicy{
		SessionIdleTime:       24 * time.Hour,
		CompletedGoalAge:      24 * time.Hour,
		DetachedToolResultAge: 24 * time.Hour,
	}, now)
	if err != nil {
		t.Fatalf("prune resources: %v", err)
	}
	if report.SessionsDeleted != 1 || report.CompletedGoalsDeleted != 1 || report.DetachedToolResultsDeleted != 1 {
		t.Fatalf("unexpected retention report: %#v", report)
	}
	if _, err := store.Load(ctx, "old-session"); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("old session still exists: %v", err)
	}
	if _, err := goals.Load(ctx, "old-session-goal"); !errors.Is(err, ErrGoalNotFound) {
		t.Fatalf("old session goal still exists: %v", err)
	}
	if _, err := goals.Load(ctx, "old-completed"); !errors.Is(err, ErrGoalNotFound) {
		t.Fatalf("old completed goal still exists: %v", err)
	}
	if _, err := results.Load(ctx, "old-session-result"); !errors.Is(err, ErrToolResultNotFound) {
		t.Fatalf("old session result still exists: %v", err)
	}
	if _, err := results.Load(ctx, "old-detached"); !errors.Is(err, ErrToolResultNotFound) {
		t.Fatalf("old detached result still exists: %v", err)
	}
	for _, id := range []string{"recent-completed", "old-paused", "old-blocked"} {
		if _, err := goals.Load(ctx, id); err != nil {
			t.Fatalf("recoverable goal %q was deleted: %v", id, err)
		}
	}
	for _, id := range []string{"recent-detached", "old-owned"} {
		if _, err := results.Load(ctx, id); err != nil {
			t.Fatalf("tool result %q was deleted: %v", id, err)
		}
	}
}

func TestPruneResourcesZeroPolicyDoesNothing(t *testing.T) {
	store := NewMemorySessionStore()
	if err := store.Save(context.Background(), &Session{ID: "session"}); err != nil {
		t.Fatalf("save session: %v", err)
	}
	report, err := PruneResources(context.Background(), store, RetentionPolicy{})
	if err != nil {
		t.Fatalf("prune resources: %v", err)
	}
	if report != (RetentionReport{}) {
		t.Fatalf("unexpected report: %#v", report)
	}
	if _, err := store.Load(context.Background(), "session"); err != nil {
		t.Fatalf("zero policy deleted session: %v", err)
	}
}

func TestPruneResourcesSupportsFileSessionStore(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, time.September, 5, 12, 0, 0, 0, time.UTC)
	store, err := NewFileSessionStore(t.TempDir())
	if err != nil {
		t.Fatalf("create file session store: %v", err)
	}
	if err := store.Save(ctx, &Session{
		ID: "old-session", CreatedAt: now.Add(-48 * time.Hour), UpdatedAt: now.Add(-48 * time.Hour),
	}); err != nil {
		t.Fatalf("save session: %v", err)
	}
	report, err := pruneResourcesAt(ctx, store, RetentionPolicy{SessionIdleTime: 24 * time.Hour}, now)
	if err != nil {
		t.Fatalf("prune resources: %v", err)
	}
	if report.SessionsDeleted != 1 {
		t.Fatalf("sessions deleted = %d, want 1", report.SessionsDeleted)
	}
	if _, err := store.Load(ctx, "old-session"); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("old session still exists: %v", err)
	}
}

func TestPruneResourcesValidatesPolicyBeforeDeleting(t *testing.T) {
	ctx := context.Background()
	store := NewMemorySessionStore()
	if err := store.Save(ctx, &Session{ID: "session", UpdatedAt: time.Now().UTC().Add(-time.Hour)}); err != nil {
		t.Fatalf("save session: %v", err)
	}
	if _, err := PruneResources(ctx, store, RetentionPolicy{
		SessionIdleTime: -time.Second,
	}); err == nil {
		t.Fatal("negative policy error = nil")
	}
	if _, err := store.Load(ctx, "session"); err != nil {
		t.Fatalf("invalid policy deleted session: %v", err)
	}
}

func TestPruneResourcesRequiresConfiguredProvidersBeforeDeleting(t *testing.T) {
	ctx := context.Background()
	backing := NewMemorySessionStore()
	if err := backing.Save(ctx, &Session{
		ID: "session", UpdatedAt: time.Now().UTC().Add(-48 * time.Hour),
	}); err != nil {
		t.Fatalf("save session: %v", err)
	}
	store := &sessionStoreWithoutResourceProviders{SessionStore: backing}
	if _, err := PruneResources(ctx, store, RetentionPolicy{
		SessionIdleTime:  24 * time.Hour,
		CompletedGoalAge: 24 * time.Hour,
	}); err == nil {
		t.Fatal("missing goal provider error = nil")
	}
	if _, err := backing.Load(ctx, "session"); err != nil {
		t.Fatalf("provider validation deleted session: %v", err)
	}
}
