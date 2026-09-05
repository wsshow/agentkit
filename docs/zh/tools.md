# 工具管理

[English](../tools.md) · [文档索引](README.md)

AgentKit 可直接使用 Eino 兼容工具，并通过一层统一策略处理分发、安全、可观察性、大型结果和大型工具目录。常见路径不要求用户构造 Eino `ToolsNode` 或手动连接中间件。

## 工具策略

```go
ToolPolicy: &agentkit.ToolPolicy{
	Sequential:    true,
	Timeout:       30 * time.Second,
	MaxResultChars: 50_000,
	Aliases: map[string]agentkit.ToolAlias{
		"web_search": {
			Names: []string{"search"},
			Arguments: map[string][]string{
				"query": {"q", "keywords"},
			},
		},
	},
	RewriteArguments: func(ctx context.Context, name, arguments string) (string, error) {
		return validateAndNormalize(arguments)
	},
	UnknownTool: func(ctx context.Context, name, arguments string) (string, error) {
		return "该工具不可用，请选择已经注册的工具。", nil
	},
	BeforeTool: func(ctx context.Context, call agentkit.ToolInvocation) error {
		return authorize(call.Name, call.Arguments)
	},
	AfterTool: func(ctx context.Context, call agentkit.ToolInvocation, outcome agentkit.ToolOutcome) {
		recordToolRun(call, outcome)
	},
}
```

工具默认并行运行。只有顺序确实重要或依赖不支持并发时才设置 `Sequential`。钩子必须并发安全。`BeforeTool` 可拒绝调用，`AfterTool` 会收到耗时、错误、保留文本大小和截断元数据。

`Timeout` 使用 context 协作取消；自定义工具应在 `ctx.Done()` 关闭后及时停止。调用下游时不要传 nil context，应传入工具 context；确实无法确定生命周期时使用 `context.TODO()`。

## 别名与未知工具

别名可以统一模型使用的其他工具名和参数名。`agentkit.New` 会根据全部本地、Skill、MCP 和动态搜索工具校验别名，冲突或不存在的正式目标会立即失败。

`RewriteArguments` 适合跨工具的参数校验与规范化。`UnknownTool` 可以返回模型可读的恢复提示，而不是立即终止运行。

## 结果限制与 panic 隔离

文本结果默认最多 `DefaultToolResultMaxChars`（100,000 个 Unicode 字符），截断时附加提示。设置 `MaxResultChars: -1` 可关闭限制。如果完整内容必须保留，应使用[大型结果压缩](#大型工具结果压缩)，而不是无限提高上限。

工具实现或中间件端点（包括读取返回流时）的 panic 会转换为包装 `ErrToolExecutionPanic` 的错误。策略控制回调和中间件工厂 panic 会转换为包装 `ErrToolPolicyPanic` 的错误。`AfterTool` panic 通过 `EventError` 上报，不会覆盖成功结果。这些保护覆盖普通、流式和多模态工具。

高级调用方可通过 `ToolPolicy.Middlewares` 加入 `agentkit.ToolMiddleware`。

## 自动修复悬空调用

每次模型请求前，AgentKit 都会修复没有配对结果的工具调用。如果取消或中断导致 assistant 工具调用缺少对应结果消息，会加入合成结果，确保 OpenAI 兼容接口接收到合法历史。

该能力修复的是协议结构，也不涉及应用存储选择，因此始终开启。应用无需再配置等价的 `patchtoolcalls` 中间件。

## 大型工具结果压缩

使用一个零值安全配置即可启用持久化卸载：

```go
ToolReduction: &agentkit.ToolReductionConfig{}
```

单个结果超过 50,000 字节时，模型上下文中的内容会被替换为简短预览和不透明结果 ID。AgentKit 会注册安全、只读的 `read_tool_result` 工具，每次最多返回 20,000 个 Unicode 字符，并提供 `next_offset` 继续读取。

上下文估算超过 160,000 tokens 时，较旧的工具轮次也会被卸载，最近一轮保持完整。可通过 `MaxResultBytes`、`MaxContextTokens` 和 `KeepRecentToolRounds` 调整。

### 存储行为

无需额外接线：

1. reduction 优先使用 `Session` 提供的 `ToolResultStoreProvider`；
2. 否则回退到并发安全的内存存储。

因此 `NewFileSessionStore` 会让卸载结果自动跨进程重启保留。只有自定义后端才需设置 `Store`。应用可通过 `agent.ToolResultStore()` 管理独立结果的保留周期。内置会话删除会清理会话所属结果；手动保存且 `SessionID` 为空的结果保持独立。

启用后，结果大小由 reduction 负责，超时、钩子、别名等其他策略仍然生效。它先于完整[上下文压缩](context.md)执行，避免摘要模型无谓消耗。MCP 上限交互详见 [MCP 结果限制](mcp.md#结果与描述限制)。

## 按需工具搜索

常用工具保留在 `Config.Tools`；大型专业目录放进一个可选配置：

```go
ToolSearch: &agentkit.ToolSearchConfig{
	Tools: []agentkit.Tool{
		lookupWeather,
		searchTickets,
		queryWarehouse,
	},
}
```

对于该目录，模型开始时只看到 `tool_search` 元工具。搜索命中后才会显示对应工具，它们仍经过相同的超时、别名、钩子、结果处理和中间件。普通 `Config.Tools` 始终可见。

工具搜索会增加一次模型决策。小型目录应直接使用 `Tools`；只有工具 schema 明显占用上下文或干扰选择时才启用搜索。仅在提供商实现原生工具搜索时设置 `UseModelNative: true`。启用该功能后，`tool_search` 是保留名称。

## 应该启用哪些能力

| 能力 | 默认 | 启用时机 |
| --- | --- | --- |
| 悬空调用修复 | 始终开启 | 所有 Agent 都需要合法协议历史 |
| `ToolPolicy` | 可选 | 需要超时、串行、别名、鉴权、审计或自定义结果限制 |
| `ToolReduction` | 可选 | 结果可能很大，但完整内容仍需恢复 |
| `ToolSearch` | 可选 | 大型目录会明显占用模型上下文 |

## 相关指南

- [MCP 管理](mcp.md)
- [上下文管理](context.md)
- [运行时与事件](runtime.md)
