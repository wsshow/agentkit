package agentkit

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/cloudwego/eino/adk"
	einotool "github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
)

// ErrMiddlewarePanic 表示自定义 ChatModelAgentMiddleware 发生 panic。
var ErrMiddlewarePanic = errors.New("agentkit: agent middleware panicked")

type guardedAgentMiddleware struct {
	middleware ChatModelAgentMiddleware
}

func guardAgentMiddleware(middleware ChatModelAgentMiddleware) ChatModelAgentMiddleware {
	return &guardedAgentMiddleware{middleware: middleware}
}

func (m *guardedAgentMiddleware) BeforeAgent(
	ctx context.Context,
	runCtx *adk.ChatModelAgentContext,
) (nextCtx context.Context, nextRunCtx *adk.ChatModelAgentContext, err error) {
	defer recoverMiddlewarePanic("BeforeAgent", &err)
	return m.middleware.BeforeAgent(ctx, runCtx)
}

func (m *guardedAgentMiddleware) AfterAgent(
	ctx context.Context,
	state *adk.ChatModelAgentState,
) (nextCtx context.Context, err error) {
	defer recoverMiddlewarePanic("AfterAgent", &err)
	return m.middleware.AfterAgent(ctx, state)
}

func (m *guardedAgentMiddleware) BeforeModelRewriteState(
	ctx context.Context,
	state *adk.ChatModelAgentState,
	modelCtx *adk.ModelContext,
) (nextCtx context.Context, nextState *adk.ChatModelAgentState, err error) {
	defer recoverMiddlewarePanic("BeforeModelRewriteState", &err)
	return m.middleware.BeforeModelRewriteState(ctx, state, modelCtx)
}

func (m *guardedAgentMiddleware) AfterModelRewriteState(
	ctx context.Context,
	state *adk.ChatModelAgentState,
	modelCtx *adk.ModelContext,
) (nextCtx context.Context, nextState *adk.ChatModelAgentState, err error) {
	defer recoverMiddlewarePanic("AfterModelRewriteState", &err)
	return m.middleware.AfterModelRewriteState(ctx, state, modelCtx)
}

func (m *guardedAgentMiddleware) WrapModel(
	ctx context.Context,
	model ChatModel,
	modelCtx *adk.ModelContext,
) (wrapped ChatModel, err error) {
	defer recoverMiddlewarePanic("WrapModel", &err)
	wrapped, err = m.middleware.WrapModel(ctx, model, modelCtx)
	if err != nil {
		return nil, err
	}
	if wrapped == nil {
		return nil, errors.New("agentkit: middleware WrapModel returned nil")
	}
	return guardChatModel(wrapped), nil
}

func (m *guardedAgentMiddleware) WrapInvokableToolCall(
	ctx context.Context,
	endpoint adk.InvokableToolCallEndpoint,
	toolCtx *adk.ToolContext,
) (wrapped adk.InvokableToolCallEndpoint, err error) {
	defer recoverMiddlewarePanic("WrapInvokableToolCall", &err)
	wrapped, err = m.middleware.WrapInvokableToolCall(ctx, endpoint, toolCtx)
	if err != nil {
		return nil, err
	}
	if wrapped == nil {
		return nil, errors.New("agentkit: middleware WrapInvokableToolCall returned nil")
	}
	next := wrapped
	return func(ctx context.Context, arguments string, options ...einotool.Option) (result string, err error) {
		defer recoverMiddlewarePanic("WrapInvokableToolCall endpoint", &err)
		return next(ctx, arguments, options...)
	}, nil
}

func (m *guardedAgentMiddleware) WrapStreamableToolCall(
	ctx context.Context,
	endpoint adk.StreamableToolCallEndpoint,
	toolCtx *adk.ToolContext,
) (wrapped adk.StreamableToolCallEndpoint, err error) {
	defer recoverMiddlewarePanic("WrapStreamableToolCall", &err)
	wrapped, err = m.middleware.WrapStreamableToolCall(ctx, endpoint, toolCtx)
	if err != nil {
		return nil, err
	}
	if wrapped == nil {
		return nil, errors.New("agentkit: middleware WrapStreamableToolCall returned nil")
	}
	next := wrapped
	return func(ctx context.Context, arguments string, options ...einotool.Option) (stream *schema.StreamReader[string], err error) {
		defer recoverMiddlewarePanic("WrapStreamableToolCall endpoint", &err)
		stream, err = next(ctx, arguments, options...)
		if err != nil || stream == nil {
			return stream, err
		}
		return guardMiddlewareStream(stream, "WrapStreamableToolCall stream"), nil
	}, nil
}

func (m *guardedAgentMiddleware) WrapEnhancedInvokableToolCall(
	ctx context.Context,
	endpoint adk.EnhancedInvokableToolCallEndpoint,
	toolCtx *adk.ToolContext,
) (wrapped adk.EnhancedInvokableToolCallEndpoint, err error) {
	defer recoverMiddlewarePanic("WrapEnhancedInvokableToolCall", &err)
	wrapped, err = m.middleware.WrapEnhancedInvokableToolCall(ctx, endpoint, toolCtx)
	if err != nil {
		return nil, err
	}
	if wrapped == nil {
		return nil, errors.New("agentkit: middleware WrapEnhancedInvokableToolCall returned nil")
	}
	next := wrapped
	return func(ctx context.Context, argument *schema.ToolArgument, options ...einotool.Option) (result *schema.ToolResult, err error) {
		defer recoverMiddlewarePanic("WrapEnhancedInvokableToolCall endpoint", &err)
		return next(ctx, argument, options...)
	}, nil
}

func (m *guardedAgentMiddleware) WrapEnhancedStreamableToolCall(
	ctx context.Context,
	endpoint adk.EnhancedStreamableToolCallEndpoint,
	toolCtx *adk.ToolContext,
) (wrapped adk.EnhancedStreamableToolCallEndpoint, err error) {
	defer recoverMiddlewarePanic("WrapEnhancedStreamableToolCall", &err)
	wrapped, err = m.middleware.WrapEnhancedStreamableToolCall(ctx, endpoint, toolCtx)
	if err != nil {
		return nil, err
	}
	if wrapped == nil {
		return nil, errors.New("agentkit: middleware WrapEnhancedStreamableToolCall returned nil")
	}
	next := wrapped
	return func(ctx context.Context, argument *schema.ToolArgument, options ...einotool.Option) (stream *schema.StreamReader[*schema.ToolResult], err error) {
		defer recoverMiddlewarePanic("WrapEnhancedStreamableToolCall endpoint", &err)
		stream, err = next(ctx, argument, options...)
		if err != nil || stream == nil {
			return stream, err
		}
		return guardMiddlewareStream(stream, "WrapEnhancedStreamableToolCall stream"), nil
	}, nil
}

func recoverMiddlewarePanic(operation string, err *error) {
	if recovered := recover(); recovered != nil {
		*err = fmt.Errorf("%w in %s: %v", ErrMiddlewarePanic, operation, recovered)
	}
}

func guardMiddlewareStream[T any](source *schema.StreamReader[T], operation string) *schema.StreamReader[T] {
	reader, writer := schema.Pipe[T](1)
	go func() {
		defer writer.Close()
		defer func() {
			if recovered := recover(); recovered != nil {
				var zero T
				writer.Send(zero, fmt.Errorf("%w in %s: %v", ErrMiddlewarePanic, operation, recovered))
			}
		}()
		defer source.Close()
		for {
			value, err := source.Recv()
			if errors.Is(err, io.EOF) {
				return
			}
			if writer.Send(value, err) || err != nil {
				return
			}
		}
	}()
	return reader
}
