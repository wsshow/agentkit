# AgentKit

[中文文档](README_zh.md)

A lightweight, event-stream-driven Agent toolkit built on top of [CloudWeGo Eino ADK](https://github.com/cloudwego/eino).

Inspired by [pi-agent-core](https://github.com/badlogic/pi-mono/tree/main/packages/agent), AgentKit brings event streaming, message queuing, and human-in-the-loop (HITL) capabilities to the Go + Eino ecosystem.

## Features

- **Event-stream architecture** — Subscribe to fine-grained events (message deltas, tool calls, errors, etc.)
- **Steering & follow-up queues** — Inject messages mid-execution to redirect the agent or append follow-up tasks
- **Human-in-the-loop (HITL)** — Interrupt agent execution and resume with user-provided data
- **Streaming support** — Real-time token-by-token output via Eino ADK streaming
- **Reasoning model support** — First-class support for thinking/reasoning models (DeepSeek-R1, o1, etc.) with streaming reasoning output
- **Tool integration** — Plug in any Eino-compatible tool with automatic tool-call handling
- **Type aliases** — Use `agentkit.ChatModel`, `agentkit.Tool`, `agentkit.ToolCall`, etc. without importing eino packages directly

## Installation

```bash
go get github.com/wsshow/agentkit
```

## Quick Start

```go
package main

import (
	"context"
	"fmt"
	"log"

	"github.com/cloudwego/eino-ext/components/model/openai"
	"github.com/wsshow/agentkit"
)

func main() {
	ctx := context.Background()

	chatModel, _ := openai.NewChatModel(ctx, &openai.ChatModelConfig{
		APIKey:  "your-api-key",
		BaseURL: "https://api.openai.com/v1",
		Model:   "gpt-4o",
	})

	agent, err := agentkit.New(ctx, &agentkit.Config{
		Name:         "assistant",
		SystemPrompt: "You are a helpful assistant.",
		Model:        chatModel,
	})
	if err != nil {
		log.Fatalln(err)
	}
	defer agent.Close()

	agent.Subscribe(func(e agentkit.Event) {
		switch e.Type {
		case agentkit.EventReasoningDelta:
			fmt.Print(e.Delta) // reasoning/thinking stream (for reasoning models)
		case agentkit.EventMessageDelta:
			fmt.Print(e.Delta)
		case agentkit.EventMessageEnd:
			fmt.Println()
		case agentkit.EventError:
			fmt.Printf("Error: %v\n", e.Error)
		}
	})

	if err := agent.Prompt(ctx, "Hello!"); err != nil {
		log.Fatalln(err)
	}
}
```

## Event Types

| Event                 | Description                                                                        |
| --------------------- | ---------------------------------------------------------------------------------- |
| `EventAgentStart`     | Agent begins processing                                                            |
| `EventTurnStart`      | New turn starts (one LLM call + tool execution cycle)                              |
| `EventMessageStart`   | Message begins (streaming or non-streaming)                                        |
| `EventReasoningDelta` | Reasoning/thinking stream delta (`Event.Delta`), for reasoning models              |
| `EventMessageDelta`   | Incremental streaming text (`Event.Delta`)                                         |
| `EventMessageEnd`     | Message complete (`Event.Content`, `Event.ReasoningContent`, `Event.ResponseMeta`) |
| `EventToolStart`      | Tool call requested (`Event.ToolCalls`)                                            |
| `EventToolUpdate`     | Tool execution progress update (`Event.Content`)                                   |
| `EventToolEnd`        | Tool call result returned (`Event.Content`)                                        |
| `EventTurnEnd`        | Turn complete                                                                      |
| `EventTransfer`       | Agent transfer (multi-agent)                                                       |
| `EventInterrupted`    | HITL interrupt (`Event.Interrupt`)                                                 |
| `EventAgentEnd`       | Agent processing complete                                                          |
| `EventError`          | Error occurred (`Event.Error`)                                                     |

### Event Struct

```go
type Event struct {
    Type             EventType
    Agent            string           // source agent name
    Content          string           // full text (message_end / tool_end)
    Delta            string           // streaming delta (message_delta / reasoning_delta)
    ReasoningContent string           // full reasoning content (message_end, reasoning models only)
    ResponseMeta     *ResponseMeta    // token usage, finish reason (message_end)
    ToolCalls        []ToolCall       // tool call list (tool_start)
    Interrupt        []InterruptPoint // interrupt points (interrupted)
    Error            error            // error details (error)
}
```

## API Reference

### Creating an Agent

```go
agent, err := agentkit.New(ctx, &agentkit.Config{
    Name:            "my-agent",
    Description:     "Agent description",
    SystemPrompt:    "System instructions",
    Model:           chatModel,                          // agentkit.ChatModel
    Tools:           []agentkit.Tool{myTool},             // optional
    Middlewares:      []adk.AgentMiddleware{},             // agent-level hooks (optional)
    ModelMiddlewares: []adk.ChatModelAgentMiddleware{},    // model-level hooks (optional)
    MaxIterations:   20,                                  // max LLM call cycles (default: 20)
    CheckPointStore: store,                               // checkpoint store (optional)
})
defer agent.Close()
```

### Core Methods

```go
// Send user input and drive agent execution (blocking, thread-safe)
err := agent.Prompt(ctx, "user message")

// Resume from current state without new message (e.g. retry after error)
err := agent.Continue(ctx)

// Resume from a HITL interrupt
err := agent.Resume(ctx, map[string]any{"interruptID": data})

// Subscribe to events, returns unsubscribe function
unsubscribe := agent.Subscribe(func(e agentkit.Event) { ... })

// Cancel current execution and wait for completion
agent.Abort()

// Reset agent state (waits for completion, then clears history and queues)
agent.Reset()

// Get full conversation history (eino schema.Message, for debugging/persistence)
history := agent.History()

// Get agent state (message records, streaming status)
state := agent.State()

// Close agent and release resources (implements io.Closer)
agent.Close()
```

> `Prompt`, `Continue`, and `Resume` are mutually exclusive — calling one while another is running returns an error.

### Steering & Follow-Up

```go
// Inject a steering message during execution (checked after each tool result)
agent.Steer("Please focus on topic X instead")

// Append a follow-up message (processed after current task completes)
agent.FollowUp("Also check Y")

// Configure queue processing mode
agent.SetSteeringMode(agentkit.QueueModeAll)        // process all queued messages at once
agent.SetFollowUpMode(agentkit.QueueModeOneAtATime)  // process one at a time (default)

// Clear queues
agent.ClearSteeringQueue()
agent.ClearFollowUpQueue()
agent.ClearAllQueues()
```

### HITL (Human-in-the-Loop)

```go
// In a tool: trigger interrupt
return "", agentkit.Interrupt(ctx, "Need user confirmation")

// With state preservation
return "", agentkit.StatefulInterrupt(ctx, "Confirm?", myState)

// In a resumed tool: check interrupt state
wasInterrupted, hasState, state := agentkit.GetInterruptState[MyState](ctx)

// Get resume data from user
isTarget, hasData, data := agentkit.GetResumeContext[bool](ctx)
```

### Tool Progress Updates

Tools can emit progress events during execution:

```go
func myTool(ctx context.Context, input string) (string, error) {
    agentkit.EmitToolUpdate(ctx, "Processing step 1...")
    // ... do work ...
    agentkit.EmitToolUpdate(ctx, "Processing step 2...")
    return "result", nil
}
```

### Type Aliases

AgentKit provides type aliases so consumers don't need to import eino packages directly:

| Alias          | Eino Type             |
| -------------- | --------------------- |
| `ChatModel`    | `model.BaseChatModel` |
| `Tool`         | `tool.BaseTool`       |
| `ToolCall`     | `schema.ToolCall`     |
| `ResponseMeta` | `schema.ResponseMeta` |
| `TokenUsage`   | `schema.TokenUsage`   |

## Examples

See the [examples](examples/) directory:

- **[simple](examples/simple/)** — Minimal multi-turn conversation (~60 lines)
- **[full](examples/full/)** — Comprehensive 7-scenario demo (tools, history, reset, follow-up, steer, HITL, state inspection)

## License

See [LICENSE](LICENSE) for details.
