package main

import (
	"context"
	"fmt"
	"log"

	"github.com/wsshow/agentkit"
	"github.com/wsshow/agentkit/examples/internal/demo"
)

func main() {
	ctx := context.Background()

	chatModel, err := demo.NewChatModel(ctx)
	if err != nil {
		log.Fatalln(err)
	}

	agent, err := agentkit.New(ctx, &agentkit.Config{
		Name:         "assistant",
		SystemPrompt: "你是一个记忆清晰的助手，回答请简洁。",
		Model:        chatModel,
	})
	if err != nil {
		log.Fatalln(err)
	}
	demo.SubscribeText(agent)

	if err := demo.Ask(ctx, agent, "我叫小王，喜欢喝咖啡。请记住。"); err != nil {
		log.Fatalln(err)
	}

	savedHistory := agent.History()
	fmt.Printf("\n已导出历史消息 %d 条\n", len(savedHistory))
	agent.Close()

	restored, err := agentkit.New(ctx, &agentkit.Config{
		Name:         "assistant",
		SystemPrompt: "你是一个记忆清晰的助手，回答请简洁。",
		Model:        chatModel,
		History:      savedHistory,
	})
	if err != nil {
		log.Fatalln(err)
	}
	defer restored.Close()
	demo.SubscribeText(restored)

	if err := demo.Ask(ctx, restored, "我喜欢喝什么？"); err != nil {
		log.Fatalln(err)
	}

	restored.SetHistory(savedHistory)
	fmt.Printf("\nSetHistory 后展示消息 %d 条\n", len(restored.State().Messages()))
}
