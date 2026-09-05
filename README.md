# AgentKit

[![CI](https://github.com/wsshow/agentkit/actions/workflows/ci.yml/badge.svg)](https://github.com/wsshow/agentkit/actions/workflows/ci.yml)

[中文文档](README_zh.md)

AgentKit is a lightweight, event-stream-driven Go library for building reliable agents on top of [CloudWeGo Eino ADK](https://github.com/cloudwego/eino). It keeps the first agent small, while providing sessions, durable goals, context compaction, skills, MCP, and tool governance when an application grows.

Inspired by [pi-agent-core](https://github.com/earendil-works/pi/tree/main/packages/agent), AgentKit focuses on a simpler public API and production-safe defaults.

## Why AgentKit

- **Easy to start** — create an Agent and call `Ask`; no graph or middleware wiring is required.
- **Easy to observe** — use request-scoped streams or global events for text, reasoning, tools, compaction, goals, interrupts, and errors.
- **Easy to keep running** — persist sessions, checkpoints, goals, and large tool results; reconnect by stable IDs after a client or process restart.
- **Safe by default** — concurrent-run protection, panic isolation, bounded cleanup, tool-call repair, result limits, and optimistic concurrency are built in.
- **Composable when needed** — add skills, MCP servers, tool search, reduction, retry/failover, HITL, and multimodal input independently.

## Installation

AgentKit requires Go 1.25.14 or later.

```bash
go get github.com/wsshow/agentkit@latest
```

## Five-Minute Start

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

	chatModel, err := openai.NewChatModel(ctx, &openai.ChatModelConfig{
		APIKey: "your-api-key",
		Model:  "gpt-4o",
	})
	if err != nil {
		log.Fatal(err)
	}

	agent, err := agentkit.New(ctx, &agentkit.Config{
		Name:         "assistant",
		SystemPrompt: "You are a helpful assistant.",
		Model:        chatModel,
	})
	if err != nil {
		log.Fatal(err)
	}
	defer agent.Close()

	result, err := agent.Ask(ctx, "Hello!")
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(result.Text)
}
```

`Ask` is the simplest blocking API. For real-time text and tool progress, use `Stream`:

```go
stream, err := agent.Stream(ctx, "Explain MCP")
if err != nil {
	log.Fatal(err)
}
defer stream.Close()

for event := range stream.Events() {
	if event.Type == agentkit.EventMessageDelta {
		fmt.Print(event.Delta)
	}
}
result, err := stream.Wait()
```

See [Runtime and events](docs/runtime.md) for the complete run API, lifecycle rules, HITL, queues, and multimodal input.

## Choose the Capabilities You Need

| Need | Start here |
| --- | --- |
| Run methods, events, cancellation, HITL, queues, multimodal input | [Runtime and events](docs/runtime.md) |
| Restore conversations and checkpoints after restart | [Sessions and persistence](docs/persistence.md) |
| Run a multi-step objective for hours or days and reconnect safely | [Durable goals](docs/goals.md) |
| Keep long conversations inside the model context window | [Context management](docs/context.md) |
| Load reusable `SKILL.md` instructions on demand | [Skills](docs/skills.md) |
| Connect stdio, SSE, or Streamable HTTP MCP servers | [MCP](docs/mcp.md) |
| Govern tools, repair calls, reduce large results, or search a catalog | [Tool management](docs/tools.md) |
| Test without a live model or external tools | [Testing](docs/testing.md) |

The [documentation index](docs/README.md) includes recommended reading paths and links between related topics.

## A Practical Production Baseline

Most stateful agents should begin with a durable session and automatic compaction. Enable result reduction when tools may return large payloads:

```go
store, err := agentkit.NewFileSessionStore("./data/agent")
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
	Compaction: &agentkit.CompactionConfig{
		MaxTokens:       80_000,
		KeepRecentTurns: 2,
	},
	ToolReduction: &agentkit.ToolReductionConfig{},
})
```

The file store is designed for a local single-process worker. Multi-replica services should implement the persistence interfaces with transactional database semantics; see [Sessions and persistence](docs/persistence.md) and [Durable goals](docs/goals.md).

## Built-In Tool Middleware Decisions

AgentKit includes the three capabilities that remove recurring application work without exposing Eino middleware plumbing:

- Dangling tool-call repair is always on because valid history is a correctness requirement.
- Large-result reduction is one opt-in zero-value configuration because it changes storage and model-visible content.
- On-demand tool search is opt-in because it is useful for large catalogs but adds an extra model decision for small ones.

See [Tool management](docs/tools.md) for defaults, ordering, and extension points.

## Examples

| Example | What it demonstrates |
| --- | --- |
| [simple](examples/simple/) | Minimal multi-turn conversation |
| [tools](examples/tools/) | Tool calls and progress events |
| [history](examples/history/) | Manual history export and restore |
| [session](examples/session/) | Automatic cross-process session restore |
| [goal](examples/goal/) | Durable objective execution and reconnect |
| [compaction](examples/compaction/) | Automatic context compaction |
| [skills](examples/skills/) | Local `SKILL.md` discovery and loading |
| [mcp](examples/mcp/) | Streamable HTTP MCP integration |
| [queues](examples/queues/) | Steering and follow-up queues |
| [hitl](examples/hitl/) | Human interrupt and resume |
| [multimodal](examples/multimodal/) | Text and image input |

## Project

- [Contributing](CONTRIBUTING.md)
- [Security policy](SECURITY.md)
- [License](LICENSE)
