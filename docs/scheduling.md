# Scheduling and Wakeups

[中文](zh/scheduling.md) · [Documentation index](README.md)

AgentKit deliberately provides a reliable scheduled-work execution boundary rather than another cron daemon. Use the scheduler already responsible for time, persistence, retries, and leader election in your application—such as a queue with delayed delivery, a cloud scheduler, Kubernetes CronJob, or a Go cron package—and let it wake `SessionManager` and `GoalRunner`.

This split keeps AgentKit small while preserving the hard agent-specific guarantees: session restoration, duplicate-run protection, goal leases, recovery boundaries, human interrupts, and result idempotency.

## Recommended Architecture

```text
external scheduler / queue
          │ due event: session ID + goal ID
          ▼
   SessionManager.Open
          │
          ▼
     GoalRunner.Resume
          │
          ▼
 session + goal + checkpoint stores
```

Persist scheduler records in the scheduler's own store. Persist conversation and agent progress in AgentKit stores. The scheduled payload should contain stable identifiers, not serialized `Agent` or `GoalRunner` values.

## Wake an Existing Goal

```go
func handleDueGoal(ctx context.Context, sessionID, goalID string) error {
	agent, err := manager.Open(ctx, sessionID)
	if err != nil {
		return err
	}
	cleanupCtx, cancelCleanup := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancelCleanup()
	defer manager.CloseSession(cleanupCtx, sessionID)

	goals, err := agentkit.NewGoalRunner(agent, &agentkit.GoalRunnerConfig{
		RequireLease: true,
	})
	if err != nil {
		return err
	}
	_, err = goals.Resume(ctx, goalID)
	if errors.Is(err, agentkit.ErrGoalLeaseHeld) {
		return nil // another worker already owns this delivery
	}
	return err
}
```

Use a database-backed `SessionStore` whose goal store implements `GoalLeaseStore` for multi-replica workers. `RequireLease` prevents an accidental deployment with only process-local ownership.

The example uses `context.WithoutCancel(ctx)` only inside a bounded resource-cleanup context after the scheduled handler ends. Give the execution context a real worker/job deadline. Never pass a nil context.

## Start a Scheduled Goal Exactly Once

Use a deterministic goal ID derived from the scheduler's job and occurrence IDs:

```go
goalID := "billing-report/" + occurrenceID
result, err := goals.Start(ctx, agentkit.GoalRequest{
	ID:              goalID,
	Objective:       "Generate and deliver the monthly billing report",
	SuccessCriteria: "The report is stored and delivery is confirmed",
})
if errors.Is(err, agentkit.ErrGoalExists) {
	_, err = goals.Resume(ctx, goalID)
}
```

Duplicate delivery then converges on the same durable goal instead of creating duplicate work. Tools with external side effects should use `agentkit.GoalOperationKey` as an idempotency key at the target system.

Do not use a random goal ID for an at-least-once scheduler delivery. Do not assume that a lease alone makes an email, payment, deployment, or other external side effect exactly once.

## Choose an Overlap Policy

Define this in the scheduler record rather than inferring it inside the Agent:

- **Skip**: if `ErrGoalLeaseHeld` is returned, acknowledge the due event and let the current owner continue. This is the safest default.
- **Queue**: retain the occurrence and retry it after the current goal reaches a terminal state.
- **Parallel**: give every occurrence a distinct session and goal ID. Use only when the tools and model clients are safe to run concurrently.
- **Replace**: explicitly pause the previous goal, wait for it to stop, and then start the new occurrence. Cancellation is cooperative, so replacement is not instantaneous.

AgentKit does not silently choose one because the correct behavior is a product decision.

## Reuse or Isolate Sessions

Reuse a stable session when recurring work should remember earlier runs:

```go
agent, _, err := manager.OpenOrCreate(ctx, agentkit.CreateSessionOptions{
	ID:    "monthly-report",
	Title: "Monthly reports",
})
```

Create a session per occurrence when runs must be independent. Always define its terminal lifecycle at creation time:

```go
sessionID := "schedule/" + jobID + "/" + occurrenceID
agent, err := manager.CreateWithOptions(ctx, agentkit.CreateSessionOptions{
	ID:    sessionID,
	Title: "Scheduled run " + occurrenceID,
	Tags:  []string{"scheduled", "temporary"},
})
if err != nil {
	return err
}

// After the run and any audit window:
err = manager.Delete(cleanupCtx, sessionID)
```

If the run must remain visible for audit, archive it instead and prune it later with an explicit retention policy. Never create an isolated session on every tick without also recording whether it is deleted, archived, or retained.

## Missed Runs and Time Zones

The external scheduler should own:

- time-zone and daylight-saving interpretation;
- missed-run behavior after downtime;
- retry/backoff and dead-letter policy;
- distributed leader election or delivery ownership;
- schedule creation, update, pause, and deletion;
- occurrence history and next-fire calculation.

AgentKit should not duplicate these concerns. Its stores are the authority for what the Agent accomplished, while the scheduler is the authority for when it should be attempted.

## Process Restart

A stopped process cannot execute timers. Run a supervisor or durable scheduler that starts a worker again. On delivery, reconstruct the store and manager, open the stable session ID, inspect the goal, and resume it. Expired goal leases allow another worker to take over; recovery rules stop ambiguous side effects from being retried without an explicit decision.

Use `StartAsync` or `ResumeAsync` only with an application or worker-lifetime context. An HTTP request context normally ends when the client disconnects and should not own a background job.

## Why There Is No Built-In Cron Loop

A built-in in-memory timer would be easy to demonstrate but unreliable across restarts. A fully durable scheduler would require cron parsing, time-zone databases, leader election, missed-run policy, job migrations, retry queues, and operational APIs. Those concerns are substantial and already solved by dedicated systems.

AgentKit may add optional adapters for popular schedulers after their common contract is proven. The core remains focused on making each wakeup safe and recoverable.

## Related Guides

- [Multi-session management](sessions.md)
- [Durable goals](goals.md)
- [Sessions and persistence](persistence.md)
