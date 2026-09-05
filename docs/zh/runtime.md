# 运行时与事件

[English](../runtime.md) · [文档索引](README.md)

Agent 运行时提供小型阻塞 API、请求级事件流、全局观察器、明确的生命周期控制和可恢复 HITL。本指南用于选择运行方法，并正确管理其状态。

## Agent 配置

```go
agent, err := agentkit.New(ctx, &agentkit.Config{
	Name:                "my-agent",
	Description:         "Agent 描述",
	SystemPrompt:        "系统指令",
	Model:               chatModel,
	Tools:               []agentkit.Tool{myTool},
	ToolPolicy:          &agentkit.ToolPolicy{Sequential: true},
	Handlers:            []agentkit.ChatModelAgentMiddleware{myHandler},
	ModelRetryConfig:    &agentkit.ModelRetryConfig{MaxRetries: 2},
	ModelFailoverConfig: failoverConfig,
	PersistenceTimeout:  30 * time.Second,
	MaxIterations:       20,
})
if err != nil {
	log.Fatal(err)
}
defer agent.Close()
```

通常只需要 `Name` 和 `Model`。其他能力分别放在专项配置中。自定义 Handler 会受到保护：hook、包装端点和返回流 panic 会变成包装 `ErrMiddlewarePanic` 的错误。

## 选择运行方法

```go
err := agent.Prompt(ctx, "用户消息")

result, err := agent.Ask(ctx, "用户消息")
fmt.Println(result.Text, result.Usage)

stream, err := agent.Stream(ctx, "用户消息")
for event := range stream.Events() {
	// 渲染文本、推理、工具或进度。
}
result, err = stream.Wait()
```

- `Prompt` 阻塞执行且只返回错误，适合由状态或订阅器消费输出的场景。
- `Ask` 阻塞执行并返回 `RunResult`，包含最终文本/消息、本次新增消息、累计用量、工具调用和待处理中断。
- `Stream` 返回请求级事件流，返回前会占用 Agent，并提供 `Cancel`、`Done`、`Wait`、`WaitContext` 和 `Close`。

`WaitContext` 只限制等待，不取消底层运行。请求事件流通过内部队列隔离慢消费者，不会阻塞 Agent。多模态版本是 `StreamParts`。

不添加新用户消息而从现有状态继续时，使用 `Continue` 或 `ContinueWithResult`；恢复 HITL 检查点使用 `Resume` 或 `ResumeWithResult`。

## 互斥与生命周期

同一个 Agent 上的 `Prompt`、`Send`、`Continue` 和 `Resume` 互斥执行，并发尝试返回 `ErrAgentRunning`。`GoalRunner` 在整个目标周期内使用同一条独占执行通道，包括工作步骤之间的判断阶段。HITL 后，在检查点恢复或明确清除前，新运行返回 `ErrResumeRequired`，避免未完成工具工作被静默遗弃。

```go
agent.Cancel()                      // 非阻塞；可在订阅回调中调用
agent.Abort()                       // 取消并等待；在订阅回调外调用
err := agent.AbortContext(stopCtx)  // 取消，再限制等待

agent.Reset()                       // 等待后清空历史与队列
err = agent.CloseContext(stopCtx)   // 禁止新运行；限制运行与 MCP 清理等待
```

`Cancel`、`AbortContext` 和 `CloseContext` 也会停止当前独占 Agent 的 `GoalRunner`。`AbortContext` 总会先发出取消。如果自定义模型、判断器或工具忽略 context，它可能在底层仍退出时先返回停机 context 错误；Agent 会保持占用直到运行真正结束。`CloseContext` 会立即禁止新运行，并在等待截止后继续于后台完成仅一次的 MCP 清理。

## 请求级配置

共享 Agent 保持稳定，单次请求通过 context 定制：

```go
runCtx := agentkit.WithRunConfig(ctx, agentkit.RunConfig{
	ModelOptions: []agentkit.ModelOption{
		model.WithTemperature(0.2),
		model.WithMaxTokens(2_000),
	},
	Values: map[string]any{
		"user_name": "Alice",
		"request_id": "req-42",
	},
})

result, err := agent.Ask(runCtx, "请总结这段内容")
```

运行值会填充 `SystemPrompt` 中的 `{user_name}` 占位符。工具和中间件可通过 `RunValue[T]` 读取类型值，通过 `RunValues` 获得副本，并用 `SetRunValue` 更新同一次底层运行后续可见的值。`ToolOptions` 是自定义工具的请求级扩展入口。`WithRunConfig` 会复制输入容器。

重试和故障转移回调都会隔离 panic。返回错误的控制回调会包装 `ErrModelPolicyPanic`；建议型回调会安全退化并发出 `EventError`。

