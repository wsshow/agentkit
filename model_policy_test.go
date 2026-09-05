package agentkit

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/schema"
)

func TestAgentReturnsShouldRetryPanicAsError(t *testing.T) {
	ctx := context.Background()
	agent, err := New(ctx, &Config{
		Name:  "assistant",
		Model: NewMockChatModel(MockModelText("candidate")),
		ModelRetryConfig: &ModelRetryConfig{
			MaxRetries: 1,
			ShouldRetry: func(context.Context, *adk.RetryContext) *adk.RetryDecision {
				panic("broken retry policy")
			},
		},
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	t.Cleanup(func() { _ = agent.Close() })

	if _, err := agent.Ask(ctx, "hello"); !errors.Is(err, ErrModelPolicyPanic) {
		t.Fatalf("Ask() error = %v, want ErrModelPolicyPanic", err)
	}
}

func TestAgentReportsLegacyRetryPanicWithoutCrashing(t *testing.T) {
	ctx := context.Background()
	modelErr := errors.New("model unavailable")
	agent, err := New(ctx, &Config{
		Name:  "assistant",
		Model: NewMockChatModel(MockModelError(modelErr)),
		ModelRetryConfig: &ModelRetryConfig{
			MaxRetries: 1,
			IsRetryAble: func(context.Context, error) bool {
				panic("broken legacy retry policy")
			},
		},
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	t.Cleanup(func() { _ = agent.Close() })
	events := newMockEventRecorder()
	agent.Subscribe(events.Record)

	if _, err := agent.Ask(ctx, "hello"); !errors.Is(err, modelErr) {
		t.Fatalf("Ask() error = %v, want original model error", err)
	}
	var found bool
	for _, event := range events.Events() {
		if event.Type == EventError && errors.Is(event.Error, ErrModelPolicyPanic) {
			found = true
		}
	}
	if !found {
		t.Fatal("missing ErrModelPolicyPanic event")
	}
}

func TestAgentReportsBackoffPanicAndContinuesRetry(t *testing.T) {
	ctx := context.Background()
	agent, err := New(ctx, &Config{
		Name: "assistant",
		Model: NewMockChatModel(
			MockModelError(errors.New("temporary failure")),
			MockModelText("recovered"),
		),
		ModelRetryConfig: &ModelRetryConfig{
			MaxRetries: 1,
			BackoffFunc: func(context.Context, int) time.Duration {
				panic("broken backoff")
			},
		},
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	t.Cleanup(func() { _ = agent.Close() })
	events := newMockEventRecorder()
	agent.Subscribe(events.Record)

	result, err := agent.Ask(ctx, "hello")
	if err != nil || result.Text != "recovered" {
		t.Fatalf("Ask() = %#v, %v, want recovered", result, err)
	}
	if event := events.Last(EventError); event == nil || !errors.Is(event.Error, ErrModelPolicyPanic) {
		t.Fatalf("last error event = %#v, want ErrModelPolicyPanic", event)
	}
}

func TestAgentReportsShouldFailoverPanicWithoutCrashing(t *testing.T) {
	ctx := context.Background()
	modelErr := errors.New("primary unavailable")
	agent, err := New(ctx, &Config{
		Name:  "assistant",
		Model: NewMockChatModel(MockModelError(modelErr)),
		ModelFailoverConfig: &ModelFailoverConfig{
			MaxRetries: 1,
			ShouldFailover: func(context.Context, *schema.Message, error) bool {
				panic("broken failover policy")
			},
			GetFailoverModel: func(context.Context, *adk.FailoverContext[*schema.Message]) (ChatModel, []*schema.Message, error) {
				return NewMockChatModel(MockModelText("backup")), nil, nil
			},
		},
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	t.Cleanup(func() { _ = agent.Close() })
	events := newMockEventRecorder()
	agent.Subscribe(events.Record)

	if _, err := agent.Ask(ctx, "hello"); !errors.Is(err, modelErr) {
		t.Fatalf("Ask() error = %v, want original model error", err)
	}
	var found bool
	for _, event := range events.Events() {
		if event.Type == EventError && errors.Is(event.Error, ErrModelPolicyPanic) {
			found = true
		}
	}
	if !found {
		t.Fatal("missing ErrModelPolicyPanic event")
	}
}

func TestAgentReturnsGetFailoverModelPanicAsError(t *testing.T) {
	ctx := context.Background()
	agent, err := New(ctx, &Config{
		Name:  "assistant",
		Model: NewMockChatModel(MockModelError(errors.New("primary unavailable"))),
		ModelFailoverConfig: &ModelFailoverConfig{
			MaxRetries: 1,
			ShouldFailover: func(context.Context, *schema.Message, error) bool {
				return true
			},
			GetFailoverModel: func(context.Context, *adk.FailoverContext[*schema.Message]) (ChatModel, []*schema.Message, error) {
				panic("broken model selector")
			},
		},
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	t.Cleanup(func() { _ = agent.Close() })

	if _, err := agent.Ask(ctx, "hello"); !errors.Is(err, ErrModelPolicyPanic) {
		t.Fatalf("Ask() error = %v, want ErrModelPolicyPanic", err)
	}
}
