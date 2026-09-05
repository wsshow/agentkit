package agentkit

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	einotool "github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
)

func TestToolReductionOffloadsAndReadsCompleteResult(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryToolResultStore()
	fullOutput := strings.Repeat("0123456789", 30)
	largeTool := MustMockTool("large_output", "return a large output", func(context.Context, string) (string, error) {
		return fullOutput, nil
	})
	model := NewMockChatModel(
		MockModelToolCallWithID("large-call", "large_output", `""`),
		MockModelRespondsAfterToolResult("large-call", func(reduced string) MockModelResponse {
			if !strings.Contains(reduced, ToolResultReadToolName) {
				return MockModelError(fmt.Errorf("reduction notice does not mention %s: %q", ToolResultReadToolName, reduced))
			}
			infos, err := store.List(context.Background())
			if err != nil || len(infos) != 1 {
				return MockModelError(fmt.Errorf("stored results = %#v, error = %v", infos, err))
			}
			arguments := fmt.Sprintf(`{"id":%q,"offset":10,"limit":15}`, infos[0].ID)
			return MockModelToolCallWithID("read-call", ToolResultReadToolName, arguments)
		}),
		MockModelTextAfterToolResult("read-call"),
	)
	agent, err := New(ctx, &Config{
		Name:  "assistant",
		Model: model,
		Tools: MockTools(largeTool),
		// The reduction layer owns result sizing when enabled, so this legacy
		// limit must not cut the opaque result ID out of the reduction notice.
		ToolPolicy:    &ToolPolicy{MaxResultChars: 10},
		ToolReduction: &ToolReductionConfig{Store: store, MaxResultBytes: 80},
	})
	if err != nil {
		t.Fatalf("create agent: %v", err)
	}
	t.Cleanup(func() { _ = agent.Close() })

	result, err := agent.Ask(ctx, "get the complete output")
	if err != nil {
		t.Fatalf("ask with reduction: %v", err)
	}
	if !strings.HasPrefix(result.Text, fullOutput[10:25]) || !strings.Contains(result.Text, "next_offset=25") {
		t.Fatalf("unexpected read result: %q", result.Text)
	}
	if len(result.ToolCalls) != 2 || result.ToolCalls[1].Function.Name != ToolResultReadToolName {
		t.Fatalf("unexpected tool calls: %#v", result.ToolCalls)
	}
	if agent.ToolResultStore() != store {
		t.Fatal("agent did not expose the configured tool result store")
	}
	infos, err := store.List(ctx)
	if err != nil {
		t.Fatalf("list stored results: %v", err)
	}
	stored, err := store.Load(ctx, infos[0].ID)
	if err != nil {
		t.Fatalf("load complete result: %v", err)
	}
	if stored.Content != fullOutput {
		t.Fatalf("stored result was truncated: got %d bytes, want %d", len(stored.Content), len(fullOutput))
	}
}

func TestToolReductionPersistsClearedContext(t *testing.T) {
	ctx := context.Background()
	sessions := NewMemorySessionStore()
	firstTool := MustMockTool("first_tool", "return first payload", func(context.Context, string) (string, error) {
		return "first payload", nil
	})
	secondTool := MustMockTool("second_tool", "return second payload", func(context.Context, string) (string, error) {
		return "second payload", nil
	})
	model := NewMockChatModel(
		MockModelToolCallWithID("first-call", "first_tool", `""`),
		MockModelToolCallWithID("second-call", "second_tool", `""`),
		MockModelText("done"),
	)
	agent, err := New(ctx, &Config{
		Name:  "assistant",
		Model: model,
		Tools: MockTools(firstTool, secondTool),
		Session: &SessionConfig{
			ID: "session", Store: sessions,
		},
		ToolReduction: &ToolReductionConfig{
			MaxResultBytes: 1_000, MaxContextTokens: 1, KeepRecentToolRounds: 1,
		},
	})
	if err != nil {
		t.Fatalf("create agent: %v", err)
	}
	t.Cleanup(func() { _ = agent.Close() })

	if _, err := agent.Ask(ctx, "run both tools"); err != nil {
		t.Fatalf("ask with history reduction: %v", err)
	}
	if agent.ToolResultStore() != sessions.ToolResultStore() {
		t.Fatal("agent did not reuse the session tool result store")
	}
	stored, err := sessions.ToolResultStore().List(ctx)
	if err != nil {
		t.Fatalf("list cleared results: %v", err)
	}
	if len(stored) != 1 {
		t.Fatalf("cleared result count = %d, want 1: %#v", len(stored), stored)
	}
	complete, err := sessions.ToolResultStore().Load(ctx, stored[0].ID)
	if err != nil {
		t.Fatalf("load cleared result: %v", err)
	}
	if complete.Content != "first payload" {
		t.Fatalf("unexpected cleared result content: %q", complete.Content)
	}

	history := agent.History()
	contextHistory := agent.ContextHistory()
	if got := toolMessageContent(history, "first-call"); got != "first payload" {
		t.Fatalf("complete history was rewritten: %q", got)
	}
	if got := toolMessageContent(contextHistory, "first-call"); !strings.Contains(got, ToolResultReadToolName) {
		t.Fatalf("old context result was not reduced: %q", got)
	}
	if got := toolMessageContent(contextHistory, "second-call"); got != "second payload" {
		t.Fatalf("recent tool round was not retained: %q", got)
	}
	session := agent.Session()
	if session == nil || session.Context == nil || !strings.Contains(toolMessageContent(session.Context, "first-call"), ToolResultReadToolName) {
		t.Fatalf("reduced context was not persisted: %#v", session)
	}
}

