# Sessions and Persistence

[中文](zh/persistence.md) · [Documentation index](README.md)

Sessions make conversation state durable without adding save calls around every run. Checkpoints preserve unfinished HITL work, while the same built-in stores can also supply goal and large-result storage.

## Automatic Restore and Save

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

`agentkit.New` restores an existing session with the same ID. `Prompt`, `Send`, `Continue`, and `Resume` save automatically, including when a model fails or the run is canceled. The session stores both full history and compacted model context when [compaction](context.md) is enabled.

Session IDs are exact durable keys. They must be non-blank and cannot contain surrounding whitespace; this rule is enforced consistently by direct `SessionConfig`, built-in stores, query results, and `SessionManager`, preventing visually identical IDs from reconnecting to different records.

Use `History: savedHistory` for manual restoration instead. `History` and `Session` are mutually exclusive so the source of truth is unambiguous.

## Direct Session Operations

Applications serving multiple conversations should normally use [SessionManager](sessions.md). It adds strict create/open semantics, per-owner isolation, in-process single-instance coordination, pagination, archive, and fork while preserving this storage contract.

```go
sessions, err := store.List(ctx)
saved, err := store.Load(ctx, "user-123")
err = store.Delete(ctx, "user-123")
```

Deleting a missing session succeeds. Built-in deletion also removes the session's checkpoint, goals, and session-owned reduced tool results. Stop workers using the session before deleting it. Repeating deletion cleans identifiable orphan goals and results.

The file store hashes IDs into safe filenames and uses synced temporary files, atomic replacement, and synced directory metadata. This prevents path traversal through a session ID and avoids exposing a half-written snapshot after a crash.

Legacy version-1 files that predate session timestamps remain readable. When either timestamp is absent, the file store uses the file's modification time as a stable fallback, so directory ordering and cursor pagination remain valid; the next save persists normal timestamps and the current revision.

## Manual State Changes

```go
history := agent.History()
session := agent.Session()

agent.SetHistory(replacement)
err := agent.SaveSession(ctx)
```

`History` and `Session` return deep copies, including nested metadata in current and legacy Eino multimodal message fields. `SetHistory` invalidates stale checkpoints; call `SaveSession` when the replacement must be immediately durable. `Reset` clears history and queues after waiting for an active run to finish.

Nil message entries can appear when external JSON or database rows are assembled incorrectly. AgentKit drops those empty entries at the common history-copy boundary, so manual history and restored sessions cannot pass a nil message into the model runtime. Non-nil messages remain in their original order.

Call `SaveSession` only while the Agent is idle. A concurrent call returns `ErrAgentRunning` instead of persisting half of a tool-call turn. Normal `Prompt`, `Send`, `Continue`, and `Resume` execution still saves internally after the complete run settles.

## Durable HITL Checkpoints

Both built-in session stores automatically provide a matching checkpoint store. A file-backed Agent can therefore resume a pending HITL interrupt after the Agent or process is recreated, without additional wiring.

Pending IDs are available through `Agent.PendingInterrupts` and `Session.PendingInterrupts`. A successful `Resume` consumes the checkpoint. `ClearCheckpoint`, `Reset`, and `SetHistory` invalidate a checkpoint that no longer matches the conversation.

Without a session, configure `Config.CheckPointStore` directly with `agentkit.NewFileCheckpointStore` or a custom implementation.

## Storage Selection

Use `agentkit.NewMemorySessionStore()` for tests and one-process ephemeral services. Use `agentkit.NewFileSessionStore()` for a local worker that needs restart durability.

For a database, implement `SessionStore`. A custom store may also implement:

- `CheckpointStoreProvider` for automatic durable HITL checkpoints;
- `GoalStoreProvider` for [durable goals](goals.md); and
- `ToolResultStoreProvider` for [large tool result reduction](tools.md#large-tool-result-reduction).

Custom persistence methods must honor their non-nil context and return promptly after it is canceled. `Config.PersistenceTimeout` defaults to `DefaultPersistenceTimeout` (30 seconds) for internal cleanup after cancellation. Increase it only when the backend legitimately needs more time.

Every successful `Load` must return a non-nil object whose ID exactly matches the requested ID. Goal snapshots must also contain a valid status, session ID, objective, and positive iteration limit. AgentKit validates these boundaries and returns `ErrInvalidPersistenceData` for malformed backend output instead of risking a panic or cross-record restore. Loaded mutable data is copied before AgentKit uses it, so a backend may safely retain its own cache object.

## Concurrent Writers

Built-in stores use `Session.Revision` for optimistic concurrency. If two Agents restore the same revision, the stale writer receives `ErrSessionConflict` instead of silently overwriting newer history.

All file-store instances that resolve to the same directory share an in-process lock. Creating two stores for one directory therefore preserves revision checks for sessions and goals, exclusive goal leases, immutable tool-result creation, and checkpoint replacement. A built-in session deletion also fences concurrent saves until its session and associated resources have been removed; a stale save then fails with `ErrSessionConflict` instead of recreating or disappearing behind the deletion.

Custom stores should provide equivalent compare-and-swap behavior. AgentKit intentionally does not merge divergent conversations because a generic merge can reorder tool calls or corrupt meaning.

The file store targets a local single-process worker. A multi-replica service should implement session, checkpoint, goal, and lease updates transactionally in a shared database.

## Retention

Long-running services can prune resources from their own scheduler:

```go
report, err := agentkit.PruneResources(ctx, store, agentkit.RetentionPolicy{
	SessionIdleTime:       30 * 24 * time.Hour,
	CompletedGoalAge:      7 * 24 * time.Hour,
	DetachedToolResultAge: 24 * time.Hour,
})
```

The zero policy deletes nothing. Pruning never deletes active, paused, or blocked goals, nor a session-owned result while its session may still reference it. Stop workers for sessions eligible for idle deletion first. The report counts directly deleted entries; cascaded resources are not counted twice.

## Related Guides

- [Durable goals](goals.md)
- [Subagents](subagents.md)
- [Context management](context.md)
- [Tool management](tools.md)
