package agentkit

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/cloudwego/eino/schema"
)

func TestNewValidatesConfig(t *testing.T) {
	model := NewMockChatModel()
	tests := []struct {
		name string
		ctx  context.Context
		cfg  *Config
		want string
	}{
		{
			name: "nil config",
			ctx:  context.Background(),
			want: "config is required",
		},
		{
			name: "nil model",
			ctx:  context.Background(),
			cfg:  &Config{},
			want: "model is required",
		},
		{
			name: "negative max iterations",
			ctx:  context.Background(),
			cfg: &Config{
				Model:         NewMockChatModel(),
				MaxIterations: -1,
			},
			want: "max iterations must not be negative",
		},
		{
			name: "negative persistence timeout",
			ctx:  context.Background(),
			cfg: &Config{
				Model:              NewMockChatModel(),
				PersistenceTimeout: -time.Second,
			},
			want: "persistence timeout must not be negative",
		},
		{
			name: "empty session ID",
			ctx:  context.Background(),
			cfg: &Config{
				Model:   model,
				Session: &SessionConfig{Store: NewMemorySessionStore()},
			},
			want: "session ID is required",
		},
		{
			name: "session ID with surrounding whitespace",
			ctx:  context.Background(),
			cfg: &Config{
				Model:   NewMockChatModel(),
				Session: &SessionConfig{ID: " session-1 ", Store: NewMemorySessionStore()},
			},
			want: "session ID must not have surrounding whitespace",
		},
		{
			name: "nil session store",
			ctx:  context.Background(),
			cfg: &Config{
				Model:   model,
				Session: &SessionConfig{ID: "session-1"},
			},
			want: "session store is required",
		},
		{
			name: "history and session",
			ctx:  context.Background(),
			cfg: &Config{
				Model:   model,
				History: []*schema.Message{},
				Session: &SessionConfig{ID: "session-1", Store: NewMemorySessionStore()},
			},
			want: "history and session cannot be configured together",
		},
		{
			name: "negative compaction tokens",
			ctx:  context.Background(),
			cfg: &Config{
				Model:      model,
				Compaction: &CompactionConfig{MaxTokens: -1},
			},
			want: "compaction max tokens must not be negative",
		},
		{
			name: "negative compaction messages",
			ctx:  context.Background(),
			cfg: &Config{
				Model:      model,
				Compaction: &CompactionConfig{MaxMessages: -1},
			},
			want: "compaction max messages must not be negative",
		},
		{
			name: "negative retained turns",
			ctx:  context.Background(),
			cfg: &Config{
				Model:      model,
				Compaction: &CompactionConfig{KeepRecentTurns: -1},
			},
			want: "compaction keep recent turns must not be negative",
		},
		{
			name: "skills missing source",
			ctx:  context.Background(),
			cfg: &Config{
				Model:  model,
				Skills: &SkillsConfig{},
			},
			want: "skills require exactly one of paths or backend",
		},
		{
			name: "skills have multiple sources",
			ctx:  context.Background(),
			cfg: &Config{
				Model: model,
				Skills: &SkillsConfig{
					Paths:   []string{"skills"},
					Backend: &MemorySkillBackend{},
				},
			},
			want: "skills require exactly one of paths or backend",
		},
		{
			name: "blank skill tool name",
			ctx:  context.Background(),
			cfg: &Config{
				Model: model,
				Skills: &SkillsConfig{
					Backend:  &MemorySkillBackend{},
					ToolName: " ",
				},
			},
			want: "skill tool name must not be blank",
		},
		{
			name: "invalid skill tool name",
			ctx:  context.Background(),
			cfg: &Config{
				Model: model,
				Skills: &SkillsConfig{
					Backend:  &MemorySkillBackend{},
					ToolName: "load skill",
				},
			},
			want: "invalid skill tool name",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			agent, err := New(tt.ctx, tt.cfg)
			if agent != nil {
				t.Fatalf("New() agent = %#v, want nil", agent)
			}
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("New() error = %v, want containing %q", err, tt.want)
			}
		})
	}
}

func TestNewRejectsInvalidLocalToolName(t *testing.T) {
	tool := MustMockTool("invalid tool", "invalid", func(context.Context, string) (string, error) {
		return "", nil
	})
	agent, err := New(context.Background(), &Config{
		Model: NewMockChatModel(),
		Tools: MockTools(tool),
	})
	if agent != nil || err == nil || !strings.Contains(err.Error(), "invalid tool name") {
		t.Fatalf("New() = %#v, %v, want invalid tool name", agent, err)
	}
}

