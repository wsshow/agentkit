// Package main 演示可跨进程恢复的 Goal 模式。
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/wsshow/agentkit"
	"github.com/wsshow/agentkit/examples/internal/demo"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	chatModel, err := demo.NewChatModel(ctx)
	if err != nil {
		log.Fatalln(err)
	}
	store, err := agentkit.NewFileSessionStore(".agentkit-goal")
	if err != nil {
		log.Fatalln(err)
	}
	agent, err := agentkit.New(ctx, &agentkit.Config{
		Name:         "goal-worker",
		SystemPrompt: "你是一个可靠的执行型助手。每一步都说明实际完成的工作和验证结果。",
		Model:        chatModel,
		Session: &agentkit.SessionConfig{
			ID:    "goal-demo",
			Store: store,
		},
	})
	if err != nil {
		log.Fatalln(err)
	}
	defer agent.Close()
	demo.SubscribeText(agent)
	agent.Subscribe(func(event agentkit.Event) {
		if event.Type == agentkit.EventGoalUpdate && event.Goal != nil {
			fmt.Printf("\n[goal] id=%s status=%s iteration=%d/%d\n",
				event.Goal.ID, event.Goal.Status, event.Goal.Iteration, event.Goal.MaxIterations)
		}
	})

	goals, err := agentkit.NewGoalRunner(agent, nil)
	if err != nil {
		log.Fatalln(err)
	}
	run, err := startOrResume(ctx, goals, os.Args[1:])
	if err != nil {
		log.Fatalln(err)
	}
	fmt.Printf("目标已持久化，ID：%s\n", run.ID())

	result, runErr := run.Wait()
	if result != nil && result.Goal != nil {
		fmt.Printf("目标停止：status=%s，reason=%s\n", result.Goal.Status, result.Goal.LastReason)
	}
	if errors.Is(runErr, context.Canceled) {
		fmt.Println("执行已停止且状态已保存；再次不带参数运行本示例即可恢复。")
		return
	}
	if runErr != nil {
		log.Fatalln(runErr)
	}
}

func startOrResume(ctx context.Context, goals *agentkit.GoalRunner, args []string) (*agentkit.GoalRun, error) {
	if len(args) > 2 {
		return nil, errors.New("用法：go run ./goal \"目标\" [\"成功标准\"]；不带参数则恢复唯一未完成目标")
	}
	if len(args) > 0 {
		return goals.StartAsync(ctx, agentkit.GoalRequest{
			Objective:       args[0],
			SuccessCriteria: optionalArgument(args, 1),
		})
	}

	infos, err := goals.List(ctx)
	if err != nil {
		return nil, err
	}
	pending := make([]string, 0, len(infos))
	for _, info := range infos {
		if info.Status != agentkit.GoalStatusCompleted {
			pending = append(pending, info.ID)
		}
	}
	switch len(pending) {
	case 0:
		return nil, errors.New("没有可恢复的目标；请运行：go run ./goal \"目标\" [\"成功标准\"]")
	case 1:
		fmt.Printf("恢复未完成目标：%s\n", pending[0])
		return goals.ResumeAsync(ctx, pending[0])
	default:
		return nil, fmt.Errorf("存在多个未完成目标，请在应用中选择明确 ID 后调用 ResumeAsync：%s",
			strings.Join(pending, ", "))
	}
}

func optionalArgument(args []string, index int) string {
	if index >= len(args) {
		return ""
	}
	return args[index]
}
