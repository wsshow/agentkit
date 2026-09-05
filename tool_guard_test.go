package agentkit

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/cloudwego/eino/schema"
)

type panickingToolMetadata struct{}

func (panickingToolMetadata) Info(context.Context) (*schema.ToolInfo, error) {
	panic("broken metadata")
}

func TestNewIsolatesToolMetadataPanics(t *testing.T) {
	ctx := context.Background()
	tests := []struct {
		name string
		cfg  *Config
	}{
		{
			name: "static tool",
			cfg:  &Config{Model: NewMockChatModel(), Tools: []Tool{panickingToolMetadata{}}},
		},
		{
			name: "dynamic tool",
			cfg: &Config{
				Model:      NewMockChatModel(),
				ToolSearch: &ToolSearchConfig{Tools: []Tool{panickingToolMetadata{}}},
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			agent, err := New(ctx, test.cfg)
			if agent != nil {
				_ = agent.Close()
			}
			if !errors.Is(err, ErrToolMetadataPanic) {
				t.Fatalf("New() error = %v, want ErrToolMetadataPanic", err)
			}
		})
	}
}

func TestToolPolicyLimitsToolResultAndReportsOutcome(t *testing.T) {
	ctx := context.Background()
	tool := MustMockTool("large", "return a large result", func(context.Context, string) (string, error) {
		return "甲乙丙丁戊", nil
	})
	call, err := tool.Invocation("large-call", "")
	if err != nil {
		t.Fatalf("Invocation() error = %v", err)
	}
	var before ToolInvocation
	var after ToolOutcome
	agent, err := New(ctx, &Config{
		Model: NewMockChatModel(
			MockModelCalls(call),
			MockModelTextAfterToolResult(call.CallID),
		),
		Tools: MockTools(tool),
		ToolPolicy: &ToolPolicy{
			MaxResultChars: 4,
			BeforeTool: func(_ context.Context, invocation ToolInvocation) error {
				before = invocation
				return nil
			},
			AfterTool: func(_ context.Context, _ ToolInvocation, outcome ToolOutcome) {
				after = outcome
			},
		},
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer agent.Close()

	result, err := agent.Ask(ctx, "run it")
	if err != nil {
		t.Fatalf("Ask() error = %v", err)
	}
	if result.Text != "甲乙丙丁"+toolResultTruncatedMarker {
		t.Fatalf("result text = %q", result.Text)
	}
	if before.Name != "large" || before.CallID != "large-call" {
		t.Fatalf("before invocation = %#v", before)
	}
	if !after.Truncated || after.OutputChars != 4 || after.Err != nil || after.Duration <= 0 {
		t.Fatalf("after outcome = %#v", after)
	}
}

func TestToolPolicyUsesDefaultResultLimit(t *testing.T) {
	value := strings.Repeat("a", DefaultToolResultMaxChars+1)
	endpoint := (*ToolPolicy)(nil).executionMiddleware().Invokable(func(context.Context, *ToolInput) (*ToolOutput, error) {
		return &ToolOutput{Result: value}, nil
	})
	output, err := endpoint(context.Background(), &ToolInput{Name: "large"})
	if err != nil {
		t.Fatalf("endpoint() error = %v", err)
	}
	if utf8.RuneCountInString(output.Result) != DefaultToolResultMaxChars+utf8.RuneCountInString(toolResultTruncatedMarker) {
		t.Fatalf("limited result has %d characters", utf8.RuneCountInString(output.Result))
	}
}

func TestToolPolicyCanDisableResultLimit(t *testing.T) {
	policy := &ToolPolicy{MaxResultChars: -1}
	value := strings.Repeat("a", DefaultToolResultMaxChars+1)
	limited, chars, truncated := limitText(value, policy.maxResultChars())
	if limited != value || chars != len(value) || truncated {
		t.Fatalf("limitText() = (%d chars, %v), want unchanged", chars, truncated)
	}
}

func TestToolPolicyTimeout(t *testing.T) {
	ctx := context.Background()
	tool := MustMockTool("wait", "wait for cancellation", func(ctx context.Context, _ string) (string, error) {
		<-ctx.Done()
		return "", ctx.Err()
	})
	call, err := tool.Invocation("wait-call", "")
	if err != nil {
		t.Fatalf("Invocation() error = %v", err)
	}
	var outcome ToolOutcome
	agent, err := New(ctx, &Config{
		Model: NewMockChatModel(MockModelCalls(call)),
		Tools: MockTools(tool),
		ToolPolicy: &ToolPolicy{
			Timeout: 20 * time.Millisecond,
			AfterTool: func(_ context.Context, _ ToolInvocation, got ToolOutcome) {
				outcome = got
			},
		},
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer agent.Close()

	_, err = agent.Ask(ctx, "wait")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Ask() error = %v, want deadline exceeded", err)
	}
	if !errors.Is(outcome.Err, context.DeadlineExceeded) {
		t.Fatalf("outcome error = %v, want deadline exceeded", outcome.Err)
	}
}

func TestToolPolicyBeforeToolCanRejectCall(t *testing.T) {
	want := errors.New("approval required")
	policy := &ToolPolicy{
		BeforeTool: func(context.Context, ToolInvocation) error { return want },
	}
	called := false
	endpoint := policy.executionMiddleware().Invokable(func(context.Context, *ToolInput) (*ToolOutput, error) {
		called = true
		return &ToolOutput{Result: "unexpected"}, nil
	})
	_, err := endpoint(context.Background(), &ToolInput{Name: "dangerous"})
	if !errors.Is(err, want) || called {
		t.Fatalf("endpoint error = %v, called = %v", err, called)
	}
}

func TestToolPolicyBeforeToolPanicBecomesError(t *testing.T) {
	policy := &ToolPolicy{
		BeforeTool: func(context.Context, ToolInvocation) error {
			panic("broken guard")
		},
	}
	called := false
	endpoint := policy.executionMiddleware().Invokable(func(context.Context, *ToolInput) (*ToolOutput, error) {
		called = true
		return &ToolOutput{Result: "unexpected"}, nil
	})
	_, err := endpoint(context.Background(), &ToolInput{Name: "dangerous"})
	if !errors.Is(err, ErrToolPolicyPanic) || called {
		t.Fatalf("endpoint error = %v, called = %v", err, called)
	}
}

func TestToolPolicyIsolatesMiddlewareFactoryPanics(t *testing.T) {
	guarded := guardToolMiddleware(ToolMiddleware{
		Invokable: func(InvokableToolEndpoint) InvokableToolEndpoint {
			panic("broken invokable middleware")
		},
		Streamable: func(StreamableToolEndpoint) StreamableToolEndpoint {
			panic("broken streamable middleware")
		},
		EnhancedInvokable: func(EnhancedInvokableToolEndpoint) EnhancedInvokableToolEndpoint {
			panic("broken enhanced invokable middleware")
		},
		EnhancedStreamable: func(EnhancedStreamableToolEndpoint) EnhancedStreamableToolEndpoint {
			panic("broken enhanced streamable middleware")
		},
	})
	if _, err := guarded.Invokable(func(context.Context, *ToolInput) (*ToolOutput, error) {
		return &ToolOutput{}, nil
	})(context.Background(), &ToolInput{}); !errors.Is(err, ErrToolPolicyPanic) {
		t.Fatalf("Invokable middleware error = %v, want ErrToolPolicyPanic", err)
	}
	if _, err := guarded.Streamable(func(context.Context, *ToolInput) (*StreamToolOutput, error) {
		return &StreamToolOutput{}, nil
	})(context.Background(), &ToolInput{}); !errors.Is(err, ErrToolPolicyPanic) {
		t.Fatalf("Streamable middleware error = %v, want ErrToolPolicyPanic", err)
	}
	if _, err := guarded.EnhancedInvokable(func(context.Context, *ToolInput) (*EnhancedInvokableToolOutput, error) {
		return &EnhancedInvokableToolOutput{}, nil
	})(context.Background(), &ToolInput{}); !errors.Is(err, ErrToolPolicyPanic) {
		t.Fatalf("EnhancedInvokable middleware error = %v, want ErrToolPolicyPanic", err)
	}
	if _, err := guarded.EnhancedStreamable(func(context.Context, *ToolInput) (*EnhancedStreamableToolOutput, error) {
		return &EnhancedStreamableToolOutput{}, nil
	})(context.Background(), &ToolInput{}); !errors.Is(err, ErrToolPolicyPanic) {
		t.Fatalf("EnhancedStreamable middleware error = %v, want ErrToolPolicyPanic", err)
	}
}

func TestAgentReportsToolMiddlewareFactoryPanic(t *testing.T) {
	tool := MustMockTool("broken", "broken middleware", func(context.Context, string) (string, error) {
		return "unused", nil
	})
	agent, err := New(context.Background(), &Config{
		Model: NewMockChatModel(MockModelToolCallWithID("broken-call", "broken", `""`)),
		Tools: MockTools(tool),
		ToolPolicy: &ToolPolicy{Middlewares: []ToolMiddleware{{
			Invokable: func(InvokableToolEndpoint) InvokableToolEndpoint {
				panic("broken middleware factory")
			},
		}}},
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	t.Cleanup(func() { _ = agent.Close() })
	if _, err := agent.Ask(context.Background(), "run"); !errors.Is(err, ErrToolPolicyPanic) {
		t.Fatalf("Ask() error = %v, want ErrToolPolicyPanic", err)
	}
}

func TestToolPolicyIsolatesToolEntryPointPanics(t *testing.T) {
	ctx := context.Background()
	input := &ToolInput{Name: "broken", CallID: "call-1"}
	middleware := (*ToolPolicy)(nil).executionMiddleware()

	invokable := middleware.Invokable(func(context.Context, *ToolInput) (*ToolOutput, error) {
		panic("broken invokable tool")
	})
	if _, err := invokable(ctx, input); !errors.Is(err, ErrToolExecutionPanic) {
		t.Fatalf("Invokable() error = %v, want ErrToolExecutionPanic", err)
	}

	streamable := middleware.Streamable(func(context.Context, *ToolInput) (*StreamToolOutput, error) {
		panic("broken streamable tool")
	})
	if _, err := streamable(ctx, input); !errors.Is(err, ErrToolExecutionPanic) {
		t.Fatalf("Streamable() error = %v, want ErrToolExecutionPanic", err)
	}

	enhanced := middleware.EnhancedInvokable(func(context.Context, *ToolInput) (*EnhancedInvokableToolOutput, error) {
		panic("broken enhanced tool")
	})
	if _, err := enhanced(ctx, input); !errors.Is(err, ErrToolExecutionPanic) {
		t.Fatalf("EnhancedInvokable() error = %v, want ErrToolExecutionPanic", err)
	}

	enhancedStream := middleware.EnhancedStreamable(func(context.Context, *ToolInput) (*EnhancedStreamableToolOutput, error) {
		panic("broken enhanced stream tool")
	})
	if _, err := enhancedStream(ctx, input); !errors.Is(err, ErrToolExecutionPanic) {
		t.Fatalf("EnhancedStreamable() error = %v, want ErrToolExecutionPanic", err)
	}
}

func TestAgentReturnsToolImplementationPanicAsError(t *testing.T) {
	ctx := context.Background()
	tool := MustMockTool("broken", "panic during execution", func(context.Context, string) (string, error) {
		panic("broken tool")
	})
	agent, err := New(ctx, &Config{
		Name:  "assistant",
		Model: NewMockChatModel(MockModelToolCallWithID("broken-call", "broken", `""`)),
		Tools: MockTools(tool),
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	t.Cleanup(func() { _ = agent.Close() })

	if _, err := agent.Ask(ctx, "run broken tool"); !errors.Is(err, ErrToolExecutionPanic) {
		t.Fatalf("Ask() error = %v, want ErrToolExecutionPanic", err)
	}
}

func TestToolPolicyIsolatesTextStreamReceivePanic(t *testing.T) {
	ctx := context.Background()
	source := schema.StreamReaderWithConvert(
		schema.StreamReaderFromArray([]string{"chunk"}),
		func(string) (string, error) {
			panic("broken text stream")
		},
	)
	endpoint := (*ToolPolicy)(nil).executionMiddleware().Streamable(func(context.Context, *ToolInput) (*StreamToolOutput, error) {
		return &StreamToolOutput{Result: source}, nil
	})
	output, err := endpoint(ctx, &ToolInput{Name: "broken_stream", CallID: "call-1"})
	if err != nil {
		t.Fatalf("Streamable() error = %v", err)
	}
	defer output.Result.Close()
	if _, err := output.Result.Recv(); !errors.Is(err, ErrToolExecutionPanic) {
		t.Fatalf("Recv() error = %v, want ErrToolExecutionPanic", err)
	}
}

func TestToolPolicyIsolatesEnhancedStreamReceivePanic(t *testing.T) {
	ctx := context.Background()
	source := schema.StreamReaderWithConvert(
		schema.StreamReaderFromArray([]*schema.ToolResult{{}}),
		func(*schema.ToolResult) (*schema.ToolResult, error) {
			panic("broken enhanced stream")
		},
	)
	endpoint := (*ToolPolicy)(nil).executionMiddleware().EnhancedStreamable(func(context.Context, *ToolInput) (*EnhancedStreamableToolOutput, error) {
		return &EnhancedStreamableToolOutput{Result: source}, nil
	})
	output, err := endpoint(ctx, &ToolInput{Name: "broken_stream", CallID: "call-1"})
	if err != nil {
		t.Fatalf("EnhancedStreamable() error = %v", err)
	}
	defer output.Result.Close()
	if _, err := output.Result.Recv(); !errors.Is(err, ErrToolExecutionPanic) {
		t.Fatalf("Recv() error = %v, want ErrToolExecutionPanic", err)
	}
}

func TestToolPolicyAfterToolPanicIsReportedWithoutFailingRun(t *testing.T) {
	ctx := context.Background()
	tool := MustMockTool("safe_result", "return result", func(context.Context, string) (string, error) {
		return "result", nil
	})
	agent, err := New(ctx, &Config{
		Name: "assistant",
		Model: NewMockChatModel(
			MockModelToolCallWithID("safe-call", "safe_result", `""`),
			MockModelTextAfterToolResult("safe-call"),
		),
		Tools: MockTools(tool),
		ToolPolicy: &ToolPolicy{AfterTool: func(context.Context, ToolInvocation, ToolOutcome) {
			panic("broken observer")
		}},
	})
	if err != nil {
		t.Fatalf("create agent: %v", err)
	}
	defer agent.Close()
	events := newMockEventRecorder()
	agent.Subscribe(events.Record)
	result, err := agent.Ask(ctx, "run")
	if err != nil || result == nil || result.Text != "result" {
		t.Fatalf("Ask() = %#v, %v", result, err)
	}
	diagnostic := events.Last(EventError)
	if diagnostic == nil || !errors.Is(diagnostic.Error, ErrToolPolicyPanic) {
		t.Fatalf("tool policy diagnostic = %#v", diagnostic)
	}
}

func TestToolPolicyLimitsTextStream(t *testing.T) {
	var outcome ToolOutcome
	policy := &ToolPolicy{
		MaxResultChars: 4,
		AfterTool: func(_ context.Context, _ ToolInvocation, got ToolOutcome) {
			outcome = got
		},
	}
	endpoint := policy.executionMiddleware().Streamable(func(context.Context, *ToolInput) (*StreamToolOutput, error) {
		return &StreamToolOutput{Result: schema.StreamReaderFromArray([]string{"你好", "世界", "!"})}, nil
	})
	output, err := endpoint(context.Background(), &ToolInput{Name: "stream"})
	if err != nil {
		t.Fatalf("endpoint() error = %v", err)
	}
	var result strings.Builder
	for {
		chunk, recvErr := output.Result.Recv()
		if errors.Is(recvErr, io.EOF) {
			break
		}
		if recvErr != nil {
			t.Fatalf("Recv() error = %v", recvErr)
		}
		result.WriteString(chunk)
	}
	output.Result.Close()
	if result.String() != "你好世界"+toolResultTruncatedMarker {
		t.Fatalf("stream result = %q", result.String())
	}
	if !outcome.Truncated || outcome.OutputChars != 4 {
		t.Fatalf("outcome = %#v", outcome)
	}
}

func TestToolPolicyLimitsEnhancedResultWithoutMutatingSource(t *testing.T) {
	policy := &ToolPolicy{MaxResultChars: 3}
	source := &schema.ToolResult{Parts: []schema.ToolOutputPart{
		{Type: schema.ToolPartTypeText, Text: "ab"},
		{Type: schema.ToolPartTypeImage},
		{Type: schema.ToolPartTypeText, Text: "cdef"},
		{Type: schema.ToolPartTypeText, Text: "later"},
	}}
	endpoint := policy.executionMiddleware().EnhancedInvokable(func(context.Context, *ToolInput) (*EnhancedInvokableToolOutput, error) {
		return &EnhancedInvokableToolOutput{Result: source}, nil
	})
	output, err := endpoint(context.Background(), &ToolInput{Name: "enhanced"})
	if err != nil {
		t.Fatalf("endpoint() error = %v", err)
	}
	if len(output.Result.Parts) != 3 || output.Result.Parts[0].Text != "ab" || output.Result.Parts[1].Type != schema.ToolPartTypeImage || output.Result.Parts[2].Text != "c"+toolResultTruncatedMarker {
		t.Fatalf("limited parts = %#v", output.Result.Parts)
	}
	if source.Parts[2].Text != "cdef" || len(source.Parts) != 4 {
		t.Fatalf("source was mutated: %#v", source.Parts)
	}
}

func TestToolPolicyLimitsEnhancedStream(t *testing.T) {
	var outcome ToolOutcome
	policy := &ToolPolicy{
		MaxResultChars: 3,
		AfterTool: func(_ context.Context, _ ToolInvocation, got ToolOutcome) {
			outcome = got
		},
	}
	endpoint := policy.executionMiddleware().EnhancedStreamable(func(context.Context, *ToolInput) (*EnhancedStreamableToolOutput, error) {
		return &EnhancedStreamableToolOutput{Result: schema.StreamReaderFromArray([]*schema.ToolResult{
			{Parts: []schema.ToolOutputPart{{Type: schema.ToolPartTypeText, Text: "ab"}}},
			{Parts: []schema.ToolOutputPart{{Type: schema.ToolPartTypeText, Text: "cd"}}},
			{Parts: []schema.ToolOutputPart{{Type: schema.ToolPartTypeText, Text: "ignored"}}},
		})}, nil
	})
	output, err := endpoint(context.Background(), &ToolInput{Name: "enhanced-stream"})
	if err != nil {
		t.Fatalf("endpoint() error = %v", err)
	}
	var texts []string
	for {
		chunk, recvErr := output.Result.Recv()
		if errors.Is(recvErr, io.EOF) {
			break
		}
		if recvErr != nil {
			t.Fatalf("Recv() error = %v", recvErr)
		}
		for _, part := range chunk.Parts {
			if part.Type == schema.ToolPartTypeText {
				texts = append(texts, part.Text)
			}
		}
	}
	output.Result.Close()
	if strings.Join(texts, "") != "abc"+toolResultTruncatedMarker {
		t.Fatalf("stream text = %q", strings.Join(texts, ""))
	}
	if !outcome.Truncated || outcome.OutputChars != 3 {
		t.Fatalf("outcome = %#v", outcome)
	}
}

func TestToolPolicyValidatesExecutionLimits(t *testing.T) {
	for _, policy := range []*ToolPolicy{
		{Timeout: -time.Second},
		{MaxResultChars: -2},
	} {
		_, err := New(context.Background(), &Config{Model: NewMockChatModel(), ToolPolicy: policy})
		if err == nil {
			t.Fatalf("New() error = nil for policy %#v", policy)
		}
	}
}
