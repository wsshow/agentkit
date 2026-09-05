# AgentKit

[![CI](https://github.com/wsshow/agentkit/actions/workflows/ci.yml/badge.svg)](https://github.com/wsshow/agentkit/actions/workflows/ci.yml)

[English](README.md)

AgentKit 是一个轻量级、事件流驱动的 Go Agent 开发库，基于 [CloudWeGo Eino ADK](https://github.com/cloudwego/eino) 构建。第一个 Agent 足够简单；当应用逐步成长时，又可以按需加入会话、持久化目标、上下文压缩、技能、MCP 和工具治理。

项目灵感来源于 [pi-agent-core](https://github.com/earendil-works/pi/tree/main/packages/agent)，重点是更简单的公共 API 与适合生产环境的安全默认值。

## 为什么选择 AgentKit

- **上手简单** — 创建 Agent 后直接调用 `Ask`，不要求用户先理解图编排或中间件。
- **易于观察** — 通过请求级事件流或全局事件观察文本、推理、工具、压缩、目标、中断和错误。
- **适合长时间运行** — 持久化会话、检查点、目标与大型工具结果；客户端断线或进程重启后可按稳定 ID 重连。
- **默认安全** — 内置并发运行保护、panic 隔离、有界清理、工具调用修复、结果限长和乐观并发控制。
- **按需组合** — 技能、MCP、工具搜索、结果压缩、重试/故障转移、HITL 和多模态能力互不捆绑。

## 安装

AgentKit 需要 Go 1.25.14 或更高版本。

```bash
go get github.com/wsshow/agentkit@latest
```

## 五分钟上手

```go
package main

import (
	"context"
	"fmt"
	"log"

	"github.com/cloudwego/eino-ext/components/model/openai"
	"github.com/wsshow/agentkit"
)

func main() {
	ctx := context.Background()

	chatModel, err := openai.NewChatModel(ctx, &openai.ChatModelConfig{
		APIKey: "your-api-key",
		Model:  "gpt-4o",
	})
	if err != nil {
		log.Fatal(err)
	}

	agent, err := agentkit.New(ctx, &agentkit.Config{
		Name:         "assistant",
		SystemPrompt: "你是一个有用的助手。",
		Model:        chatModel,
	})
	if err != nil {
		log.Fatal(err)
	}
	defer agent.Close()

	result, err := agent.Ask(ctx, "你好！")
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(result.Text)
}
```

`Ask` 是最简单的阻塞式 API。需要实时文本和工具进度时，使用 `Stream`：

```go
stream, err := agent.Stream(ctx, "解释一下 MCP")
if err != nil {
	log.Fatal(err)
}
defer stream.Close()

for event := range stream.Events() {
	if event.Type == agentkit.EventMessageDelta {
		fmt.Print(event.Delta)
	}
}
result, err := stream.Wait()
```

完整运行 API、生命周期规则、HITL、队列和多模态输入请阅读[运行时与事件](docs/zh/runtime.md)。

## 按需选择能力

| 需求 | 从这里开始 |
| --- | --- |
| 运行方法、事件、取消、HITL、队列、多模态输入 | [运行时与事件](docs/zh/runtime.md) |
| 进程重启后恢复对话和检查点 | [会话与持久化](docs/zh/persistence.md) |
| 运行数小时或数天的多步骤目标，并安全重连 | [持久化目标](docs/zh/goals.md) |
| 让长对话保持在模型上下文窗口内 | [上下文管理](docs/zh/context.md) |
| 按需加载可复用的 `SKILL.md` 指令 | [技能管理](docs/zh/skills.md) |
| 连接 stdio、SSE 或 Streamable HTTP MCP 服务 | [MCP 管理](docs/zh/mcp.md) |
| 治理工具、修复调用、压缩大型结果或搜索工具目录 | [工具管理](docs/zh/tools.md) |
| 不调用真实模型和外部工具完成测试 | [测试](docs/zh/testing.md) |

[文档索引](docs/zh/README.md)提供推荐阅读路径和相关主题链接。

## 实用的生产基线

大多数有状态 Agent 都适合从持久化会话和自动上下文压缩开始。工具可能返回大型内容时，再启用结果压缩：

```go
store, err := agentkit.NewFileSessionStore("./data/agent")
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
	Compaction: &agentkit.CompactionConfig{
		MaxTokens:       80_000,
		KeepRecentTurns: 2,
	},
	ToolReduction: &agentkit.ToolReductionConfig{},
})
```

文件存储面向本地单进程 worker。多副本服务应使用具备事务语义的数据库实现持久化接口，详见[会话与持久化](docs/zh/persistence.md)和[持久化目标](docs/zh/goals.md)。

## 内置工具中间件取舍

AgentKit 直接封装了三项真正能减少重复工作的能力，用户无需接触 Eino 中间件接线：

- 悬空工具调用修复默认开启，因为合法历史格式属于正确性要求。
- 大型结果压缩采用一个零值安全的可选配置，因为它会改变存储方式和模型看到的内容。
- 按需工具搜索保持可选；它适合大型工具目录，但对小型目录只会增加一次模型决策。

默认值、执行顺序和扩展点详见[工具管理](docs/zh/tools.md)。

## 示例

| 示例 | 内容 |
| --- | --- |
| [simple](examples/simple/) | 最简多轮对话 |
| [tools](examples/tools/) | 工具调用和进度事件 |
| [history](examples/history/) | 手动导出并恢复历史 |
| [session](examples/session/) | 自动跨进程恢复会话 |
| [goal](examples/goal/) | 持久化目标执行和重连 |
| [compaction](examples/compaction/) | 自动上下文压缩 |
| [skills](examples/skills/) | 发现并加载本地 `SKILL.md` |
| [mcp](examples/mcp/) | Streamable HTTP MCP 集成 |
| [queues](examples/queues/) | 转向与后续消息队列 |
| [hitl](examples/hitl/) | 人工中断和恢复 |
| [multimodal](examples/multimodal/) | 文本与图片输入 |

## 项目

- [参与贡献](CONTRIBUTING.md)
- [安全策略](SECURITY.md)
- [许可证](LICENSE)
