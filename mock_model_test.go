package agentkit

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cloudwego/eino/components/tool/utils"
	"github.com/cloudwego/eino/schema"
)

func TestMockChatModelTextResponse(t *testing.T) {
	ctx := context.Background()
	model := NewMockChatModel(MockModelText("你好"))
	agent, err := New(ctx, &Config{
		Name:  "test-agent",
		Model: model,
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer agent.Close()

	if err := agent.Prompt(ctx, "打个招呼"); err != nil {
		t.Fatalf("Prompt() error = %v", err)
	}

	messages := agent.State().Messages()
	if len(messages) != 2 {
		t.Fatalf("messages length = %d, want 2", len(messages))
	}
	if messages[1].Content != "你好" {
		t.Fatalf("assistant content = %q, want %q", messages[1].Content, "你好")
	}

	calls := model.Calls()
	if len(calls) != 1 {
		t.Fatalf("calls length = %d, want 1", len(calls))
	}
	if !calls[0].Streaming {
		t.Fatal("call should be streaming")
	}
	if got := lastMessageContent(calls[0].Input); got != "打个招呼" {
		t.Fatalf("last input content = %q, want %q", got, "打个招呼")
	}
}

func TestMockChatModelWithSystemPrompt(t *testing.T) {
	ctx := context.Background()
	chatModel := NewMockChatModel(MockExpect(MockModelText("好的，我来帮你。"), func(call MockModelCall) error {
		if got := firstMessageContentByRole(call.Input, schema.System); got != "你是一个有用的助手。" {
			return fmt.Errorf("system prompt = %q, want %q", got, "你是一个有用的助手。")
		}
		if got := lastMessageContentByRole(call.Input, schema.User); got != "帮我总结一下" {
			return fmt.Errorf("user input = %q, want %q", got, "帮我总结一下")
		}
		return nil
	}))

	agent, err := New(ctx, &Config{
		Name:         "assistant",
		SystemPrompt: "你是一个有用的助手。",
		Model:        chatModel,
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer agent.Close()

	if err := agent.Prompt(ctx, "帮我总结一下"); err != nil {
		t.Fatalf("Prompt() error = %v", err)
	}

	messages := agent.State().Messages()
	if len(messages) != 2 {
		t.Fatalf("messages length = %d, want 2", len(messages))
	}
	if messages[0].Role != RoleUser || messages[0].Content != "帮我总结一下" {
		t.Fatalf("user message = %#v", messages[0])
	}
	if messages[1].Role != RoleAssistant || messages[1].Agent != "assistant" || messages[1].Content != "好的，我来帮你。" {
		t.Fatalf("assistant message = %#v", messages[1])
	}

	calls := chatModel.Calls()
	if len(calls) != 1 {
		t.Fatalf("calls length = %d, want 1", len(calls))
	}
}

func TestMockChatModelStreamResponse(t *testing.T) {
	ctx := context.Background()
	model := NewMockChatModel(MockModelStream("hel", "lo"))
	agent, err := New(ctx, &Config{
		Name:  "test-agent",
		Model: model,
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer agent.Close()

	var deltas strings.Builder
	agent.Subscribe(func(event Event) {
		if event.Type == EventMessageDelta {
			deltas.WriteString(event.Delta)
		}
	})

	if err := agent.Prompt(ctx, "say hello"); err != nil {
		t.Fatalf("Prompt() error = %v", err)
	}

	if got := deltas.String(); got != "hello" {
		t.Fatalf("stream deltas = %q, want %q", got, "hello")
	}
	messages := agent.State().Messages()
	if got := messages[len(messages)-1].Content; got != "hello" {
		t.Fatalf("state content = %q, want %q", got, "hello")
	}
}

func TestAgentEventSequenceForTextResponse(t *testing.T) {
	ctx := context.Background()
	model := NewMockChatModel(MockModelText("你好"))
	agent, err := New(ctx, &Config{
		Name:  "assistant",
		Model: model,
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer agent.Close()

	events := newMockEventRecorder()
	agent.Subscribe(events.Record)

	if err := agent.Prompt(ctx, "打个招呼"); err != nil {
		t.Fatalf("Prompt() error = %v", err)
	}

	events.RequireTypes(t,
		EventAgentStart,
		EventTurnStart,
		EventMessageStart,
		EventMessageEnd,
		EventMessageStart,
		EventMessageDelta,
		EventMessageEnd,
		EventTurnEnd,
		EventAgentEnd,
	)
	allEvents := events.Events()
	if allEvents[2].Role != RoleUser || allEvents[2].Content != "打个招呼" {
		t.Fatalf("user message start = %#v", allEvents[2])
	}
	if allEvents[3].Role != RoleUser || allEvents[3].Content != "打个招呼" {
		t.Fatalf("user message end = %#v", allEvents[3])
	}
	if allEvents[4].Role != RoleAssistant {
		t.Fatalf("assistant message start = %#v", allEvents[4])
	}
	end := events.Last(EventMessageEnd)
	if end == nil || end.Agent != "assistant" || end.Role != RoleAssistant || end.Content != "你好" {
		t.Fatalf("message end event = %#v", end)
	}
}

func TestAgentStreamEventsReasoningAndMeta(t *testing.T) {
	ctx := context.Background()
	meta := &schema.ResponseMeta{
		FinishReason: "stop",
		Usage: &schema.TokenUsage{
			PromptTokens:     3,
			CompletionTokens: 2,
			TotalTokens:      5,
		},
	}
	model := NewMockChatModel(MockModelResponse{Chunks: []*schema.Message{
		{Role: schema.Assistant, ReasoningContent: "想"},
		{Role: schema.Assistant, Content: "你"},
		{Role: schema.Assistant, ReasoningContent: "好了", Content: "好", ResponseMeta: meta},
	}})
	agent, err := New(ctx, &Config{
		Name:  "assistant",
		Model: model,
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer agent.Close()

	events := newMockEventRecorder()
	agent.Subscribe(events.Record)

	if err := agent.Prompt(ctx, "打个招呼"); err != nil {
		t.Fatalf("Prompt() error = %v", err)
	}

	if got := events.Deltas(EventReasoningDelta); strings.Join(got, "") != "想好了" {
		t.Fatalf("reasoning deltas = %v", got)
	}
	if got := events.Deltas(EventMessageDelta); strings.Join(got, "") != "你好" {
		t.Fatalf("message deltas = %v", got)
	}
	end := events.Last(EventMessageEnd)
	if end == nil {
		t.Fatal("missing message end event")
	}
	if end.Content != "你好" || end.ReasoningContent != "想好了" {
		t.Fatalf("message end event = %#v", end)
	}
	if end.ResponseMeta == nil || end.ResponseMeta.FinishReason != "stop" || end.ResponseMeta.Usage.TotalTokens != 5 {
		t.Fatalf("response meta = %#v", end.ResponseMeta)
	}

	messages := agent.State().Messages()
	last := messages[len(messages)-1]
	if last.Content != "你好" || last.ReasoningContent != "想好了" {
		t.Fatalf("state message = %#v", last)
	}
}

func TestAgentStreamErrorEvent(t *testing.T) {
	ctx := context.Background()
	streamErr := errors.New("stream interrupted")
	model := NewMockChatModel(MockModelStreamError(streamErr, "partial"))
	agent, err := New(ctx, &Config{
		Name:  "assistant",
		Model: model,
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer agent.Close()

	events := newMockEventRecorder()
	agent.Subscribe(events.Record)

	err = agent.Prompt(ctx, "hello")
	if !errors.Is(err, streamErr) {
		t.Fatalf("Prompt() error = %v, want %v", err, streamErr)
	}
	errorEvent := events.Last(EventError)
	if errorEvent == nil || !errors.Is(errorEvent.Error, streamErr) {
		t.Fatalf("error event = %#v", errorEvent)
	}
	if got := events.Deltas(EventMessageDelta); strings.Join(got, "") != "partial" {
		t.Fatalf("message deltas = %v", got)
	}
	if got := agent.State().Messages(); len(got) != 1 {
		t.Fatalf("messages length = %d, want 1", len(got))
	}
}

func TestAgentToolEventsAndUpdates(t *testing.T) {
	ctx := context.Background()
	tool := MustMockTool("echo", "echo text", func(ctx context.Context, input *mockEchoInput) (string, error) {
		EmitToolUpdate(ctx, "正在执行")
		return "echo: " + input.Text, nil
	})
	call := tool.Call("echo_call", &mockEchoInput{Text: "hi"})
	model := NewMockChatModel(
		MockModelCalls(call),
		MockModelTextAfter(call, func(result string) string {
			return "工具返回 " + result
		}),
	)
	agent, err := New(ctx, &Config{
		Name:  "assistant",
		Model: model,
		Tools: MockTools(tool),
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer agent.Close()

	events := newMockEventRecorder()
	agent.Subscribe(events.Record)

	if err := agent.Prompt(ctx, "调用 echo"); err != nil {
		t.Fatalf("Prompt() error = %v", err)
	}

	events.RequireTypes(t,
		EventAgentStart,
		EventTurnStart,
		EventMessageStart,
		EventMessageEnd,
		EventMessageStart,
		EventMessageEnd,
		EventToolStart,
		EventToolUpdate,
		EventToolEnd,
		EventMessageStart,
		EventMessageEnd,
		EventTurnEnd,
		EventTurnStart,
		EventMessageStart,
		EventMessageDelta,
		EventMessageEnd,
		EventTurnEnd,
		EventAgentEnd,
	)

	toolStart := events.Last(EventToolStart)
	if toolStart == nil || len(toolStart.ToolCalls) != 1 || toolStart.ToolCalls[0].ID != "echo_call" {
		t.Fatalf("tool start event = %#v", toolStart)
	}
	if update := events.Last(EventToolUpdate); update == nil || update.Content != "正在执行" {
		t.Fatalf("tool update event = %#v", update)
	} else if update.ToolCallID != "echo_call" || update.ToolName != "echo" || update.ToolArguments != `{"text":"hi"}` {
		t.Fatalf("tool update match = %#v", update)
	}
	if end := events.Last(EventToolEnd); end == nil || end.Content != "echo: hi" {
		t.Fatalf("tool end event = %#v", end)
	} else if end.ToolCallID != "echo_call" || end.ToolName != "echo" || end.ToolArguments != `{"text":"hi"}` {
		t.Fatalf("tool end match = %#v", end)
	}
	toolMessageEnd := events.LastMessageEndByRole(RoleTool)
	if toolMessageEnd == nil || toolMessageEnd.Content != "echo: hi" {
		t.Fatalf("tool message end = %#v", toolMessageEnd)
	}
	if got := events.Count(EventTurnStart); got != 2 {
		t.Fatalf("turn start count = %d, want 2", got)
	}
	if got := events.Count(EventTurnEnd); got != 2 {
		t.Fatalf("turn end count = %d, want 2", got)
	}
}

func TestMockChatModelToolCallResponse(t *testing.T) {
	ctx := context.Background()
	tool, err := utils.InferTool("echo", "echo text", func(ctx context.Context, input *mockEchoInput) (string, error) {
		return "echo: " + input.Text, nil
	})
	if err != nil {
		t.Fatalf("InferTool() error = %v", err)
	}

	model := NewMockChatModel(
		MockModelToolCallWithID("echo_call", "echo", `{"text":"hi"}`),
		MockModelAfterToolResult("echo_call", MockModelText("工具返回 echo: hi")),
	)
	agent, err := New(ctx, &Config{
		Name:  "test-agent",
		Model: model,
		Tools: []Tool{tool},
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer agent.Close()

	if err := agent.Prompt(ctx, "调用 echo"); err != nil {
		t.Fatalf("Prompt() error = %v", err)
	}

	if got := model.RemainingResponses(); got != 0 {
		t.Fatalf("remaining responses = %d, want 0", got)
	}
	if calls := model.Calls(); len(calls) != 2 {
		t.Fatalf("calls length = %d, want 2", len(calls))
	}
	calls := model.Calls()
	if !inputHasToolResult(calls[1].Input, "echo_call") {
		t.Fatal("second call input should contain matched tool result")
	}
	if !historyHasRole(agent.History(), schema.Tool) {
		t.Fatal("history should contain a tool message")
	}
	messages := agent.State().Messages()
	if got := messages[len(messages)-1].Content; got != "工具返回 echo: hi" {
		t.Fatalf("final content = %q, want %q", got, "工具返回 echo: hi")
	}
}

func TestMockChatModelToolResultMismatch(t *testing.T) {
	ctx := context.Background()
	tool, err := utils.InferTool("echo", "echo text", func(ctx context.Context, input *mockEchoInput) (string, error) {
		return "echo: " + input.Text, nil
	})
	if err != nil {
		t.Fatalf("InferTool() error = %v", err)
	}

	model := NewMockChatModel(
		MockModelToolCallWithID("echo_call", "echo", `{"text":"hi"}`),
		MockModelAfterToolResult("other_call", MockModelText("should fail")),
	)
	agent, err := New(ctx, &Config{
		Name:  "test-agent",
		Model: model,
		Tools: []Tool{tool},
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer agent.Close()

	err = agent.Prompt(ctx, "调用 echo")
	if err == nil {
		t.Fatal("Prompt() should fail on tool result mismatch")
	}
	if !strings.Contains(err.Error(), "other_call") {
		t.Fatalf("Prompt() error = %v, want missing call ID", err)
	}
}

func TestMockToolCallFuncReturnsToolResultText(t *testing.T) {
	ctx := context.Background()
	call, err := MockToolCallFunc("echo", "echo text", &mockEchoInput{Text: "hi"},
		func(ctx context.Context, input *mockEchoInput) (string, error) {
			return "echo: " + input.Text, nil
		})
	if err != nil {
		t.Fatalf("MockToolCallFunc() error = %v", err)
	}

	model := NewMockChatModel(
		MockModelCalls(call),
		MockModelTextAfter(call, func(result string) string {
			return result
		}),
	)
	agent, err := New(ctx, &Config{
		Name:  "test-agent",
		Model: model,
		Tools: MockTools(call),
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer agent.Close()

	if err := agent.Prompt(ctx, "调用 echo"); err != nil {
		t.Fatalf("Prompt() error = %v", err)
	}

	messages := agent.State().Messages()
	if got := messages[len(messages)-1].Content; got != "echo: hi" {
		t.Fatalf("final content = %q, want %q", got, "echo: hi")
	}
}

func TestMockToolTypedInputAndOutput(t *testing.T) {
	ctx := context.Background()
	fetch := MustMockTool("fetch", "fetch URL", mockFetch)
	fetchCall := fetch.Call("fetch_call", &mockFetchRequest{
		URL:    "https://example.com",
		Format: "text",
	})

	model := NewMockChatModel(
		MockModelCalls(fetchCall),
		MockModelRespondsAfter(fetchCall, func(resp *mockFetchResponse) MockModelResponse {
			return MockModelText(resp.Content)
		}),
	)
	agent, err := New(ctx, &Config{
		Name:  "test-agent",
		Model: model,
		Tools: MockTools(fetch),
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer agent.Close()

	if err := agent.Prompt(ctx, "抓取网页"); err != nil {
		t.Fatalf("Prompt() error = %v", err)
	}

	messages := agent.State().Messages()
	if got := messages[len(messages)-1].Content; got != "example content" {
		t.Fatalf("final content = %q, want %q", got, "example content")
	}
}

func TestMockToolMultipleRounds(t *testing.T) {
	ctx := context.Background()
	weather, err := NewMockTool("weather", "query weather",
		func(ctx context.Context, input *mockWeatherInput) (string, error) {
			return input.City + "晴", nil
		})
	if err != nil {
		t.Fatalf("NewMockTool() error = %v", err)
	}
	beijing := weather.Call("beijing_weather", &mockWeatherInput{City: "北京"})
	shanghai := weather.Call("shanghai_weather", &mockWeatherInput{City: "上海"})

	model := NewMockChatModel(
		MockModelCalls(beijing),
		MockModelCallsAfter(beijing, shanghai),
		MockModelTextAfter(shanghai, func(result string) string {
			return result
		}),
	)
	agent, err := New(ctx, &Config{
		Name:  "test-agent",
		Model: model,
		Tools: MockTools(weather),
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer agent.Close()

	if err := agent.Prompt(ctx, "查天气"); err != nil {
		t.Fatalf("Prompt() error = %v", err)
	}

	messages := agent.State().Messages()
	if got := messages[len(messages)-1].Content; got != "上海晴" {
		t.Fatalf("final content = %q, want %q", got, "上海晴")
	}
	if calls := model.Calls(); len(calls) != 3 {
		t.Fatalf("calls length = %d, want 3", len(calls))
	}
}

func TestMockToolParallelRound(t *testing.T) {
	ctx := context.Background()
	weather := MustMockTool("weather", "query weather",
		func(ctx context.Context, input *mockWeatherInput) (string, error) {
			return input.City + "晴", nil
		})
	beijing := weather.Call("beijing_weather", &mockWeatherInput{City: "北京"})
	shanghai := weather.Call("shanghai_weather", &mockWeatherInput{City: "上海"})

	tools := MockTools(beijing, shanghai)
	if len(tools) != 1 {
		t.Fatalf("tools length = %d, want 1", len(tools))
	}

	model := NewMockChatModel(
		MockModelCalls(beijing, shanghai),
		MockModelTextAfterAll("完成", beijing, shanghai),
	)
	agent, err := New(ctx, &Config{
		Name:  "test-agent",
		Model: model,
		Tools: tools,
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer agent.Close()

	if err := agent.Prompt(ctx, "查天气"); err != nil {
		t.Fatalf("Prompt() error = %v", err)
	}

	calls := model.Calls()
	if len(calls) != 2 {
		t.Fatalf("calls length = %d, want 2", len(calls))
	}
	if !inputHasToolResult(calls[1].Input, "beijing_weather") || !inputHasToolResult(calls[1].Input, "shanghai_weather") {
		t.Fatal("second call input should contain both tool results")
	}
}

func TestAgentSteeringAfterToolResult(t *testing.T) {
	ctx := context.Background()
	weather := MustMockTool("weather", "query weather",
		func(ctx context.Context, input *mockWeatherInput) (string, error) {
			return input.City + "晴", nil
		})
	beijing := weather.Call("beijing_weather", &mockWeatherInput{City: "北京"})

	model := NewMockChatModel(
		MockModelCalls(beijing),
		MockExpect(MockModelText("已改查上海"), func(call MockModelCall) error {
			if got := lastMessageContentByRole(call.Input, schema.User); got != "改查上海" {
				return fmt.Errorf("last user input = %q, want %q", got, "改查上海")
			}
			if !inputHasToolResult(call.Input, "beijing_weather") {
				return errors.New("missing weather tool result")
			}
			return nil
		}),
	)
	agent, err := New(ctx, &Config{
		Name:  "assistant",
		Model: model,
		Tools: MockTools(weather),
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer agent.Close()

	steered := false
	events := newMockEventRecorder()
	agent.Subscribe(func(event Event) {
		events.Record(event)
		if event.Type == EventToolEnd && !steered {
			steered = true
			agent.Steer("改查上海")
		}
	})

	if err := agent.Prompt(ctx, "查北京天气"); err != nil {
		t.Fatalf("Prompt() error = %v", err)
	}

	messages := agent.State().Messages()
	if got := messageContents(messages); strings.Join(got, "|") != "查北京天气|改查上海|已改查上海" {
		t.Fatalf("state messages = %#v", messages)
	}
	calls := model.Calls()
	if len(calls) != 2 {
		t.Fatalf("model calls length = %d, want 2", len(calls))
	}
	if got := lastMessageContentByRole(calls[len(calls)-1].Input, schema.User); got != "改查上海" {
		t.Fatalf("last model user input = %q, want %q", got, "改查上海")
	}
	if got := events.Count(EventAgentStart); got != 1 {
		t.Fatalf("agent start count = %d, want 1", got)
	}
	if got := events.Count(EventAgentEnd); got != 1 {
		t.Fatalf("agent end count = %d, want 1", got)
	}
}

func TestAgentFollowUpQueueProcessesAfterCurrentRun(t *testing.T) {
	ctx := context.Background()
	model := NewMockChatModel(
		MockModelText("第一条"),
		MockExpect(MockModelText("第二条"), func(call MockModelCall) error {
			if got := lastMessageContentByRole(call.Input, schema.User); got != "继续" {
				return fmt.Errorf("last user input = %q, want %q", got, "继续")
			}
			return nil
		}),
	)
	agent, err := New(ctx, &Config{
		Name:  "assistant",
		Model: model,
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer agent.Close()

	queued := false
	agent.Subscribe(func(event Event) {
		if event.Type == EventMessageEnd && event.Content == "第一条" && !queued {
			queued = true
			agent.FollowUp("继续")
		}
	})

	if err := agent.Prompt(ctx, "开始"); err != nil {
		t.Fatalf("Prompt() error = %v", err)
	}

	messages := agent.State().Messages()
	if got := messageContents(messages); strings.Join(got, "|") != "开始|第一条|继续|第二条" {
		t.Fatalf("state messages = %#v", messages)
	}
	if calls := model.Calls(); len(calls) != 2 {
		t.Fatalf("model calls length = %d, want 2", len(calls))
	}
}

func TestAgentFollowUpQueueAllModeBatchesMessages(t *testing.T) {
	ctx := context.Background()
	model := NewMockChatModel(
		MockModelText("第一条"),
		MockExpect(MockModelText("完成"), func(call MockModelCall) error {
			if got := userMessageContents(call.Input); strings.Join(got, "|") != "开始|继续一|继续二" {
				return fmt.Errorf("user inputs = %v", got)
			}
			return nil
		}),
	)
	agent, err := New(ctx, &Config{
		Name:  "assistant",
		Model: model,
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer agent.Close()

	agent.SetFollowUpMode(QueueModeAll)
	queued := false
	agent.Subscribe(func(event Event) {
		if event.Type == EventMessageEnd && event.Content == "第一条" && !queued {
			queued = true
			agent.FollowUp("继续一")
			agent.FollowUp("继续二")
		}
	})

	if err := agent.Prompt(ctx, "开始"); err != nil {
		t.Fatalf("Prompt() error = %v", err)
	}

	if calls := model.Calls(); len(calls) != 2 {
		t.Fatalf("model calls length = %d, want 2", len(calls))
	}
	messages := agent.State().Messages()
	if got := messageContents(messages); strings.Join(got, "|") != "开始|第一条|继续一|继续二|完成" {
		t.Fatalf("state messages = %#v", messages)
	}
}

func TestAgentConfigHistoryRestoresModelInputAndState(t *testing.T) {
	ctx := context.Background()
	history := []*schema.Message{
		schema.UserMessage("之前的问题"),
		schema.AssistantMessage("之前的回答", nil),
		schema.AssistantMessage("", []schema.ToolCall{{
			ID: "weather_call",
			Function: schema.FunctionCall{
				Name:      "weather",
				Arguments: `{"city":"北京"}`,
			},
		}}),
		schema.ToolMessage("北京晴", "weather_call", schema.WithToolName("weather")),
	}
	model := NewMockChatModel(MockExpect(MockModelText("继续回答"), func(call MockModelCall) error {
		if got := userMessageContents(call.Input); strings.Join(got, "|") != "之前的问题|继续" {
			return fmt.Errorf("user messages = %v", got)
		}
		if !inputHasToolResult(call.Input, "weather_call") {
			return errors.New("missing restored tool result")
		}
		return nil
	}))
	agent, err := New(ctx, &Config{
		Name:    "assistant",
		Model:   model,
		History: history,
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer agent.Close()

	if got := messageContents(agent.State().Messages()); strings.Join(got, "|") != "之前的问题|之前的回答" {
		t.Fatalf("restored state messages = %#v", agent.State().Messages())
	}

	history[0].Content = "外部修改"
	if err := agent.Prompt(ctx, "继续"); err != nil {
		t.Fatalf("Prompt() error = %v", err)
	}
	if got := firstMessageContentByRole(agent.History(), schema.User); got != "之前的问题" {
		t.Fatalf("history was modified through source slice: %q", got)
	}
}

func TestAgentSetHistoryReplacesHistoryAndState(t *testing.T) {
	ctx := context.Background()
	model := NewMockChatModel(
		MockModelText("旧回答"),
		MockExpect(MockModelText("新回答"), func(call MockModelCall) error {
			if got := userMessageContents(call.Input); strings.Join(got, "|") != "新问题|继续" {
				return fmt.Errorf("user messages = %v", got)
			}
			if firstMessageContentByRole(call.Input, schema.Assistant) != "新回答前文" {
				return errors.New("missing restored assistant message")
			}
			return nil
		}),
	)
	agent, err := New(ctx, &Config{Name: "assistant", Model: model})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer agent.Close()

	if err := agent.Prompt(ctx, "旧问题"); err != nil {
		t.Fatalf("Prompt() error = %v", err)
	}

	agent.SetHistory([]*schema.Message{
		schema.UserMessage("新问题"),
		schema.AssistantMessage("新回答前文", nil),
	})
	if got := messageContents(agent.State().Messages()); strings.Join(got, "|") != "新问题|新回答前文" {
		t.Fatalf("state messages after SetHistory = %#v", agent.State().Messages())
	}

	if err := agent.Prompt(ctx, "继续"); err != nil {
		t.Fatalf("Prompt() error = %v", err)
	}
}

func TestAgentHistoryReturnsCopy(t *testing.T) {
	ctx := context.Background()
	agent, err := New(ctx, &Config{
		Name:  "assistant",
		Model: NewMockChatModel(MockModelText("回答")),
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer agent.Close()

	if err := agent.Prompt(ctx, "问题"); err != nil {
		t.Fatalf("Prompt() error = %v", err)
	}

	history := agent.History()
	history[0].Content = "外部修改"
	history = append(history, schema.UserMessage("额外消息"))
	if len(history) != 3 {
		t.Fatalf("modified history length = %d, want 3", len(history))
	}

	got := agent.History()
	if len(got) != 2 {
		t.Fatalf("history length = %d, want 2", len(got))
	}
	if got[0].Content != "问题" {
		t.Fatalf("history content = %q, want %q", got[0].Content, "问题")
	}
}

func TestAgentSendCopiesCallerContentParts(t *testing.T) {
	agent, err := New(context.Background(), &Config{
		Name:  "assistant",
		Model: NewMockChatModel(MockModelText("done")),
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer agent.Close()

	part := ImageURL("https://example.com/original.png")
	part.Extra = map[string]any{"labels": []string{"original"}}
	if err := agent.Send(context.Background(), part); err != nil {
		t.Fatalf("Send() error = %v", err)
	}

	*part.Image.URL = "https://example.com/mutated.png"
	part.Extra["labels"].([]string)[0] = "mutated"

	history := agent.History()
	if got := *history[0].UserInputMultiContent[0].Image.URL; got != "https://example.com/original.png" {
		t.Fatalf("stored image URL = %q, want original", got)
	}
	if got := history[0].UserInputMultiContent[0].Extra["labels"].([]string)[0]; got != "original" {
		t.Fatalf("stored labels = %q, want original", got)
	}
}

func TestAgentHistoryDeeplyIsolatesMutableMessageFields(t *testing.T) {
	ctx := context.Background()
	agent, err := New(ctx, &Config{Name: "assistant", Model: NewMockChatModel()})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer agent.Close()

	imageURL := "https://example.com/original.png"
	toolIndex := 2
	streamIndex := 3
	source := []*schema.Message{
		{
			Role: schema.User,
			UserInputMultiContent: []schema.MessageInputPart{{
				Type: schema.ChatMessagePartTypeImageURL,
				Image: &schema.MessageInputImage{MessagePartCommon: schema.MessagePartCommon{
					URL: &imageURL,
				}},
				Extra: map[string]any{"nested": map[string]any{"label": "original"}},
			}},
		},
		{
			Role: schema.Assistant,
			ToolCalls: []schema.ToolCall{{
				Index: &toolIndex,
				ID:    "call-1",
				Extra: map[string]any{"tags": []string{"original"}},
			}},
			AssistantGenMultiContent: []schema.MessageOutputPart{{
				Type:          schema.ChatMessagePartTypeText,
				Text:          "original",
				StreamingMeta: &schema.MessageStreamingMeta{Index: streamIndex},
			}},
			ResponseMeta: &schema.ResponseMeta{
				Usage: &schema.TokenUsage{TotalTokens: 42},
				LogProbs: &schema.LogProbs{Content: []schema.LogProb{{
					Token: "original",
					Bytes: []int64{1, 2},
					TopLogProbs: []schema.TopLogProb{{
						Token: "original",
						Bytes: []int64{3, 4},
					}},
				}}},
			},
		},
	}
	agent.SetHistory(source)

	assertOriginal := func(t *testing.T, history []*schema.Message) {
		t.Helper()
		if got := *history[0].UserInputMultiContent[0].Image.URL; got != "https://example.com/original.png" {
			t.Fatalf("image URL = %q, want original", got)
		}
		if got := history[0].UserInputMultiContent[0].Extra["nested"].(map[string]any)["label"]; got != "original" {
			t.Fatalf("nested extra = %v, want original", got)
		}
		if got := *history[1].ToolCalls[0].Index; got != 2 {
			t.Fatalf("tool index = %d, want 2", got)
		}
		if got := history[1].ToolCalls[0].Extra["tags"].([]string)[0]; got != "original" {
			t.Fatalf("tool tags = %q, want original", got)
		}
		if got := history[1].AssistantGenMultiContent[0].StreamingMeta.Index; got != 3 {
			t.Fatalf("stream index = %d, want 3", got)
		}
		if got := history[1].ResponseMeta.Usage.TotalTokens; got != 42 {
			t.Fatalf("total tokens = %d, want 42", got)
		}
		if got := history[1].ResponseMeta.LogProbs.Content[0].Bytes[0]; got != 1 {
			t.Fatalf("log probability bytes = %d, want 1", got)
		}
		if got := history[1].ResponseMeta.LogProbs.Content[0].TopLogProbs[0].Bytes[0]; got != 3 {
			t.Fatalf("top log probability bytes = %d, want 3", got)
		}
	}

	*source[0].UserInputMultiContent[0].Image.URL = "mutated source"
	source[0].UserInputMultiContent[0].Extra["nested"].(map[string]any)["label"] = "mutated source"
	*source[1].ToolCalls[0].Index = 99
	source[1].ToolCalls[0].Extra["tags"].([]string)[0] = "mutated source"
	source[1].AssistantGenMultiContent[0].StreamingMeta.Index = 99
	source[1].ResponseMeta.Usage.TotalTokens = 99
	source[1].ResponseMeta.LogProbs.Content[0].Bytes[0] = 99
	source[1].ResponseMeta.LogProbs.Content[0].TopLogProbs[0].Bytes[0] = 99

	history := agent.History()
	assertOriginal(t, history)
	*history[0].UserInputMultiContent[0].Image.URL = "mutated snapshot"
	history[0].UserInputMultiContent[0].Extra["nested"].(map[string]any)["label"] = "mutated snapshot"
	*history[1].ToolCalls[0].Index = 100
	history[1].ToolCalls[0].Extra["tags"].([]string)[0] = "mutated snapshot"
	history[1].AssistantGenMultiContent[0].StreamingMeta.Index = 100
	history[1].ResponseMeta.Usage.TotalTokens = 100
	history[1].ResponseMeta.LogProbs.Content[0].Bytes[0] = 100
	history[1].ResponseMeta.LogProbs.Content[0].TopLogProbs[0].Bytes[0] = 100

	assertOriginal(t, agent.History())
}

func TestAgentCancelIsSafeFromSubscriber(t *testing.T) {
	agent, err := New(context.Background(), &Config{
		Name:  "assistant",
		Model: NewMockChatModel(MockModelText("unused")),
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer agent.Close()

	agent.Subscribe(func(event Event) {
		if event.Type == EventAgentStart {
			agent.Cancel()
		}
	})

	done := make(chan error, 1)
	go func() {
		done <- agent.Prompt(context.Background(), "cancel me")
	}()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Prompt() error = %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Cancel() from subscriber deadlocked")
	}
}

func TestAgentAbortCanCancelBeforeIterationStarts(t *testing.T) {
	agent, err := New(context.Background(), &Config{
		Name:  "assistant",
		Model: NewMockChatModel(MockModelText("unused")),
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer agent.Close()

	started := make(chan struct{})
	release := make(chan struct{})
	agent.Subscribe(func(event Event) {
		if event.Type == EventAgentStart {
			close(started)
			<-release
		}
	})

	runDone := make(chan error, 1)
	go func() {
		runDone <- agent.Prompt(context.Background(), "abort me")
	}()
	<-started

	canceled := make(chan struct{})
	var cancelObserved sync.Once
	agent.mu.Lock()
	cancel := agent.cancelFn
	agent.cancelFn = func() {
		cancel()
		cancelObserved.Do(func() { close(canceled) })
	}
	agent.mu.Unlock()

	abortDone := make(chan struct{})
	go func() {
		agent.Abort()
		close(abortDone)
	}()
	<-canceled
	close(release)

	select {
	case err := <-runDone:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Prompt() error = %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Prompt() did not stop after Abort()")
	}
	select {
	case <-abortDone:
	case <-time.After(2 * time.Second):
		t.Fatal("Abort() did not wait for Prompt() to stop")
	}
}

func TestAgentAbortContextBoundsNonCooperativeRun(t *testing.T) {
	agent, err := New(context.Background(), &Config{
		Name:  "assistant",
		Model: NewMockChatModel(MockModelText("unused")),
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer agent.Close()

	started := make(chan struct{})
	release := make(chan struct{})
	agent.Subscribe(func(event Event) {
		if event.Type == EventAgentStart {
			close(started)
			<-release
		}
	})
	runDone := make(chan error, 1)
	go func() {
		runDone <- agent.Prompt(context.Background(), "abort me")
	}()
	<-started

	waitCtx, cancelWait := context.WithTimeout(context.Background(), 20*time.Millisecond)
	err = agent.AbortContext(waitCtx)
	cancelWait()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("AbortContext() error = %v, want context.DeadlineExceeded", err)
	}
	close(release)
	select {
	case err := <-runDone:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Prompt() error = %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Prompt() did not observe the cancellation request")
	}
}

func TestAgentRunningRejectsConcurrentPrompt(t *testing.T) {
	ctx := context.Background()
	started := make(chan struct{})
	release := make(chan struct{})
	tool := MustMockTool("wait", "wait for release", func(ctx context.Context, input *mockEchoInput) (string, error) {
		close(started)
		select {
		case <-release:
			return "done", nil
		case <-ctx.Done():
			return "", ctx.Err()
		}
	})
	call := tool.Call("wait_call", &mockEchoInput{Text: "hi"})
	model := NewMockChatModel(
		MockModelCalls(call),
		MockModelTextAfter(call, func(result string) string {
			return result
		}),
	)
	agent, err := New(ctx, &Config{
		Name:  "assistant",
		Model: model,
		Tools: MockTools(tool),
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer agent.Close()

	runErr := make(chan error, 1)
	go func() {
		runErr <- agent.Prompt(ctx, "开始")
	}()

	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("tool did not start")
	}

	err = agent.Prompt(ctx, "并发输入")
	if !errors.Is(err, ErrAgentRunning) {
		t.Fatalf("concurrent Prompt() error = %v, want ErrAgentRunning", err)
	}

	close(release)
	select {
	case err := <-runErr:
		if err != nil {
			t.Fatalf("first Prompt() error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("first prompt did not finish")
	}
}

func TestMockChatModelNoResponse(t *testing.T) {
	ctx := context.Background()
	model := NewMockChatModel()
	agent, err := New(ctx, &Config{
		Name:  "test-agent",
		Model: model,
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer agent.Close()

	err = agent.Prompt(ctx, "hello")
	if !errors.Is(err, ErrMockModelNoResponse) {
		t.Fatalf("Prompt() error = %v, want %v", err, ErrMockModelNoResponse)
	}
}

type mockEchoInput struct {
	Text string `json:"text"`
}

type mockWeatherInput struct {
	City string `json:"city"`
}

type mockFetchRequest struct {
	URL     string `json:"url" jsonschema:"required,description:要获取内容的URL地址"`
	Format  string `json:"format,omitempty" jsonschema:"description:返回内容的格式"`
	Timeout int    `json:"timeout,omitempty" jsonschema:"description:请求超时时间"`
}

type mockFetchResponse struct {
	Content      string `json:"content" jsonschema:"description:响应内容"`
	StatusCode   int    `json:"status_code,omitempty" jsonschema:"description:HTTP状态码"`
	ContentType  string `json:"content_type,omitempty" jsonschema:"description:原始内容类型"`
	IsTruncated  bool   `json:"is_truncated,omitempty" jsonschema:"description:内容是否被截断"`
	ErrorMessage string `json:"error_message,omitempty" jsonschema:"description:错误信息"`
}

func mockFetch(ctx context.Context, req *mockFetchRequest) (*mockFetchResponse, error) {
	if req.URL == "" {
		return &mockFetchResponse{ErrorMessage: "URL is required"}, nil
	}
	return &mockFetchResponse{
		Content:     "example content",
		StatusCode:  200,
		ContentType: "text/plain",
	}, nil
}

func lastMessageContent(messages []*schema.Message) string {
	if len(messages) == 0 {
		return ""
	}
	return messages[len(messages)-1].Content
}

type mockEventRecorder struct {
	mu     sync.Mutex
	events []Event
}

func newMockEventRecorder() *mockEventRecorder {
	return &mockEventRecorder{}
}

func (r *mockEventRecorder) Record(event Event) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, event)
}

func (r *mockEventRecorder) Events() []Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]Event, len(r.events))
	copy(out, r.events)
	return out
}

func (r *mockEventRecorder) Last(eventType EventType) *Event {
	events := r.Events()
	for i := len(events) - 1; i >= 0; i-- {
		if events[i].Type == eventType {
			return &events[i]
		}
	}
	return nil
}

func (r *mockEventRecorder) Count(eventType EventType) int {
	events := r.Events()
	count := 0
	for _, event := range events {
		if event.Type == eventType {
			count++
		}
	}
	return count
}

func (r *mockEventRecorder) Deltas(eventType EventType) []string {
	events := r.Events()
	out := make([]string, 0, len(events))
	for _, event := range events {
		if event.Type == eventType {
			out = append(out, event.Delta)
		}
	}
	return out
}

func (r *mockEventRecorder) LastMessageEndByRole(role RoleType) *Event {
	events := r.Events()
	for i := len(events) - 1; i >= 0; i-- {
		if events[i].Type == EventMessageEnd && events[i].Role == role {
			return &events[i]
		}
	}
	return nil
}

func (r *mockEventRecorder) RequireTypes(t *testing.T, expected ...EventType) {
	t.Helper()
	events := r.Events()
	actual := make([]EventType, 0, len(events))
	for _, event := range events {
		actual = append(actual, event.Type)
	}
	if len(actual) != len(expected) {
		t.Fatalf("event types = %v, want %v", actual, expected)
	}
	for i := range expected {
		if actual[i] != expected[i] {
			t.Fatalf("event types = %v, want %v", actual, expected)
		}
	}
}

func messageContents(messages []Message) []string {
	out := make([]string, 0, len(messages))
	for _, message := range messages {
		out = append(out, message.Content)
	}
	return out
}

func userMessageContents(messages []*schema.Message) []string {
	out := make([]string, 0, len(messages))
	for _, message := range messages {
		if message.Role == schema.User {
			out = append(out, message.Content)
		}
	}
	return out
}

func firstMessageContentByRole(messages []*schema.Message, role schema.RoleType) string {
	for _, message := range messages {
		if message.Role == role {
			return message.Content
		}
	}
	return ""
}

func lastMessageContentByRole(messages []*schema.Message, role schema.RoleType) string {
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == role {
			return messages[i].Content
		}
	}
	return ""
}

func historyHasRole(messages []*schema.Message, role schema.RoleType) bool {
	for _, message := range messages {
		if message.Role == role {
			return true
		}
	}
	return false
}

func inputHasToolResult(messages []*schema.Message, callID string) bool {
	for _, message := range messages {
		if message.Role == schema.Tool && message.ToolCallID == callID {
			return true
		}
	}
	return false
}
