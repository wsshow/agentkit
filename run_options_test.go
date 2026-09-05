package agentkit

import (
	"context"
	"fmt"
	"testing"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
)

type requestOptionTool struct {
	optionValue string
	runValue    string
	updated     bool
}

func (t *requestOptionTool) Info(context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{Name: "request_options", Desc: "inspect request options"}, nil
}

func (t *requestOptionTool) InvokableRun(ctx context.Context, _ string, options ...tool.Option) (string, error) {
	config := tool.GetImplSpecificOptions(&requestToolOptions{}, options...)
	t.optionValue = config.value
	t.runValue, _ = RunValue[string](ctx, "request_id")
	SetRunValue(ctx, "tool_updated", true)
	t.updated, _ = RunValue[bool](ctx, "tool_updated")
	return fmt.Sprintf("%s:%s", t.runValue, t.optionValue), nil
}

type requestToolOptions struct {
	value string
}

func requestToolOption(value string) ToolOption {
	return tool.WrapImplSpecificOptFn(func(options *requestToolOptions) {
		options.value = value
	})
}

func TestRunConfigAppliesToModelToolAndValues(t *testing.T) {
	ctx := context.Background()
	requestTool := &requestOptionTool{}
	chatModel := NewMockChatModel(
		MockModelToolCallWithID("request-options-call", "request_options", `{}`),
		MockModelTextAfterToolResult("request-options-call"),
	)
	temperature := float32(0.25)
	values := map[string]any{"request_id": "request-42", "user": "Alice", "tool_updated": false}
	runCtx := WithRunConfig(ctx, RunConfig{
		ModelOptions: []ModelOption{model.WithTemperature(temperature)},
		ToolOptions:  []ToolOption{requestToolOption("enabled")},
		Values:       values,
	})
	values["request_id"] = "mutated"
	values["user"] = "Bob"

	agent, err := New(ctx, &Config{
		SystemPrompt: "Hello {user}; updated={tool_updated}",
		Model:        chatModel,
		Tools:        []Tool{requestTool},
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer agent.Close()

	result, err := agent.Ask(runCtx, "inspect")
	if err != nil {
		t.Fatalf("Ask() error = %v", err)
	}
	if result.Text != "request-42:enabled" {
		t.Fatalf("result text = %q", result.Text)
	}
	if requestTool.runValue != "request-42" || requestTool.optionValue != "enabled" || !requestTool.updated {
		t.Fatalf("tool observed run value %q, option %q, updated %v", requestTool.runValue, requestTool.optionValue, requestTool.updated)
	}

	calls := chatModel.Calls()
	if len(calls) != 2 {
		t.Fatalf("model calls = %d, want 2", len(calls))
	}
	for index, call := range calls {
		options := model.GetCommonOptions(&model.Options{}, call.Options...)
		if options.Temperature == nil || *options.Temperature != temperature {
			t.Fatalf("call %d temperature = %v", index, options.Temperature)
		}
		wantSystem := "Hello Alice; updated=false"
		if len(call.Input) == 0 || call.Input[0].Role != schema.System || call.Input[0].Content != wantSystem {
			var got []string
			for _, message := range call.Input {
				got = append(got, fmt.Sprintf("%s:%s", message.Role, message.Content))
			}
			t.Fatalf("call %d messages = %#v, want first system %q", index, got, wantSystem)
		}
	}
}

func TestRunConfigCopiesInputAndSnapshots(t *testing.T) {
	values := map[string]any{
		"nested": map[string]any{"label": "original"},
	}
	ctx := WithRunConfig(context.Background(), RunConfig{Values: values})
	values["nested"].(map[string]any)["label"] = "mutated"

	value, ok := RunValue[map[string]any](ctx, "nested")
	if !ok || value["label"] != "original" {
		t.Fatalf("RunValue() = %#v, %v", value, ok)
	}
	value["label"] = "returned value mutation"
	snapshot := RunValues(ctx)
	if snapshot["nested"].(map[string]any)["label"] != "original" {
		t.Fatalf("RunValue() exposed stored data: %#v", snapshot)
	}
	snapshot["nested"].(map[string]any)["label"] = "snapshot mutation"
	value, _ = RunValue[map[string]any](ctx, "nested")
	if value["label"] != "original" {
		t.Fatalf("stored value changed to %#v", value)
	}
}

func TestRunValueRejectsWrongType(t *testing.T) {
	ctx := WithRunConfig(context.Background(), RunConfig{Values: map[string]any{"count": 3}})
	if _, ok := RunValue[string](ctx, "count"); ok {
		t.Fatal("RunValue[string]() unexpectedly accepted an int")
	}
}
