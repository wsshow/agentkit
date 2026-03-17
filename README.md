# AgentKit

[中文文档](README_zh.md)

A lightweight, event-stream-driven Agent toolkit built on top of [CloudWeGo Eino ADK](https://github.com/cloudwego/eino).

Inspired by [pi-agent-core](https://github.com/badlogic/pi-mono/tree/main/packages/agent), AgentKit brings event streaming, message queuing, and human-in-the-loop (HITL) capabilities to the Go + Eino ecosystem.

## Features

- **Event-stream architecture** — Subscribe to fine-grained events (message deltas, tool calls, errors, etc.)
- **Steering & follow-up queues** — Inject messages mid-execution to redirect the agent or append follow-up tasks
- **Human-in-the-loop (HITL)** — Interrupt agent execution and resume with user-provided data
- **Streaming support** — Real-time token-by-token output via Eino ADK streaming
- **Tool integration** — Plug in any Eino-compatible tool with automatic tool-call handling

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
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/components/tool/utils"
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

	agent.Subscribe(func(e agentkit.Event) {
		switch e.Type {
		case agentkit.EventMessageDelta:
			fmt.Print(e.Delta)
		case agentkit.EventToolStart:
			fmt.Printf("\nCalling tool: %s\n", e.ToolCalls[0].Function.Name)
		case agentkit.EventError:
			fmt.Printf("Error: %v\n", e.Error)
		}
	})

	err = agent.Prompt(ctx, "Hello!")
	if err != nil {
		log.Fatalln(err)
	}
}
```

## Event Types

| Event               | Description                                             |
| ------------------- | ------------------------------------------------------- |
| `EventAgentStart`   | Agent begins processing                                 |
| `EventTurnStart`    | New turn starts (one LLM call + tool execution cycle)   |
| `EventMessageStart` | Message begins (streaming or non-streaming)             |
| `EventMessageDelta` | Incremental streaming text (available in `Event.Delta`) |
| `EventMessageEnd`   | Message complete (full content in `Event.Content`)      |
| `EventToolStart`    | Tool call requested (details in `Event.ToolCalls`)      |
| `EventToolUpdate`   | Tool execution progress update                          |
| `EventToolEnd`      | Tool call result returned                               |
| `EventTurnEnd`      | Turn complete                                           |
| `EventTransfer`     | Agent transfer (multi-agent)                            |
| `EventInterrupted`  | HITL interrupt (details in `Event.Interrupt`)           |
| `EventAgentEnd`     | Agent processing complete                               |
| `EventError`        | Error occurred (details in `Event.Error`)               |

## API Reference

### Creating an Agent

```go
agent, err := agentkit.New(ctx, &agentkit.Config{
	Name:          "my-agent",
	Description:   "Agent description",
	SystemPrompt:  "System instructions",
	Model:         chatModel,
	Tools:         []agentkit.Tool{},
	MaxIterations: 20,                 // max LLM call cycles (default: 20)
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
agent.SetSteeringMode(agentkit.QueueModeAll)   // process all queued messages at once
agent.SetFollowUpMode(agentkit.QueueModeOneAtATime) // process one at a time (default)
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

## License

See [LICENSE](LICENSE) for details.
