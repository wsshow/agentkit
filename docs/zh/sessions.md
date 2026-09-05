# 多会话管理

[English](../sessions.md) · [文档索引](README.md)

`SessionManager` 是面向应用的多会话管理层。它保持“一份 `Agent` 实例只属于一个会话”这一关键隔离规则，同时统一处理创建、重连、查找、所有者隔离和资源释放。

应用只有一个已知会话时，直接使用 `SessionConfig` 即可；当会话 ID 来自用户、路由、任务或会话列表时，使用 `SessionManager`。

## 共享一套 Agent 配置

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
		SystemPrompt: "你是一个有用的助手。",
		Model:        chatModel,
		Compaction:   &agentkit.CompactionConfig{},
	},
})
if err != nil {
	log.Fatal(err)
}
defer manager.Close()
```

`AgentConfig` 是可复用模板。管理器在打开每个会话时注入不同的 `SessionConfig`，因此模板中不要设置 `History` 或 `Session`。

不同会话可以同时运行，所以共享的模型和其他依赖必须支持并发调用。需要为每个会话单独创建依赖时，使用[按会话创建 Agent](#按会话创建-agent)。

## 创建或重连

应用已有稳定会话 ID 时，`OpenOrCreate` 通常是最简单且正确的入口：

```go
agent, created, err := manager.OpenOrCreate(ctx, agentkit.CreateSessionOptions{
	ID:    "conversation-42",
	Title: "退款问题",
	Tags:  []string{"support", "priority"},
})
```

ID 已存在时，以已有元数据和历史为准，`created` 为 false；不存在时，管理器会先持久化会话，再初始化 Agent。因此即使模型或 MCP 初始化失败，也不会丢失会话记录，修复外部依赖后调用 `Open` 重试即可。

不同管理器同时对同一显式 ID 调用 `OpenOrCreate` 时，由存储 CAS 选出唯一创建方；失败方会自动打开赢家写入的记录并返回 `created=false`。调用方不需要在这条幂等路径上额外捕获 `ErrSessionAlreadyExists`。

需要严格区分创建与打开时使用：

```go
newAgent, err := manager.Create(ctx) // 自动生成 UUID
namedAgent, err := manager.CreateWithOptions(ctx, agentkit.CreateSessionOptions{ID: "known-id"})
existingAgent, err := manager.Open(ctx, "known-id")
```

`CreateWithOptions` 遇到重复 ID 返回 `ErrSessionAlreadyExists`；`Open` 遇到不存在的 ID 返回 `ErrSessionNotFound`，不会因为拼写错误静默创建。重复打开同一 ID 会返回同一个活动 `*Agent`。

进程重启后，重新创建存储和管理器，再用相同 ID 调用 `Open`。Agent 会恢复历史、压缩后的上下文、检查点和待处理中断。可先检查 `agent.PendingInterrupts()` 再决定是否 `Resume`；持久化 `/goal` 工作可基于重新打开的 Agent 创建 `GoalRunner`。

## 所有者隔离

通过 `OwnerID` 把一个管理器限制在单个应用所有者内：

```go
manager, err := agentkit.NewSessionManager(&agentkit.SessionManagerConfig{
	Store:       sharedStore,
	OwnerID:     authenticatedUserID,
	AgentConfig: template,
})
```

创建和列表操作会自动应用该 Owner；打开、读取、更新、归档、Fork 和删除其他 Owner 的记录都会返回 `ErrSessionAccessDenied`。可以把 `OwnerID` 当作应用命名空间，它可以表示用户、租户、工作区或组合键。

管理器的 `OwnerID` 为空时不会限制范围，可以访问存储内的全部会话，只应供可信的管理型 worker 使用。Owner 隔离属于纵深防御，不代替身份认证；应从可信的服务端身份生成，不能直接相信请求 JSON。

## 查询与分页

```go
active := false
page, err := manager.List(ctx, agentkit.SessionQuery{
	Tags:     []string{"support"}, // 提供的标签必须全部匹配
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

结果先按 `UpdatedAt` 从新到旧排列，再按 ID 排列。游标是不透明值，应原样传回。默认页大小为 `DefaultSessionPageSize`，硬上限为 `MaxSessionPageSize`。

内置内存和文件存储直接支持查询。已有自定义 `SessionStore` 不需要修改：公共 `QuerySessions` 函数会自动回退到 `List`。数据库后端应额外实现可选的 `SessionQueryStore`，在数据库中完成筛选和游标分页。

自定义查询后端返回数量不得超过 `Limit`，必须应用全部筛选条件、保持约定排序，并根据最后一条结果生成 `NextCursor`。AgentKit 会先校验这些约束以及 ID、时间戳、计数和游标结构，再把页面交给调用方。畸形结果返回 `ErrInvalidPersistenceData`；返回的标签切片也会复制，调用方无法修改后端缓存。

## 元数据、归档与 Fork

替换展示元数据时不会重写会话内容和运行状态：

```go
session, err := manager.UpdateMetadata(ctx, "conversation-42", agentkit.SessionMetadata{
	Title: "退款已批准",
	Tags:  []string{"support", "resolved"},
})
```

使用限定 Owner 的管理器时，替换值未填写 `OwnerID` 会自动保留管理器配置的 Owner。

Agent 正在运行时，`UpdateMetadata` 会等到完整运行和最终会话保存结束后再替换元数据。等待遵循调用方 context，也不会持久化半个工具调用回合。

归档属于生命周期操作，不等于删除：

```go
err = manager.Archive(ctx, "conversation-42")
err = manager.Unarchive(ctx, "conversation-42")
```

归档会先关闭活动 Agent。归档会话仍可查询，但 `Open` 会返回 `ErrSessionArchived`。`Delete` 是永久删除，同时会移除内置存储中的检查点、目标和大型工具结果。

Fork 会从一致的会话快照创建独立分支：

```go
branch, err := manager.Fork(ctx, "conversation-42", agentkit.CreateSessionOptions{
	Title: "另一种解决方案",
})
```

完整历史和压缩上下文会复制。运行状态不会复制：新分支使用自己的检查点 ID，也不会继承目标或大型工具结果。旧上下文里的卸载结果引用仍会作为历史文本存在，但新分支不能读取源会话的结果；仍需完整载荷时应重新执行相应工具。

存在待处理 HITL 中断的源会话不能 Fork，此时返回 `ErrResumeRequired`。应先恢复检查点，或使用 `ClearCheckpoint` 明确放弃。这样可以避免分支继承一条 assistant 工具调用消息，却没有完成该回合所需的检查点和工具结果。

源 Agent 正在运行时，`Fork` 会等待当前运行（包括队列中的 follow-up 工作和最终持久化）完全结束后再取快照。等待遵循 `Fork` 的 context；context 到期时不会创建目标会话，因此分支不会从半个工具调用回合开始。

## Agent 生命周期与并发

```go
err := manager.CloseSession(ctx, "conversation-42")
err = manager.Delete(ctx, "conversation-42")
err = manager.CloseContext(ctx)
```

服务需要统一接收所有已打开会话的事件时，只订阅管理器一次即可：

```go
unsubscribe := manager.Subscribe(func(event agentkit.Event) {
	logEvent(event.SessionID, event)
})
defer unsubscribe()
```

管理器会自动接入后续创建的 Agent，并在实例关闭时解除转发。通过 `agent.Subscribe` 直接订阅时，会话型 Agent 的事件同样携带 `SessionID`。单个 Agent 内保持事件顺序；不同会话可以同时发出事件，因此回调必须并发安全。

`CloseSession` 释放 Agent 和 MCP 资源但保留持久化数据；`Delete` 会先关闭再删除；关闭管理器会关闭全部活动 Agent，但不会删除任何会话。

即使持久化记录已经被其他进程删除，`Delete` 仍会关闭当前管理器跟踪的活动 Agent。该调用保持幂等，不会在会话已从存储消失后留下继续运行的本地实例。

应用已经持有会话 ID 时优先使用 `CloseSession`，这样能明确表达管理器生命周期。直接调用 `agent.Close` 也安全：Agent 完成清理后会通知管理器立即更新活动实例表，下次 `Open` 会创建新的 Agent。

同一会话的管理操作会串行执行，等待期间会响应 context 取消；不同会话不会互相阻塞。管理器只能防止当前管理器进程内出现重复活动实例，跨管理器或跨进程仍由 `Session.Revision` 阻止陈旧写入静默覆盖。多副本执行还需要在工作分发层基于数据库实现所有权或租约机制。

如果另一个管理器抢先写入 revision，失败方 Agent 会立即变为陈旧状态，并从 `ActiveSessionIDs` 中消失。再次对该 ID 调用 `Open` 会关闭失败实例并从最新持久化快照重建，因此重连代码不需要额外的冲突恢复分支。

活动实例表采用显式生命周期管理，不是不可见的缓存。客户端永久断开或应用级闲置策略淘汰时应关闭会话；短暂网络断开无需关闭，重连后调用 `Open` 会获得已有实例。

## 按会话创建 Agent

每个会话需要单独的模型客户端、凭据、工具或其他资源时使用 `AgentFactory`：

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

工厂必须使用传入的 `SessionConfig`；绑定到其他 ID 或其他可比较 `SessionStore` 的 Agent 会被管理器以 `ErrSessionFactoryMismatch` 拒绝。这能避免工厂配置错误导致管理器读取一份会话、Agent 却写入另一份存储。工厂 panic 会转换为 `ErrSessionFactoryPanic`。预连接的 MCP `Session` 由 Agent 独占并负责关闭，因此不能通过 `AgentConfig` 在多个 Agent 间复用；此时应使用 MCP 传输配置或工厂。

## 相关指南

- [会话与持久化](persistence.md)
- [持久化目标](goals.md)
- [上下文管理](context.md)
- [MCP 管理](mcp.md)
