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
- **Multimodal input** — Send text, images, audio, video, and files via `Send()` with ergonomic constructors
- **Session persistence** — Automatically save and restore complete conversations with built-in concurrent memory and atomic file stores
- **Automatic context compaction** — Summarize contexts over token or message limits while preserving full conversation history
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
| `EventTurnStart`      | New turn starts before the next model request                                      |
| `EventMessageStart`   | Message begins (`Event.Role` identifies user, assistant, or tool)                  |
| `EventReasoningDelta` | Reasoning/thinking stream delta (`Event.Delta`), for reasoning models              |
| `EventMessageDelta`   | Incremental streaming text (`Event.Delta`)                                         |
| `EventMessageEnd`     | Message complete (`Event.Role`, `Event.Content`, `Event.ResponseMeta`)             |
| `EventToolStart`      | Tool call requested (`Event.ToolCalls`)                                            |
| `EventToolUpdate`     | Tool execution progress update (`Event.ToolCallID`, `Event.Content`)               |
| `EventToolEnd`        | Tool call result returned (`Event.ToolCallID`, `Event.ToolName`, `Event.Content`)  |
| `EventTurnEnd`        | Turn complete after the assistant message and tool results                         |
| `EventTransfer`       | Agent transfer (multi-agent)                                                       |
| `EventInterrupted`    | HITL interrupt (`Event.Interrupt`)                                                 |
| `EventCompactionStart` | Automatic context compaction started (`Event.Compaction.MessagesBefore`)          |
| `EventCompactionEnd`  | Automatic context compaction completed (`Event.Compaction`)                       |
| `EventAgentEnd`       | Agent processing complete                                                          |
| `EventError`          | Error occurred (`Event.Error`)                                                     |

### Event Struct

```go
type Event struct {
    Type             EventType
    Agent            string           // source agent name
    Role             RoleType         // message role (message_start / message_end)
    Content          string           // full text (message_end / tool_end)
    Delta            string           // streaming delta (message_delta / reasoning_delta)
    ReasoningContent string           // full reasoning content (message_end, reasoning models only)
    ResponseMeta     *ResponseMeta    // token usage, finish reason (message_end)
    ToolCalls        []ToolCall       // tool call list (tool_start)
    ToolCallID       string           // tool call ID (tool_update / tool_end)
    ToolName         string           // tool name (tool_update / tool_end)
    ToolArguments    string           // tool arguments (tool_update / tool_end)
    Interrupt        []InterruptPoint // interrupt points (interrupted)
    Compaction       *CompactionInfo  // context message counts before/after compaction
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
    Handlers:         []agentkit.ChatModelAgentMiddleware{myHandler}, // optional
    ModelRetryConfig: &agentkit.ModelRetryConfig{MaxRetries: 2},      // optional
    ModelFailoverConfig: failoverConfig,                              // optional
    MaxIterations:   20,                                  // max LLM call cycles (default: 20)
    CheckPointStore: store,                               // checkpoint store (optional)
    Session: &agentkit.SessionConfig{                     // automatic restore/save (optional)
        ID: "user-123",
        Store: sessionStore,
    },
    Compaction: &agentkit.CompactionConfig{               // automatic context compaction (optional)
        MaxTokens: 80_000,
        KeepRecentTurns: 2,
    },
})
defer agent.Close()
```

For manual history restoration, use `History: savedHistory` instead of `Session`; the two options are mutually exclusive.

### Core Methods

```go
// Send user text input and drive agent execution (blocking, thread-safe)
err := agent.Prompt(ctx, "user message")

// Send multimodal input (text + images, audio, video, files)
err := agent.Send(ctx,
    agentkit.Text("What is in this image?"),
    agentkit.ImageURL("https://example.com/cat.jpg"),
)

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

// Get full conversation history for debugging or persistence (returns a copy)
history := agent.History()

// Get the context actually sent to the model (same as History before compaction)
contextHistory := agent.ContextHistory()

// Replace full conversation history and sync display state
agent.SetHistory(history)

// Get a session snapshot; Prompt/Send/Continue/Resume save automatically
session := agent.Session()

// Save immediately after a manual change such as SetHistory
err := agent.SaveSession(ctx)

// Get agent state (message records, streaming status)
state := agent.State()

// Close agent and release resources (implements io.Closer)
agent.Close()
```

> `Prompt`, `Continue`, and `Resume` are mutually exclusive — calling one while another is running returns an error.

### Session Management

Configure a session ID and store. `New` restores an existing conversation, and every run is saved automatically—even when the model fails or the run is canceled:

```go
store, err := agentkit.NewFileSessionStore("./data/sessions")
if err != nil {
    log.Fatal(err)
}

agent, err := agentkit.New(ctx, &agentkit.Config{
    Name:  "assistant",
    Model: chatModel,
    Session: &agentkit.SessionConfig{
        ID:    "user-123",
        Store: store,
    },
})
```

The file store uses safe hashed file names and atomic replacement, preventing path traversal through session IDs and half-written JSON after a crash. Manage sessions directly through the store:

```go
sessions, err := store.List(ctx)
saved, err := store.Load(ctx, "user-123")
err = store.Delete(ctx, "user-123") // deleting a missing session also succeeds
```

