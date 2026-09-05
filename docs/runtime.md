# Runtime and Events

[中文](zh/runtime.md) · [Documentation index](README.md)

The Agent runtime provides a small blocking API, a request-scoped event stream, global observers, explicit lifecycle control, and resumable HITL. This guide is the reference for choosing a run method and handling its state safely.

## Agent Configuration

```go
agent, err := agentkit.New(ctx, &agentkit.Config{
	Name:                "my-agent",
	Description:         "Agent description",
	SystemPrompt:        "System instructions",
	Model:               chatModel,
	Tools:               []agentkit.Tool{myTool},
	ToolPolicy:          &agentkit.ToolPolicy{Sequential: true},
	Handlers:            []agentkit.ChatModelAgentMiddleware{myHandler},
	ModelRetryConfig:    &agentkit.ModelRetryConfig{MaxRetries: 2},
	ModelFailoverConfig: failoverConfig,
	PersistenceTimeout:  30 * time.Second,
	MaxIterations:       20,
})
if err != nil {
	log.Fatal(err)
}
defer agent.Close()
```

Only `Name` and `Model` are normally needed. Optional capabilities live in focused configurations documented elsewhere. Custom handlers are guarded: hook, wrapped endpoint, and returned-stream panics become errors wrapping `ErrMiddlewarePanic`.

## Choose a Run Method

```go
err := agent.Prompt(ctx, "user message")

result, err := agent.Ask(ctx, "user message")
fmt.Println(result.Text, result.Usage)

stream, err := agent.Stream(ctx, "user message")
for event := range stream.Events() {
	// Render text, reasoning, tools, or progress.
}
result, err = stream.Wait()
```

- `Prompt` blocks and returns only an error. Use it when state or subscribers consume the output.
- `Ask` blocks and returns `RunResult`, including final text/message, messages added in the run, accumulated usage, tool calls, and pending interrupts.
- `Stream` returns a request-scoped event stream. It reserves the Agent before returning and provides `Cancel`, `Done`, `Wait`, `WaitContext`, and `Close`.

`WaitContext` bounds waiting without canceling the underlying run. A slow event consumer does not block the Agent because request streams use an internal queue. `StreamParts` is the multimodal equivalent.

Resume from existing state without a new user message with `Continue` or `ContinueWithResult`. Resume a HITL checkpoint with `Resume` or `ResumeWithResult`.

## Mutual Exclusion and Lifecycle

`Prompt`, `Send`, `Continue`, and `Resume` are mutually exclusive on one Agent. A concurrent attempt returns `ErrAgentRunning`. A `GoalRunner` reserves the same execution lane for its whole goal cycle, including evaluation between work steps. After a HITL interrupt, fresh runs return `ErrResumeRequired` until the checkpoint is resumed or explicitly cleared, preventing unfinished tool work from being silently abandoned.

```go
agent.Cancel()                    // non-blocking; safe inside subscribers
agent.Abort()                     // cancel and wait; call outside subscribers
err := agent.AbortContext(stopCtx) // cancel, then bound the wait

agent.Reset()                     // wait, then clear history and queues
err = agent.CloseContext(stopCtx) // prevent new runs; bound run and MCP cleanup
```

`Cancel`, `AbortContext`, and `CloseContext` also stop a `GoalRunner` currently owning the Agent. `AbortContext` always sends cancellation first. If custom model, evaluator, or tool code ignores its context, it may return the shutdown context error while that code is still unwinding; the Agent remains reserved until the run exits. `CloseContext` prevents new runs immediately and continues one-time MCP cleanup in the background after a wait deadline.

## Request-Scoped Configuration

Keep one shared Agent stable while customizing an individual request:

```go
runCtx := agentkit.WithRunConfig(ctx, agentkit.RunConfig{
	ModelOptions: []agentkit.ModelOption{
		model.WithTemperature(0.2),
		model.WithMaxTokens(2_000),
	},
	Values: map[string]any{
		"user_name": "Alice",
		"request_id": "req-42",
	},
})

result, err := agent.Ask(runCtx, "Summarize this")
```

Run values fill `{user_name}` placeholders in `SystemPrompt`. Tools and middleware can read a typed value with `RunValue[T]`, obtain a copy with `RunValues`, and update values visible later in the same underlying run with `SetRunValue`. `ToolOptions` is the request-level escape hatch for custom tools. `WithRunConfig` copies its input containers.

