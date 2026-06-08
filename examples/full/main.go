// Package main 演示 agentkit 的全部核心功能：
//
//   - 工具调用（含 EmitToolUpdate 进度事件）
//   - 上下文历史保持（多轮对话）
//   - Reset 状态重置
//   - FollowUp 后续消息队列
//   - Steer 执行中重定向
//   - HITL 人机协作（中断 + 恢复）
//   - State / History 状态检查
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"github.com/cloudwego/eino-ext/components/model/openai"
	"github.com/cloudwego/eino/components/tool/utils"
	"github.com/joho/godotenv"
	"github.com/wsshow/agentkit"
)

// ── ANSI 颜色 ──────────────────────────────────────────────────────────────

const (
	colorReset  = "\033[0m"
	colorRed    = "\033[31m"
	colorGreen  = "\033[32m"
	colorYellow = "\033[33m"
	colorBlue   = "\033[34m"
	colorPurple = "\033[35m"
	colorCyan   = "\033[36m"
	colorGray   = "\033[90m"
	colorBold   = "\033[1m"
)

// ── 创建聊天模型 ────────────────────────────────────────────────────────────

func newChatModel() (agentkit.ChatModel, error) {
	if err := godotenv.Load(); err != nil {
		return nil, err
	}
	return openai.NewChatModel(context.Background(), &openai.ChatModelConfig{
		APIKey:  os.Getenv("FEIKONG_OPENAI_API_KEY"),
		BaseURL: os.Getenv("FEIKONG_OPENAI_BASE_URL"),
		Model:   os.Getenv("FEIKONG_OPENAI_MODEL"),
	})
}

// ── 天气查询工具 ────────────────────────────────────────────────────────────

type WeatherInput struct {
	City string `json:"city" jsonschema:"description=城市名称，例如：北京、上海、广州"`
}

type WeatherOutput struct {
	City        string `json:"city"`
	Temperature string `json:"temperature"`
	Condition   string `json:"condition"`
	Humidity    string `json:"humidity"`
}

func newWeatherTool() agentkit.Tool {
	t, err := utils.InferTool("get_weather", "查询指定城市的当前天气信息",
		func(ctx context.Context, input *WeatherInput) (*WeatherOutput, error) {
			agentkit.EmitToolUpdate(ctx, fmt.Sprintf("正在查询 %s 的天气...", input.City))
			db := map[string]*WeatherOutput{
				"北京": {City: "北京", Temperature: "22°C", Condition: "晴", Humidity: "45%"},
				"上海": {City: "上海", Temperature: "26°C", Condition: "多云", Humidity: "68%"},
				"广州": {City: "广州", Temperature: "30°C", Condition: "阵雨", Humidity: "82%"},
				"深圳": {City: "深圳", Temperature: "29°C", Condition: "多云转晴", Humidity: "75%"},
				"杭州": {City: "杭州", Temperature: "24°C", Condition: "阴", Humidity: "60%"},
			}
			if w, ok := db[input.City]; ok {
				return w, nil
			}
			return &WeatherOutput{City: input.City, Temperature: "25°C", Condition: "晴", Humidity: "50%"}, nil
		})
	if err != nil {
		log.Fatalf("failed to create weather tool: %v", err)
	}
	return t
}

// ── HITL 确认工具 ───────────────────────────────────────────────────────────
//
// 演示 Human-in-the-Loop 中断/恢复模式：
//   - 首次调用：触发 agentkit.Interrupt 暂停 Agent
//   - Resume 时：通过 agentkit.GetResumeContext 获取 bool 决策并执行

type ConfirmInput struct {
	Action string `json:"action" jsonschema:"description=需要用户确认的操作描述"`
}

func newConfirmTool() agentkit.Tool {
	t, err := utils.InferTool("confirm_action", "执行需要用户明确授权的敏感操作（如删除、发布等）",
		func(ctx context.Context, input *ConfirmInput) (string, error) {
			wasInterrupted, _, _ := agentkit.GetInterruptState[any](ctx)
			if !wasInterrupted {
				// 首次调用：中断，等待用户确认
				return "", agentkit.Interrupt(ctx, fmt.Sprintf("操作「%s」需要用户授权，是否批准？", input.Action))
			}

			// 从恢复上下文中获取用户决策
			isTarget, hasData, approved := agentkit.GetResumeContext[bool](ctx)
			if !isTarget {
				// 非目标（兄弟工具被选择），重新中断
				return "", agentkit.Interrupt(ctx, fmt.Sprintf("操作「%s」需要用户授权，是否批准？", input.Action))
			}
			if hasData && approved {
				return fmt.Sprintf("✅ 操作「%s」已获授权，执行完成", input.Action), nil
			}
			return fmt.Sprintf("❌ 操作「%s」已被用户拒绝，已取消", input.Action), nil
		})
	if err != nil {
		log.Fatalf("failed to create confirm tool: %v", err)
	}
	return t
}