Use `agentkit.NewMemorySessionStore()` for tests and single-process services. Implement `agentkit.SessionStore` for a database backend. `History` and `Session` cannot be configured together, so the restore source is always unambiguous. Only one Agent should write a given session ID at a time: the built-in stores are concurrency-safe, but they do not merge divergent conversations.

### Automatic Context Compaction

Enable `Compaction` to summarize context after a configured limit while preserving the most recent user turns verbatim:

```go
agent, err := agentkit.New(ctx, &agentkit.Config{
    Name:  "assistant",
    Model: chatModel,
    Compaction: &agentkit.CompactionConfig{
        MaxTokens:       80_000, // keep below the model's context window
        MaxMessages:     100,    // optional; either limit can trigger
        KeepRecentTurns: 2,      // default: 1
        Model:           summaryModel, // optional; defaults to the main model
    },
})
```

The two history views have distinct responsibilities:

- `History()` always returns the full, unabridged conversation for UI, auditing, and export.
- `ContextHistory()` returns the compacted context actually sent to the model.
- With `Session` configured, both are persisted so a restart does not accidentally restore the full history into the model context.

With no explicit limit, compaction starts above the estimated `DefaultCompactionMaxTokens` (100,000). Summary errors are returned normally and never replace the original context. Subscribe to `EventCompactionStart` and `EventCompactionEnd` to show progress.

### Integration Tests

Use `MockChatModel` to run agents without calling a real model:

```go
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

if err := agent.Prompt(ctx, "say hello"); err != nil {
    t.Fatal(err)
}

calls := model.Calls()
if calls[0].Input[len(calls[0].Input)-1].Content != "say hello" {
    t.Fatal("unexpected input")
}
```

Common response helpers:

```go
agentkit.MockModelText("done")
agentkit.MockModelStream("part 1", "part 2")
agentkit.MockModelError(err)
agentkit.MockModelStreamError(err, "partial")
```

Tool calls can execute real functions:

```go
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

Use `MockModelCalls` when one model response calls multiple tools:

```go
beijing := weather.Call("beijing_weather", &WeatherInput{City: "Beijing"})
shanghai := weather.Call("shanghai_weather", &WeatherInput{City: "Shanghai"})

model := agentkit.NewMockChatModel(
    agentkit.MockModelCalls(beijing, shanghai),
    agentkit.MockModelTextAfterAll("done", beijing, shanghai),
)
```

### Steering & Follow-Up

```go
// Inject a steering message during execution (checked after the current tool batch)
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

### Multimodal Input

`Send` accepts variadic `ContentPart` values built with constructor functions:

```go
// Text + image
agent.Send(ctx,
    agentkit.Text("What is in this image?"),
    agentkit.ImageURL("https://example.com/cat.jpg"),
)

// Image with quality control
agent.Send(ctx,
    agentkit.Text("Describe in detail"),
    agentkit.ImageURL("https://example.com/photo.jpg", agentkit.ImageDetailHigh),
)

// Base64 encoded image
agent.Send(ctx,
    agentkit.Text("Identify this"),
    agentkit.ImageBase64(base64Data, "image/png"),
)

// Audio / Video / File
agent.Send(ctx, agentkit.Text("Transcribe"), agentkit.AudioURL("https://example.com/speech.mp3"))
agent.Send(ctx, agentkit.Text("Summarize"), agentkit.VideoURL("https://example.com/clip.mp4"))
agent.Send(ctx, agentkit.Text("Analyze"), agentkit.FileURL("https://example.com/report.pdf"))
```

Available constructors:

| Constructor                          | Description                          |
| ------------------------------------ | ------------------------------------ |
| `Text(s)`                            | Text content                         |
| `ImageURL(url, detail...)`           | Image from URL (optional quality)    |
| `ImageBase64(data, mime, detail...)` | Image from Base64                    |
| `AudioURL(url)`                      | Audio from URL                       |
| `AudioBase64(data, mime)`            | Audio from Base64                    |
| `VideoURL(url)`                      | Video from URL                       |
| `VideoBase64(data, mime)`            | Video from Base64                    |
| `FileURL(url)`                       | File from URL                        |
| `FileBase64(data, mime, name...)`    | File from Base64 (optional filename) |

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

| Alias            | Eino Type                 |
| ---------------- | ------------------------- |
| `ChatModel`      | `model.BaseChatModel`     |
| `Tool`           | `tool.BaseTool`           |
| `ToolCall`       | `schema.ToolCall`         |
| `ResponseMeta`   | `schema.ResponseMeta`     |
| `TokenUsage`     | `schema.TokenUsage`       |
| `ContentPart`    | `schema.MessageInputPart` |
| `ImageURLDetail` | `schema.ImageURLDetail`   |

## Examples

See the [examples](examples/) directory:

- **[simple](examples/simple/)** — Minimal multi-turn conversation (~60 lines)
- **[tools](examples/tools/)** — Tool calls with progress events
- **[history](examples/history/)** — Export and restore conversation history
- **[session](examples/session/)** — Automatically persist and restore sessions across processes
- **[compaction](examples/compaction/)** — Automatically compact long conversation contexts
- **[queues](examples/queues/)** — Follow-up and steering queues
- **[hitl](examples/hitl/)** — Human-in-the-loop interrupt and resume
- **[multimodal](examples/multimodal/)** — Text and image inputs

## License

See [LICENSE](LICENSE) for details.
