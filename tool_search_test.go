package agentkit

import (
	"context"
	"errors"
	"strings"
	"testing"
)

type toolSearchWeatherInput struct {
	City string `json:"city"`
}

func TestToolSearchLoadsDynamicToolOnDemand(t *testing.T) {
	ctx := context.Background()
	weather := MustMockTool("weather_lookup", "look up current weather by city",
		func(_ context.Context, input *toolSearchWeatherInput) (string, error) {
			return "sunny in " + input.City, nil
		})
	var guardedTool string
	model := NewMockChatModel(
		MockModelToolCallWithID("search-call", toolSearchToolName, `{"query":"weather"}`),
		MockModelToolCallWithID("weather-call", "weather_lookup", `{"city":"Shanghai"}`),
		MockModelTextAfterToolResult("weather-call"),
	)
	agent, err := New(ctx, &Config{
		Name:  "assistant",
		Model: model,
		ToolSearch: &ToolSearchConfig{
			Tools: MockTools(weather),
		},
		ToolPolicy: &ToolPolicy{
			AfterTool: func(_ context.Context, call ToolInvocation, _ ToolOutcome) {
				if call.Name == "weather_lookup" {
					guardedTool = call.Name
				}
			},
		},
	})
	if err != nil {
		t.Fatalf("create agent: %v", err)
	}
	t.Cleanup(func() { _ = agent.Close() })

	result, err := agent.Ask(ctx, "what is the weather in Shanghai?")
	if err != nil {
		t.Fatalf("ask with tool search: %v", err)
	}
	if result.Text != "sunny in Shanghai" {
		t.Fatalf("unexpected result: %q", result.Text)
	}
	if guardedTool != "weather_lookup" {
		t.Fatalf("dynamic tool did not use ToolPolicy, got %q", guardedTool)
	}
	if len(result.ToolCalls) != 2 {
		t.Fatalf("expected search and dynamic tool calls, got %#v", result.ToolCalls)
	}
}

func TestToolSearchValidatesConfiguration(t *testing.T) {
	ctx := context.Background()
	reserved := MustMockTool(toolSearchToolName, "reserved", func(context.Context, string) (string, error) {
		return "", nil
	})
	duplicate := MustMockTool("duplicate", "duplicate", func(context.Context, string) (string, error) {
		return "", nil
	})
	tests := []struct {
		name string
		cfg  *Config
		want string
	}{
		{
			name: "empty dynamic tools",
			cfg:  &Config{Model: NewMockChatModel(), ToolSearch: &ToolSearchConfig{}},
			want: "at least one dynamic tool",
		},
		{
			name: "reserved name",
			cfg: &Config{
				Model:      NewMockChatModel(),
				ToolSearch: &ToolSearchConfig{Tools: MockTools(reserved)},
			},
			want: "reserved",
		},
		{
			name: "duplicate static and dynamic name",
			cfg: &Config{
				Model:      NewMockChatModel(),
				Tools:      MockTools(duplicate),
				ToolSearch: &ToolSearchConfig{Tools: MockTools(duplicate)},
			},
			want: "duplicate tool name",
		},
		{
			name: "reserved alias",
			cfg: &Config{
				Model: NewMockChatModel(),
				Tools: MockTools(duplicate),
				ToolSearch: &ToolSearchConfig{
					Tools: MockTools(MustMockTool("dynamic", "dynamic", func(context.Context, string) (string, error) {
						return "", nil
					})),
				},
				ToolPolicy: &ToolPolicy{Aliases: map[string]ToolAlias{
					"duplicate": {Names: []string{toolSearchToolName}},
				}},
			},
			want: "reserved",
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

func TestToolSearchRejectsNilDynamicTool(t *testing.T) {
	err := validateToolSearchConfig(&ToolSearchConfig{Tools: []Tool{nil}})
	if err == nil {
		t.Fatal("expected nil tool error")
	}
	if errors.Is(err, context.Canceled) {
		t.Fatalf("unexpected error: %v", err)
	}
}
