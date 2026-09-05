package agentkit

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/cloudwego/eino/components/tool/utils"
	"github.com/cloudwego/eino/schema"
)

type checkpointApprovalInput struct {
	Action string `json:"action"`
}

func newCheckpointApprovalTool(t *testing.T) Tool {
	t.Helper()
	approvalTool, err := utils.InferTool("approve_action", "approve an action",
		func(ctx context.Context, input *checkpointApprovalInput) (string, error) {
			wasInterrupted, _, _ := GetInterruptState[any](ctx)
			if !wasInterrupted {
				return "", Interrupt(ctx, "approve "+input.Action)
			}
			isTarget, hasData, approved := GetResumeContext[bool](ctx)
			if !isTarget {
				return "", Interrupt(ctx, "approve "+input.Action)
			}
			if hasData && approved {
				return "approved", nil
			}
			return "rejected", nil
		})
	if err != nil {
		t.Fatalf("InferTool() error = %v", err)
	}
	return approvalTool
}

func TestFileSessionRestoresAndConsumesHITLCheckpoint(t *testing.T) {
	ctx := context.Background()
	store, err := NewFileSessionStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewFileSessionStore() error = %v", err)
	}
	const (
		sessionID = "conversation-1"
		callID    = "approval-call"
	)
	first, err := New(ctx, &Config{
		Name:  "assistant",
		Model: NewMockChatModel(MockModelToolCallWithID(callID, "approve_action", `{"action":"cleanup"}`)),
		Tools: []Tool{newCheckpointApprovalTool(t)},
		Session: &SessionConfig{
			ID:    sessionID,
			Store: store,
		},
	})
	if err != nil {
		t.Fatalf("first New() error = %v", err)
	}
	if err := first.Prompt(ctx, "clean up"); err != nil {
		t.Fatalf("Prompt() error = %v", err)
	}
	pending := first.PendingInterrupts()
	if len(pending) != 1 || pending[0].ID == "" || pending[0].Info != "approve cleanup" {
		t.Fatalf("PendingInterrupts() = %#v", pending)
	}
	persistedBeforeResume, err := store.Load(ctx, sessionID)
	if err != nil {
		t.Fatalf("Load() before resume error = %v", err)
	}
	if persistedBeforeResume.CheckpointID == "" || len(persistedBeforeResume.PendingInterrupts) != 1 {
		t.Fatalf("persisted session before resume = %#v", persistedBeforeResume)
	}
	oldCheckpointID := persistedBeforeResume.CheckpointID
	if _, existed, err := store.CheckpointStore().Get(ctx, oldCheckpointID); err != nil || !existed {
		t.Fatalf("persisted checkpoint = existed %v, error %v", existed, err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("first Close() error = %v", err)
	}

	second, err := New(ctx, &Config{
		Name:  "assistant",
		Model: NewMockChatModel(MockModelTextAfterToolResult(callID)),
		Tools: []Tool{newCheckpointApprovalTool(t)},
		Session: &SessionConfig{
			ID:    sessionID,
			Store: store,
		},
	})
	if err != nil {
		t.Fatalf("second New() error = %v", err)
	}
	defer second.Close()

	historyBeforePrompt := second.History()
	if err := second.Prompt(ctx, "do something else"); !errors.Is(err, ErrResumeRequired) {
		t.Fatalf("Prompt() with pending checkpoint error = %v, want ErrResumeRequired", err)
	}
	if got := second.History(); len(got) != len(historyBeforePrompt) {
		t.Fatalf("rejected Prompt() changed history from %d to %d messages", len(historyBeforePrompt), len(got))
	}
	restoredPending := second.PendingInterrupts()
	if len(restoredPending) != 1 || restoredPending[0].ID != pending[0].ID {
		t.Fatalf("restored pending interrupts = %#v, want %#v", restoredPending, pending)
	}

	resumeResult, err := second.ResumeWithResult(ctx, map[string]any{pending[0].ID: true})
	if err != nil {
		t.Fatalf("Resume() error = %v", err)
	}
	if resumeResult == nil || resumeResult.Text != "approved" || resumeResult.IsInterrupted() {
		t.Fatalf("ResumeWithResult() = %#v", resumeResult)
	}
	if got := second.PendingInterrupts(); len(got) != 0 {
		t.Fatalf("PendingInterrupts() after resume = %#v", got)
	}
	if got := second.History(); len(got) == 0 || got[len(got)-1].Content != "approved" {
		t.Fatalf("history after resume = %#v", got)
	}
	if _, existed, err := store.CheckpointStore().Get(ctx, oldCheckpointID); err != nil || existed {
		t.Fatalf("consumed checkpoint = existed %v, error %v", existed, err)
	}
	persistedAfterResume, err := store.Load(ctx, sessionID)
	if err != nil {
		t.Fatalf("Load() after resume error = %v", err)
	}
	if len(persistedAfterResume.PendingInterrupts) != 0 {
		t.Fatalf("persisted pending interrupts after resume = %#v", persistedAfterResume.PendingInterrupts)
	}
	if persistedAfterResume.CheckpointID == oldCheckpointID {
		t.Fatalf("checkpoint ID was not rotated after resume: %q", oldCheckpointID)
	}
}