func TestToolReductionCoversDynamicallyDiscoveredTools(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryToolResultStore()
	fullOutput := strings.Repeat("dynamic result ", 200)
	dynamic := MustMockTool("dynamic_report", "generate a detailed report", func(context.Context, string) (string, error) {
		return fullOutput, nil
	})
	model := NewMockChatModel(
		MockModelToolCallWithID("search-call", toolSearchToolName, `{"query":"detailed report"}`),
		MockModelToolCallWithID("dynamic-call", "dynamic_report", `""`),
		MockModelTextAfterToolResult("dynamic-call"),
	)
	agent, err := New(ctx, &Config{
		Name:       "assistant",
		Model:      model,
		ToolSearch: &ToolSearchConfig{Tools: MockTools(dynamic)},
		ToolReduction: &ToolReductionConfig{
			Store: store, MaxResultBytes: 1_000,
		},
	})
	if err != nil {
		t.Fatalf("create agent: %v", err)
	}
	t.Cleanup(func() { _ = agent.Close() })

	result, err := agent.Ask(ctx, "generate the report")
	if err != nil {
		t.Fatalf("ask with dynamic reduction: %v", err)
	}
	if !strings.Contains(result.Text, ToolResultReadToolName) {
		t.Fatalf("dynamic result was not reduced: %q", result.Text)
	}
	infos, err := store.List(ctx)
	if err != nil || len(infos) != 1 {
		t.Fatalf("stored dynamic results = %#v, error = %v", infos, err)
	}
	stored, err := store.Load(ctx, infos[0].ID)
	if err != nil {
		t.Fatalf("load dynamic result: %v", err)
	}
	if stored.Content != fullOutput {
		t.Fatalf("dynamic result was not stored completely: got %d bytes, want %d", len(stored.Content), len(fullOutput))
	}
}

func TestToolReductionResultSurvivesAgentRestart(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	firstSessions, err := NewFileSessionStore(dir)
	if err != nil {
		t.Fatalf("create first session store: %v", err)
	}
	fullOutput := strings.Repeat("durable output ", 30)
	largeTool := MustMockTool("durable_report", "return a durable report", func(context.Context, string) (string, error) {
		return fullOutput, nil
	})
	firstAgent, err := New(ctx, &Config{
		Name: "assistant",
		Model: NewMockChatModel(
			MockModelToolCallWithID("report-call", "durable_report", `""`),
			MockModelText("saved"),
		),
		Tools:         MockTools(largeTool),
		Session:       &SessionConfig{ID: "durable-session", Store: firstSessions},
		ToolReduction: &ToolReductionConfig{MaxResultBytes: 80},
	})
	if err != nil {
		t.Fatalf("create first agent: %v", err)
	}
	if _, err := firstAgent.Ask(ctx, "create the report"); err != nil {
		_ = firstAgent.Close()
		t.Fatalf("run first agent: %v", err)
	}
	if err := firstAgent.Close(); err != nil {
		t.Fatalf("close first agent: %v", err)
	}
	infos, err := firstSessions.ToolResultStore().List(ctx)
	if err != nil || len(infos) != 1 {
		t.Fatalf("stored results before restart = %#v, error = %v", infos, err)
	}

	secondSessions, err := NewFileSessionStore(dir)
	if err != nil {
		t.Fatalf("reopen session store: %v", err)
	}
	arguments := fmt.Sprintf(`{"id":%q,"limit":20}`, infos[0].ID)
	secondAgent, err := New(ctx, &Config{
		Name: "assistant",
		Model: NewMockChatModel(
			MockModelToolCallWithID("read-after-restart", ToolResultReadToolName, arguments),
			MockModelTextAfterToolResult("read-after-restart"),
		),
		Session:       &SessionConfig{ID: "durable-session", Store: secondSessions},
		ToolReduction: &ToolReductionConfig{MaxResultBytes: 80},
	})
	if err != nil {
		t.Fatalf("recreate agent: %v", err)
	}
	t.Cleanup(func() { _ = secondAgent.Close() })

	result, err := secondAgent.Ask(ctx, "read the saved report")
	if err != nil {
		t.Fatalf("read after restart: %v", err)
	}
	if !strings.HasPrefix(result.Text, fullOutput[:20]) {
		t.Fatalf("unexpected result after restart: %q", result.Text)
	}
}