func TestAgentIgnoresNilHistoryMessages(t *testing.T) {
	agent, err := New(context.Background(), &Config{
		Model: NewMockChatModel(MockModelText("continued")),
		History: []*schema.Message{
			nil,
			schema.UserMessage("resume me"),
			nil,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer agent.Close()

	result, err := agent.ContinueWithResult(context.Background())
	if err != nil {
		t.Fatalf("ContinueWithResult() error = %v", err)
	}
	if result.Text != "continued" {
		t.Fatalf("ContinueWithResult().Text = %q, want continued", result.Text)
	}
	if history := agent.History(); len(history) != 2 || history[0] == nil || history[1] == nil {
		t.Fatalf("History() = %#v, want two non-nil messages", history)
	}
}

func TestNewAlwaysValidatesLocalTools(t *testing.T) {
	ctx := context.Background()
	first := MustMockTool("duplicate", "first", func(context.Context, string) (string, error) {
		return "", nil
	})
	second := MustMockTool("duplicate", "second", func(context.Context, string) (string, error) {
		return "", nil
	})
	tests := []struct {
		name  string
		tools []Tool
		want  string
	}{
		{name: "nil tool", tools: []Tool{nil}, want: "tool 0 is nil"},
		{name: "duplicate tool", tools: []Tool{first.Tool, second.Tool}, want: `duplicate tool name "duplicate"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := New(ctx, &Config{Model: NewMockChatModel(), Tools: tt.tools})
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("New() error = %v, want containing %q", err, tt.want)
			}
		})
	}
}

func TestAgentCannotRunAfterClose(t *testing.T) {
	agent, err := New(context.Background(), &Config{Model: NewMockChatModel(MockModelText("unused"))})
	if err != nil {
		t.Fatal(err)
	}
	if err := agent.Close(); err != nil {
		t.Fatal(err)
	}
	if err := agent.Close(); err != nil {
		t.Fatalf("second Close() error = %v", err)
	}

	tests := []struct {
		name string
		run  func() error
	}{
		{name: "prompt", run: func() error { return agent.Prompt(context.Background(), "hello") }},
		{name: "send", run: func() error { return agent.Send(context.Background(), Text("hello")) }},
		{name: "continue", run: func() error { return agent.Continue(context.Background()) }},
		{name: "resume", run: func() error { return agent.Resume(context.Background(), nil) }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.run(); !errors.Is(err, ErrAgentClosed) {
				t.Fatalf("run error = %v, want ErrAgentClosed", err)
			}
		})
	}
}

func TestAgentRunMethodsRejectCanceledContextBeforeChangingState(t *testing.T) {
	tests := []struct {
		name string
		run  func(*Agent, context.Context) error
	}{
		{name: "prompt", run: func(agent *Agent, ctx context.Context) error {
			return agent.Prompt(ctx, "hello")
		}},
		{name: "send", run: func(agent *Agent, ctx context.Context) error {
			return agent.Send(ctx, Text("hello"))
		}},
		{name: "continue", run: func(agent *Agent, ctx context.Context) error {
			return agent.Continue(ctx)
		}},
		{name: "resume", run: func(agent *Agent, ctx context.Context) error {
			return agent.Resume(ctx, nil)
		}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			agent, err := New(context.Background(), &Config{Model: NewMockChatModel(MockModelText("unused"))})
			if err != nil {
				t.Fatal(err)
			}
			defer agent.Close()

			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			err = tt.run(agent, ctx)
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("run error = %v, want context.Canceled", err)
			}
			if got := agent.History(); len(got) != 0 {
				t.Fatalf("history changed after rejected run: %#v", got)
			}
			if got := agent.State().Messages(); len(got) != 0 {
				t.Fatalf("state changed after rejected run: %#v", got)
			}
		})

		t.Run(tt.name+" rejects canceled context", func(t *testing.T) {
			agent, err := New(context.Background(), &Config{Model: NewMockChatModel(MockModelText("unused"))})
			if err != nil {
				t.Fatal(err)
			}
			defer agent.Close()

			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			if err := tt.run(agent, ctx); !errors.Is(err, context.Canceled) {
				t.Fatalf("run error = %v, want context.Canceled", err)
			}
			if got := agent.History(); len(got) != 0 {
				t.Fatalf("history changed after rejected run: %#v", got)
			}
			if got := agent.State().Messages(); len(got) != 0 {
				t.Fatalf("state changed after rejected run: %#v", got)
			}
		})
	}
}

func TestAgentContinueReturnsStateErrors(t *testing.T) {
	agent, err := New(context.Background(), &Config{Model: NewMockChatModel()})
	if err != nil {
		t.Fatal(err)
	}
	defer agent.Close()

	if err := agent.Continue(context.Background()); !errors.Is(err, ErrNoMessagesToContinue) {
		t.Fatalf("Continue() error = %v, want ErrNoMessagesToContinue", err)
	}

	agent.SetHistory([]*schema.Message{schema.AssistantMessage("done", nil)})
	if err := agent.Continue(context.Background()); !errors.Is(err, ErrCannotContinue) {
		t.Fatalf("Continue() error = %v, want ErrCannotContinue", err)
	}
}
