# Testing Agents

[中文](zh/testing.md) · [Documentation index](README.md)

AgentKit's mocks exercise the real Agent runtime without network calls. They are useful for testing prompts, streams, tool sequencing, error recovery, HITL, sessions, and goals with deterministic model responses.

## Mock a Text or Streaming Response

```go
func TestGreeting(t *testing.T) {
	ctx := context.Background()
	model := agentkit.NewMockChatModel(
		agentkit.MockModelStream("hel", "lo"),
	)

	agent, err := agentkit.New(ctx, &agentkit.Config{
		Name:  "test-agent",
		Model: model,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer agent.Close()

	result, err := agent.Ask(ctx, "say hello")
	if err != nil {
		t.Fatal(err)
	}
	if result.Text != "hello" {
		t.Fatalf("unexpected result: %q", result.Text)
	}

	calls := model.Calls()
	if calls[0].Input[len(calls[0].Input)-1].Content != "say hello" {
		t.Fatal("unexpected input")
	}
}
```

Common response helpers are:

```go
agentkit.MockModelText("done")
agentkit.MockModelStream("part 1", "part 2")
agentkit.MockModelError(err)
agentkit.MockModelStreamError(err, "partial")
```

Each configured response is consumed in order. `Calls` returns recorded requests, which lets a test assert the actual model-facing history and options rather than only the final text.

## Mock Real Tool Functions

```go
type WeatherInput struct {
	City string `json:"city"`
}

type WeatherOutput struct {
	City      string `json:"city"`
	Condition string `json:"condition"`
}

weather := agentkit.MustMockTool(
	"get_weather",
	"query weather",
	func(ctx context.Context, input *WeatherInput) (*WeatherOutput, error) {
		return &WeatherOutput{City: input.City, Condition: "sunny"}, nil
	},
)

beijing := weather.Call("beijing_weather", &WeatherInput{City: "Beijing"})
shanghai := weather.Call("shanghai_weather", &WeatherInput{City: "Shanghai"})

model := agentkit.NewMockChatModel(
	agentkit.MockModelCalls(beijing),
	agentkit.MockModelCallsAfter(beijing, shanghai),
	agentkit.MockModelRespondsAfter(shanghai, func(out *WeatherOutput) agentkit.MockModelResponse {
		return agentkit.MockModelText(out.City + " is " + out.Condition)
	}),
)

agent, err := agentkit.New(ctx, &agentkit.Config{
	Name:  "test-agent",
	Model: model,
	Tools: agentkit.MockTools(weather),
})
```

`MockModelCallsAfter` verifies that an earlier result is present before returning the next call. `MockModelRespondsAfter` decodes the result into its typed output before building the final response. These helpers catch broken tool result ordering instead of accepting any prompt.

When one model response should request several tools, group them:

```go
model := agentkit.NewMockChatModel(
	agentkit.MockModelCalls(beijing, shanghai),
	agentkit.MockModelTextAfterAll("done", beijing, shanghai),
)
```

## What to Assert

For a stable integration test, assert the contract visible to the application:

- final text, token usage, tool calls, or pending interrupts from `RunResult`;
- event order and selected fields from a request-scoped stream;
- model inputs captured by `Calls`;
- persisted session/goal state after rebuilding the Agent and store;
- error identity with `errors.Is` or `errors.As`; and
- that cancellation and wait deadlines have distinct effects.

Avoid asserting internal implementation details that are not part of the public API. Use a non-nil context in every test; `context.Background()` is normally the correct root for a test case.

## Testing Persistence and Recovery

Use `NewMemorySessionStore` for fast behavior tests. Use a temporary directory with `NewFileSessionStore(t.TempDir())` when the test must cover serialization, process-style reconstruction, checkpoints, goal leases, or reduced tool results.

To simulate a restart, close the first Agent, construct a new store and Agent from the same directory and session ID, then call the public restore or resume method. This validates the same boundary production code relies on.

## Related Guides

- [Runtime and events](runtime.md)
- [Sessions and persistence](persistence.md)
- [Durable goals](goals.md)
