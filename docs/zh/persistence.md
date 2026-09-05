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

会话 ID 是精确的持久化键：不能为空，也不能带首尾空白。直接 `SessionConfig`、内置存储、查询结果和 `SessionManager` 会统一执行这条规则，避免视觉上相同的 ID 重连到两条不同记录。

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

早期缺少会话时间戳的 version-1 文件仍可读取。任一时间戳缺失时，文件存储会使用文件修改时间作为稳定回退值，确保目录排序和游标分页继续有效；下次保存会持久化正常时间戳和当前 revision。

## 手动修改状态

```go
history := agent.History()
session := agent.Session()

agent.SetHistory(replacement)
err := agent.SaveSession(ctx)
```

`History` 和 `Session` 返回深拷贝，其中包括 Eino 新旧多模态消息字段的嵌套元数据。`SetHistory` 会使旧检查点失效；需要立即持久化替换内容时调用 `SaveSession`。`Reset` 会等待当前运行结束，然后清空历史和队列。

外部 JSON 或数据库记录拼装错误时可能产生 nil 消息项。AgentKit 会在统一历史复制边界丢弃这些空项，避免手工历史或恢复会话把 nil 消息传进模型运行时；其余非 nil 消息的原始顺序保持不变。

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

每次成功的 `Load` 都必须返回非 nil 对象，且其 ID 必须与请求 ID 完全一致。目标快照还必须包含合法状态、会话 ID、目标内容和正数迭代上限。AgentKit 会在边界统一校验；后端返回畸形数据时返回 `ErrInvalidPersistenceData`，而不是冒险 panic 或恢复到其他记录。载入的可变数据在使用前会复制，因此后端可以安全保留自己的缓存对象。

## 并发写入

内置存储通过 `Session.Revision` 实现乐观并发控制。如果两个 Agent 恢复了同一 revision，落后的写入方会收到 `ErrSessionConflict`，不会静默覆盖更新的历史。

发生冲突后，失败方 Agent 会被标记为陈旧，后续模型或工具执行会直接返回 `ErrSessionStale`。直接管理 Agent 时，应使用同一 ID 重建实例并载入权威快照；使用 `SessionManager` 时只需再次调用 `Open`，管理器会关闭陈旧实例并返回重新恢复的 Agent。分叉的内存消息不会被自动合并或重试。

解析到同一目录的所有文件存储实例会共享进程内锁。因此，即使为同一目录创建两个存储，会话和目标的 revision 校验、目标租约互斥、工具结果不可变创建和检查点替换仍然成立。内置会话删除还会阻止并发保存，直至主记录及配套资源全部删除；随后陈旧保存会返回 `ErrSessionConflict`，不会在删除期间被重新创建或悄悄丢失。

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
