package agentkit

import (
	"context"
	"strings"
	"testing"
)

func TestNewValidatesConfig(t *testing.T) {
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
