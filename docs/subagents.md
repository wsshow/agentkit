# Subagents

[中文](zh/subagents.md) · [Documentation index](README.md)

Subagents let one coordinator delegate focused work without requiring an application to build a graph or manually wrap agents as tools. They are intentionally declarative: define stable specialists once, and AgentKit exposes each one to the coordinator as a tool whose name and description guide model routing.

## Minimal Configuration

```go
agent, err := agentkit.New(ctx, &agentkit.Config{
	Name:         "coordinator",
	SystemPrompt: "Delegate research when specialist evidence would help.",
	Model:        chatModel,
	SubAgents: []agentkit.SubAgentConfig{
		{
			Name:         "researcher",
			Description:  "Research one focused question and return evidence",
			SystemPrompt: "Be concise and distinguish facts from uncertainty.",
			// Model omitted: inherit the coordinator model.
			Tools: []agentkit.Tool{webSearch},
		},
	},
})
```

No routing callback is required. The coordinator sees a `researcher` tool with one required string argument named `request`. A call such as `{"request":"compare the two APIs"}` starts an isolated child run and returns its final text as the tool result.

`Name` and `Description` are required and must be unique. Name collisions with root tools, Skills, MCP tools, Tool Search, aliases, and other subagents fail during `agentkit.New` rather than during a run.

## Isolation and Inheritance

Safe isolation is the default:

| Capability | Default behavior |
| --- | --- |
| Conversation | The child receives only the delegation request |
| Model | Inherits the coordinator model when `Model` is nil |
| Retry and failover | Inherit the coordinator policies when omitted |
| Tools | Never inherited; only `SubAgentConfig.Tools` are visible |
| Skills, MCP, Tool Search | Configured independently for each child |
| Session and compaction | Owned by the coordinator, not duplicated per child |
| Final result | Returned to the coordinator as an ordinary tool result |
| Token usage | Child model usage is added to the current `RunResult.Usage` |

Set `IncludeHistory: true` only when the specialist genuinely needs the coordinator's conversation. This shares parent history with that child and should be treated as an explicit privacy and token-cost decision.

Each child can override `Model`, `ModelRetryConfig`, `ModelFailoverConfig`, `MaxIterations`, and `ToolPolicy`. Child tools receive the normal request context and can use `RunValue`, `SetRunValue`, `EmitToolUpdate`, and HITL helpers. The coordinator's `ToolReduction` can offload the child final result after it returns; child-internal tool results still use the child's `ToolPolicy.MaxResultChars` limit.

## Bounded Execution

AgentKit applies safe process-local defaults whenever at least one subagent is configured:

```go
SubAgentPolicy: &agentkit.SubAgentPolicy{
	MaxDelegations: 8,                // per top-level fresh run
	MaxParallel:    4,                // across different specialists
	Timeout:        10 * time.Minute, // per delegation
}
```

Zero values select those defaults. Negative values are rejected. `MaxParallel` is also capped by the number of configured specialists. A second overlapping call to the same specialist returns `ErrSubAgentBusy`, preventing accidental concurrent mutation of one child runtime. Calls to different specialists may run in parallel.

Timeout and cancellation are cooperative: models and tools must stop when their context is canceled. `ErrSubAgentBudgetExceeded`, `ErrSubAgentBusy`, deadline errors, and guarded model/tool panics flow through the normal run error path and are also visible on the matching delegation-end event.

## Events and Correlation

Every delegation has stable `DelegationInfo`:

```go
agent.Subscribe(func(event agentkit.Event) {
	if event.Delegation == nil {
		return
	}
	fmt.Printf("%s: %s via %v\n",
		event.Delegation.ID,
		event.Agent,
		event.Delegation.Path,
	)
})
```

- `EventDelegationStart` is emitted when the coordinator requests the child.
- Child message, reasoning, tool, progress, transfer, and error events carry the same `Event.Delegation`. A nested HITL `EventInterrupted` also carries it when exactly one delegation is active.
- `EventDelegationEnd` carries the terminal error when a delegation fails.
- The coordinator's matching `EventToolEnd` also carries the delegation.

`DelegationInfo.ID` matches the coordinator tool-call ID. `ParentAgent`, `Agent`, and `Path` identify the route. Event snapshots are deep-copied for each subscriber.

Child events are observable only: they are not appended to the coordinator's `State`, `History`, session messages, or model context. Only the final child tool result enters parent history. This avoids duplicate content and preserves valid assistant/tool message ordering.

## HITL, Sessions, and Durable Goals

A child tool can call `Interrupt` or `StatefulInterrupt` exactly like a root tool. Eino's nested checkpoint is bridged into the coordinator checkpoint. With a configured `Session`, an application can:

1. receive `EventInterrupted` and persist the session automatically;
2. close the current Agent or restart the process;
3. recreate the same coordinator and subagent configuration with the same session ID;
4. call `Resume` or `ResumeWithResult` using the pending interrupt ID.

The resumed child retains its saved tool state and completes the original coordinator tool call. Subagents also work unchanged inside `GoalRunner`; the goal persists only after safe step boundaries and can be reconnected by goal ID.

An abrupt process loss in the middle of ordinary model or tool execution cannot reconstruct uncommitted side effects. For work lasting hours or days, use [durable goals](goals.md), place external effects behind idempotency keys, and introduce HITL/stateful checkpoints at meaningful boundaries. A worker or supervisor is still required to keep or restart the process.

## Deliberate Boundaries

The first stable API uses one static coordinator-to-specialist level. Subagents cannot dynamically invent persistent workers or declare their own `SubAgents`. This keeps tool visibility, cost, permissions, checkpoints, and concurrency understandable. Build a second coordinator explicitly if the application truly needs another orchestration boundary.

Likewise, a subagent is an isolated delegation, not an independent long-lived chat session. Durable user conversation belongs to the coordinator `Session`; durable autonomous progress belongs to `GoalRunner`.

## Related Guides

- [Runtime and events](runtime.md)
- [Sessions and persistence](persistence.md)
- [Durable goals](goals.md)
- [Tool management](tools.md)
- [MCP](mcp.md)
