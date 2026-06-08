package main

import (
	"context"
	"fmt"
	"log"

	"github.com/cloudwego/eino/components/tool/utils"
	"github.com/wsshow/agentkit"
	"github.com/wsshow/agentkit/examples/internal/demo"
)

type ConfirmInput struct {
	Action string `json:"action" jsonschema:"description=需要用户确认的操作"`
}

func newConfirmTool() agentkit.Tool {
	tool, err := utils.InferTool("confirm_action", "执行需要用户确认的操作",
		func(ctx context.Context, input *ConfirmInput) (string, error) {
			wasInterrupted, _, _ := agentkit.GetInterruptState[any](ctx)
			if !wasInterrupted {
				return "", agentkit.Interrupt(ctx, fmt.Sprintf("是否批准操作：%s？", input.Action))
			}

			isTarget, hasData, approved := agentkit.GetResumeContext[bool](ctx)
			if !isTarget {
				return "", agentkit.Interrupt(ctx, fmt.Sprintf("是否批准操作：%s？", input.Action))
			}
			if hasData && approved {
				return "操作已获批准并执行完成", nil
			}
			return "操作未获批准，已取消", nil
		})
	if err != nil {
		log.Fatalln(err)
	}
	return tool
}

func main() {
	ctx := context.Background()

	chatModel, err := demo.NewChatModel(ctx)
	if err != nil {
		log.Fatalln(err)
	}

	agent, err := agentkit.New(ctx, &agentkit.Config{
		Name:         "assistant",
		SystemPrompt: "你可以执行需要用户确认的敏感操作。",
		Model:        chatModel,
		Tools:        []agentkit.Tool{newConfirmTool()},
	})
	if err != nil {
		log.Fatalln(err)
	}
	defer agent.Close()

	demo.SubscribeText(agent)

	var interruptID string
	agent.Subscribe(func(e agentkit.Event) {
		if e.Type == agentkit.EventInterrupted && len(e.Interrupt) > 0 {
			interruptID = e.Interrupt[0].ID
		}
	})

	if err := demo.Ask(ctx, agent, "帮我删除所有临时文件"); err != nil {
		log.Fatalln(err)
	}

	if interruptID == "" {
		log.Fatalln("missing interrupt id")
	}

	fmt.Println("\n用户批准操作，调用 Resume")
	if err := agent.Resume(ctx, map[string]any{interruptID: true}); err != nil {
		log.Fatalln(err)
	}
}
