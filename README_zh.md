# AgentKit

[English](README.md)

轻量级、事件流驱动的 Agent 工具包，基于 [CloudWeGo Eino ADK](https://github.com/cloudwego/eino) 构建。

灵感来源于 [pi-agent-core](https://github.com/badlogic/pi-mono/tree/main/packages/agent)，在 Go + Eino 生态下实现事件流、消息队列和 HITL（人机协作）机制。

## 特性

- **事件流架构** — 订阅细粒度事件（消息增量、工具调用、错误等）
- **转向与后续消息队列** — 在执行过程中注入消息以重定向 Agent 或追加后续任务
- **人机协作（HITL）** — 中断 Agent 执行并在用户提供数据后恢复
- **流式输出** — 通过 Eino ADK 流式传输实时逐 token 输出
- **推理模型支持** — 原生支持思考/推理模型（DeepSeek-R1、o1 等），流式输出推理过程
- **多模态输入** — 通过 `Send()` 发送文本、图片、音频、视频、文件，配套简洁构造函数
- **工具集成** — 接入任何 Eino 兼容工具，自动处理工具调用
- **类型别名** — 直接使用 `agentkit.ChatModel`、`agentkit.Tool`、`agentkit.ToolCall` 等，无需直接导入 eino 包

## 安装

```bash
go get github.com/wsshow/agentkit
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

	chatModel, _ := openai.NewChatModel(ctx, &openai.ChatModelConfig{
		APIKey:  "your-api-key",
		BaseURL: "https://api.openai.com/v1",
		Model:   "gpt-4o",
	})

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
| `EventTurnStart`      | 新一轮开始（一次 LLM 调用 + 工具执行周期）                                  |
| `EventMessageStart`   | 消息开始（流式或非流式）                                                    |
| `EventReasoningDelta` | 推理/思考过程流式增量（`Event.Delta`），仅推理模型                          |
| `EventMessageDelta`   | 流式增量文本（`Event.Delta`）                                               |
| `EventMessageEnd`     | 消息结束（`Event.Content`、`Event.ReasoningContent`、`Event.ResponseMeta`） |
| `EventToolStart`      | 工具调用请求（`Event.ToolCalls`）                                           |
| `EventToolUpdate`     | 工具执行进度更新（`Event.Content`）                                         |
| `EventToolEnd`        | 工具调用结果返回（`Event.Content`）                                         |
| `EventTurnEnd`        | 一轮结束                                                                    |
| `EventTransfer`       | Agent 转移（多 Agent 场景）                                                 |
| `EventInterrupted`    | HITL 中断（`Event.Interrupt`）                                              |
| `EventAgentEnd`       | Agent 处理完成                                                              |
| `EventError`          | 发生错误（`Event.Error`）                                                   |

### Event 结构体

```go
type Event struct {
    Type             EventType
    Agent            string           // 产生事件的 Agent 名称
    Content          string           // 文本内容（message_end / tool_end）
    Delta            string           // 流式增量内容（message_delta / reasoning_delta）
    ReasoningContent string           // 完整推理内容（message_end，仅推理模型）
    ResponseMeta     *ResponseMeta    // 响应元数据：token 用量、完成原因（message_end）
    ToolCalls        []ToolCall       // 工具调用列表（tool_start）
    Interrupt        []InterruptPoint // 中断点列表（interrupted）
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
})
defer agent.Close()
```

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

// 订阅事件，返回取消订阅函数
unsubscribe := agent.Subscribe(func(e agentkit.Event) { ... })

// 取消当前执行并等待完成
agent.Abort()

// 重置 Agent 状态（等待完成后清空历史和队列）
agent.Reset()

// 获取完整对话历史，用于调试或持久化
history := agent.History()

// 获取 Agent 状态（消息记录、流式状态）
state := agent.State()

// 关闭 Agent，释放资源（实现 io.Closer 接口）
agent.Close()
```

> `Prompt`、`Continue`、`Resume` 互斥执行 — 在另一个正在运行时调用会返回错误。

### 转向与后续消息

```go
// 在执行期间注入转向消息（每次工具结果返回后检查）
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
- **[full](examples/full/)** — 综合 7 场景演示（工具调用、历史上下文、重置、后续消息、转向、HITL、状态检查）

## 许可证

详见 [LICENSE](LICENSE)。
