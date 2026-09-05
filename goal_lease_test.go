package agentkit

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

type stuckGoalLeaseReleaseStore struct {
	*MemoryGoalStore
	release chan struct{}
}

func (s *stuckGoalLeaseReleaseStore) ReleaseGoalLease(context.Context, *GoalLease) error {
	<-s.release
	return nil
}

func TestMemoryGoalLeaseAcquisitionRenewalAndFencing(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, time.September, 5, 12, 0, 0, 0, time.UTC)
	store := NewMemoryGoalStore()
	store.now = func() time.Time { return now }

	first, err := store.AcquireGoalLease(ctx, "goal", "worker-1", time.Minute)
	if err != nil {
		t.Fatalf("acquire first lease: %v", err)
	}
	if first.GoalID != "goal" || first.WorkerID != "worker-1" || first.Token == "" || !first.ExpiresAt.Equal(now.Add(time.Minute)) {
		t.Fatalf("unexpected first lease: %#v", first)
	}
	if _, err := store.AcquireGoalLease(ctx, "goal", "worker-2", time.Minute); !errors.Is(err, ErrGoalLeaseHeld) {
		t.Fatalf("expected ErrGoalLeaseHeld, got %v", err)
	} else {
		var held *GoalLeaseHeldError
		if !errors.As(err, &held) || held.Lease.WorkerID != "worker-1" || !held.Lease.ExpiresAt.Equal(first.ExpiresAt) {
			t.Fatalf("unexpected structured lease error: %#v", held)
		}
	}
	goal := &Goal{
		ID: "goal", SessionID: "session", Objective: "finish",
		Status: GoalStatusActive, MaxIterations: 3,
	}
	if err := store.SaveGoalWithLease(ctx, goal, first); err != nil {
		t.Fatalf("save with first lease: %v", err)
	}
	loaded, err := store.Load(ctx, goal.ID)
	if err != nil {
		t.Fatalf("load goal: %v", err)
	}

	now = now.Add(30 * time.Second)
	renewed, err := store.RenewGoalLease(ctx, first, 2*time.Minute)
	if err != nil {
		t.Fatalf("renew first lease: %v", err)
	}
	if renewed.Token != first.Token || !renewed.ExpiresAt.Equal(now.Add(2*time.Minute)) {
		t.Fatalf("unexpected renewed lease: %#v", renewed)
	}
	now = renewed.ExpiresAt.Add(time.Nanosecond)
	second, err := store.AcquireGoalLease(ctx, "goal", "worker-2", time.Minute)
	if err != nil {
		t.Fatalf("take over expired lease: %v", err)
	}
	if second.Token == first.Token {
		t.Fatal("takeover reused the stale fencing token")
	}

	loaded.LastReason = "stale write"
	if err := store.SaveGoalWithLease(ctx, loaded, first); !errors.Is(err, ErrGoalLeaseLost) {
		t.Fatalf("expected stale save to fail with ErrGoalLeaseLost, got %v", err)
	}
	if err := store.DeleteGoalWithLease(ctx, goal.ID, first); !errors.Is(err, ErrGoalLeaseLost) {
		t.Fatalf("expected stale delete to fail with ErrGoalLeaseLost, got %v", err)
	}
	if err := store.ReleaseGoalLease(ctx, first); !errors.Is(err, ErrGoalLeaseLost) {
		t.Fatalf("expected stale release to fail with ErrGoalLeaseLost, got %v", err)
	}

	loaded.LastReason = "current write"
	if err := store.SaveGoalWithLease(ctx, loaded, second); err != nil {
		t.Fatalf("save with takeover lease: %v", err)
	}
	if err := store.ReleaseGoalLease(ctx, second); err != nil {
		t.Fatalf("release takeover lease: %v", err)
	}
	if err := store.ReleaseGoalLease(ctx, second); err != nil {
		t.Fatalf("repeat release: %v", err)
	}
}

