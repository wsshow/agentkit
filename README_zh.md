# AgentKit

[![CI](https://github.com/wsshow/agentkit/actions/workflows/ci.yml/badge.svg)](https://github.com/wsshow/agentkit/actions/workflows/ci.yml)

[English](README.md)

轻量级、事件流驱动的 Agent 工具包，基于 [CloudWeGo Eino ADK](https://github.com/cloudwego/eino) 构建。

灵感来源于 [pi-agent-core](https://github.com/earendil-works/pi/tree/main/packages/agent)，在 Go + Eino 生态下实现事件流、消息队列和 HITL（人机协作）机制。

## 特性

- **事件流架构** — 订阅细粒度事件（消息增量、工具调用、错误等）
- **转向与后续消息队列** — 在执行过程中注入消息以重定向 Agent 或追加后续任务
- **人机协作（HITL）** — 中断 Agent 执行并在用户提供数据后恢复
- **流式输出** — 通过 Eino ADK 流式传输实时逐 token 输出
- **推理模型支持** — 原生支持思考/推理模型（DeepSeek-R1、o1 等），流式输出推理过程
- **多模态输入** — 通过 `Send()` 发送文本、图片、音频、视频、文件，配套简洁构造函数
- **会话持久化** — 自动保存和恢复完整对话，内置并发安全的内存与原子文件存储
- **自动上下文压缩** — 超过 token 或消息阈值时自动摘要，完整历史与模型上下文分离保存
- **按需技能** — 从本地目录或自定义后端加载可复用的 `SKILL.md` 指令
- **MCP 连接管理** — 连接 stdio、SSE、Streamable HTTP 服务器，自动发现、重连、筛选并释放资源
- **工具集成** — 接入任何 Eino 兼容工具，自动处理工具调用
- **类型别名** — 直接使用 `agentkit.ChatModel`、`agentkit.Tool`、`agentkit.ToolCall` 等，无需直接导入 eino 包

## 安装

AgentKit 需要 Go 1.25.14 或更高版本。

```bash
go get github.com/wsshow/agentkit@latest
```

## 快速开始

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
		APIKey:  "your-api-key",
		BaseURL: "https://api.openai.com/v1",
		Model:   "gpt-4o",
	})
	if err != nil {
		log.Fatalln(err)
	}

	agent, err := agentkit.New(ctx, &agentkit.Config{
		Name:         "assistant",
		SystemPrompt: "你是一个有用的助手。",
		Model:        chatModel,
	})
	if err != nil {
		log.Fatalln(err)
	}
	defer agent.Close()

	agent.Subscribe(func(e agentkit.Event) {
		switch e.Type {
		case agentkit.EventReasoningDelta:
			fmt.Print(e.Delta) // 推理/思考过程的流式输出（推理模型）
		case agentkit.EventMessageDelta:
			fmt.Print(e.Delta)
		case agentkit.EventMessageEnd:
			fmt.Println()
		case agentkit.EventError:
			fmt.Printf("错误: %v\n", e.Error)
		}
	})

	if err := agent.Prompt(ctx, "你好！"); err != nil {
		log.Fatalln(err)
	}
}
```

## 事件类型

| 事件                  | 说明                                                                        |
| --------------------- | --------------------------------------------------------------------------- |
| `EventAgentStart`     | Agent 开始处理                                                              |
| `EventTurnStart`      | 新一轮开始，发生在下一次模型请求前                                          |
| `EventMessageStart`   | 消息开始（通过 `Event.Role` 区分用户、助手、工具消息）                      |
| `EventReasoningDelta` | 推理/思考过程流式增量（`Event.Delta`），仅推理模型                          |
| `EventMessageDelta`   | 流式增量文本（`Event.Delta`）                                               |
| `EventMessageEnd`     | 消息结束（`Event.Role`、`Event.Content`、`Event.ResponseMeta`）             |
| `EventToolStart`      | 工具调用请求（`Event.ToolCalls`）                                           |
| `EventToolUpdate`     | 工具执行进度更新（`Event.ToolCallID`、`Event.Content`）                     |
| `EventToolEnd`        | 工具调用结果返回（`Event.ToolCallID`、`Event.ToolName`、`Event.Content`）   |
| `EventTurnEnd`        | 助手消息和工具结果都处理完后，一轮结束                                      |
| `EventTransfer`       | Agent 转移（多 Agent 场景）                                                 |
| `EventInterrupted`    | HITL 中断（`Event.Interrupt`）                                              |
| `EventCompactionStart` | 自动上下文压缩开始（`Event.Compaction.MessagesBefore`）                    |
| `EventCompactionEnd`  | 自动上下文压缩完成（`Event.Compaction`）                                    |
| `EventAgentEnd`       | Agent 处理完成                                                              |
| `EventError`          | 发生错误（`Event.Error`）                                                   |

### Event 结构体

```go
type Event struct {
    Type             EventType
    Agent            string           // 产生事件的 Agent 名称
    Role             RoleType         // 消息角色（message_start / message_end）
    Content          string           // 文本内容（message_end / tool_end）
    Delta            string           // 流式增量内容（message_delta / reasoning_delta）
    ReasoningContent string           // 完整推理内容（message_end，仅推理模型）
    ResponseMeta     *ResponseMeta    // 响应元数据：token 用量、完成原因（message_end）
    ToolCalls        []ToolCall       // 工具调用列表（tool_start）
    ToolCallID       string           // 工具调用 ID（tool_update / tool_end）
    ToolName         string           // 工具名称（tool_update / tool_end）
    ToolArguments    string           // 工具调用参数（tool_update / tool_end）
    Interrupt        []InterruptPoint // 中断点列表（interrupted）
    Compaction       *CompactionInfo  // 上下文压缩前后的消息数量
    Error            error            // 错误信息（error）
}
```

## API 参考

### 创建 Agent

```go
agent, err := agentkit.New(ctx, &agentkit.Config{
    Name:            "my-agent",
    Description:     "Agent 描述",
    SystemPrompt:    "系统指令",
    Model:           chatModel,                          // agentkit.ChatModel
    Tools:           []agentkit.Tool{myTool},             // 可选
    Handlers:         []agentkit.ChatModelAgentMiddleware{myHandler}, // 可选
    ModelRetryConfig: &agentkit.ModelRetryConfig{MaxRetries: 2},      // 可选
    ModelFailoverConfig: failoverConfig,                              // 可选
    MaxIterations:   20,                                  // 最大 LLM 调用轮次（默认 20）
    CheckPointStore: store,                               // 检查点存储（可选）
    Session: &agentkit.SessionConfig{                     // 自动恢复/保存（可选）
        ID: "user-123",
        Store: sessionStore,
    },
    Compaction: &agentkit.CompactionConfig{               // 自动上下文压缩（可选）
        MaxTokens: 80_000,
        KeepRecentTurns: 2,
    },
    Skills: &agentkit.SkillsConfig{                       // 按需加载 SKILL.md（可选）
        Paths: []string{"./skills"},
    },
    MCP: &agentkit.MCPConfig{                             // 托管 MCP 服务器（可选）
        Servers: []agentkit.MCPServerConfig{{
            Name:      "search",
            Transport: agentkit.MCPTransportStreamableHTTP,
            URL:       "https://mcp.example.com/mcp",
        }},
    },
})
defer agent.Close()
```

如需手动恢复历史，可使用 `History: savedHistory`；它与 `Session` 二选一。

### 核心方法

```go
// 发送用户文本输入并驱动 Agent 执行（阻塞调用，并发安全）
err := agent.Prompt(ctx, "用户消息")

// 发送多模态输入（文本 + 图片、音频、视频、文件）
err := agent.Send(ctx,
    agentkit.Text("这张图片里是什么？"),
    agentkit.ImageURL("https://example.com/cat.jpg"),
)

// 从当前状态恢复执行，不添加新消息（例如错误后重试）
err := agent.Continue(ctx)

// 从 HITL 中断恢复
err := agent.Resume(ctx, map[string]any{"interruptID": data})

// 查看或明确放弃等待处理的 HITL 中断
pending := agent.PendingInterrupts()
err = agent.ClearCheckpoint(ctx)

// 订阅事件，返回取消订阅函数
unsubscribe := agent.Subscribe(func(e agentkit.Event) { ... })

// 请求取消且不阻塞（可在订阅回调内调用）
agent.Cancel()

// 取消当前执行并等待完成（请在订阅回调外调用）
agent.Abort()

// 重置 Agent 状态（等待完成后清空历史和队列）
agent.Reset()

// 获取完整对话历史，用于调试或持久化（返回副本）
history := agent.History()

// 获取实际发送给模型的上下文（压缩前与 History 相同）
contextHistory := agent.ContextHistory()

// 替换完整对话历史，并同步展示状态
agent.SetHistory(history)

// 获取会话快照；Prompt/Send/Continue/Resume 后会自动保存
session := agent.Session()

// 在 SetHistory 等手动修改后立即保存
err := agent.SaveSession(ctx)

// 获取 Agent 状态（消息记录、流式状态）
state := agent.State()

// 关闭 Agent，释放资源（实现 io.Closer 接口）
agent.Close()
```

> `Prompt`、`Send`、`Continue`、`Resume` 互斥执行。可通过 `errors.Is(err, agentkit.ErrAgentRunning)` 判断并发执行错误。发生 HITL 中断后应先调用 `Resume`；在检查点被恢复或清理前，新执行会返回 `agentkit.ErrResumeRequired`，避免未完成的工具操作被悄悄丢弃。

### 会话管理

只需配置会话 ID 和存储，`New` 会自动恢复已有对话，每次运行结束（包括模型报错或取消）都会自动保存：

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

文件存储使用安全的哈希文件名和“临时文件 + 原子替换”，避免非法会话 ID 造成路径穿越，也避免进程异常时留下半个 JSON。管理会话可直接使用存储：

```go
sessions, err := store.List(ctx)
saved, err := store.Load(ctx, "user-123")
err = store.Delete(ctx, "user-123") // 删除不存在的会话也会成功
```

两个内置会话存储都会自动提供配套的检查点存储。因此使用文件会话时无需额外配置，Agent 或进程重建后仍可恢复 HITL 中断。待处理的中断 ID 可通过 `Agent.PendingInterrupts` 和 `Session.PendingInterrupts` 获取；成功 `Resume` 后会消费检查点，`ClearCheckpoint`、`Reset`、`SetHistory` 和删除会话都会让旧检查点失效。不使用 `Session` 时，也可以通过 `agentkit.NewFileCheckpointStore` 和 `Config.CheckPointStore` 单独启用持久化检查点。

测试或单进程服务可使用 `agentkit.NewMemorySessionStore()`。自定义数据库只需实现 `agentkit.SessionStore`；如需自动提供持久化检查点，可额外实现 `agentkit.CheckpointStoreProvider`。`History` 与 `Session` 不能同时配置，避免恢复来源不明确。同一个会话 ID 同一时间应只由一个 Agent 写入；内置存储保证并发安全，但不会擅自合并两段分叉的对话。

### 自动上下文压缩

启用 `Compaction` 后，AgentKit 会在上下文超过阈值时调用摘要模型，并原样保留最近的用户轮次：

```go
agent, err := agentkit.New(ctx, &agentkit.Config{
    Name:  "assistant",
    Model: chatModel,
    Compaction: &agentkit.CompactionConfig{
        MaxTokens:       80_000, // 建议低于模型的上下文窗口
        MaxMessages:     100,    // 可选；任一阈值超出即触发
        KeepRecentTurns: 2,      // 默认 1
        Model:           summaryModel, // 可选；默认复用主模型
    },
})
```

两种历史各司其职：

- `History()` 始终返回未删减的完整对话，适合 UI、审计和导出。
- `ContextHistory()` 返回实际发送给模型的压缩上下文。
- 配置了 `Session` 时两者会一起持久化，重启后不会重新塞回完整历史。

如果不设置任何阈值，默认在估算超过 `DefaultCompactionMaxTokens`（100,000）时触发。摘要失败会作为正常错误返回，原上下文不会被覆盖。可订阅 `EventCompactionStart` 和 `EventCompactionEnd` 展示压缩进度。

### 技能管理

每个可复用技能放在独立目录中：

```text
skills/
└── concise-answer/
    └── SKILL.md
```

```markdown
---
name: concise-answer
description: 让回答保持简短直接
---
回答不超过三个短句。
```

然后在 Agent 上启用技能目录：

```go
agent, err := agentkit.New(ctx, &agentkit.Config{
    Name:  "assistant",
    Model: chatModel,
    Skills: &agentkit.SkillsConfig{
        Paths: []string{"./skills"},
        // ToolName: "load_skill", // 可选，默认为 "skill"
    },
})
```

`Paths` 可以指向一个 `SKILL.md` 文件、单个技能目录，或由一级技能子目录组成的集合目录。每次列出或加载技能时都会重新读取文件，因此修改后无需重建 Agent。技能重名、frontmatter 格式错误、指令为空或文件超过 1 MiB 都会返回明确错误。

如需使用程序化或远端存储，用 `Backend` 代替 `Paths`。AgentKit 内置并发安全的 `NewMemorySkillBackend`，也暴露了精简的 `SkillBackend` 接口供自定义实现。简易配置有意只支持内联技能；包含 `context`、`agent` 或 `model` 覆盖的技能会尽早报错。需要 Eino 高级 fork/模型路由时，可通过 `Handlers` 安装完整配置的 Eino 技能中间件。

### MCP 管理

AgentKit 可以连接 MCP 服务器、发现并向模型暴露工具，在连接级故障后自动重连，并随 Agent 一起关闭所有会话：

```go
agent, err := agentkit.New(ctx, &agentkit.Config{
    Name:  "assistant",
    Model: chatModel,
    MCP: &agentkit.MCPConfig{
        Servers: []agentkit.MCPServerConfig{
            {
                Name:       "search",
                Transport:  agentkit.MCPTransportStreamableHTTP,
                URL:        "https://mcp.example.com/mcp",
                Headers:    map[string]string{"Authorization": "Bearer " + token},
                ToolNames:  []string{"search", "fetch"}, // 可选白名单
                ToolPrefix: "search__",                  // 可选命名空间
            },
        },
    },
})
if err != nil {
    log.Fatal(err)
}
defer agent.Close() // 同时关闭所有 MCP 会话
```

本地 stdio 服务器的配置如下：

```go
MCP: &agentkit.MCPConfig{
    Servers: []agentkit.MCPServerConfig{{
        Name:       "filesystem",
        Transport:  agentkit.MCPTransportStdio,
        Command:    "filesystem-mcp",
        Args:       []string{"--root", workspace},
        Env:        map[string]string{"LOG_LEVEL": "warn"}, // 与当前进程环境合并
        WorkingDir: workspace,
        ToolPrefix: "fs__",
    }},
},
```

旧式 SSE 服务器可使用 `MCPTransportSSE`。`New` 会完整读取所有分页工具一次；服务器之后增删工具时需重建 Agent。暴露给模型的工具名必须唯一，因此当多个服务器或本地工具重名时应设置 `ToolPrefix`。`ToolNames` 中服务器未提供的名字会直接导致初始化失败，不会静默遗漏。

为避免单次响应耗尽模型上下文，MCP 结果默认最多保留 `DefaultMCPMaxResultChars`（100,000 个字符），工具描述最多保留 `DefaultMCPMaxDescriptionChars`（4,000 个字符）。在 `MCPConfig` 中将对应限制设为正数可自定义，设为 `-1` 可关闭限制。静态请求头会在初始化时复制；如果凭证需要动态刷新，请传入带认证 `RoundTripper` 的自定义 `HTTPClient`，并避免在代码中硬编码密钥。

高级场景可以用已连接的 `MCPClientSession` 代替传输配置。AgentKit 会取得该会话的所有权，并在初始化失败或 `Agent.Close` 时关闭它。

### 集成测试

使用 `MockChatModel` 运行 Agent，无需调用真实模型：

```go
model := agentkit.NewMockChatModel(
    agentkit.MockModelStream("你", "好"),
)

agent, err := agentkit.New(ctx, &agentkit.Config{
    Name:  "test-agent",
    Model: model,
})
if err != nil {
    t.Fatal(err)
}
defer agent.Close()

if err := agent.Prompt(ctx, "打个招呼"); err != nil {
    t.Fatal(err)
}

calls := model.Calls()
if calls[0].Input[len(calls[0].Input)-1].Content != "打个招呼" {
    t.Fatal("unexpected input")
}
```

常用响应辅助函数：

```go
agentkit.MockModelText("完成")
agentkit.MockModelStream("第 1 段", "第 2 段")
agentkit.MockModelError(err)
agentkit.MockModelStreamError(err, "部分内容")
```

工具调用可以直接执行真实函数：

```go
weather := agentkit.MustMockTool(
    "get_weather",
    "查询天气",
    func(ctx context.Context, input *WeatherInput) (*WeatherOutput, error) {
        return &WeatherOutput{City: input.City, Condition: "晴"}, nil
    },
)

beijing := weather.Call("beijing_weather", &WeatherInput{City: "北京"})
shanghai := weather.Call("shanghai_weather", &WeatherInput{City: "上海"})

model := agentkit.NewMockChatModel(
    agentkit.MockModelCalls(beijing),
    agentkit.MockModelCallsAfter(beijing, shanghai),
    agentkit.MockModelRespondsAfter(shanghai, func(out *WeatherOutput) agentkit.MockModelResponse {
        return agentkit.MockModelText(out.City + out.Condition)
    }),
)

agent, err := agentkit.New(ctx, &agentkit.Config{
    Name:  "test-agent",
    Model: model,
    Tools: agentkit.MockTools(weather),
})
```

如果一次模型回复里要调用多个工具，可以使用 `MockModelCalls`：

```go
beijing := weather.Call("beijing_weather", &WeatherInput{City: "北京"})
shanghai := weather.Call("shanghai_weather", &WeatherInput{City: "上海"})

model := agentkit.NewMockChatModel(
    agentkit.MockModelCalls(beijing, shanghai),
    agentkit.MockModelTextAfterAll("完成", beijing, shanghai),
)
```

### 转向与后续消息

```go
// 在执行期间注入转向消息（当前工具批次完成后检查）
agent.Steer("请改为关注 X 话题")

// 追加后续消息（当前任务完成后处理）
agent.FollowUp("另外请检查 Y")

// 配置队列处理模式
agent.SetSteeringMode(agentkit.QueueModeAll)        // 一次性处理所有排队消息
agent.SetFollowUpMode(agentkit.QueueModeOneAtATime)  // 逐条处理（默认）

// 清空队列
agent.ClearSteeringQueue()
agent.ClearFollowUpQueue()
agent.ClearAllQueues()
```

### HITL（人机协作）

```go
// 在工具中：触发中断
return "", agentkit.Interrupt(ctx, "需要用户确认")

// 带状态保存的中断
return "", agentkit.StatefulInterrupt(ctx, "确认？", myState)

// 在恢复后的工具中：检查中断状态
wasInterrupted, hasState, state := agentkit.GetInterruptState[MyState](ctx)

// 获取用户恢复数据
isTarget, hasData, data := agentkit.GetResumeContext[bool](ctx)
```

### 多模态输入

`Send` 接受可变参数 `ContentPart`，通过构造函数创建：

```go
// 文本 + 图片
agent.Send(ctx,
    agentkit.Text("这张图片里是什么？"),
    agentkit.ImageURL("https://example.com/cat.jpg"),
)

// 控制图片识别质量
agent.Send(ctx,
    agentkit.Text("请详细描述"),
    agentkit.ImageURL("https://example.com/photo.jpg", agentkit.ImageDetailHigh),
)

// Base64 编码图片
agent.Send(ctx,
    agentkit.Text("识别一下"),
    agentkit.ImageBase64(base64Data, "image/png"),
)

// 音频 / 视频 / 文件
agent.Send(ctx, agentkit.Text("请转写"), agentkit.AudioURL("https://example.com/speech.mp3"))
agent.Send(ctx, agentkit.Text("请总结"), agentkit.VideoURL("https://example.com/clip.mp4"))
agent.Send(ctx, agentkit.Text("请分析"), agentkit.FileURL("https://example.com/report.pdf"))
```

可用构造函数：

| 构造函数                             | 说明                      |
| ------------------------------------ | ------------------------- |
| `Text(s)`                            | 文本内容                  |
| `ImageURL(url, detail...)`           | 图片 URL（可选质量参数）  |
| `ImageBase64(data, mime, detail...)` | Base64 图片               |
| `AudioURL(url)`                      | 音频 URL                  |
| `AudioBase64(data, mime)`            | Base64 音频               |
| `VideoURL(url)`                      | 视频 URL                  |
| `VideoBase64(data, mime)`            | Base64 视频               |
| `FileURL(url)`                       | 文件 URL                  |
| `FileBase64(data, mime, name...)`    | Base64 文件（可选文件名） |

### 工具进度更新

工具可以在执行过程中发送进度事件：

```go
func myTool(ctx context.Context, input string) (string, error) {
    agentkit.EmitToolUpdate(ctx, "正在处理第 1 步...")
    // ... 执行工作 ...
    agentkit.EmitToolUpdate(ctx, "正在处理第 2 步...")
    return "result", nil
}
```

### 类型别名

AgentKit 提供类型别名，消费者无需直接导入 eino 包：

| 别名             | Eino 类型                 |
| ---------------- | ------------------------- |
| `ChatModel`      | `model.BaseChatModel`     |
| `Tool`           | `tool.BaseTool`           |
| `ToolCall`       | `schema.ToolCall`         |
| `ResponseMeta`   | `schema.ResponseMeta`     |
| `TokenUsage`     | `schema.TokenUsage`       |
| `ContentPart`    | `schema.MessageInputPart` |
| `ImageURLDetail` | `schema.ImageURLDetail`   |

## 示例

查看 [examples](examples/) 目录：

- **[simple](examples/simple/)** — 最简多轮对话（~60 行）
- **[tools](examples/tools/)** — 工具调用和进度事件
- **[history](examples/history/)** — 导出并恢复对话历史
- **[session](examples/session/)** — 自动持久化并跨进程恢复会话
- **[compaction](examples/compaction/)** — 自动压缩长对话上下文
- **[skills](examples/skills/)** — 从本地 `SKILL.md` 文件加载可复用指令
- **[mcp](examples/mcp/)** — 连接并调用 Streamable HTTP MCP 服务器
- **[queues](examples/queues/)** — 后续消息和转向队列
- **[hitl](examples/hitl/)** — 人机协作中断和恢复
- **[multimodal](examples/multimodal/)** — 文本和图片输入

## 许可证

详见 [LICENSE](LICENSE)。