## 事件参考

| 事件 | 含义与常用字段 |
| --- | --- |
| `EventAgentStart` | Agent 开始执行 |
| `EventTurnStart` | 下一次模型请求前开始新一轮 |
| `EventMessageStart` | 消息开始；查看 `Role` |
| `EventReasoningDelta` | `Delta` 中的推理增量 |
| `EventMessageDelta` | `Delta` 中的助手文本增量 |
| `EventMessageEnd` | `Content` 中的完整消息及 `ResponseMeta` |
| `EventToolStart` | `ToolCalls` 中的调用请求 |
| `EventToolUpdate` | `Content` 中的进度，由 `ToolCallID` 标识 |
| `EventToolEnd` | 结果、名称、参数与 ID |
| `EventTurnEnd` | 助手消息和工具结果处理完成 |
| `EventTransfer` | 多 Agent 转移 |
| `EventInterrupted` | `Interrupt` 中的 HITL 中断点 |
| `EventCompactionStart` / `EventCompactionEnd` | `Compaction` 中的压缩数量 |
| `EventGoalUpdate` | `Goal` 中已提交的目标快照 |
| `EventDelegationStart` / `EventDelegationEnd` | `Delegation` 中的有界子智能体生命周期；最终失败位于 `Error` |
| `EventAgentEnd` | Agent 执行结束 |
| `EventError` | `Error` 中的非终止或终止错误 |

`agent.Subscribe` 注册全局观察器并返回取消订阅函数。绑定会话的 Agent 所产生事件会携带稳定的 `SessionID`。事件容器和内置可变字段会为每个回调复制；`InterruptPoint.Info` 内常见的 JSON-like map、切片和字节数据也会递归复制。存入 `Info` 的自定义不透明指针值仍应视为不可变。全局回调同步执行，应尽快返回。回调 panic 会被隔离，其他订阅者会收到包装 `ErrSubscriberPanic`、并保留原 Agent 与会话标识的 `EventError`。需要汇聚多个会话时使用 `SessionManager.Subscribe`。

工具无需知道订阅者即可报告进度：

```go
func myTool(ctx context.Context, input string) (string, error) {
	agentkit.EmitToolUpdate(ctx, "正在处理第 1 步...")
	return "result", nil
}
```

## 转向与后续消息队列

```go
agent.Steer("改为重点关注 X")
agent.FollowUp("另外检查 Y")

agent.SetSteeringMode(agentkit.QueueModeAll)
agent.SetFollowUpMode(agentkit.QueueModeOneAtATime)

agent.ClearSteeringQueue()
agent.ClearFollowUpQueue()
agent.ClearAllQueues()
```

转向消息会在当前工具批次结束后检查，用于重定向当前工作；后续消息会在当前任务结束后执行，默认逐条处理。

## HITL 人机协作

工具可以中止执行并保存中断：

```go
return "", agentkit.Interrupt(ctx, "需要用户确认")

return "", agentkit.StatefulInterrupt(ctx, "确认？", myState)
```

恢复后，工具可以取得原状态和用户提交值：

```go
wasInterrupted, hasState, state := agentkit.GetInterruptState[MyState](ctx)
isTarget, hasData, data := agentkit.GetResumeContext[bool](ctx)
```

使用 `PendingInterrupts` 展示待处理 ID；只有应用明确放弃它们时才调用 `ClearCheckpoint`。清除时会先为每个未完成调用加入明确的合成工具结果，再轮换检查点；后续 Prompt 或 Fork 因此会保留提供商可接受的完整对话，而不是一条孤立的 assistant 工具调用。跨重启恢复需配置[会话与检查点](persistence.md)。

## 多模态输入

```go
result, err := agent.AskParts(ctx,
	agentkit.Text("描述这张图片"),
	agentkit.ImageURL(imageURL, agentkit.ImageDetailHigh),
)
```

只需错误结果时可使用 `Send`。构造函数包括 `Text`、`ImageURL`、`ImageBase64`、`AudioURL`、`AudioBase64`、`VideoURL`、`VideoBase64`、`FileURL` 和 `FileBase64`。最终是否接受某种内容仍取决于所配置的模型提供商。

## Eino 类型别名

AgentKit 为常用 Eino 类型提供别名，应用通常只需导入一个包：`ChatModel`、`Tool`、`ToolMiddleware`、`ToolInput`、`ToolOutput`、`ToolCall`、`ResponseMeta`、`TokenUsage`、`ContentPart` 和 `ImageURLDetail`。

## 相关指南

- [会话与持久化](persistence.md)
- [工具管理](tools.md)
- [子智能体](subagents.md)
- [测试](testing.md)
