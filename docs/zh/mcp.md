# MCP 管理

[English](../mcp.md) · [文档索引](README.md)

AgentKit 可以负责 MCP 连接从初始化到清理的完整生命周期：连接服务、分页发现全部工具、筛选和命名空间化、向模型暴露工具、在连接级故障后重连托管传输，并随 Agent 一起关闭会话。

## Streamable HTTP

```go
agent, err := agentkit.New(ctx, &agentkit.Config{
	Name:  "assistant",
	Model: chatModel,
	MCP: &agentkit.MCPConfig{
		InitializationTimeout: 30 * time.Second,
		Servers: []agentkit.MCPServerConfig{
			{
				Name:       "search",
				Transport:  agentkit.MCPTransportStreamableHTTP,
				URL:        "https://mcp.example.com/mcp",
				Headers:    map[string]string{"Authorization": "Bearer " + token},
				ToolNames:  []string{"search", "fetch"},
				ToolPrefix: "search__",
			},
		},
	},
})
if err != nil {
	log.Fatal(err)
}
defer agent.Close()
```

`ToolNames` 是可选白名单。请求的名称不存在时会初始化失败，不会静默遗漏。暴露的工具名必须在本地、Skill、搜索和 MCP 工具之间全局唯一；使用 `ToolPrefix` 建立命名空间。

## 本地 stdio 服务

```go
MCP: &agentkit.MCPConfig{
	Servers: []agentkit.MCPServerConfig{
		{
			Name:       "filesystem",
			Transport:  agentkit.MCPTransportStdio,
			Command:    "filesystem-mcp",
			Args:       []string{"--root", workspace},
			Env:        map[string]string{"LOG_LEVEL": "warn"},
			WorkingDir: workspace,
			ToolPrefix: "fs__",
		},
	},
}
```

配置的环境变量会与当前进程环境合并。旧式 SSE 服务也可使用 `MCPTransportSSE`。

## 初始化与发现

每个服务的连接和首次完整分页发现共用默认 30 秒截止时间。`InitializationTimeout` 可调整该限制；调用方 context 更早的截止时间仍然优先。后续服务失败时，已经打开的会话也使用同一有界时长清理。若自定义会话忽略 `Close`，清理会留在后台继续，避免 `New` 永久等待。

工具只在 `agentkit.New` 时发现一次。服务增删工具后需要重建 Agent。

## 结果与描述限制

MCP 工具结果默认最多 `DefaultMCPMaxResultChars`（100,000 个 Unicode 字符），描述默认最多 `DefaultMCPMaxDescriptionChars`（4,000 个字符）。对应 `MCPConfig` 字段可以设为：

- 正数：自定义上限；
- `-1`：关闭上限；
- 零：使用默认值。

启用[工具结果压缩](tools.md#大型工具结果压缩)后，AgentKit 会关闭 MCP 默认结果上限，让完整内容进入持久化。显式设置的 MCP 正数上限仍然优先，超过部分会按用户意图丢弃。

## 认证

静态 `Headers` 会在初始化时复制。凭证会轮换或需要动态刷新时，传入带认证 `RoundTripper` 的自定义 `HTTPClient`。不要把凭证提交到源代码或配置文件。

## 重连与所有权边界

对于 AgentKit 根据传输配置创建的会话，连接级工具故障会让当前托管会话失效并允许重连。AgentKit 拥有这些会话，并在初始化失败或 `Agent.Close`/`CloseContext` 时关闭它们。

高级调用方也可以直接提供已经连接的 `MCPClientSession`。AgentKit 仍会取得所有权并负责关闭，但重连能力由自定义会话实现负责。传入自定义会话并不意味着 AgentKit 能重建一个未知传输。

优雅停机时，如果要同时限制当前运行和 MCP 清理的等待时间，使用 `CloseContext`。若自定义关闭逻辑忽略 context，仅一次的清理会在截止后留在后台继续。

## 相关指南

- [工具管理](tools.md)
- [运行时与事件](runtime.md)
- [技能管理](skills.md)
