package agentkit

import (
	"context"
	"strings"
	"testing"

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
			name: "nil context",
			cfg:  &Config{Model: NewMockChatModel()},
			want: "context is required",
		},
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
			name: "empty session ID",
			ctx:  context.Background(),
			cfg: &Config{
				Model:   model,
				Session: &SessionConfig{Store: NewMemorySessionStore()},
			},
			want: "session ID is required",
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
