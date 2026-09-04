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
		SystemPrompt: "你是一个能持续对话的助手，回答请简洁。",
		Model:        chatModel,
		Compaction: &agentkit.CompactionConfig{
			MaxMessages:     6,
			KeepRecentTurns: 2,
		},
	})
	if err != nil {
		log.Fatalln(err)
	}
	defer agent.Close()
	demo.SubscribeText(agent)
	agent.Subscribe(func(event agentkit.Event) {
		if event.Type == agentkit.EventCompactionEnd {
			fmt.Printf("\n[上下文已压缩] %d → %d 条消息\n",
				event.Compaction.MessagesBefore,
				event.Compaction.MessagesAfter,
			)
		}
	})

	questions := []string{
		"记住：项目代号是 Aurora。",
		"这个项目使用 Go。",
		"稳定性是第一优先级。",
		"请复述项目代号、语言和第一优先级。",
	}
	for _, question := range questions {
		if err := demo.Ask(ctx, agent, question); err != nil {
			log.Fatalln(err)
		}
	}

	fmt.Printf("\n完整历史 %d 条，当前模型上下文 %d 条。\n",
		len(agent.History()),
		len(agent.ContextHistory()),
	)
}
