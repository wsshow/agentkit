# Durable Goals

[中文](zh/goals.md) · [Documentation index](README.md)

`GoalRunner` is AgentKit's `/goal`-style mode. It turns one durable objective into bounded Agent steps, evaluates the success criteria after every step, and automatically continues when the goal is incomplete. Its state can be inspected and resumed after a client disconnect, cancellation, or process restart.

## Start a Goal

A durable session is the simplest foundation because built-in session stores automatically supply a matching goal store:

```go
store, err := agentkit.NewFileSessionStore("./data/agent")
if err != nil {
	log.Fatal(err)
}

agent, err := agentkit.New(ctx, &agentkit.Config{
	Name:  "release-agent",
	Model: chatModel,
	Session: &agentkit.SessionConfig{
		ID:    "release-session",
		Store: store,
	},
})
if err != nil {
	log.Fatal(err)
}
defer agent.Close()

goals, err := agentkit.NewGoalRunner(agent, nil)
if err != nil {
	log.Fatal(err)
}

result, err := goals.Start(ctx, agentkit.GoalRequest{
	ID:              "release-v2",
	Objective:       "Prepare and verify the v2 release",
	SuccessCriteria: "Tests pass and release artifacts are ready",
})
```

`ID` is optional; AgentKit generates a UUID when it is omitted. The evaluator uses the primary model to decide whether the criteria are met. If not, it creates a concrete continuation prompt and starts the next step, up to the configured iteration limit.

## Background Execution and Client Reconnect

Request/response servers should use an application or worker lifetime context—not a short HTTP request context:

```go
run, err := goals.StartAsync(workerCtx, agentkit.GoalRequest{
	Objective: "Prepare and verify the v2 release",
})
if err != nil {
	return err
}

goalID := run.ID() // already persisted; safe to return to the client
result, err := run.WaitContext(waitCtx) // timeout does not cancel the goal
```

Live callers can use `Done` and `Wait`. A disconnected client can reconnect with `goalID`, then call `Get` or `List` to read the same durable state. Every committed change also emits `EventGoalUpdate`; `Event.Goal` is an isolated snapshot at the committed revision, which is useful for WebSocket or SSE status updates.

Control methods are separate from waiting:

```go
err = run.Pause(controlCtx)

resumed, err := goals.ResumeAsync(workerCtx, goalID)
resumed, err = goals.ResumePendingAsync(workerCtx)
resumed, err = goals.ResumeInterruptAsync(workerCtx, goalID, targets)
retried, err := goals.RetryAsync(workerCtx, goalID)
```

`Pause` first persists the pause request and then cancels active work. `WaitContext` only bounds how long the caller waits; it does not cancel the underlying goal.

## Restore After a Process Restart

Recreate the file store and Agent with the same session ID, create a `GoalRunner`, then call:

```go
result, err := goals.Resume(ctx, "release-v2")
```

When the ID was generated automatically, `ResumePending` resumes the current session's only unfinished goal. If several unfinished goals exist, it returns `ErrGoalResumeAmbiguous` rather than guessing. `List` is scoped to the current session and returns reconnect-ready summaries including objective, iteration limit, pending phase, latest reason, and latest error.

For a goal paused at HITL, submit the pending interrupt IDs and user data through `ResumeInterrupt` or `ResumeInterruptAsync`.

## Recovery Safety

Goal state is committed before work, after Agent output, and after evaluation. Recovery uses those boundaries:

- If saved session history proves the step completed, `Resume` evaluates it without repeating the work.
- If a process may have stopped after an external side effect but before progress was saved, the goal becomes `blocked` and returns `ErrGoalRecoveryRequired`.
- Only explicit `Retry` or `RetryAsync` may replay that uncertain step.

This favors visible recovery decisions over pretending that arbitrary external effects can be exactly once. If work and the subsequent recovery-state save both fail, `GoalRunner` returns both with `errors.Join`, allowing callers to inspect each cause with `errors.Is`.

## Side-Effect Idempotency

Tools can obtain a stable operation key from the active goal attempt:

```go
key, ok := agentkit.GoalOperationKey(ctx, "publish-release")
if ok {
	// Pass key to an idempotent API or atomically store it with the result.
}
```

The key remains stable for the same attempt across process recovery and an explicit retry. The next successful goal iteration receives a new key. `CurrentGoalRun` also exposes `GoalID`, `SessionID`, and `Attempt` for audit metadata.

The external system must enforce uniqueness. AgentKit supplies the identity but cannot make an arbitrary third-party API transactional.

## Evaluation Reliability

The default model evaluator reuses the Agent's `ModelRetryConfig` and `ModelFailoverConfig`, exhausting retries on the current model before failing over, just like normal Agent calls. A custom `GoalEvaluator` can be configured through `GoalRunnerConfig`. If it panics, AgentKit returns an error wrapping `ErrGoalEvaluatorPanic`; completed Agent work remains committed as pending evaluation and can be resumed without repetition.

## Worker Leases

Built-in goal stores implement `GoalLeaseStore`. `GoalRunner` automatically:

1. acquires ownership for every state-changing operation;
2. renews the lease during long model or tool calls; and
3. fences every save with an opaque token.

A concurrent worker receives `ErrGoalLeaseHeld`. Use `errors.As` with `GoalLeaseHeldError` to inspect its owner and expiration. A worker that loses ownership is canceled and receives `ErrGoalLeaseLost`. After a crashed worker's lease expires, a replacement can call `Resume` and follow the same recovery rules.

The default lease lasts one minute and renews approximately every 20 seconds. Set `WorkerID` and `LeaseDuration` through `GoalRunnerConfig`. Production code using a custom store can set `RequireLease: true` so a legacy store cannot silently fall back to single-worker behavior.

## What “Long-Running” Does and Does Not Mean

AgentKit provides durable state, reconnectable IDs, recovery boundaries, leases, and pause/resume controls. It does not keep code executing after the host process has stopped. A supervisor must restart a worker and invoke `Resume` when the lease becomes available.

The file store is suitable for one local process. Its lease survives restart but is not a distributed filesystem lock. Multi-replica deployments should transactionally implement `SessionStore`, `CheckpointStore`, `GoalStore`, and `GoalLeaseStore` in a shared database.

Internal persistence cleanup uses a bounded context. Custom stores must honor cancellation; AgentKit does not abandon an unknown write in a background goroutine because that would make the final commit outcome ambiguous.

## Runnable Example

The [goal example](../examples/goal/) starts a persisted goal from command-line text. After interruption or process restart, running it without a new objective resumes the current session's only unfinished goal, so the user does not have to remember its ID.

## Related Guides

- [Sessions and persistence](persistence.md)
- [Subagents](subagents.md)
- [Runtime and events](runtime.md)
- [Testing](testing.md)
