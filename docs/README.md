# AgentKit Documentation

[中文索引](zh/README.md) · [Project README](../README.md)

This guide is organized by the problem an application needs to solve. You do not need to enable every subsystem: start with the runtime, then add only the capabilities your agent needs.

## Recommended Reading Paths

For a first agent:

1. Run the root [five-minute example](../README.md#five-minute-start).
2. Learn the run methods and event lifecycle in [Runtime and events](runtime.md).
3. Use [Testing](testing.md) to make the behavior deterministic.

For a production assistant:

1. Add [Sessions and persistence](persistence.md).
2. Add [Context management](context.md) before conversations grow large.
3. Review [Tool management](tools.md), especially timeouts and large results.

For work that may take hours or days:

1. Start with the production-assistant path.
2. Use [Durable goals](goals.md) for restart and reconnect semantics.
3. Use [MCP](mcp.md) or [Skills](skills.md) only where the task requires them.

For coordinated specialist work:

1. Define bounded, isolated specialists with [Subagents](subagents.md).
2. Give each specialist only the tools and MCP servers it needs.
3. Combine with [Durable goals](goals.md) when work must survive across step boundaries.

## Guides

| Guide | Covers |
| --- | --- |
| [Runtime and events](runtime.md) | `Ask`, streams, events, lifecycle, request values, HITL, queues, multimodal input |
| [Sessions and persistence](persistence.md) | Session restore/save, checkpoints, stores, concurrency, retention |
| [Durable goals](goals.md) | `/goal`-style loops, async execution, reconnect, recovery, leases, idempotency |
| [Subagents](subagents.md) | Declarative specialists, isolation, budgets, events, nested HITL and goal integration |
| [Context management](context.md) | Full history versus model context, automatic compaction, ordering with reduction |
| [Skills](skills.md) | Local `SKILL.md` discovery, custom backends, validation and boundaries |
| [MCP](mcp.md) | Managed transports, tool discovery, limits, authentication, reconnection and ownership |
| [Tool management](tools.md) | Policy, aliases, hooks, repair, result reduction, dynamic tool search |
| [Testing](testing.md) | Mock models, mock tools, streaming failures and interaction assertions |

Runnable programs live in the [examples directory](../examples/). Public API details also remain available through [Go package documentation](https://pkg.go.dev/github.com/wsshow/agentkit).

## Documentation Conventions

- Examples always pass a non-nil `context.Context`. Use `context.TODO()` when the correct lifetime is not known yet.
- Zero-value optional configurations shown as `&agentkit.SomeConfig{}` are intentionally safe defaults.
- “Durable” means state survives through the configured store. It does not mean a stopped process continues executing without a worker or supervisor.
- File-backed stores target one local process. Database-backed transactional implementations are required for multi-replica ownership.
