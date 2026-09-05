package agentkit

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/schema"
)

func TestAgentCompactionPreservesFullSessionAndRestoresCompactContext(t *testing.T) {
	ctx := context.Background()
	store := NewMemorySessionStore()
	initial := []*schema.Message{
		schema.UserMessage("old question one"),
		schema.AssistantMessage("old answer one", nil),
		schema.UserMessage("old question two"),
		schema.AssistantMessage("old answer two", nil),
	}
	if err := store.Save(ctx, &Session{
		ID:        "compaction-session",
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
		Messages:  initial,
	}); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	summaryModel := NewMockChatModel(MockExpect(MockModelText("summary of older turns"), func(call MockModelCall) error {
		contents := nonSystemContents(call.Input)
		if !slices.Contains(contents, "old question one") || !slices.Contains(contents, "old answer two") {
			return fmt.Errorf("summary input does not contain older turns: %v", contents)
		}
		if slices.Contains(contents, "latest question") {
			return fmt.Errorf("summary input contains preserved latest turn: %v", contents)
		}
		return nil
	}))
	primaryModel := NewMockChatModel(MockExpect(MockModelText("latest answer"), func(call MockModelCall) error {
		contents := nonSystemContents(call.Input)
		if !containsSubstring(contents, "summary of older turns") {
			return fmt.Errorf("primary input does not contain summary: %v", contents)
		}
		if !slices.Contains(contents, "latest question") {
			return fmt.Errorf("primary input does not preserve latest question: %v", contents)
		}
		if slices.Contains(contents, "old answer one") {
			return fmt.Errorf("primary input still contains compacted message: %v", contents)
		}
		return nil
	}))

	agent, err := New(ctx, &Config{
		Name:         "assistant",
		SystemPrompt: "always be precise",
		Model:        primaryModel,
		Session: &SessionConfig{
			ID:    "compaction-session",
			Store: store,
		},
		Compaction: &CompactionConfig{
			Model:           summaryModel,
			MaxMessages:     4,
			KeepRecentTurns: 1,
		},
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	events := newMockEventRecorder()
	agent.Subscribe(events.Record)
	if err := agent.Prompt(ctx, "latest question"); err != nil {
		t.Fatalf("Prompt() error = %v", err)
	}
	_ = agent.Close()

	if got := len(agent.History()); got != 6 {
		t.Fatalf("full history length = %d, want 6", got)
	}
	contextHistory := agent.ContextHistory()
	if got := len(contextHistory); got != 3 {
		t.Fatalf("context history length = %d, want 3", got)
	}
	if !strings.Contains(contextHistory[0].Content, "summary of older turns") || contextHistory[1].Content != "latest question" || contextHistory[2].Content != "latest answer" {
		t.Fatalf("context history = %#v", contextHistory)
	}
	start := events.Last(EventCompactionStart)
	end := events.Last(EventCompactionEnd)
	if start == nil || start.Compaction == nil || start.Compaction.MessagesBefore != 6 {
		t.Fatalf("compaction start = %#v", start)
	}
	if end == nil || end.Compaction == nil || end.Compaction.MessagesBefore != 6 || end.Compaction.MessagesAfter != 3 {
		t.Fatalf("compaction end = %#v", end)
	}
	if got := countMessageRole(contextHistory, schema.System); got != 0 {
		t.Fatalf("stored context contains %d runtime system messages", got)
	}

	persisted, err := store.Load(ctx, "compaction-session")
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if len(persisted.Messages) != 6 || len(persisted.Context) != 3 {
		t.Fatalf("persisted full/context lengths = %d/%d, want 6/3", len(persisted.Messages), len(persisted.Context))
	}

	restoredModel := NewMockChatModel(MockExpect(MockModelText("restored answer"), func(call MockModelCall) error {
		contents := nonSystemContents(call.Input)
		if !containsSubstring(contents, "summary of older turns") || !slices.Contains(contents, "after restart") {
			return fmt.Errorf("restored model input = %v", contents)
		}
		if slices.Contains(contents, "old question one") {
			return fmt.Errorf("restored model received full history instead of compact context: %v", contents)
		}
		if got := countMessageRole(call.Input, schema.System); got != 1 {
			return fmt.Errorf("restored model received %d system messages, want 1", got)
		}
		return nil
	}))
	restored, err := New(ctx, &Config{
		Name:         "assistant",
		SystemPrompt: "always be precise",
		Model:        restoredModel,
		Session: &SessionConfig{
			ID:    "compaction-session",
			Store: store,
		},
	})
	if err != nil {
		t.Fatalf("New() restore error = %v", err)
	}
	defer restored.Close()
	if err := restored.Prompt(ctx, "after restart"); err != nil {
		t.Fatalf("restored Prompt() error = %v", err)
	}
	if got := len(restored.History()); got != 8 {
		t.Fatalf("restored full history length = %d, want 8", got)
	}
}

func TestAgentCompactionMessageThresholdDoesNotTriggerEarly(t *testing.T) {
	ctx := context.Background()
	agent, err := New(ctx, &Config{
		Name:  "assistant",
		Model: NewMockChatModel(MockModelText("answer")),
		History: []*schema.Message{
			schema.UserMessage("old question"),
			schema.AssistantMessage("old answer", nil),
		},
		Compaction: &CompactionConfig{
			Model:       NewMockChatModel(),
			MaxMessages: 4,
		},
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer agent.Close()
	events := newMockEventRecorder()
	agent.Subscribe(events.Record)
	if err := agent.Prompt(ctx, "new question"); err != nil {
		t.Fatalf("Prompt() error = %v", err)
	}
	if got := events.Count(EventCompactionStart); got != 0 {
		t.Fatalf("compaction start count = %d, want 0", got)
	}
	if got := len(agent.ContextHistory()); got != 4 {
		t.Fatalf("context history length = %d, want 4", got)
	}
}

func TestAgentCompactionPreservesOnlyAvailableRecentTurn(t *testing.T) {
	ctx := context.Background()
	summaryModel := NewMockChatModel(MockModelText("must not be used"))
	primaryModel := NewMockChatModel(MockExpect(MockModelText("answer"), func(call MockModelCall) error {
		contents := nonSystemContents(call.Input)
		if !slices.Contains(contents, "the only user turn must stay exact") {
			return fmt.Errorf("primary input lost the only user turn: %v", contents)
		}
		if containsSubstring(contents, "must not be used") {
			return fmt.Errorf("primary input contains an unexpected summary: %v", contents)
		}
		return nil
	}))
	agent, err := New(ctx, &Config{
		Name:         "assistant",
		SystemPrompt: "be precise",
		Model:        primaryModel,
		Compaction: &CompactionConfig{
			Model:           summaryModel,
			MaxTokens:       1,
			KeepRecentTurns: 3,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer agent.Close()
	events := newMockEventRecorder()
	agent.Subscribe(events.Record)

	if err := agent.Prompt(ctx, "the only user turn must stay exact"); err != nil {
		t.Fatalf("Prompt() error = %v", err)
	}
	if calls := summaryModel.Calls(); len(calls) != 0 {
		t.Fatalf("summary model calls = %d, want 0", len(calls))
	}
	if got := events.Count(EventCompactionStart); got != 0 {
		t.Fatalf("compaction start count = %d, want 0", got)
	}
	contextHistory := agent.ContextHistory()
	if len(contextHistory) != 2 || contextHistory[0].Content != "the only user turn must stay exact" {
		t.Fatalf("ContextHistory() = %#v, want exact user turn and answer", contextHistory)
	}
}

func TestSplitCompactionHistoryKeepsAllWhenTurnsAreInsufficient(t *testing.T) {
	messages := []*schema.Message{
		schema.SystemMessage("system"),
		schema.UserMessage("first"),
		schema.AssistantMessage("answer", nil),
		schema.UserMessage("second"),
	}
	older, recent := splitCompactionHistory(messages, 3)
	if len(older) != 0 || len(recent) != len(messages) {
		t.Fatalf("split = older %d, recent %d; want 0, %d", len(older), len(recent), len(messages))
	}
}

func TestAgentCompactionFailureKeepsOriginalContext(t *testing.T) {
	ctx := context.Background()
	summaryErr := errors.New("summary unavailable")
	agent, err := New(ctx, &Config{
		Name:  "assistant",
		Model: NewMockChatModel(),
		History: []*schema.Message{
			schema.UserMessage("question one"),
			schema.AssistantMessage("answer one", nil),
			schema.UserMessage("question two"),
			schema.AssistantMessage("answer two", nil),
		},
		Compaction: &CompactionConfig{
			Model:       NewMockChatModel(MockModelError(summaryErr)),
			MaxMessages: 4,
		},
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer agent.Close()
	events := newMockEventRecorder()
	agent.Subscribe(events.Record)

	err = agent.Prompt(ctx, "latest question")
	if !errors.Is(err, summaryErr) {
		t.Fatalf("Prompt() error = %v, want %v", err, summaryErr)
	}
	contextHistory := agent.ContextHistory()
	if len(contextHistory) != 5 || contextHistory[0].Content != "question one" || contextHistory[4].Content != "latest question" {
		t.Fatalf("context changed after failed compaction: %#v", contextHistory)
	}
	if got := events.Count(EventCompactionStart); got != 1 {
		t.Fatalf("compaction start count = %d, want 1", got)
	}
	if got := events.Count(EventCompactionEnd); got != 0 {
		t.Fatalf("compaction end count = %d, want 0", got)
	}
}

func TestAgentCompactionReusesModelRetry(t *testing.T) {
	ctx := context.Background()
	transient := errors.New("temporary summary failure")
	summaryModel := NewMockChatModel(
		MockModelError(transient),
		MockModelText("summary after retry"),
	)
	primaryModel := NewMockChatModel(MockExpect(MockModelText("latest answer"), func(call MockModelCall) error {
		if !containsSubstring(nonSystemContents(call.Input), "summary after retry") {
			return fmt.Errorf("primary input does not contain retried summary: %v", nonSystemContents(call.Input))
		}
		return nil
	}))
	agent, err := New(ctx, &Config{
		Name:  "assistant",
		Model: primaryModel,
		History: []*schema.Message{
			schema.UserMessage("question one"),
			schema.AssistantMessage("answer one", nil),
			schema.UserMessage("question two"),
			schema.AssistantMessage("answer two", nil),
		},
		ModelRetryConfig: &ModelRetryConfig{
			MaxRetries:  1,
			BackoffFunc: func(context.Context, int) time.Duration { return 0 },
		},
		Compaction: &CompactionConfig{
			Model:       summaryModel,
			MaxMessages: 4,
		},
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	t.Cleanup(func() { _ = agent.Close() })

	if err := agent.Prompt(ctx, "latest question"); err != nil {
		t.Fatalf("Prompt() error = %v", err)
	}
	if got := len(summaryModel.Calls()); got != 2 {
		t.Fatalf("summary model calls = %d, want 2", got)
	}
}

func TestAgentCompactionRetriesBeforeModelFailover(t *testing.T) {
	ctx := context.Background()
	primaryErr := errors.New("summary model unavailable")
	summaryModel := NewMockChatModel(
		MockModelError(primaryErr),
		MockModelError(primaryErr),
	)
	backupModel := NewMockChatModel(MockModelText("summary from backup"))
	primaryModel := NewMockChatModel(MockExpect(MockModelText("latest answer"), func(call MockModelCall) error {
		if !containsSubstring(nonSystemContents(call.Input), "summary from backup") {
			return fmt.Errorf("primary input does not contain backup summary: %v", nonSystemContents(call.Input))
		}
		return nil
	}))
	var failoverCalls int
	agent, err := New(ctx, &Config{
		Name:  "assistant",
		Model: primaryModel,
		History: []*schema.Message{
			schema.UserMessage("question one"),
			schema.AssistantMessage("answer one", nil),
			schema.UserMessage("question two"),
			schema.AssistantMessage("answer two", nil),
		},
		ModelRetryConfig: &ModelRetryConfig{
			MaxRetries:  1,
			BackoffFunc: func(context.Context, int) time.Duration { return 0 },
		},
		ModelFailoverConfig: &ModelFailoverConfig{
			MaxRetries: 1,
			ShouldFailover: func(_ context.Context, _ *schema.Message, err error) bool {
				var exhausted *adk.RetryExhaustedError
				return errors.As(err, &exhausted) && errors.Is(exhausted.LastErr, primaryErr)
			},
			GetFailoverModel: func(_ context.Context, failover *adk.FailoverContext[*schema.Message]) (ChatModel, []*schema.Message, error) {
				failoverCalls++
				if failover.FailoverAttempt != 1 {
					t.Fatalf("failover attempt = %d, want 1", failover.FailoverAttempt)
				}
				return backupModel, nil, nil
			},
		},
		Compaction: &CompactionConfig{
			Model:       summaryModel,
			MaxMessages: 4,
		},
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	t.Cleanup(func() { _ = agent.Close() })

	if err := agent.Prompt(ctx, "latest question"); err != nil {
		t.Fatalf("Prompt() error = %v", err)
	}
	if len(summaryModel.Calls()) != 2 || len(backupModel.Calls()) != 1 || failoverCalls != 1 {
		t.Fatalf("summary calls = primary %d, backup %d, failover %d; want 2, 1, 1",
			len(summaryModel.Calls()), len(backupModel.Calls()), failoverCalls)
	}
}

func nonSystemContents(messages []*schema.Message) []string {
	contents := make([]string, 0, len(messages))
	for _, message := range messages {
		if message != nil && message.Role != schema.System {
			contents = append(contents, message.Content)
		}
	}
	return contents
}

func containsSubstring(values []string, substring string) bool {
	for _, value := range values {
		if strings.Contains(value, substring) {
			return true
		}
	}
	return false
}

func countMessageRole(messages []*schema.Message, role schema.RoleType) int {
	count := 0
	for _, message := range messages {
		if message != nil && message.Role == role {
			count++
		}
	}
	return count
}
