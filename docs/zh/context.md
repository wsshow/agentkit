# 上下文管理

[English](../context.md) · [文档索引](README.md)

AgentKit 会为应用保留完整对话，同时独立压缩实际发送给模型的上下文。这样既能保留 UI 和审计记录，也不会被迫把全部历史反复塞进模型窗口。

## 启用自动压缩

```go
agent, err := agentkit.New(ctx, &agentkit.Config{
	Name:  "assistant",
	Model: chatModel,
	Compaction: &agentkit.CompactionConfig{
		MaxTokens:       80_000,
		MaxMessages:     100,
		KeepRecentTurns: 2,
		Model:           summaryModel,
	},
})
```

`MaxTokens` 与 `MaxMessages` 是两个独立触发条件，任意一个超过阈值都会开始压缩。二者都不设置时，AgentKit 使用 `DefaultCompactionMaxTokens`（估算 100,000 tokens）。`KeepRecentTurns` 默认为 1，最近的用户轮次会原样保留。`Model` 可选，默认复用 Agent 主模型。

token 阈值应低于模型的硬性上下文窗口，同时为下一次输入、工具定义和模型回复留出空间。

## 两种历史视图

- `agent.History()` 返回未删减的完整对话，适合 UI、审计和导出。
- `agent.ContextHistory()` 返回实际发送给模型的压缩上下文。
- 配置 `Session` 后，两种视图都会持久化。Agent 重建后不会意外把完整历史重新发送给模型。

`SetHistory` 会替换完整历史并同步展示状态。需要让修改跨重启保留时，随后调用 `SaveSession`。

## 失败与可观察性

压缩模型会复用 Agent 的 `ModelRetryConfig` 和 `ModelFailoverConfig`，即使选择了独立摘要模型也一样。策略全部耗尽后仍然失败时，AgentKit 返回错误并保持原上下文不变。

可订阅 `EventCompactionStart` 与 `EventCompactionEnd`；其中的 `Event.Compaction` 会报告压缩前后的消息数量。

## 与大型工具结果压缩的关系

上下文压缩负责总结对话；[工具结果压缩](tools.md#大型工具结果压缩)则把完整的大型工具结果移入存储，再给模型提供有界读取工具。二者同时启用时，结果压缩先执行，摘要模型无需先吞入大块旧工具结果。

实用选择是：持久化对话 Agent 默认启用上下文压缩；只有工具可能返回大型文档、日志、搜索结果或数据集时，再启用结果压缩。

## 相关指南

- [会话与持久化](persistence.md)
- [工具管理](tools.md)
- [运行时与事件](runtime.md)
