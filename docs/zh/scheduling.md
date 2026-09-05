# 调度与唤醒

[English](../scheduling.md) · [文档索引](README.md)

AgentKit 有意提供可靠的定时工作执行边界，而不是再实现一个 cron 守护进程。时间计算、持久化、重试和选主应交给应用已有的调度系统，例如延迟队列、云调度器、Kubernetes CronJob 或 Go cron 包；到期时再唤醒 `SessionManager` 与 `GoalRunner`。

这种拆分能保持 AgentKit 简洁，同时保留真正属于 Agent 的困难保证：会话恢复、防止重复运行、目标租约、恢复边界、人工中断与结果幂等。

## 推荐架构

```text
外部调度器 / 队列
          │ 到期事件：会话 ID + 目标 ID
          ▼
   SessionManager.Open
          │
          ▼
     GoalRunner.Resume
          │
          ▼
 会话 + 目标 + 检查点存储
```

调度记录保存在调度器自己的 Store 中；对话与 Agent 进度保存在 AgentKit Store 中。调度载荷只保存稳定 ID，不要序列化 `Agent` 或 `GoalRunner`。

## 唤醒已有目标

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
		return nil // 已有其他 worker 持有本次执行
	}
	return err
}
```

多副本 worker 应使用数据库型 `SessionStore`，并让它提供实现了 `GoalLeaseStore` 的目标存储。`RequireLease` 可以防止部署时意外退化为仅有进程内所有权。

示例中的 `context.WithoutCancel(ctx)` 只用于创建调度处理结束后的有界资源清理 context。实际执行 context 应设置合理的 worker/job 截止时间，绝不能传 nil context。

## 只启动一次定时目标

使用调度任务 ID 和 occurrence ID 生成确定性的目标 ID：

```go
goalID := "billing-report/" + occurrenceID
result, err := goals.Start(ctx, agentkit.GoalRequest{
	ID:              goalID,
	Objective:       "生成并交付月度账单报告",
	SuccessCriteria: "报告已保存，并确认交付成功",
})
if errors.Is(err, agentkit.ErrGoalExists) {
	_, err = goals.Resume(ctx, goalID)
}
```

这样，调度器重复投递会汇合到同一个持久化目标，而不会生成重复工作。具有外部副作用的工具应使用 `agentkit.GoalOperationKey` 作为目标系统的幂等键。

至少一次投递的调度器不能使用随机目标 ID。也不能认为仅靠租约就能让邮件、付款、部署等外部副作用做到严格一次。

## 明确重叠策略

该策略应保存在调度记录中，不能由 Agent 自行猜测：

- **跳过**：收到 `ErrGoalLeaseHeld` 时确认本次到期事件，让当前 Owner 继续；这是最安全的默认值。
- **排队**：保留 occurrence，等待当前目标进入终态后再重试。
- **并行**：每次 occurrence 使用不同的会话和目标 ID；仅当工具与模型客户端可安全并发时使用。
- **替换**：显式暂停旧目标，等待其停止后再启动新的 occurrence。取消是协作式的，因此替换不会瞬间完成。

AgentKit 不会静默选择策略，因为正确行为属于产品决策。

## 复用或隔离会话

周期工作需要记住之前的运行时，复用稳定会话：

```go
agent, _, err := manager.OpenOrCreate(ctx, agentkit.CreateSessionOptions{
	ID:    "monthly-report",
	Title: "月度报告",
})
```

每次运行需要完全独立时，为 occurrence 创建会话，并在创建时就明确终态生命周期：

```go
sessionID := "schedule/" + jobID + "/" + occurrenceID
agent, err := manager.CreateWithOptions(ctx, agentkit.CreateSessionOptions{
	ID:    sessionID,
	Title: "定时运行 " + occurrenceID,
	Tags:  []string{"scheduled", "temporary"},
})
if err != nil {
	return err
}

// 运行和审计窗口结束后：
err = manager.Delete(cleanupCtx, sessionID)
```

需要保留审计记录时，应先归档，再通过显式保留策略清理。绝不能在每次 tick 创建隔离会话，却不记录它最终会被删除、归档还是长期保留。

## 补偿运行与时区

以下能力应由外部调度器负责：

- 时区与夏令时解释；
- 服务停机后的补偿运行策略；
- 重试、退避与死信策略；
- 分布式选主或投递所有权；
- 调度的创建、更新、暂停和删除；
- occurrence 历史与下次触发时间计算。

AgentKit 不应重复实现这些能力。AgentKit Store 负责记录 Agent 做了什么，调度器负责决定何时尝试。

## 进程重启

进程停止后无法继续执行内存计时器，因此必须由 supervisor 或持久化调度器重新启动 worker。收到投递后，重建 Store 和管理器，打开稳定会话 ID，读取目标并继续执行。目标租约过期后其他 worker 可以接管；恢复规则会阻止存在歧义的外部副作用被静默重试。

只有传入应用或 worker 生命周期 context 时才使用 `StartAsync` 或 `ResumeAsync`。HTTP 请求 context 通常会在客户端断线时结束，不应拥有后台任务。

## 为什么核心没有内置 cron 循环

内存定时器容易演示，却无法可靠跨重启。完整的持久化调度器又必须处理 cron 解析、时区数据库、选主、补偿策略、任务迁移、重试队列和运维 API，这些并不是小功能，而且已有专门系统成熟解决。

等常见接入契约经过验证后，AgentKit 可以增加热门调度器的可选适配器；核心仍专注于让每次唤醒都安全且可恢复。

## 相关指南

- [多会话管理](sessions.md)
- [持久化目标](goals.md)
- [会话与持久化](persistence.md)
