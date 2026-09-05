package agentkit

import (
	"context"
	"fmt"
	"testing"

	"github.com/cloudwego/eino/schema"
)

func TestAgentRepairsDanglingToolCallsBeforeModelRequest(t *testing.T) {
	ctx := context.Background()
	const callID = "interrupted-call"
	model := NewMockChatModel(MockExpect(MockModelText("continued"), func(call MockModelCall) error {
		for _, message := range call.Input {
			if message != nil && message.Role == schema.Tool && message.ToolCallID == callID {
				if message.ToolName != "slow_tool" {
					return fmt.Errorf("patched tool name = %q, want slow_tool", message.ToolName)
				}
				if message.Content == "" {
					return fmt.Errorf("patched tool result is empty")
				}
				return nil
			}
		}
		return fmt.Errorf("no patched result for dangling tool call %q", callID)
	}))
	agent, err := New(ctx, &Config{
		Name:  "assistant",
		Model: model,
		History: []*schema.Message{
			schema.UserMessage("run the slow operation"),
			schema.AssistantMessage("", []schema.ToolCall{{
				ID: callID,
				Function: schema.FunctionCall{
					Name:      "slow_tool",
					Arguments: `{"value":"unfinished"}`,
				},
			}}),
		},
	})
	if err != nil {
		t.Fatalf("create agent: %v", err)
	}
	t.Cleanup(func() { _ = agent.Close() })

	result, err := agent.Ask(ctx, "skip that operation and continue")
	if err != nil {
		t.Fatalf("ask after dangling tool call: %v", err)
	}
	if result.Text != "continued" {
		t.Fatalf("unexpected response: %q", result.Text)
	}
}
