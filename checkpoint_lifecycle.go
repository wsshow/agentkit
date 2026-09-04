package agentkit

import (
	"context"
	"fmt"

	"github.com/google/uuid"
)

func (a *Agent) markInterrupted(points []InterruptPoint) {
	a.mu.Lock()
	a.pendingInterrupts = cloneInterruptPoints(points)
	a.runInterrupted = true
	a.mu.Unlock()
}

func (a *Agent) wasInterrupted() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.runInterrupted
}

func (a *Agent) ensureNoPendingCheckpoint(ctx context.Context) error {
	a.mu.Lock()
	store := a.checkpointStore
	id := a.checkPointID
	pending := len(a.pendingInterrupts) > 0
	a.mu.Unlock()
	if pending {
		return ErrResumeRequired
	}
	if store == nil || id == "" {
		return nil
	}
	_, existed, err := store.Get(ctx, id)
	if err != nil {
		return fmt.Errorf("agentkit: inspect checkpoint %q: %w", id, err)
	}
	if existed {
		return ErrResumeRequired
	}
	return nil
}

// discardCheckpoint rotates the ID even when a custom store cannot delete values.
// This makes stale checkpoints unreachable before any external I/O is attempted.
func (a *Agent) discardCheckpoint(ctx context.Context) error {
	a.mu.Lock()
	store := a.checkpointStore
	oldID := a.checkPointID
	a.checkPointID = a.name + "/" + uuid.NewString()
	a.pendingInterrupts = nil
	a.runInterrupted = false
	a.mu.Unlock()

	if store == nil || oldID == "" {
		return nil
	}
	deleter, ok := store.(CheckpointDeleter)
	if !ok {
		return nil
	}
	if err := deleter.Delete(ctx, oldID); err != nil {
		return fmt.Errorf("agentkit: delete checkpoint %q: %w", oldID, err)
	}
	return nil
}
