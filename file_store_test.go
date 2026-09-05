package agentkit

import (
	"context"
	"errors"
	"sync"
	"testing"

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