func TestToolResultReaderUsesUnicodeOffsetsAndBounds(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryToolResultStore()
	if err := store.Save(ctx, &StoredToolResult{ID: "unicode", Content: "甲乙丙丁戊"}); err != nil {
		t.Fatalf("save result: %v", err)
	}
	reader, err := newToolResultReader(store)
	if err != nil {
		t.Fatalf("create reader: %v", err)
	}
	info, err := reader.Info(ctx)
	if err != nil {
		t.Fatalf("inspect reader: %v", err)
	}
	if info.Name != ToolResultReadToolName {
		t.Fatalf("reader name = %q", info.Name)
	}
	invokable, ok := reader.(einotool.InvokableTool)
	if !ok {
		t.Fatal("reader is not invokable")
	}
	output, err := invokable.InvokableRun(ctx, `{"id":"unicode","offset":1,"limit":2}`)
	if err != nil {
		t.Fatalf("read Unicode result: %v", err)
	}
	if !strings.HasPrefix(output, "乙丙") || !strings.Contains(output, "next_offset=3") {
		t.Fatalf("unexpected Unicode chunk: %q", output)
	}
	if _, err := invokable.InvokableRun(ctx, `{"id":"unicode","offset":6}`); err == nil {
		t.Fatal("expected out-of-range offset error")
	}
	if _, err := invokable.InvokableRun(ctx, `{"id":"unicode","limit":-1}`); err == nil {
		t.Fatal("expected negative limit error")
	}
}

func TestToolReductionValidatesConfigurationAndReaderName(t *testing.T) {
	ctx := context.Background()
	reserved := MustMockTool(ToolResultReadToolName, "reserved", func(context.Context, string) (string, error) {
		return "", nil
	})
	tests := []struct {
		name string
		cfg  *Config
		want string
	}{
		{
			name: "negative result bytes",
			cfg:  &Config{Model: NewMockChatModel(), ToolReduction: &ToolReductionConfig{MaxResultBytes: -1}},
			want: "max result bytes",
		},
		{
			name: "negative context tokens",
			cfg:  &Config{Model: NewMockChatModel(), ToolReduction: &ToolReductionConfig{MaxContextTokens: -1}},
			want: "max context tokens",
		},
		{
			name: "negative retained rounds",
			cfg:  &Config{Model: NewMockChatModel(), ToolReduction: &ToolReductionConfig{KeepRecentToolRounds: -1}},
			want: "keep recent tool rounds",
		},
		{
			name: "reserved reader name",
			cfg: &Config{
				Model: NewMockChatModel(), Tools: MockTools(reserved), ToolReduction: &ToolReductionConfig{},
			},
			want: "duplicate tool name",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			agent, err := New(ctx, tt.cfg)
			if agent != nil {
				_ = agent.Close()
			}
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("expected error containing %q, got %v", tt.want, err)
			}
		})
	}
}

func TestToolReductionUsesMemoryStoreWithoutSession(t *testing.T) {
	agent, err := New(context.Background(), &Config{
		Model: NewMockChatModel(), ToolReduction: &ToolReductionConfig{},
	})
	if err != nil {
		t.Fatalf("create agent: %v", err)
	}
	t.Cleanup(func() { _ = agent.Close() })
	if _, ok := agent.ToolResultStore().(*MemoryToolResultStore); !ok {
		t.Fatalf("fallback store type = %T, want *MemoryToolResultStore", agent.ToolResultStore())
	}
}

func TestToolReductionDoesNotMutateRelatedConfigs(t *testing.T) {
	policy := &ToolPolicy{MaxResultChars: 10}
	effectivePolicy := toolPolicyForReduction(policy, true)
	if effectivePolicy == policy || effectivePolicy.MaxResultChars != -1 || policy.MaxResultChars != 10 {
		t.Fatalf("unexpected effective tool policy: original=%#v effective=%#v", policy, effectivePolicy)
	}
	mcp := &MCPConfig{}
	effectiveMCP := mcpConfigForReduction(mcp, true)
	if effectiveMCP == mcp || effectiveMCP.MaxResultChars != -1 || mcp.MaxResultChars != 0 {
		t.Fatalf("unexpected effective MCP config: original=%#v effective=%#v", mcp, effectiveMCP)
	}
	explicitMCP := &MCPConfig{MaxResultChars: 100}
	if got := mcpConfigForReduction(explicitMCP, true); got != explicitMCP {
		t.Fatal("explicit MCP result limit should be preserved")
	}
}

func toolMessageContent(messages []*schema.Message, callID string) string {
	for _, message := range messages {
		if message != nil && message.Role == schema.Tool && message.ToolCallID == callID {
			return message.Content
		}
	}
	return ""
}

func TestToolResultBackendRejectsNilRequest(t *testing.T) {
	backend := &toolResultBackend{store: NewMemoryToolResultStore()}
	if err := backend.Write(context.TODO(), nil); err == nil {
		t.Fatal("expected nil request error")
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	err := backend.Write(canceled, nil)
	if err == nil || errors.Is(err, context.Canceled) {
		t.Fatalf("nil request should be validated before context, got %v", err)
	}
}
