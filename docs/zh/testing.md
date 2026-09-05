# 测试 Agent

[English](../testing.md) · [文档索引](README.md)

AgentKit 的 mock 会执行真实 Agent 运行时，但不发送网络请求。它适合用确定性模型回复测试提示词、事件流、工具顺序、错误恢复、HITL、会话和目标。

## 模拟文本或流式回复

```go
func TestGreeting(t *testing.T) {
	ctx := context.Background()
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

	result, err := agent.Ask(ctx, "打个招呼")
	if err != nil {
		t.Fatal(err)
	}
	if result.Text != "你好" {
		t.Fatalf("unexpected result: %q", result.Text)
	}

	calls := model.Calls()
	if calls[0].Input[len(calls[0].Input)-1].Content != "打个招呼" {
		t.Fatal("unexpected input")
	}
}
```

常用响应辅助函数：

```go
agentkit.MockModelText("完成")
agentkit.MockModelStream("第 1 段", "第 2 段")
agentkit.MockModelError(err)
agentkit.MockModelStreamError(err, "部分内容")
```

配置的响应会按顺序消费。`Calls` 返回记录的请求，让测试可以断言真正发送给模型的历史和选项，而不只是最终文本。

## 模拟真实工具函数

```go
type WeatherInput struct {
	City string `json:"city"`
}

type WeatherOutput struct {
	City      string `json:"city"`
	Condition string `json:"condition"`
}

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

`MockModelCallsAfter` 会验证前一个结果已经出现，再返回下一次调用。`MockModelRespondsAfter` 会把结果解码为类型化输出，再构造最终回复。这些辅助函数能够发现工具结果顺序损坏，而不是接受任意输入。

一次模型回复需要请求多个工具时，可放在同一响应中：

```go
model := agentkit.NewMockChatModel(
	agentkit.MockModelCalls(beijing, shanghai),
	agentkit.MockModelTextAfterAll("完成", beijing, shanghai),
)
```

## 应该断言什么

稳定的集成测试应断言应用可见契约：

- `RunResult` 中的最终文本、token 用量、工具调用或待处理中断；
- 请求级事件流的事件顺序与关键字段；
- `Calls` 捕获的模型输入；
- 重建 Agent 和存储后的会话/目标持久化状态；
- 通过 `errors.Is` 或 `errors.As` 判断的错误身份；
- 取消与等待截止时间分别产生的效果。

不要断言不属于公共 API 的内部实现细节。每个测试都应使用非空 context；`context.Background()` 通常适合作为测试用例的根 context。

## 测试持久化与恢复

快速行为测试使用 `NewMemorySessionStore`。需要覆盖序列化、进程式重建、检查点、目标租约或大型工具结果时，使用临时目录：`NewFileSessionStore(t.TempDir())`。

模拟重启时，关闭第一个 Agent，再用同一目录和会话 ID 创建新存储与 Agent，随后调用公共恢复方法。这样验证的正是生产代码依赖的边界。

## 相关指南

- [运行时与事件](runtime.md)
- [会话与持久化](persistence.md)
- [持久化目标](goals.md)