func TestSetHistoryAndResetInvalidateCheckpoints(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryCheckpointStore()
	agent, err := New(ctx, &Config{
		Name:            "assistant",
		Model:           NewMockChatModel(MockModelText("done")),
		CheckPointStore: store,
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer agent.Close()

	oldID := agent.checkPointID
	if err := store.Set(ctx, oldID, []byte("stale")); err != nil {
		t.Fatalf("Set() error = %v", err)
	}
	agent.mu.Lock()
	agent.pendingInterrupts = []InterruptPoint{{ID: "pending"}}
	agent.mu.Unlock()
	agent.SetHistory([]*schema.Message{schema.UserMessage("replacement")})
	if _, existed, err := store.Get(ctx, oldID); err != nil || existed {
		t.Fatalf("checkpoint after SetHistory = existed %v, error %v", existed, err)
	}
	if got := agent.PendingInterrupts(); len(got) != 0 {
		t.Fatalf("pending interrupts after SetHistory = %#v", got)
	}

	resetID := agent.checkPointID
	if err := store.Set(ctx, resetID, []byte("stale again")); err != nil {
		t.Fatalf("second Set() error = %v", err)
	}
	agent.Reset()
	if _, existed, err := store.Get(ctx, resetID); err != nil || existed {
		t.Fatalf("checkpoint after Reset = existed %v, error %v", existed, err)
	}
	if got := agent.History(); len(got) != 0 {
		t.Fatalf("history after Reset = %#v", got)
	}
}

func TestClearCheckpointPersistsInvalidation(t *testing.T) {
	ctx := context.Background()
	store := NewMemorySessionStore()
	agent, err := New(ctx, &Config{
		Name:  "assistant",
		Model: NewMockChatModel(MockModelText("done")),
		Session: &SessionConfig{
			ID:    "conversation-1",
			Store: store,
		},
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer agent.Close()

	oldID := agent.checkPointID
	if err := store.CheckpointStore().Set(ctx, oldID, []byte("pending")); err != nil {
		t.Fatalf("Set() error = %v", err)
	}
	agent.mu.Lock()
	agent.pendingInterrupts = []InterruptPoint{{ID: "interrupt-1", Info: "confirm"}}
	agent.mu.Unlock()

	if err := agent.ClearCheckpoint(ctx); err != nil {
		t.Fatalf("ClearCheckpoint() error = %v", err)
	}
	if _, existed, err := store.CheckpointStore().Get(ctx, oldID); err != nil || existed {
		t.Fatalf("checkpoint after clear = existed %v, error %v", existed, err)
	}
	session, err := store.Load(ctx, "conversation-1")
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if len(session.PendingInterrupts) != 0 || session.CheckpointID == oldID {
		t.Fatalf("persisted session after clear = %#v", session)
	}
}

func TestClearCheckpointCompletesInterruptedToolTurn(t *testing.T) {
	ctx := context.Background()
	store := NewMemorySessionStore()
	const callID = "abandoned-approval"
	model := NewMockChatModel(
		MockModelToolCallWithID(callID, "approve_action", `{"action":"release"}`),
		MockExpect(MockModelText("continued"), func(call MockModelCall) error {
			if len(call.Input) != 4 {
				return fmt.Errorf("model input has %d messages, want 4", len(call.Input))
			}
			toolResult := call.Input[2]
			if toolResult.Role != schema.Tool || toolResult.ToolCallID != callID || toolResult.ToolName != "approve_action" || toolResult.Content != abandonedToolCallResult {
				return fmt.Errorf("abandoned tool result = %#v", toolResult)
			}
			return nil
		}),
	)
	agent, err := New(ctx, &Config{
		Name:  "assistant",
		Model: model,
		Tools: []Tool{newCheckpointApprovalTool(t)},
		Session: &SessionConfig{
			ID: "clear-interrupt", Store: store,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer agent.Close()
	if err := agent.Prompt(ctx, "release"); err != nil {
		t.Fatal(err)
	}
	if len(agent.PendingInterrupts()) != 1 {
		t.Fatal("agent did not retain its interrupt")
	}
	if err := agent.ClearCheckpoint(ctx); err != nil {
		t.Fatal(err)
	}
	history := agent.History()
	if len(history) != 3 || history[2].Role != schema.Tool || history[2].ToolCallID != callID {
		t.Fatalf("history after ClearCheckpoint() = %#v", history)
	}
	if err := agent.Prompt(ctx, "take another path"); err != nil {
		t.Fatalf("Prompt() after ClearCheckpoint() error = %v", err)
	}
	persisted, err := store.Load(ctx, "clear-interrupt")
	if err != nil {
		t.Fatal(err)
	}
	if len(persisted.Messages) != 5 || persisted.Messages[2].Content != abandonedToolCallResult {
		t.Fatalf("persisted history = %#v", persisted.Messages)
	}
}

func TestSessionDeleteRemovesItsCheckpoint(t *testing.T) {
	ctx := context.Background()
	memoryStore := NewMemorySessionStore()
	fileStore, err := NewFileSessionStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewFileSessionStore() error = %v", err)
	}
	stores := []struct {
		name  string
		store SessionStore
		cp    CheckpointStore
	}{
		{name: "memory", store: memoryStore, cp: memoryStore.CheckpointStore()},
		{name: "file", store: fileStore, cp: fileStore.CheckpointStore()},
	}

	for _, tt := range stores {
		t.Run(tt.name, func(t *testing.T) {
			const checkpointID = "checkpoint-1"
			if err := tt.store.Save(ctx, &Session{ID: "session-1", CheckpointID: checkpointID}); err != nil {
				t.Fatalf("Save() error = %v", err)
			}
			if err := tt.cp.Set(ctx, checkpointID, []byte("saved")); err != nil {
				t.Fatalf("checkpoint Set() error = %v", err)
			}
			if err := tt.store.Delete(ctx, "session-1"); err != nil {
				t.Fatalf("Delete() error = %v", err)
			}
			if _, existed, err := tt.cp.Get(ctx, checkpointID); err != nil || existed {
				t.Fatalf("checkpoint after session delete = existed %v, error %v", existed, err)
			}
		})
	}
}

func TestCheckpointSessionInfoReportsPendingCount(t *testing.T) {
	info := sessionInfo(&Session{
		ID:                "session-1",
		PendingInterrupts: []InterruptPoint{{ID: "one"}, {ID: "two"}},
	})
	if info.PendingInterruptCount != 2 {
		t.Fatalf("PendingInterruptCount = %d, want 2", info.PendingInterruptCount)
	}
}
