# Multi-Session Management

[中文](zh/sessions.md) · [Documentation index](README.md)

`SessionManager` is the application-facing layer for serving many independent conversations. It keeps the important isolation rule—one `Agent` instance belongs to exactly one session—while taking care of creation, reconnect, lookup, ownership, and resource cleanup.

Use a direct `SessionConfig` when an application has only one known conversation. Use `SessionManager` when session IDs come from users, routes, jobs, or a conversation list.

## Start with One Shared Agent Configuration

```go
store, err := agentkit.NewFileSessionStore("./data/sessions")
if err != nil {
	log.Fatal(err)
}

manager, err := agentkit.NewSessionManager(&agentkit.SessionManagerConfig{
	Store:   store,
	OwnerID: "user-123",
	AgentConfig: &agentkit.Config{
		Name:         "assistant",
		SystemPrompt: "You are a helpful assistant.",
		Model:        chatModel,
		Compaction:   &agentkit.CompactionConfig{},
	},
})
if err != nil {
	log.Fatal(err)
}
defer manager.Close()
```

`AgentConfig` is a reusable template. The manager supplies a different `SessionConfig` whenever it opens a conversation. Do not set `History` or `Session` in the template.

The configured model and other shared dependencies must be safe for concurrent calls because different sessions can run at the same time. If a dependency must be created per session, use an [Agent factory](#per-session-agent-factory).

## Create or Reconnect

For a stable application-provided ID, `OpenOrCreate` is usually the smallest correct API:

```go
agent, created, err := manager.OpenOrCreate(ctx, agentkit.CreateSessionOptions{
	ID:    "conversation-42",
	Title: "Refund question",
	Tags:  []string{"support", "priority"},
})
```

If that ID exists, its existing metadata and history win and `created` is false. If it does not exist, the manager persists the new session before initializing its Agent. A model or MCP initialization failure therefore does not lose the conversation record: fix the dependency and call `Open` again.

Use strict methods when the distinction matters:

```go
newAgent, err := manager.Create(ctx) // generated UUID
namedAgent, err := manager.CreateWithOptions(ctx, agentkit.CreateSessionOptions{ID: "known-id"})
existingAgent, err := manager.Open(ctx, "known-id")
```

`CreateWithOptions` returns `ErrSessionAlreadyExists` for a duplicate ID. `Open` returns `ErrSessionNotFound` instead of silently creating a typo. Repeated `Open` calls for the same ID return the same active `*Agent`.

After a process restart, reconstruct the store and manager, then call `Open` with the same ID. History, compacted context, checkpoint, and pending interrupts are restored by the Agent. Inspect `agent.PendingInterrupts()` before choosing `Resume`; durable `/goal` work can construct a `GoalRunner` from the reopened Agent.

## Ownership Isolation

`OwnerID` scopes a manager to one application owner:

```go
manager, err := agentkit.NewSessionManager(&agentkit.SessionManagerConfig{
	Store:       sharedStore,
	OwnerID:     authenticatedUserID,
	AgentConfig: template,
})
```

Create and list operations automatically apply that owner. Open, get, update, archive, fork, and delete reject records belonging to another owner with `ErrSessionAccessDenied`. Treat `OwnerID` as an application namespace—it may represent a user, tenant, workspace, or a composite key.

An empty manager `OwnerID` is intentionally unscoped and can access every session in the store. Use it only for trusted administrative workers. Owner isolation is defense in depth, not authentication; derive it from trusted server identity rather than request JSON.

## List and Page Sessions

```go
active := false
page, err := manager.List(ctx, agentkit.SessionQuery{
	Tags:     []string{"support"}, // all supplied tags must match
	Archived: &active,
	Limit:    20,
})

if page.NextCursor != "" {
	next, err := manager.List(ctx, agentkit.SessionQuery{
		Tags:     []string{"support"},
		Archived: &active,
		Limit:    20,
		Cursor:   page.NextCursor,
	})
}
```

Results are ordered by `UpdatedAt` descending and then ID. Cursors are opaque and should be returned unchanged. The default page size is `DefaultSessionPageSize` and the hard maximum is `MaxSessionPageSize`.

The built-in memory and file stores support the query directly. Existing custom `SessionStore` implementations remain source compatible: the public `QuerySessions` helper falls back to `List`. Database backends should implement the optional `SessionQueryStore` interface so filtering and cursor pagination happen in the database.

## Metadata, Archive, and Fork

Replace user-facing metadata without rewriting conversation state:

```go
session, err := manager.UpdateMetadata(ctx, "conversation-42", agentkit.SessionMetadata{
	Title: "Refund approved",
	Tags:  []string{"support", "resolved"},
})
```

A scoped manager preserves its configured `OwnerID` when the replacement omits it.

If the Agent is running, `UpdateMetadata` waits for the complete run and final session save before replacing metadata. The wait follows the call context and never persists a partial tool-call turn.

Archiving is a lifecycle operation, not deletion:

```go
err = manager.Archive(ctx, "conversation-42")
err = manager.Unarchive(ctx, "conversation-42")
```

Archive closes an active Agent first. Archived sessions remain queryable, but `Open` returns `ErrSessionArchived`. `Delete` is permanent and also removes built-in checkpoint, goal, and reduced-result resources.

Forking creates an independent branch from a coherent conversation snapshot:

```go
branch, err := manager.Fork(ctx, "conversation-42", agentkit.CreateSessionOptions{
	Title: "Alternative solution",
})
```

Full history and compacted context are copied. Operational state is deliberately not copied: the branch receives its own checkpoint ID and does not inherit pending interrupts, goals, or reduced tool results. References to an offloaded result in older copied context remain visible as historical text, but the branch cannot read the source session's result; start a fresh tool call when the complete payload is still needed.

If the source Agent is running, `Fork` waits for its current run—including queued follow-up work and final persistence—to settle before taking the snapshot. The wait follows the `Fork` context and creates no target session when that context expires, so a branch never starts from a partial tool-call turn.

## Agent Lifecycle and Concurrency

```go
err := manager.CloseSession(ctx, "conversation-42")
err = manager.Delete(ctx, "conversation-42")
err = manager.CloseContext(ctx)
```

Subscribe once when a service needs a unified event feed for every opened conversation:

```go
unsubscribe := manager.Subscribe(func(event agentkit.Event) {
	logEvent(event.SessionID, event)
})
defer unsubscribe()
```

The manager automatically attaches future Agents and detaches them when they close. Session-bound events also carry `SessionID` when subscribed directly through `agent.Subscribe`. Event order is preserved within one Agent; callbacks must be concurrency-safe because different sessions can emit at the same time.

`CloseSession` releases Agent and MCP resources but keeps persistent data. `Delete` closes first and then removes the data. Closing the manager closes every active Agent but does not delete sessions.

If another process has already removed the durable record, `Delete` still closes any Agent tracked by this manager. The call remains idempotent and cannot leave a local instance running after its session has disappeared from storage.

Prefer `CloseSession` when application code has the session ID because it expresses the intended manager lifecycle. Direct `agent.Close` is also safe: the Agent notifies its manager after cleanup, the active registry is updated immediately, and the next `Open` creates a replacement.

Operations for the same session are serialized and honor context cancellation while waiting. Different sessions are not serialized. The manager prevents duplicate active instances only inside that manager process; `Session.Revision` still prevents silent stale writes across managers or processes. Multi-replica execution needs a database-backed ownership or lease mechanism around work dispatch in addition to the storage interfaces.

The active registry is explicitly lifecycle-managed rather than an invisible cache. Close sessions when clients disconnect permanently or when an application-level idle policy evicts them. Temporary network disconnects do not require closing the Agent; a reconnect can call `Open` and receive the existing instance.

## Per-Session Agent Factory

Use `AgentFactory` when each session needs its own model client, credentials, tools, or other resources:

```go
manager, err := agentkit.NewSessionManager(&agentkit.SessionManagerConfig{
	Store: store,
	AgentFactory: func(ctx context.Context, session agentkit.SessionConfig) (*agentkit.Agent, error) {
		model, err := newModelForSession(ctx, session.ID)
		if err != nil {
			return nil, err
		}
		return agentkit.New(ctx, &agentkit.Config{
			Name:    "assistant",
			Model:   model,
			Session: &session,
		})
	},
})
```

The factory must use the supplied `SessionConfig`; the manager rejects an Agent bound to a different ID or a different comparable `SessionStore` with `ErrSessionFactoryMismatch`. This prevents a factory mistake from making the manager read one conversation while the Agent writes another. Factory panics are converted to `ErrSessionFactoryPanic`. A preconnected MCP `Session` cannot be reused through `AgentConfig` because each Agent owns and closes its MCP connections—use MCP transport settings or a factory instead.

## Related Guides

- [Sessions and persistence](persistence.md)
- [Durable goals](goals.md)
- [Context management](context.md)
- [MCP](mcp.md)
