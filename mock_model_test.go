package agentkit

import (
	"context"
	"errors"
	"strings"
	"testing"

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
