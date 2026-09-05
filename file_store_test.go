package agentkit

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/cloudwego/eino/schema"
)

func TestFileSessionStoresShareDirectoryLocks(t *testing.T) {
	dir := t.TempDir()
	first, err := NewFileSessionStore(dir)
	if err != nil {
		t.Fatalf("first NewFileSessionStore() error = %v", err)
	}
	second, err := NewFileSessionStore(dir)
	if err != nil {
		t.Fatalf("second NewFileSessionStore() error = %v", err)
	}

	if first.mu != second.mu {
		t.Fatal("session stores for the same directory do not share a lock")
	}
	if first.checkpoints.mu != second.checkpoints.mu {
		t.Fatal("checkpoint stores for the same directory do not share a lock")
	}
	if first.goals.mu != second.goals.mu {
		t.Fatal("goal stores for the same directory do not share a lock")
	}
	if first.toolResults.mu != second.toolResults.mu {
		t.Fatal("tool result stores for the same directory do not share a lock")
	}
}

func TestFileSessionStoresSerializeRevisionChecksAcrossInstances(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	first, err := NewFileSessionStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	second, err := NewFileSessionStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := first.Save(ctx, &Session{ID: "shared"}); err != nil {
		t.Fatal(err)
	}

	firstSnapshot, err := first.Load(ctx, "shared")
	if err != nil {
		t.Fatal(err)
	}
	secondSnapshot, err := second.Load(ctx, "shared")
	if err != nil {
		t.Fatal(err)
	}
	firstSnapshot.Messages = []*schema.Message{schema.UserMessage("first")}
	secondSnapshot.Messages = []*schema.Message{schema.UserMessage("second")}

	start := make(chan struct{})
	errs := make(chan error, 2)
	var wait sync.WaitGroup
	for _, save := range []func() error{
		func() error { return first.Save(ctx, firstSnapshot) },
		func() error { return second.Save(ctx, secondSnapshot) },
	} {
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			errs <- save()
		}()
	}
	close(start)
	wait.Wait()
	close(errs)

	var succeeded, conflicted int
	for err := range errs {
		switch {
		case err == nil:
			succeeded++
		case errors.Is(err, ErrSessionConflict):
			conflicted++
		default:
			t.Fatalf("Save() error = %v, want nil or ErrSessionConflict", err)
		}
	}
	if succeeded != 1 || conflicted != 1 {
		t.Fatalf("concurrent saves: succeeded=%d conflicted=%d, want 1 and 1", succeeded, conflicted)
	}
}

func TestSessionStoreDeleteFencesConcurrentSave(t *testing.T) {
	t.Run("memory", func(t *testing.T) {
		store := NewMemorySessionStore()
		testSessionStoreDeleteFencesConcurrentSave(t, store, store.CheckpointStore(), &store.goals.mu)
	})
	t.Run("file", func(t *testing.T) {
		store, err := NewFileSessionStore(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		testSessionStoreDeleteFencesConcurrentSave(t, store, store.CheckpointStore(), store.goals.mu)
	})
}

func testSessionStoreDeleteFencesConcurrentSave(
	t *testing.T,
	store SessionStore,
	checkpoints CheckpointStore,
	goalLock *sync.RWMutex,
) {
	t.Helper()
	ctx := context.Background()
	const sessionID = "delete-save-race"
	const checkpointID = "delete-save-checkpoint"
	if err := store.Save(ctx, &Session{ID: sessionID, CheckpointID: checkpointID}); err != nil {
		t.Fatal(err)
	}
	snapshot, err := store.Load(ctx, sessionID)
	if err != nil {
		t.Fatal(err)
	}
	if err := checkpoints.Set(ctx, checkpointID, []byte("checkpoint")); err != nil {
		t.Fatal(err)
	}

	goalLock.Lock()
	locked := true
	defer func() {
		if locked {
			goalLock.Unlock()
		}
	}()
	deleteDone := make(chan error, 1)
	go func() { deleteDone <- store.Delete(ctx, sessionID) }()

	deadline := time.Now().Add(2 * time.Second)
	for {
		_, exists, err := checkpoints.Get(ctx, checkpointID)
		if err != nil {
			t.Fatal(err)
		}
		if !exists {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("Delete() did not reach resource cleanup")
		}
		time.Sleep(time.Millisecond)
	}

	saveDone := make(chan error, 1)
	go func() { saveDone <- store.Save(ctx, snapshot) }()
	var earlySave error
	var savedEarly bool
	select {
	case earlySave = <-saveDone:
		savedEarly = true
	case <-time.After(50 * time.Millisecond):
	}

	goalLock.Unlock()
	locked = false
	if err := <-deleteDone; err != nil {
		t.Fatalf("Delete() error = %v", err)
	}
	if savedEarly {
		t.Fatalf("Save() completed before Delete(): %v", earlySave)
	}
	if err := <-saveDone; !errors.Is(err, ErrSessionConflict) {
		t.Fatalf("Save() error = %v, want ErrSessionConflict", err)
	}
}
