# AgentKit

[![CI](https://github.com/wsshow/agentkit/actions/workflows/ci.yml/badge.svg)](https://github.com/wsshow/agentkit/actions/workflows/ci.yml)

[English](README.md)

轻量级、事件流驱动的 Agent 工具包，基于 [CloudWeGo Eino ADK](https://github.com/cloudwego/eino) 构建。

灵感来源于 [pi-agent-core](https://github.com/earendil-works/pi/tree/main/packages/agent)，在 Go + Eino 生态下实现事件流、消息队列和 HITL（人机协作）机制。

## 特性

- **事件流架构** — 订阅细粒度事件（消息增量、工具调用、错误等）
- **简单运行结果** — 通过 `Ask` 直接获得最终回复、累计用量、工具调用和中断信息，无需先编写订阅器
- **请求级配置** — 按单次请求覆盖模型/工具选项并注入类型安全的运行值，不修改 Agent 全局配置
- **转向与后续消息队列** — 在执行过程中注入消息以重定向 Agent 或追加后续任务
- **人机协作（HITL）** — 中断 Agent 执行并在用户提供数据后恢复
- **流式输出** — 通过 Eino ADK 流式传输实时逐 token 输出
- **推理模型支持** — 原生支持思考/推理模型（DeepSeek-R1、o1 等），流式输出推理过程
- **多模态输入** — 通过 `Send()` 发送文本、图片、音频、视频、文件，配套简洁构造函数
- **会话持久化** — 自动保存和恢复完整对话，内置并发安全的内存与原子文件存储
- **持久化目标运行** — 保存目标、逐步判断完成度，并在取消或重启后安全恢复
- **自动上下文压缩** — 超过 token 或消息阈值时自动摘要，完整历史与模型上下文分离保存
- **按需技能** — 从本地目录或自定义后端加载可复用的 `SKILL.md` 指令
- **MCP 连接管理** — 连接 stdio、SSE、Streamable HTTP 服务器，自动发现、重连、筛选并释放资源
- **按需工具发现** — 大型工具目录不会一次性塞入模型上下文，模型搜索后才加载所需工具
- **可恢复的大型工具结果** — 将超大结果移出上下文并持久化，模型需要时再分段回取
- **受保护的工具集成** — 接入任何 Eino 兼容工具，内置结果大小限制、可选超时、审计钩子和自动调用处理
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

	result, err := agent.Ask(ctx, "你好！")
	if err != nil {
		log.Fatalln(err)
	}
	fmt.Println(result.Text)
}
```

`Ask` 是最简单的阻塞式 API。`RunResult` 还包含最终 schema 消息、本次新增消息、累计 token 用量、工具调用和待处理的 HITL 中断。需要实时输出或工具进度时，可使用请求级事件流：

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

`Stream` 会在返回前占用 Agent，提供 `Cancel`、`Done`、`Wait`、`Close`，并通过内部队列隔离慢事件消费者，避免阻塞 Agent 执行。多模态输入使用 `StreamParts`。全局 `Subscribe` 仍适合日志和应用级观察器；全局回调会同步执行，因此应尽快返回。单个回调发生 panic 时会被隔离，其余订阅者会收到包装 `ErrSubscriberPanic` 的 `EventError`。

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
| `EventGoalUpdate`     | Goal 状态已持久化（`Event.Goal`）                                           |
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
    Goal             *Goal            // 已持久化的目标快照（goal_update）
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
    ToolPolicy:      &agentkit.ToolPolicy{Sequential: true}, // 可选
    Handlers:         []agentkit.ChatModelAgentMiddleware{myHandler}, // 可选
    ModelRetryConfig: &agentkit.ModelRetryConfig{MaxRetries: 2},      // 可选
    ModelFailoverConfig: failoverConfig,                              // 可选
    PersistenceTimeout: 30 * time.Second,                             // 可选；这也是默认值
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
    ToolReduction: &agentkit.ToolReductionConfig{},       // 持久化并回取大型工具结果（可选）
    Skills: &agentkit.SkillsConfig{                       // 按需加载 SKILL.md（可选）
        Paths: []string{"./skills"},
    },
    ToolSearch: &agentkit.ToolSearchConfig{               // 大型按需工具目录（可选）
        Tools: []agentkit.Tool{rareToolA, rareToolB},
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

// 或直接获得最终回复与本次运行元数据
result, err := agent.Ask(ctx, "用户消息")
fmt.Println(result.Text, result.Usage)

// 或消费本次请求专属的事件流
stream, err := agent.Stream(ctx, "用户消息")
for event := range stream.Events() { ... }
result, err = stream.Wait()
result, err = stream.WaitContext(waitCtx) // 限制等待时间，但不取消底层运行

// 发送多模态输入（文本 + 图片、音频、视频、文件）
err := agent.Send(ctx,
    agentkit.Text("这张图片里是什么？"),
    agentkit.ImageURL("https://example.com/cat.jpg"),
)

// 返回 RunResult 的多模态调用
result, err = agent.AskParts(ctx, agentkit.Text("描述这张图"), agentkit.ImageURL(imageURL))

// 从当前状态恢复执行，不添加新消息（例如错误后重试）
err := agent.Continue(ctx)
result, err = agent.ContinueWithResult(ctx)

// 从 HITL 中断恢复
err := agent.Resume(ctx, map[string]any{"interruptID": data})
result, err = agent.ResumeWithResult(ctx, map[string]any{"interruptID": data})

// 查看或明确放弃等待处理的 HITL 中断
pending := agent.PendingInterrupts()
err = agent.ClearCheckpoint(ctx)

// 订阅事件，返回取消订阅函数
unsubscribe := agent.Subscribe(func(e agentkit.Event) { ... })

// 请求取消且不阻塞（可在订阅回调内调用）
agent.Cancel()

// 取消当前执行并等待完成（请在订阅回调外调用）
agent.Abort()

// 或使用 context 限制优雅停机的等待时间
err := agent.AbortContext(shutdownCtx)

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

// 或同时限制运行退出和 MCP 清理的等待时间
err = agent.CloseContext(shutdownCtx)
```

> `Prompt`、`Send`、`Continue`、`Resume` 互斥执行。可通过 `errors.Is(err, agentkit.ErrAgentRunning)` 判断并发执行错误。发生 HITL 中断后应先调用 `Resume`；在检查点被恢复或清理前，新执行会返回 `agentkit.ErrResumeRequired`，避免未完成的工具操作被悄悄丢弃。

`AbortContext` 总会先发出取消请求，再限制等待时间。如果自定义模型或工具忽略 context，该方法可能在返回停机 context 错误时，底层代码仍在退出过程中；Agent 会保持占用状态，直到本次运行真正结束。`CloseContext` 还会立即禁止新运行，并在等待截止后继续于后台完成仅一次的 MCP 清理。

### 请求级配置

可通过 `context` 定制单次请求，同时保持共享 Agent 的配置稳定：

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

准备 `SystemPrompt` 时，运行值会替换其中的 `{user_name}` 占位符。工具和中间件可通过 `agentkit.RunValue[T](ctx, key)` 读取，通过 `RunValues` 获取副本，并用 `SetRunValue` 更新同一次底层运行中后续工具或中间件可见的值。`ToolOptions` 为自定义工具提供对应的请求级扩展入口。`WithRunConfig` 会复制传入容器，并且无需修改现有 API 即可配合 `Ask`、`Send`、`Stream`、`Continue` 和 `Resume` 使用。

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

两个内置会话存储都会自动提供配套的检查点存储。因此使用文件会话时无需额外配置，Agent 或进程重建后仍可恢复 HITL 中断。待处理的中断 ID 可通过 `Agent.PendingInterrupts` 和 `Session.PendingInterrupts` 获取；成功 `Resume` 后会消费检查点，`ClearCheckpoint`、`Reset` 和 `SetHistory` 会让旧检查点失效。删除内置会话会级联清理它的检查点、目标和会话所属的大型工具结果；删除前应先停止使用该会话的 worker。重复删除也会继续清理可识别的孤儿目标与结果。不使用 `Session` 时，也可以通过 `agentkit.NewFileCheckpointStore` 和 `Config.CheckPointStore` 单独启用持久化检查点。

它们也会提供不可变的 `ToolResultStore`，用于保存不应长期留在模型上下文里的完整工具结果。压缩会自动记录 `SessionID`；手动保存且 `SessionID` 为空的结果保持独立，不会随会话删除。不使用会话时可直接创建 `agentkit.NewMemoryToolResultStore` 或 `agentkit.NewFileToolResultStore`；自定义会话后端可额外实现 `agentkit.ToolResultStoreProvider`。

长期运行的服务可通过一次显式调用执行保留期清扫（通常由应用自己的调度器触发）：

```go
report, err := agentkit.PruneResources(ctx, store, agentkit.RetentionPolicy{
    SessionIdleTime:       30 * 24 * time.Hour,
    CompletedGoalAge:      7 * 24 * time.Hour,
    DetachedToolResultAge: 24 * time.Hour,
})
```

零值策略不会删除任何数据。清扫绝不会删除 active、paused 或 blocked 目标，也不会让仍可能被会话引用的工具结果按时间过期。调用闲置会话清扫前，应先停止可能命中这些会话的 worker。返回报告只统计直接删除的条目，不会重复计算会话删除时级联清掉的资源。

测试或单进程服务可使用 `agentkit.NewMemorySessionStore()`。自定义数据库只需实现 `agentkit.SessionStore`；如需自动提供持久化检查点，可额外实现 `agentkit.CheckpointStoreProvider`。`History` 与 `Session` 不能同时配置，避免恢复来源不明确。内置存储使用 `Session.Revision` 做乐观并发控制：两个 Agent 从同一版本恢复时，陈旧写入会返回 `ErrSessionConflict`，不会静默覆盖较新的历史。自定义存储也应提供相同的 compare-and-swap 语义；分叉对话不会被擅自合并。

### 持久化 Goal 模式

`GoalRunner` 会把一个长期目标拆成多次普通、可提交的 Agent 步骤。每一步结束后，默认由主模型根据完成标准进行判断；若尚未完成，就生成明确的下一步提示并继续，直到完成或达到有界的最大轮次：

```go
store, err := agentkit.NewFileSessionStore("./data/agent")
if err != nil {
    log.Fatal(err)
}

agent, err := agentkit.New(ctx, &agentkit.Config{
    Name:  "release-agent",
    Model: chatModel,
    Session: &agentkit.SessionConfig{
        ID:    "release-session",
        Store: store,
    },
})
if err != nil {
    log.Fatal(err)
}
defer agent.Close()

goals, err := agentkit.NewGoalRunner(agent, nil)
if err != nil {
    log.Fatal(err)
}
result, err := goals.Start(ctx, agentkit.GoalRequest{
    ID:              "release-v2", // 可省略；为空时自动生成 UUID
    Objective:       "准备并验证 v2 版本发布",
    SuccessCriteria: "测试通过且发布产物已经就绪",
})
```

进程重启后，使用相同目录、Agent 名称和会话 ID 重建文件存储与 Agent，再调用 `goals.Resume(ctx, "release-v2")`。若 ID 是自动生成的，`goals.ResumePending(ctx)` 会恢复当前会话唯一的未完成目标；存在多个目标时返回 `ErrGoalResumeAmbiguous`，不会擅自选择。`goals.List(ctx)` 只列出当前会话中可直接用于重连界面的摘要，包含目标内容、迭代上限、待处理阶段、最近原因和最近错误。内置会话存储会自动提供配套的 `GoalStore`；自定义判断器或存储可通过 `GoalRunnerConfig` 设置。状态控制使用 `Get`、`Pause` 和 `Clear`；目标进入 HITL 后，通过 `ResumeInterrupt` 提交待处理的中断 ID。

每次状态成功落盘后，都会通过 `Agent.Subscribe` 发出 `EventGoalUpdate`。其中的 `Event.Goal` 是与持久化版本一致、彼此隔离的快照，应用在线时无需轮询即可更新进度，断线重连后再用 `Get` 对齐最新状态。

目标状态会在工作开始前、Agent 产出后和完成度判断后分别提交。如果已保存的会话历史能证明某一步已经结束，`Resume` 会直接判断该结果，不重复执行。若进程可能在外部副作用已经发生、但会话进度尚未保存时退出，目标会以 `ErrGoalRecoveryRequired` 进入 `blocked`，只有显式调用 `Retry` 才会重放这个不确定步骤。这里优先保证安全，不虚假承诺外部操作 exactly-once。

执行外部副作用的工具可以用极低成本参与持久化去重：

```go
key, ok := agentkit.GoalOperationKey(ctx, "publish-release")
// 将 key 传给支持幂等的 API，或与操作结果一起原子保存。
```

同一目标尝试跨进程恢复或显式 `Retry` 时会得到相同 key；成功进入下一次目标迭代后则会得到新 key。需要更完整审计信息时，`CurrentGoalRun` 会返回对应的 `GoalID`、`SessionID` 和 `Attempt`。唯一性仍须由外部系统保证，AgentKit 不会宣称通用 exactly-once。

请求取消后的 Session、Checkpoint 和 Goal 内部收尾会使用有界 context。`Config.PersistenceTimeout` 默认为 `DefaultPersistenceTimeout`（30 秒），异常自定义存储不会再让任务永久无法退出；只有后端确实需要更久时才应调大。

如果工作本身和随后的恢复状态落盘同时失败，GoalRunner 会用 `errors.Join` 一起返回，调用方可分别通过 `errors.Is` 判断。框架不会只报告模型/工具错误，却静默隐藏持久化恢复点可能已经陈旧。

默认的模型判断器会直接复用 Agent 的 `ModelRetryConfig` 和 `ModelFailoverConfig`，包括自定义重试判断、退避与备用模型选择，并与普通 Agent 调用一样先耗尽当前模型重试再切换。因此短暂的判断请求故障无需重复配置；自定义 `GoalEvaluator` 仍完全由应用自行控制。

两个内置目标存储也实现了可选的 `GoalLeaseStore` 接口。`GoalRunner` 会自动发现它，在每个修改状态的操作前取得所有权，在耗时较长的模型或工具调用期间后台续期，并用不透明 Token fencing 每一次保存。并发 worker 会收到 `ErrGoalLeaseHeld`，可通过 `errors.As` 取得 `GoalLeaseHeldError` 中的持有者和到期时间；已经丢失所有权的 worker 会被取消并收到 `ErrGoalLeaseLost`。worker 崩溃且租约过期后，替代 worker 可直接调用 `Resume`，继续沿用已有的安全恢复规则。

默认租约为一分钟，大约每 20 秒续期一次。可通过 `GoalRunnerConfig` 设置 `WorkerID` 和 `LeaseDuration`；生产环境可设置 `RequireLease: true`，避免旧的自定义存储意外退化为单 worker 模式。基础 `GoalStore` 接口保持兼容。

文件存储依旧定位于本地单进程 worker；其中的租约可以跨重启保留，但不是分布式文件锁。多副本部署应通过数据库事务实现 `SessionStore`、`CheckpointStore`、`GoalStore` 和 `GoalLeaseStore`。`GoalRunner` 不会在宿主进程停止后凭空继续运行；应由 supervisor 在租约可用后拉起 worker 并调用 `Resume`。

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

如果不设置任何阈值，默认在估算超过 `DefaultCompactionMaxTokens`（100,000）时触发。摘要模型会自动复用 Agent 的 `ModelRetryConfig` 和 `ModelFailoverConfig`，即使通过 `Compaction.Model` 使用独立模型也无需重复配置。耗尽这些策略后仍然失败时，错误会正常返回且原上下文不会被覆盖。可订阅 `EventCompactionStart` 和 `EventCompactionEnd` 展示压缩进度。

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

### 工具策略

可以集中配置工具分发，无需直接构造 Eino `ToolsNode`：

```go
ToolPolicy: &agentkit.ToolPolicy{
    Sequential: true, // 默认并行执行
    Timeout: 30 * time.Second,
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

`New` 会根据全部本地、Skill 和 MCP 工具校验别名，别名冲突或引用不存在的正式工具都会立即失败。所有文本工具结果默认最多保留 `DefaultToolResultMaxChars`（100,000 个 Unicode 字符），截断时会附加提示标记；将 `MaxResultChars` 设为 `-1` 可关闭限制。启用 `ToolReduction` 后，它会安全接管这项有损限长，确保超大结果先完整持久化。`Timeout` 使用 `context` 协作取消，因此自定义工具应在 `ctx.Done()` 关闭后及时停止。`BeforeTool` 可通过返回错误拒绝调用，`AfterTool` 会收到耗时、错误、保留文本大小和截断信息。普通策略回调的 panic 会被隔离：控制型回调返回包装 `ErrToolPolicyPanic` 的错误，`AfterTool` panic 则通过 `EventError` 上报，不会覆盖成功的工具结果。工具默认并行执行，因此钩子必须并发安全。这些保护统一覆盖普通、流式及多模态工具。高级拦截场景仍可通过 `Middlewares` 传入 `agentkit.ToolMiddleware`。

AgentKit 还会在每次模型请求前自动修复没有配对结果的工具调用。该能力默认开启，取消或中断的工具批次不会再留下被 OpenAI 兼容接口拒绝的历史格式。

### 大型工具结果压缩

只需一个零值安全的配置即可启用持久化卸载：

```go
ToolReduction: &agentkit.ToolReductionConfig{}
```

单个结果超过 50,000 字节时，会被替换为简短预览和一个不透明结果 ID。AgentKit 会自动注册安全、只读的 `read_tool_result` 工具，每次最多返回 20,000 个 Unicode 字符，并通过 `next_offset` 指示下一段位置。上下文估算超过 160,000 tokens 时，较旧的工具轮次也会被卸载，最近一轮仍保持完整。可通过 `MaxResultBytes`、`MaxContextTokens` 和 `KeepRecentToolRounds` 调整这些默认值。

用户无需额外接线：压缩会优先复用 `Session` 提供的 `ToolResultStoreProvider`，否则自动使用并发安全的内存存储。因此配合 `NewFileSessionStore` 时，卸载结果会自动跨进程重启保留；只有自定义后端才需要设置 `Store`。应用可通过 `agent.ToolResultStore()` 按自己的保留策略列出或删除独立结果；会话所属结果会由内置会话删除自动清理。

启用后由 reduction 统一负责结果大小控制，`ToolPolicy` 的超时、钩子、别名等其他能力仍然生效。对于 MCP 工具，AgentKit 会关闭其默认结果上限，让完整输出进入 reduction；若用户显式设置了正数 `MCPConfig.MaxResultChars`，该上限会被保留，超出部分会按用户意图丢弃。reduction 会先于完整上下文摘要执行，避免摘要模型先吞入大块旧工具结果。

### 按需工具搜索

常用工具继续放在 `Tools`；数量较多、使用频率较低的专业工具只需放进一个可选配置：

```go
ToolSearch: &agentkit.ToolSearchConfig{
    Tools: []agentkit.Tool{
        lookupWeather,
        searchTickets,
        queryWarehouse,
    },
}
```

对于动态目录，模型起初只看到 `tool_search` 元工具；普通 `Tools` 仍然可见。搜索命中后模型才会看到对应动态工具，它们仍统一经过 `ToolPolicy` 的超时、结果处理、钩子、别名和中间件。小型工具集应继续直接使用 `Tools`；工具搜索会多一次决策，只在工具 schema 已明显占用上下文时才值得启用。仅当模型提供商支持原生工具搜索协议时设置 `UseModelNative: true`。启用后 `tool_search` 为保留名称。

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
| `ToolMiddleware` | `compose.ToolMiddleware`  |
| `ToolInput`      | `compose.ToolInput`       |
| `ToolOutput`     | `compose.ToolOutput`      |
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