// ── 事件订阅（统一彩色输出）──────────────────────────────────────────────────

func subscribe(agent *agentkit.Agent) {
	agent.Subscribe(func(e agentkit.Event) {
		switch e.Type {
		case agentkit.EventAgentStart:
			fmt.Printf("%s%s╭─── Agent [%s] 开始 ───%s\n", colorBold, colorGreen, e.Agent, colorReset)
		case agentkit.EventTurnStart:
			fmt.Printf("%s│ ▶ 新一轮%s\n", colorCyan, colorReset)
		case agentkit.EventMessageStart:
			if e.Role == agentkit.RoleAssistant {
				fmt.Printf("%s│ 💬 %s", colorBlue, colorReset)
			}
		case agentkit.EventReasoningDelta:
			fmt.Printf("%s%s%s", colorGray, e.Delta, colorReset)
		case agentkit.EventMessageDelta:
			fmt.Print(e.Delta)
		case agentkit.EventMessageEnd:
			if e.Role == agentkit.RoleAssistant {
				fmt.Println()
			}
		case agentkit.EventToolStart:
			names := make([]string, len(e.ToolCalls))
			for i, tc := range e.ToolCalls {
				names[i] = tc.Function.Name
			}
			fmt.Printf("%s│ 🔧 调用工具: %s%s\n", colorYellow, strings.Join(names, ", "), colorReset)
		case agentkit.EventToolUpdate:
			fmt.Printf("%s│    ⏳ %s%s\n", colorGray, e.Content, colorReset)
		case agentkit.EventToolEnd:
			fmt.Printf("%s│ 📋 结果: %s%s\n", colorGray, truncate(e.Content, 40), colorReset)
		case agentkit.EventTurnEnd:
			fmt.Printf("%s│ ■ 轮次结束%s\n", colorCyan, colorReset)
		case agentkit.EventInterrupted:
			fmt.Printf("%s│ ⏸  中断: %v%s\n", colorPurple, e.Interrupt[0].Info, colorReset)
		case agentkit.EventError:
			fmt.Printf("%s│ ❌ 错误: %v%s\n", colorRed, e.Error, colorReset)
		case agentkit.EventAgentEnd:
			fmt.Printf("%s%s╰─── Agent [%s] 结束 ───%s\n", colorBold, colorGreen, e.Agent, colorReset)
		}
	})
}

// ── 辅助函数 ─────────────────────────────────────────────────────────────────

// truncate 按 Unicode 字符数截断字符串，避免在中文等多字节字符中间截断。
func truncate(s string, maxRunes int) string {
	runes := []rune(s)
	if len(runes) <= maxRunes {
		return s
	}
	return string(runes[:maxRunes]) + "..."
}

func printScene(n int, title string) {
	fmt.Printf("\n%s%s══════════ 场景%d：%s ══════════%s\n\n", colorBold, colorPurple, n, title, colorReset)
}

func mustPrompt(ctx context.Context, agent *agentkit.Agent, input string) {
	fmt.Printf("%s👤 用户: %s%s\n", colorCyan, input, colorReset)
	if err := agent.Prompt(ctx, input); err != nil {
		log.Fatalln(err)
	}
	fmt.Println()
}

// ── 主程序 ────────────────────────────────────────────────────────────────────

