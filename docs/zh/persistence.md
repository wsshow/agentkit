# 会话与持久化

[English](../persistence.md) · [文档索引](README.md)

会话可以让对话状态持久化，无需在每次运行前后手写保存逻辑。检查点负责保存未完成的 HITL 工作；同一套内置存储还能提供目标和大型结果存储。

## 自动恢复与保存

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

`agentkit.New` 会恢复同一 ID 的已有会话。`Prompt`、`Send`、`Continue` 和 `Resume` 都会自动保存，包括模型失败或运行取消的情况。启用[上下文压缩](context.md)后，会话会同时保存完整历史和模型上下文。

需要手动恢复时可改用 `History: savedHistory`。`History` 与 `Session` 互斥，避免出现两个状态来源。

## 直接管理会话

需要服务多个会话的应用通常应使用[多会话管理器](sessions.md)。它在保持本节存储契约的基础上，提供严格的创建/打开语义、Owner 隔离、进程内单实例协调、分页、归档和分支。

```go
sessions, err := store.List(ctx)
saved, err := store.Load(ctx, "user-123")
err = store.Delete(ctx, "user-123")
```

删除不存在的会话也会成功。内置删除还会级联清理该会话的检查点、目标和会话所属的大型工具结果。删除前应停止使用该会话的 worker。重复删除还会清理能够识别的孤立目标和结果。

文件存储会把 ID 哈希为安全文件名，并使用同步临时文件、原子替换和目录元数据同步。这样既能阻止会话 ID 路径穿越，也不会在崩溃后暴露写了一半的快照。

## 手动修改状态

```go
history := agent.History()
session := agent.Session()

agent.SetHistory(replacement)
err := agent.SaveSession(ctx)
```

`History` 和 `Session` 返回副本。`SetHistory` 会使旧检查点失效；需要立即持久化替换内容时调用 `SaveSession`。`Reset` 会等待当前运行结束，然后清空历史和队列。

只在 Agent 空闲时调用 `SaveSession`。并发调用会返回 `ErrAgentRunning`，而不是把半个工具调用回合持久化。正常的 `Prompt`、`Send`、`Continue` 和 `Resume` 仍会在完整运行稳定结束后执行内部保存。

## 持久化 HITL 检查点

两个内置会话存储都会自动提供匹配的检查点存储。因此文件会话在 Agent 或进程重建后也能恢复待处理 HITL，不需要额外配置。

待处理 ID 可通过 `Agent.PendingInterrupts` 和 `Session.PendingInterrupts` 获得。成功 `Resume` 后检查点会被消费；`ClearCheckpoint`、`Reset` 和 `SetHistory` 会让已经不匹配对话的检查点失效。

不使用会话时，可通过 `agentkit.NewFileCheckpointStore` 或自定义实现直接设置 `Config.CheckPointStore`。

## 如何选择存储

测试和单进程临时服务使用 `agentkit.NewMemorySessionStore()`；需要跨重启的本地 worker 使用 `agentkit.NewFileSessionStore()`。

数据库场景实现 `SessionStore`。自定义存储还可以实现：

- `CheckpointStoreProvider`：自动提供持久化 HITL 检查点；
- `GoalStoreProvider`：支持[持久化目标](goals.md)；
- `ToolResultStoreProvider`：支持[大型工具结果压缩](tools.md#大型工具结果压缩)。

自定义持久化方法必须遵循传入的非空 context，并在其取消后及时返回。请求取消后的内部收尾默认使用 `DefaultPersistenceTimeout`（30 秒），可通过 `Config.PersistenceTimeout` 修改；只有后端确实需要更久时才应增大。

## 并发写入

内置存储通过 `Session.Revision` 实现乐观并发控制。如果两个 Agent 恢复了同一 revision，落后的写入方会收到 `ErrSessionConflict`，不会静默覆盖更新的历史。

自定义存储应提供等价的 compare-and-swap 语义。AgentKit 不会自动合并分叉对话，因为通用合并可能打乱工具调用或破坏语义。

文件存储面向本地单进程 worker。多副本服务应在共享数据库事务中实现会话、检查点、目标和租约更新。

## 保留策略

长期服务可通过自己的调度器清理资源：

```go
report, err := agentkit.PruneResources(ctx, store, agentkit.RetentionPolicy{
	SessionIdleTime:       30 * 24 * time.Hour,
	CompletedGoalAge:      7 * 24 * time.Hour,
	DetachedToolResultAge: 24 * time.Hour,
})
```

零值策略不会删除任何内容。清理不会删除 active、paused 或 blocked 目标，也不会在会话仍可能引用结果时删除会话所属结果。应先停止符合闲置删除条件的会话 worker。报告只统计直接删除项，级联资源不会重复计数。

## 相关指南

- [持久化目标](goals.md)
- [子智能体](subagents.md)
- [上下文管理](context.md)
- [工具管理](tools.md)
