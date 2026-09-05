package agentkit

import (
	"context"
	"fmt"

	"github.com/cloudwego/eino/schema"
	"github.com/google/uuid"
)

const abandonedToolCallResult = "AgentKit abandoned this tool call after its pending checkpoint was cleared."

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
	if len(a.pendingInterrupts) > 0 || a.runInterrupted {
		a.history = appendAbandonedToolResults(a.history)
		a.contextHistory = appendAbandonedToolResults(a.contextHistory)
		a.toolCalls = make(map[string]toolCallInfo)
		a.toolBatchDone = nil
		a.toolBatchDoneFlag = false
	}
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

func appendAbandonedToolResults(messages []*schema.Message) []*schema.Message {
	assistantIndex := -1
	for index := len(messages) - 1; index >= 0; index-- {
		message := messages[index]
		if message != nil && message.Role == schema.Assistant && len(message.ToolCalls) > 0 {
			assistantIndex = index
			break
		}
	}
	if assistantIndex < 0 {
		return messages
	}
	completed := make(map[string]struct{})
	for _, message := range messages[assistantIndex+1:] {
		if message != nil && message.Role == schema.Tool && message.ToolCallID != "" {
			completed[message.ToolCallID] = struct{}{}
		}
	}
	for _, call := range messages[assistantIndex].ToolCalls {
		if call.ID == "" {
			continue
		}
		if _, exists := completed[call.ID]; exists {
			continue
		}
		messages = append(messages, schema.ToolMessage(
			abandonedToolCallResult,
			call.ID,
			schema.WithToolName(call.Function.Name),
		))
	}
	return messages
}
