package agentkit

import (
	"context"
	"errors"
	"testing"

	"github.com/cloudwego/eino/components/tool/utils"
	"github.com/cloudwego/eino/schema"
)

type resultEchoInput struct {
	Text string `json:"text"`
}

func TestAskReturnsCompleteIsolatedRunResult(t *testing.T) {
	ctx := context.Background()
	echo, err := utils.InferTool("echo", "echo text", func(_ context.Context, input *resultEchoInput) (string, error) {
		return input.Text, nil
	})
	if err != nil {
		t.Fatalf("InferTool() error = %v", err)
	}
	call := MockModelToolCallWithID("echo-call", "echo", `{"text":"hello"}`)
	call.Message.ResponseMeta = &schema.ResponseMeta{
		FinishReason: "tool_calls",
		Usage: &schema.TokenUsage{
			PromptTokens:            10,
			CompletionTokens:        2,
			TotalTokens:             12,
			PromptTokenDetails:      schema.PromptTokenDetails{CachedTokens: 3},
			CompletionTokensDetails: schema.CompletionTokensDetails{ReasoningTokens: 1},
		},
	}
	final := MockModelText("done")
	final.Message.ResponseMeta = &schema.ResponseMeta{
		FinishReason: "stop",
		Usage: &schema.TokenUsage{
			PromptTokens:            20,
			CompletionTokens:        4,
			TotalTokens:             24,
			PromptTokenDetails:      schema.PromptTokenDetails{CachedTokens: 5},
			CompletionTokensDetails: schema.CompletionTokensDetails{ReasoningTokens: 2},
		},
	}
	agent, err := New(ctx, &Config{
		Name:  "assistant",
		Model: NewMockChatModel(call, final),
		Tools: []Tool{echo},
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer agent.Close()

	result, err := agent.Ask(ctx, "say hello")
	if err != nil {
		t.Fatalf("Ask() error = %v", err)
	}
	if result == nil || result.Response == nil {
		t.Fatalf("Ask() result = %#v", result)
	}
	if result.Text != "done" || result.Response.Content != "done" || result.FinishReason != "stop" {
		t.Fatalf("final response = text %q, message %#v, finish %q", result.Text, result.Response, result.FinishReason)
	}
	if len(result.Messages) != 4 {
		t.Fatalf("result messages = %d, want user, assistant tool call, tool, assistant", len(result.Messages))
	}
	if len(result.ToolCalls) != 1 || result.ToolCalls[0].ID != "echo-call" {
		t.Fatalf("tool calls = %#v", result.ToolCalls)
	}
	if result.Usage == nil || result.Usage.PromptTokens != 30 || result.Usage.CompletionTokens != 6 || result.Usage.TotalTokens != 36 {
		t.Fatalf("usage = %#v", result.Usage)
	}
	if result.Usage.PromptTokenDetails.CachedTokens != 8 || result.Usage.CompletionTokensDetails.ReasoningTokens != 3 {
		t.Fatalf("usage details = %#v", result.Usage)
	}
	if result.IsInterrupted() {
		t.Fatalf("IsInterrupted() = true, interrupts %#v", result.Interrupts)
	}

	result.Response.Content = "changed"
	result.Messages[0].Content = "changed input"
	result.ToolCalls[0].Function.Name = "changed"
	result.Usage.TotalTokens = 999
	history := agent.History()
	if history[0].Content != "say hello" || history[len(history)-1].Content != "done" {
		t.Fatalf("mutating result changed history: %#v", history)
	}
	if history[1].ToolCalls[0].Function.Name != "echo" || history[len(history)-1].ResponseMeta.Usage.TotalTokens != 24 {
		t.Fatalf("mutating result metadata changed history: %#v", history)
	}
}

func TestAskReturnsPartialResultWithError(t *testing.T) {
	ctx := context.Background()
	modelErr := errors.New("model unavailable")
	agent, err := New(ctx, &Config{Model: NewMockChatModel(MockModelError(modelErr))})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer agent.Close()

	result, err := agent.Ask(ctx, "keep this input")
	if !errors.Is(err, modelErr) {
		t.Fatalf("Ask() error = %v, want model error", err)
	}
	if result == nil || result.Response != nil || len(result.Messages) != 1 || result.Messages[0].Content != "keep this input" {
		t.Fatalf("partial result = %#v", result)
	}
}

func TestAskPartsAndContinueWithResult(t *testing.T) {
	ctx := context.Background()
	agent, err := New(ctx, &Config{
		Model: NewMockChatModel(MockModelText("image received"), MockModelText("continued")),
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer agent.Close()

	partsResult, err := agent.AskParts(ctx, Text("describe "), ImageURL("https://example.com/image.png"))
	if err != nil {
		t.Fatalf("AskParts() error = %v", err)
	}
	if partsResult.Text != "image received" || len(partsResult.Messages) != 2 {
		t.Fatalf("AskParts() result = %#v", partsResult)
	}

	agent.SetHistory([]*schema.Message{schema.UserMessage("retry me")})
	continued, err := agent.ContinueWithResult(ctx)
	if err != nil {
		t.Fatalf("ContinueWithResult() error = %v", err)
	}
	if continued.Text != "continued" || len(continued.Messages) != 1 {
		t.Fatalf("ContinueWithResult() = %#v", continued)
	}
}
