package agentkit

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/cloudwego/eino/schema"
)

type modelGuardStub struct {
	generate func(context.Context, []*schema.Message, ...ModelOption) (*schema.Message, error)
	stream   func(context.Context, []*schema.Message, ...ModelOption) (*schema.StreamReader[*schema.Message], error)
}

func (m *modelGuardStub) Generate(
	ctx context.Context,
	input []*schema.Message,
	options ...ModelOption,
) (*schema.Message, error) {
	return m.generate(ctx, input, options...)
}

func (m *modelGuardStub) Stream(
	ctx context.Context,
	input []*schema.Message,
	options ...ModelOption,
) (*schema.StreamReader[*schema.Message], error) {
	return m.stream(ctx, input, options...)
}

func TestGuardedChatModelConvertsEntryPointPanics(t *testing.T) {
	model := guardChatModel(&modelGuardStub{
		generate: func(context.Context, []*schema.Message, ...ModelOption) (*schema.Message, error) {
			panic("broken generate")
		},
		stream: func(context.Context, []*schema.Message, ...ModelOption) (*schema.StreamReader[*schema.Message], error) {
			panic("broken stream")
		},
	})
	if _, err := model.Generate(context.Background(), nil); !errors.Is(err, ErrModelPanic) {
		t.Fatalf("Generate() error = %v, want ErrModelPanic", err)
	}
	if _, err := model.Stream(context.Background(), nil); !errors.Is(err, ErrModelPanic) {
		t.Fatalf("Stream() error = %v, want ErrModelPanic", err)
	}
}

func TestGuardedChatModelConvertsStreamReadPanic(t *testing.T) {
	model := guardChatModel(&modelGuardStub{
		generate: func(context.Context, []*schema.Message, ...ModelOption) (*schema.Message, error) {
			return schema.AssistantMessage("unused", nil), nil
		},
		stream: func(context.Context, []*schema.Message, ...ModelOption) (*schema.StreamReader[*schema.Message], error) {
			source := schema.StreamReaderFromArray([]*schema.Message{schema.AssistantMessage("chunk", nil)})
			return schema.StreamReaderWithConvert(source, func(*schema.Message) (*schema.Message, error) {
				panic("broken recv")
			}), nil
		},
	})
	stream, err := model.Stream(context.Background(), nil)
	if err != nil {
		t.Fatalf("Stream() error = %v", err)
	}
	defer stream.Close()
	if _, err := stream.Recv(); !errors.Is(err, ErrModelPanic) {
		t.Fatalf("Recv() error = %v, want ErrModelPanic", err)
	}
}

func TestAgentConvertsModelStreamPanic(t *testing.T) {
	model := &modelGuardStub{
		generate: func(context.Context, []*schema.Message, ...ModelOption) (*schema.Message, error) {
			return schema.AssistantMessage("unused", nil), nil
		},
		stream: func(context.Context, []*schema.Message, ...ModelOption) (*schema.StreamReader[*schema.Message], error) {
			panic("provider stream panic")
		},
	}
	agent, err := New(context.Background(), &Config{Model: model})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	t.Cleanup(func() { _ = agent.Close() })
	if _, err := agent.Ask(context.Background(), "hello"); !errors.Is(err, ErrModelPanic) {
		t.Fatalf("Ask() error = %v, want ErrModelPanic", err)
	}
}

func TestModelPanicParticipatesInRetry(t *testing.T) {
	calls := 0
	model := &modelGuardStub{
		generate: func(context.Context, []*schema.Message, ...ModelOption) (*schema.Message, error) {
			calls++
			if calls == 1 {
				panic("transient provider panic")
			}
			return schema.AssistantMessage("recovered", nil), nil
		},
		stream: func(context.Context, []*schema.Message, ...ModelOption) (*schema.StreamReader[*schema.Message], error) {
			return nil, errors.New("unused")
		},
	}
	result, err := generateModelWithRetry(context.Background(), model, nil, &ModelRetryConfig{
		MaxRetries:  1,
		BackoffFunc: func(context.Context, int) time.Duration { return 0 },
	})
	if err != nil || result == nil || result.Content != "recovered" || calls != 2 {
		t.Fatalf("generateModelWithRetry() = %#v, %v after %d calls", result, err, calls)
	}
}
