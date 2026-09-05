package agentkit

import (
	"context"
	"errors"
	"testing"

	"github.com/cloudwego/eino/adk"
	einotool "github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
)

type panickingLifecycleMiddleware struct {
	*BaseChatModelAgentMiddleware
}

func (m *panickingLifecycleMiddleware) BeforeAgent(
	context.Context,
	*adk.ChatModelAgentContext,
) (context.Context, *adk.ChatModelAgentContext, error) {
	panic("broken before agent")
}

type panickingWrapModelMiddleware struct {
	*BaseChatModelAgentMiddleware
}

func (m *panickingWrapModelMiddleware) WrapModel(
	context.Context,
	ChatModel,
	*adk.ModelContext,
) (ChatModel, error) {
	panic("broken model wrapper")
}

type panickingToolEndpointMiddleware struct {
	*BaseChatModelAgentMiddleware
}

type panickingToolStreamMiddleware struct {
	*BaseChatModelAgentMiddleware
}

func (m *panickingToolStreamMiddleware) WrapStreamableToolCall(
	context.Context,
	adk.StreamableToolCallEndpoint,
	*adk.ToolContext,
) (adk.StreamableToolCallEndpoint, error) {
	return func(context.Context, string, ...einotool.Option) (*schema.StreamReader[string], error) {
		source := schema.StreamReaderFromArray([]string{"chunk"})
		return schema.StreamReaderWithConvert(source, func(string) (string, error) {
			panic("broken wrapped stream")
		}), nil
	}, nil
}

func (m *panickingToolEndpointMiddleware) WrapInvokableToolCall(
	context.Context,
	adk.InvokableToolCallEndpoint,
	*adk.ToolContext,
) (adk.InvokableToolCallEndpoint, error) {
	return func(context.Context, string, ...einotool.Option) (string, error) {
		panic("broken wrapped tool")
	}, nil
}

func TestAgentConvertsMiddlewareHookPanics(t *testing.T) {
	tests := []struct {
		name    string
		handler ChatModelAgentMiddleware
	}{
		{
			name:    "lifecycle hook",
			handler: &panickingLifecycleMiddleware{BaseChatModelAgentMiddleware: &BaseChatModelAgentMiddleware{}},
		},
		{
			name:    "model wrapper",
			handler: &panickingWrapModelMiddleware{BaseChatModelAgentMiddleware: &BaseChatModelAgentMiddleware{}},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			agent, err := New(context.Background(), &Config{
				Model:    NewMockChatModel(MockModelText("unused")),
				Handlers: []ChatModelAgentMiddleware{test.handler},
			})
			if err != nil {
				t.Fatalf("New() error = %v", err)
			}
			t.Cleanup(func() { _ = agent.Close() })
			if _, err := agent.Ask(context.Background(), "hello"); !errors.Is(err, ErrMiddlewarePanic) {
				t.Fatalf("Ask() error = %v, want ErrMiddlewarePanic", err)
			}
		})
	}
}

func TestAgentConvertsMiddlewareToolEndpointPanic(t *testing.T) {
	tool := MustMockTool("broken", "broken tool", func(context.Context, string) (string, error) {
		return "unused", nil
	})
	agent, err := New(context.Background(), &Config{
		Model: NewMockChatModel(MockModelToolCallWithID("broken-call", "broken", `""`)),
		Tools: MockTools(tool),
		Handlers: []ChatModelAgentMiddleware{&panickingToolEndpointMiddleware{
			BaseChatModelAgentMiddleware: &BaseChatModelAgentMiddleware{},
		}},
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	t.Cleanup(func() { _ = agent.Close() })
	if _, err := agent.Ask(context.Background(), "run"); !errors.Is(err, ErrMiddlewarePanic) {
		t.Fatalf("Ask() error = %v, want ErrMiddlewarePanic", err)
	}
}

func TestGuardedMiddlewareConvertsReturnedStreamPanic(t *testing.T) {
	handler := guardAgentMiddleware(&panickingToolStreamMiddleware{
		BaseChatModelAgentMiddleware: &BaseChatModelAgentMiddleware{},
	})
	endpoint, err := handler.WrapStreamableToolCall(
		context.Background(),
		func(context.Context, string, ...einotool.Option) (*schema.StreamReader[string], error) {
			return schema.StreamReaderFromArray([]string{"unused"}), nil
		},
		&adk.ToolContext{Name: "stream"},
	)
	if err != nil {
		t.Fatalf("WrapStreamableToolCall() error = %v", err)
	}
	stream, err := endpoint(context.Background(), `{}`)
	if err != nil {
		t.Fatalf("endpoint() error = %v", err)
	}
	defer stream.Close()
	if _, err := stream.Recv(); !errors.Is(err, ErrMiddlewarePanic) {
		t.Fatalf("Recv() error = %v, want ErrMiddlewarePanic", err)
	}
}

func TestNewRejectsNilMiddleware(t *testing.T) {
	_, err := New(context.Background(), &Config{
		Model:    NewMockChatModel(),
		Handlers: []ChatModelAgentMiddleware{nil},
	})
	if err == nil {
		t.Fatal("New() error = nil, want nil middleware rejection")
	}
}