func TestMemoryGoalLeaseAllowsOnlyOneConcurrentOwner(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryGoalStore()
	const workers = 20
	var wg sync.WaitGroup
	var mu sync.Mutex
	winners := make([]*GoalLease, 0, 1)
	errorsSeen := make([]error, 0, workers-1)
	for i := range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			lease, err := store.AcquireGoalLease(ctx, "goal", "worker-"+string(rune('a'+i)), time.Minute)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				errorsSeen = append(errorsSeen, err)
				return
			}
			winners = append(winners, lease)
		}()
	}
	wg.Wait()
	if len(winners) != 1 || len(errorsSeen) != workers-1 {
		t.Fatalf("winners = %d, errors = %d", len(winners), len(errorsSeen))
	}
	for _, err := range errorsSeen {
		if !errors.Is(err, ErrGoalLeaseHeld) {
			t.Fatalf("unexpected acquisition error: %v", err)
		}
	}
}

func TestGoalLeaseValidation(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryGoalStore()
	if _, err := store.AcquireGoalLease(ctx, " goal ", "worker", time.Minute); err == nil {
		t.Fatal("expected goal ID validation error")
	}
	if _, err := store.AcquireGoalLease(ctx, "goal", " ", time.Minute); err == nil {
		t.Fatal("expected worker validation error")
	}
	if _, err := store.AcquireGoalLease(ctx, "goal", "worker", 0); err == nil {
		t.Fatal("expected duration validation error")
	}
	if _, err := store.RenewGoalLease(ctx, nil, time.Minute); err == nil {
		t.Fatal("expected nil lease validation error")
	}
	lease, err := store.AcquireGoalLease(ctx, "goal", "worker", time.Minute)
	if err != nil {
		t.Fatalf("acquire lease: %v", err)
	}
	if err := store.SaveGoalWithLease(ctx, &Goal{
		ID: "other", SessionID: "session", Objective: "finish",
		Status: GoalStatusActive, MaxIterations: 1,
	}, lease); err == nil {
		t.Fatal("expected mismatched goal validation error")
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if err := store.ReleaseGoalLease(canceled, lease); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
}

func TestMemoryGoalDeleteRemovesLease(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryGoalStore()
	lease, err := store.AcquireGoalLease(ctx, "obsolete", "worker-1", time.Minute)
	if err != nil {
		t.Fatalf("acquire lease: %v", err)
	}
	if err := store.Delete(ctx, lease.GoalID); err != nil {
		t.Fatalf("delete goal: %v", err)
	}
	if _, err := store.AcquireGoalLease(ctx, lease.GoalID, "worker-2", time.Minute); err != nil {
		t.Fatalf("acquire lease after delete: %v", err)
	}
}

func TestGoalLeaseHeartbeatStopIsBounded(t *testing.T) {
	heartbeat := &goalLeaseHeartbeat{
		stopCh: make(chan struct{}),
		done:   make(chan struct{}),
	}
	release := make(chan struct{})
	go func() {
		<-heartbeat.stopCh
		<-release
		close(heartbeat.done)
	}()
	defer close(release)

	started := time.Now()
	err := heartbeat.stop(10 * time.Millisecond)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("stop error = %v, want context.DeadlineExceeded", err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("heartbeat stop took too long: %s", elapsed)
	}
}

func TestGoalLeaseHeartbeatStopTimeoutIsBounded(t *testing.T) {
	if got := goalLeaseHeartbeatStopTimeout(time.Millisecond); got != goalLeaseHeartbeatStopMinWait {
		t.Fatalf("short lease stop timeout = %s, want %s", got, goalLeaseHeartbeatStopMinWait)
	}
	if got := goalLeaseHeartbeatStopTimeout(time.Hour); got != goalLeaseHeartbeatStopMaxWait {
		t.Fatalf("long lease stop timeout = %s, want %s", got, goalLeaseHeartbeatStopMaxWait)
	}
}

func TestGoalLeaseReleaseIsBounded(t *testing.T) {
	store := &stuckGoalLeaseReleaseStore{
		MemoryGoalStore: NewMemoryGoalStore(),
		release:         make(chan struct{}),
	}
	defer close(store.release)
	err := releaseGoalLease(context.Background(), store, &GoalLease{GoalID: "goal"}, 10*time.Millisecond)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("release error = %v, want context.DeadlineExceeded", err)
	}
}
