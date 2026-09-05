# 子智能体

[English](../subagents.md) · [文档索引](README.md)

子智能体让一个协调 Agent 把专业工作委派出去，应用无需构造图，也不需要手动把 Agent 包成工具。它刻意采用声明式设计：一次定义稳定的专家，AgentKit 自动把每个专家暴露为工具，由名称和描述引导主模型选择。

## 最小配置

```go
agent, err := agentkit.New(ctx, &agentkit.Config{
	Name:         "coordinator",
	SystemPrompt: "需要专业证据时，把调研工作委派出去。",
	Model:        chatModel,
	SubAgents: []agentkit.SubAgentConfig{
		{
			Name:         "researcher",
			Description:  "调研一个明确问题并返回证据",
			SystemPrompt: "回答简洁，并区分事实与不确定内容。",
			// 省略 Model：继承协调 Agent 的模型。
			Tools: []agentkit.Tool{webSearch},
		},
	},
})
```

不需要编写路由回调。协调 Agent 会看到一个名为 `researcher` 的工具，它只有一个必填字符串参数 `request`。例如调用 `{"request":"比较两个 API"}` 会启动隔离的子运行，并把子智能体最终文本作为工具结果返回。

`Name` 和 `Description` 必填，名称必须唯一。它与根工具、Skills、MCP 工具、Tool Search、别名或其他子智能体冲突时，`agentkit.New` 会立即报错，不会把问题留到运行时。

## 隔离与继承

默认采用安全隔离：

| 能力 | 默认行为 |
| --- | --- |
| 对话 | 子智能体只收到本次委派请求 |
| 模型 | `Model` 为 nil 时继承协调 Agent 模型 |
| 重试与故障转移 | 省略时继承协调 Agent 策略 |
| 工具 | 永不继承；只看到自己的 `SubAgentConfig.Tools` |
| Skills、MCP、Tool Search | 每个子智能体独立配置 |
| Session 与上下文压缩 | 由协调 Agent 统一拥有，不为每个子智能体复制 |
| 最终结果 | 作为普通工具结果返回协调 Agent |
| Token 用量 | 子模型用量自动加入本次 `RunResult.Usage` |

只有专家确实需要主对话时才设置 `IncludeHistory: true`。这会把父级历史明确共享给该子智能体，应把它当作隐私和 token 成本方面的主动选择。

每个子智能体可以覆盖 `Model`、`ModelRetryConfig`、`ModelFailoverConfig`、`MaxIterations` 和 `ToolPolicy`。子工具会收到正常的请求 context，可以使用 `RunValue`、`SetRunValue`、`EmitToolUpdate` 和 HITL 辅助函数。协调 Agent 的 `ToolReduction` 可在子智能体返回后卸载其最终结果；子智能体内部工具结果仍由自己的 `ToolPolicy.MaxResultChars` 限制。

## 有界执行

只要配置了子智能体，AgentKit 就会应用安全的进程内默认值：

```go
SubAgentPolicy: &agentkit.SubAgentPolicy{
	MaxDelegations: 8,                // 每次新的顶层运行
	MaxParallel:    4,                // 不同专家之间的并发数
	Timeout:        10 * time.Minute, // 每次委派
}
```

零值选择上述默认值，负值会被拒绝。`MaxParallel` 还会限制在已配置专家数量以内。同一个专家已有重叠调用时，第二个调用返回 `ErrSubAgentBusy`，避免一个子运行时被意外并发修改；不同专家可以并行。

超时和取消采用 context 协作机制，模型与工具必须在 context 取消后及时退出。`ErrSubAgentBudgetExceeded`、`ErrSubAgentBusy`、截止时间错误，以及已经防护的模型/工具 panic，都会走正常运行错误链路，也会出现在对应的委派结束事件上。

## 事件与关联

每次委派都有稳定的 `DelegationInfo`：

```go
agent.Subscribe(func(event agentkit.Event) {
	if event.Delegation == nil {
		return
	}
	fmt.Printf("%s: %s via %v\n",
		event.Delegation.ID,
		event.Agent,
		event.Delegation.Path,
	)
})
```

- 协调 Agent 请求子智能体时发出 `EventDelegationStart`。
- 子智能体的消息、推理、工具、进度、转移和错误事件都携带同一个 `Event.Delegation`。只有一个委派处于活动状态时，嵌套 HITL 的 `EventInterrupted` 也会携带它。
- 委派失败时，`EventDelegationEnd` 的 `Error` 携带最终错误。
- 协调 Agent 对应的 `EventToolEnd` 也携带该委派信息。

`DelegationInfo.ID` 与协调 Agent 的工具调用 ID 相同，`ParentAgent`、`Agent` 和 `Path` 标识委派路径。每个订阅者拿到的事件都是深拷贝快照。

子事件只用于观察，不会写入协调 Agent 的 `State`、`History`、Session 消息或模型上下文。只有最终子工具结果进入父级历史，从而避免重复内容，并保持合法的 assistant/tool 消息顺序。

## HITL、Session 与持久化目标

子工具可以像根工具一样调用 `Interrupt` 或 `StatefulInterrupt`。Eino 的嵌套检查点会桥接到协调 Agent 的检查点。配置 `Session` 后，应用可以：

1. 接收 `EventInterrupted`，同时让 Session 自动持久化；
2. 关闭当前 Agent 或重启进程；
3. 使用相同 Session ID 和相同的协调/子智能体配置重建 Agent；
4. 使用待处理中断 ID 调用 `Resume` 或 `ResumeWithResult`。

恢复后的子智能体会保留工具状态，并完成原来的协调 Agent 工具调用。子智能体也可直接用于 `GoalRunner`；Goal 只在安全步骤边界后持久化，并可通过目标 ID 重连。

进程在普通模型或工具执行中途突然退出时，无法重建尚未提交的外部副作用。持续数小时或数天的工作应使用[持久化目标](goals.md)，为外部副作用使用幂等键，并在有意义的边界加入 HITL/状态检查点。进程退出后仍需要 worker 或 supervisor 重新启动任务。

## 刻意保留的边界

首个稳定 API 只提供静态的一层“协调者到专家”关系。子智能体不能动态创造持久 worker，也不能继续声明自己的 `SubAgents`。这样工具可见性、成本、权限、检查点和并发都更容易理解。应用确实需要第二层编排边界时，应明确构造另一个协调 Agent。

子智能体是一轮隔离委派，不是独立的长期聊天 Session。持久用户对话属于协调 Agent 的 `Session`；持久自治进度属于 `GoalRunner`。

## 相关指南

- [运行时与事件](runtime.md)
- [会话与持久化](persistence.md)
- [持久化目标](goals.md)
- [工具管理](tools.md)
- [MCP 管理](mcp.md)
