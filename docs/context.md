# Context Management

[中文](zh/context.md) · [Documentation index](README.md)

AgentKit keeps the complete conversation available to the application while allowing the model-facing context to be compacted independently. This avoids the common tradeoff between preserving an audit trail and staying inside a model context window.

## Enable Automatic Compaction

```go
agent, err := agentkit.New(ctx, &agentkit.Config{
	Name:  "assistant",
	Model: chatModel,
	Compaction: &agentkit.CompactionConfig{
		MaxTokens:       80_000,
		MaxMessages:     100,
		KeepRecentTurns: 2,
		Model:           summaryModel,
	},
})
```

`MaxTokens` and `MaxMessages` are independent triggers; exceeding either starts compaction. If neither is set, AgentKit uses `DefaultCompactionMaxTokens` (100,000 estimated tokens). `KeepRecentTurns` defaults to one and keeps the newest user turns verbatim. `Model` is optional and defaults to the main Agent model.

Compaction only runs when at least one complete conversation exists before the requested recent turns. If the history contains fewer than `KeepRecentTurns`, AgentKit preserves the whole current task even when a threshold is exceeded; it never rewrites the only user instruction into a summary. A single oversized turn should be reduced at its source—use tool-result reduction for large tool payloads or reject/segment oversized user input.

Set the token threshold below the provider's hard context window. Leave room for the next prompt, tool definitions, and the model response.

## Two History Views

- `agent.History()` returns the complete, unabridged conversation for a UI, audit log, or export.
- `agent.ContextHistory()` returns the compacted messages actually sent to the model.
- With `Session` configured, both views are persisted. A restarted Agent does not accidentally send the full unabridged history back to the model.

`Config.SystemPrompt` is injected for each model run and is not copied into the persisted compact context. This prevents the same instruction from accumulating after repeated compaction or session restores. Explicit system messages supplied as conversation history remain part of that history.

`SetHistory` replaces the full history and synchronizes the display state. If the change must survive a restart, call `SaveSession` afterward.

## Failure and Observability

The compaction model reuses the Agent's `ModelRetryConfig` and `ModelFailoverConfig`, including when a separate summary model is selected. If summary generation still fails after those policies are exhausted, AgentKit returns the error and keeps the original context unchanged.

Subscribe to `EventCompactionStart` and `EventCompactionEnd`. Their `Event.Compaction` value reports message counts before and after the operation.

## Relationship to Large Tool Results

Context compaction summarizes a conversation. [Tool result reduction](tools.md#large-tool-result-reduction) moves complete oversized tool payloads into a store and gives the model a bounded read tool. When both are enabled, reduction runs first so the summarizer does not have to consume old bulky tool payloads.

A practical default is to enable compaction for any persistent conversational Agent, then enable reduction only if tools can return large documents, logs, search results, or datasets.

## Related Guides

- [Sessions and persistence](persistence.md)
- [Tool management](tools.md)
- [Runtime and events](runtime.md)
