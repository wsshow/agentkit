# 持久化目标

[English](../goals.md) · [文档索引](README.md)

`GoalRunner` 是 AgentKit 内置的 `/goal` 式模式。它把一个持久化目标转成有界的 Agent 步骤，每一步后判断成功标准，未完成时自动继续。客户端断线、运行取消或进程重启后，应用仍能查看并恢复同一目标。

## 启动目标

持久化会话是最简单的基础，因为内置会话存储会自动提供匹配的目标存储：

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
	Objective:       "准备并验证 v2 发布",
	SuccessCriteria: "测试通过且发布产物准备完毕",
})
```

`ID` 可选，省略时 AgentKit 会生成 UUID。默认判断器使用主模型检查成功标准；未完成时会生成具体的继续提示并开始下一步，直到配置的迭代上限。

## 后台执行与客户端重连

请求/响应服务应使用应用或 worker 生命周期 context，而不是短生命周期 HTTP 请求 context：

```go
run, err := goals.StartAsync(workerCtx, agentkit.GoalRequest{
	Objective: "准备并验证 v2 发布",
})
if err != nil {
	return err
}

goalID := run.ID() // 已持久化，可以安全返回给客户端
result, err := run.WaitContext(waitCtx) // 等待超时不会取消目标
```

在线调用方可使用 `Done` 和 `Wait`。客户端断线后可带 `goalID` 重连，再通过 `Get` 或 `List` 读取同一份持久化状态。每次提交还会发出 `EventGoalUpdate`；`Event.Goal` 是对应 revision 的隔离快照，适合 WebSocket 或 SSE 状态更新。

控制操作与等待相互独立：

```go
err = run.Pause(controlCtx)

resumed, err := goals.ResumeAsync(workerCtx, goalID)
resumed, err = goals.ResumePendingAsync(workerCtx)
resumed, err = goals.ResumeInterruptAsync(workerCtx, goalID, targets)
retried, err := goals.RetryAsync(workerCtx, goalID)
```

`Pause` 会先持久化暂停请求，再取消当前工作。`WaitContext` 只限制调用方等待时长，不会取消底层目标。

## 进程重启后恢复

使用同一会话 ID 重建文件存储和 Agent，再创建 `GoalRunner` 并调用：

```go
result, err := goals.Resume(ctx, "release-v2")
```

自动生成 ID 时，`ResumePending` 可以恢复当前会话唯一的未完成目标。如果存在多个未完成目标，它会返回 `ErrGoalResumeAmbiguous`，不会猜测。`List` 只返回当前会话的重连摘要，其中包含目标、迭代上限、待处理阶段、最新原因和最新错误。

目标停在 HITL 时，通过 `ResumeInterrupt` 或 `ResumeInterruptAsync` 提交待处理的中断 ID 和用户数据。

## 恢复安全性

目标状态会在执行工作前、Agent 输出后和判断后分别提交。恢复会利用这些边界：

- 已保存会话历史能够证明步骤完成时，`Resume` 直接进入判断，不重复工作。
- 进程可能在外部副作用完成后、进度保存前退出时，目标进入 `blocked` 并返回 `ErrGoalRecoveryRequired`。
- 只有显式 `Retry` 或 `RetryAsync` 才能重放这个结果不确定的步骤。

这种设计优先让恢复决策可见，不会假装任意外部副作用都能 exactly-once。如果工作本身与后续恢复状态保存同时失败，`GoalRunner` 会通过 `errors.Join` 返回两个错误，调用方可分别用 `errors.Is` 检查。

## 副作用幂等

工具可以从当前目标尝试中取得稳定操作 key：

```go
key, ok := agentkit.GoalOperationKey(ctx, "publish-release")
if ok {
	// 传给幂等 API，或与操作结果一起原子保存。
}
```

同一尝试跨进程恢复或显式重试时 key 保持不变；成功进入下一次目标迭代后会得到新 key。`CurrentGoalRun` 还会提供 `GoalID`、`SessionID` 和 `Attempt` 作为审计信息。

唯一性必须由外部系统保证。AgentKit 提供身份，但无法让任意第三方 API 自动具备事务性。

## 判断可靠性

默认模型判断器会复用 Agent 的 `ModelRetryConfig` 和 `ModelFailoverConfig`，与普通调用一样，先耗尽当前模型重试再故障转移。也可通过 `GoalRunnerConfig` 配置自定义 `GoalEvaluator`。判断器 panic 会转换为包装 `ErrGoalEvaluatorPanic` 的错误；已完成的 Agent 工作保持为待判断，恢复时无需重复执行。

## Worker 租约

内置目标存储实现了 `GoalLeaseStore`。`GoalRunner` 会自动：

1. 在每个修改状态的操作前取得所有权；
2. 在耗时较长的模型或工具调用中续租；
3. 使用不透明 token fencing 每一次保存。

并发 worker 会收到 `ErrGoalLeaseHeld`，可用 `errors.As` 读取 `GoalLeaseHeldError` 中的持有者与到期时间。丢失所有权的 worker 会被取消并收到 `ErrGoalLeaseLost`。崩溃 worker 的租约到期后，替代 worker 可调用 `Resume` 并沿用相同恢复规则。

默认租约为一分钟，约每 20 秒续期。可通过 `GoalRunnerConfig` 设置 `WorkerID` 和 `LeaseDuration`。生产环境使用自定义存储时可设置 `RequireLease: true`，避免旧存储静默退化为单 worker 行为。

## “长任务”的准确含义

AgentKit 提供持久化状态、可重连 ID、恢复边界、租约和暂停/恢复控制。它不会在宿主进程停止后继续执行代码；supervisor 必须重启 worker，并在租约可用后调用 `Resume`。

文件存储适合本地单进程。它的租约可跨重启保留，但不是分布式文件锁。多副本部署应在共享数据库事务中实现 `SessionStore`、`CheckpointStore`、`GoalStore` 和 `GoalLeaseStore`。

内部持久化收尾使用有界 context。自定义存储必须遵循取消；AgentKit 不会把结果未知的写入遗留到后台，因为那会让最终是否提交变得不确定。

## 可运行示例

[goal 示例](../../examples/goal/)可以从命令行文本启动持久化目标。中断或进程重启后，不传新目标再次运行即可恢复当前会话唯一的未完成目标，用户不需要记住 ID。

## 相关指南

- [会话与持久化](persistence.md)
- [子智能体](subagents.md)
- [运行时与事件](runtime.md)
- [测试](testing.md)
