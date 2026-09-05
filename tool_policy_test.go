package agentkit

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"sync"
	"testing"
)

type policySearchInput struct {
	Query string `json:"query"`
}

func TestToolPolicyResolvesAliasesBeforeRewritingArguments(t *testing.T) {
	ctx := context.Background()
	search := MustMockTool("search", "search text", func(_ context.Context, input *policySearchInput) (string, error) {
		return "found " + input.Query, nil
	})
	var rewrittenName, rewrittenArguments string
	middlewareCalled := false
	agent, err := New(ctx, &Config{
		Model: NewMockChatModel(
			MockModelToolCallWithID("search-call", "lookup", `{"q":"agentkit"}`),
			MockModelTextAfterToolResult("search-call"),
		),
		Tools: MockTools(search),
		ToolPolicy: &ToolPolicy{
			Aliases: map[string]ToolAlias{
				"search": {
					Names:     []string{"lookup"},
					Arguments: map[string][]string{"query": {"q"}},
				},
			},
			RewriteArguments: func(_ context.Context, name, arguments string) (string, error) {
				rewrittenName, rewrittenArguments = name, arguments
				return arguments, nil
			},
			Middlewares: []ToolMiddleware{{
				Invokable: func(next InvokableToolEndpoint) InvokableToolEndpoint {
					return func(ctx context.Context, input *ToolInput) (*ToolOutput, error) {
						middlewareCalled = true
						return next(ctx, input)
					}
				},
			}},
		},
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer agent.Close()

	result, err := agent.Ask(ctx, "find agentkit")
	if err != nil {
		t.Fatalf("Ask() error = %v", err)
	}
	if result.Text != "found agentkit" {
		t.Fatalf("result text = %q", result.Text)
	}
	if rewrittenName != "search" {
		t.Fatalf("rewrite tool name = %q, want search", rewrittenName)
	}
	if !middlewareCalled {
		t.Fatal("tool middleware was not called")
	}
	var arguments map[string]string
	if err := json.Unmarshal([]byte(rewrittenArguments), &arguments); err != nil {
		t.Fatalf("rewrite arguments %q: %v", rewrittenArguments, err)
	}
	if arguments["query"] != "agentkit" {
		t.Fatalf("rewrite arguments = %v", arguments)
	}
}

func TestToolPolicyHandlesUnknownTool(t *testing.T) {
	ctx := context.Background()
	var gotName, gotArguments string
	dummy := MustMockTool("available", "available tool", func(context.Context, string) (string, error) {
		return "available", nil
	})
	agent, err := New(ctx, &Config{
		Model: NewMockChatModel(
			MockModelToolCallWithID("missing-call", "missing", `{"value":1}`),
			MockModelTextAfterToolResult("missing-call"),
		),
		Tools: MockTools(dummy),
		ToolPolicy: &ToolPolicy{
			UnknownTool: func(_ context.Context, name, arguments string) (string, error) {
				gotName, gotArguments = name, arguments
				return "tool unavailable", nil
			},
		},
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer agent.Close()

	result, err := agent.Ask(ctx, "call it")
	if err != nil {
		t.Fatalf("Ask() error = %v", err)
	}
	if result.Text != "tool unavailable" || gotName != "missing" || gotArguments != `{"value":1}` {
		t.Fatalf("unknown tool result = %#v, name %q, arguments %q", result, gotName, gotArguments)
	}
}

func TestToolPolicyExecutesCallsSequentially(t *testing.T) {
	ctx := context.Background()
	var mu sync.Mutex
	var order []string
	newTool := func(name string) *MockTool[*policySearchInput, string] {
		return MustMockTool(name, name, func(_ context.Context, _ *policySearchInput) (string, error) {
			mu.Lock()
			order = append(order, name)
			mu.Unlock()
			return name, nil
		})
	}
	firstTool, secondTool := newTool("first"), newTool("second")
	first, err := firstTool.Invocation("first-call", &policySearchInput{})
	if err != nil {
		t.Fatalf("first Invocation() error = %v", err)
	}
	second, err := secondTool.Invocation("second-call", &policySearchInput{})
	if err != nil {
		t.Fatalf("second Invocation() error = %v", err)
	}
	agent, err := New(ctx, &Config{
		Model: NewMockChatModel(
			MockModelCalls(first, second),
			MockModelTextAfterAll("done", first, second),
		),
		Tools:      MockTools(firstTool, secondTool),
		ToolPolicy: &ToolPolicy{Sequential: true},
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer agent.Close()
	if _, err := agent.Ask(ctx, "run both"); err != nil {
		t.Fatalf("Ask() error = %v", err)
	}
	if !slices.Equal(order, []string{"first", "second"}) {
		t.Fatalf("tool execution order = %v", order)
	}
}

func TestToolPolicyValidatesAliases(t *testing.T) {
	ctx := context.Background()
	one := MustMockTool("one", "one", func(context.Context, string) (string, error) { return "", nil })
	two := MustMockTool("two", "two", func(context.Context, string) (string, error) { return "", nil })
	tests := []struct {
		name    string
		aliases map[string]ToolAlias
		want    string
	}{
		{name: "unknown canonical tool", aliases: map[string]ToolAlias{"missing": {}}, want: "unknown canonical tool"},
		{name: "canonical name collision", aliases: map[string]ToolAlias{"one": {Names: []string{"two"}}}, want: "conflicts"},
		{name: "duplicate alias", aliases: map[string]ToolAlias{
			"one": {Names: []string{"shared"}},
			"two": {Names: []string{"shared"}},
		}, want: "conflicts"},
		{name: "argument alias collision", aliases: map[string]ToolAlias{
			"one": {Arguments: map[string][]string{"query": {"value"}, "value": nil}},
		}, want: "argument alias"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := New(ctx, &Config{
				Model:      NewMockChatModel(),
				Tools:      MockTools(one, two),
				ToolPolicy: &ToolPolicy{Aliases: tt.aliases},
			})
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("New() error = %v, want containing %q", err, tt.want)
			}
		})
	}
}

func TestToolPolicyAcceptsSkillToolAlias(t *testing.T) {
	backend, err := NewMemorySkillBackend(Skill{
		FrontMatter: SkillInfo{Name: "answer", Description: "answer consistently"},
		Content:     "Answer concisely.",
	})
	if err != nil {
		t.Fatalf("NewMemorySkillBackend() error = %v", err)
	}
	agent, err := New(context.Background(), &Config{
		Model:  NewMockChatModel(),
		Skills: &SkillsConfig{Backend: backend},
		ToolPolicy: &ToolPolicy{Aliases: map[string]ToolAlias{
			"skill": {Names: []string{"load_skill"}},
		}},
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer agent.Close()
}