func main() {
	ctx := context.Background()

	chatModel, err := newChatModel()
	if err != nil {
		log.Fatalln(err)
	}

	// 创建工具列表
	agent, err := agentkit.New(ctx, &agentkit.Config{
		Name:         "weather-assistant",
		Description:  "天气查询与操作助手",
		SystemPrompt: "你是一个助手，可以查询各城市天气，也可以执行需要用户确认的敏感操作。回答请简洁。",
		Model:        chatModel,
		Tools:        []agentkit.Tool{newWeatherTool(), newConfirmTool()},
	})
	if err != nil {
		log.Fatalln(err)
	}
	defer agent.Close()

	subscribe(agent)

	// ── 场景 1：多工具调用 ───────────────────────────────────────────────────
	printScene(1, "多城市天气查询（Prompt + 并行工具调用）")
	mustPrompt(ctx, agent, "帮我同时查一下北京和上海的天气")

	// ── 场景 2：历史上下文 ───────────────────────────────────────────────────
	printScene(2, "历史上下文保持（无需重新查询）")
	mustPrompt(ctx, agent, "刚才两个城市哪个更热？如果我要出门需要带伞吗？")

	// ── 场景 3：Reset 后重新开始 ─────────────────────────────────────────────
	printScene(3, "Reset 后新对话（状态清空验证）")
	fmt.Printf("%s🔄 调用 agent.Reset()%s\n", colorYellow, colorReset)
	agent.Reset()
	mustPrompt(ctx, agent, "广州今天适合出游吗？")

	// ── 场景 4：FollowUp 队列 ────────────────────────────────────────────────
	printScene(4, "FollowUp 队列（当前任务完成后自动追加）")
	fmt.Printf("%s📬 FollowUp: \"顺便也查一下杭州\"%s\n", colorYellow, colorReset)
	agent.FollowUp("顺便也查一下杭州的天气")
	mustPrompt(ctx, agent, "深圳天气如何？")

	// ── 场景 5：Steer 执行中重定向 ───────────────────────────────────────────
	printScene(5, "Steer 执行中重定向（工具粒度中断）")
	go func() {
		time.Sleep(300 * time.Millisecond)
		fmt.Printf("\n%s🔀 注入 Steer: \"不要查天气了，告诉我你能做什么\"%s\n", colorYellow, colorReset)
		agent.Steer("不要查天气了，告诉我你能做什么")
	}()
	mustPrompt(ctx, agent, "帮我查北京、上海、广州、深圳、杭州五个城市的天气")

	// ── 场景 6：HITL 中断 + 恢复 ─────────────────────────────────────────────
	//
	// 流程：
	//   1. Prompt → Agent 调用 confirm_action → 工具触发 Interrupt → Prompt 返回
	//   2. 捕获 interruptID → 调用 Resume(approved=true) → Agent 继续执行
	printScene(6, "HITL 人机协作（中断 + 授权恢复）")

	var interruptID string
	unsubscribe := agent.Subscribe(func(e agentkit.Event) {
		if e.Type == agentkit.EventInterrupted && len(e.Interrupt) > 0 {
			interruptID = e.Interrupt[0].ID
		}
	})
	defer unsubscribe()

	fmt.Printf("%s👤 用户: 帮我删除所有临时文件%s\n", colorCyan, colorReset)
	if err := agent.Prompt(ctx, "帮我删除所有临时文件"); err != nil {
		log.Fatalln(err)
	}

	// Prompt 返回后（Agent 在中断后结束本轮执行），模拟用户审核并批准操作
	if interruptID != "" {
		fmt.Printf("\n%s✅ 用户批准操作，调用 Resume(approved=true)%s\n\n", colorYellow, colorReset)
		if err := agent.Resume(ctx, map[string]any{interruptID: true}); err != nil {
			log.Fatalln(err)
		}
	}
	fmt.Println()

	// ── 场景 7：State + History 状态检查 ─────────────────────────────────────
	printScene(7, "State + History 检查")

	fmt.Printf("%s📊 Agent.Name(): %s%s\n", colorCyan, agent.Name(), colorReset)

	// State.Messages()：简化消息列表（user/assistant 文本）
	msgs := agent.State().Messages()
	fmt.Printf("%s📊 State.Messages() 共 %d 条:%s\n", colorCyan, len(msgs), colorReset)
	for i, m := range msgs {
		fmt.Printf("%s   [%d] role=%s agent=%s: %s%s\n", colorGray, i, m.Role, m.Agent, truncate(m.Content, 30), colorReset)
	}

	// History()：完整消息历史（含 tool_calls、role 等）
	history := agent.History()
	fmt.Printf("\n%s📜 History() 共 %d 条（含 tool/assistant 完整消息）:%s\n", colorCyan, len(history), colorReset)
	for i, m := range history {
		detail := string(m.Role)
		if len(m.ToolCalls) > 0 {
			names := make([]string, len(m.ToolCalls))
			for j, tc := range m.ToolCalls {
				names[j] = tc.Function.Name
			}
			detail += fmt.Sprintf(" [tool_calls: %s]", strings.Join(names, ", "))
		}
		fmt.Printf("%s   [%d] %s: %s%s\n", colorGray, i, detail, truncate(m.Content, 25), colorReset)
	}
}