Retry and failover callbacks are panic-isolated. Control callbacks that return errors wrap `ErrModelPolicyPanic`; advisory callbacks fall back safely and emit `EventError`.

## Event Reference

| Event | Meaning and useful fields |
| --- | --- |
| `EventAgentStart` | Agent execution started |
| `EventTurnStart` | A new turn started before the next model request |
| `EventMessageStart` | Message started; inspect `Role` |
| `EventReasoningDelta` | Incremental reasoning in `Delta` |
| `EventMessageDelta` | Incremental assistant text in `Delta` |
| `EventMessageEnd` | Complete message in `Content`, plus `ResponseMeta` |
| `EventToolStart` | Requested calls in `ToolCalls` |
| `EventToolUpdate` | Progress in `Content`, identified by `ToolCallID` |
| `EventToolEnd` | Result, name, arguments, and ID |
| `EventTurnEnd` | Assistant message and tool results completed |
| `EventTransfer` | Multi-agent transfer |
| `EventInterrupted` | HITL points in `Interrupt` |
| `EventCompactionStart` / `EventCompactionEnd` | Compaction counts in `Compaction` |
| `EventGoalUpdate` | Committed goal snapshot in `Goal` |
| `EventDelegationStart` / `EventDelegationEnd` | Bounded subagent lifecycle in `Delegation`; terminal failure in `Error` |
| `EventAgentEnd` | Agent execution ended |
| `EventError` | Non-fatal or terminal error in `Error` |

`agent.Subscribe` registers a global observer and returns an unsubscribe function. Events from a session-bound Agent carry its stable `SessionID`. Event containers and built-in mutable fields are copied per callback; JSON-like maps, slices, and byte data inside `InterruptPoint.Info` are copied recursively as well. Treat custom opaque pointer values stored in `Info` as immutable. Global callbacks run synchronously and should return quickly. A callback panic is isolated; other subscribers receive an `EventError` wrapping `ErrSubscriberPanic`. For a combined feed across conversations, use `SessionManager.Subscribe`.

Tools can report progress without knowing about subscribers:

```go
func myTool(ctx context.Context, input string) (string, error) {
	agentkit.EmitToolUpdate(ctx, "Processing step 1...")
	return "result", nil
}
```

## Steering and Follow-Up Queues

```go
agent.Steer("Focus on topic X instead")
agent.FollowUp("Also check Y")

agent.SetSteeringMode(agentkit.QueueModeAll)
agent.SetFollowUpMode(agentkit.QueueModeOneAtATime)

agent.ClearSteeringQueue()
agent.ClearFollowUpQueue()
agent.ClearAllQueues()
```

Steering is checked after the current tool batch and redirects active work. Follow-up messages run after the current task. Follow-up defaults to one item at a time.

## Human in the Loop

A tool can stop execution and persist an interrupt:

```go
return "", agentkit.Interrupt(ctx, "Need user confirmation")

return "", agentkit.StatefulInterrupt(ctx, "Confirm?", myState)
```

When resumed, the tool can recover its state and the submitted user value:

```go
wasInterrupted, hasState, state := agentkit.GetInterruptState[MyState](ctx)
isTarget, hasData, data := agentkit.GetResumeContext[bool](ctx)
```

Use `PendingInterrupts` to present outstanding IDs and `ClearCheckpoint` only when the application intentionally abandons them. Clearing adds an explicit synthetic tool result for every unfinished call before rotating the checkpoint, so later prompts and forks retain a provider-valid conversation instead of an orphaned assistant tool call. For restart durability, configure [sessions and checkpoints](persistence.md).

## Multimodal Input

```go
result, err := agent.AskParts(ctx,
	agentkit.Text("Describe this image"),
	agentkit.ImageURL(imageURL, agentkit.ImageDetailHigh),
)
```

`Send` is the error-only equivalent. Constructors include `Text`, `ImageURL`, `ImageBase64`, `AudioURL`, `AudioBase64`, `VideoURL`, `VideoBase64`, `FileURL`, and `FileBase64`. Whether a part is accepted ultimately depends on the configured model provider.

## Eino Type Aliases

AgentKit aliases common Eino types so applications usually need one package import: `ChatModel`, `Tool`, `ToolMiddleware`, `ToolInput`, `ToolOutput`, `ToolCall`, `ResponseMeta`, `TokenUsage`, `ContentPart`, and `ImageURLDetail`.

## Related Guides

- [Sessions and persistence](persistence.md)
- [Tool management](tools.md)
- [Subagents](subagents.md)
- [Testing](testing.md)
