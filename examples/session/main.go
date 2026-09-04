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
	store, err := agentkit.NewFileSessionStore(".agentkit-sessions")
	if err != nil {
		log.Fatalln(err)
	}

	agent, err := agentkit.New(ctx, &agentkit.Config{
		Name:         "assistant",
		SystemPrompt: "你是一个记忆清晰的助手，回答请简洁。",
		Model:        chatModel,
		Session: &agentkit.SessionConfig{
			ID:    "demo-user",
			Store: store,
		},
	})
	if err != nil {
		log.Fatalln(err)
	}
	defer agent.Close()
	demo.SubscribeText(agent)

	if err := demo.Ask(ctx, agent, "记住我最喜欢的颜色是蓝色，然后告诉我你记住了什么。"); err != nil {
		log.Fatalln(err)
	}

	sessions, err := store.List(ctx)
	if err != nil {
		log.Fatalln(err)
	}
	fmt.Printf("\n已保存 %d 个会话；再次运行本示例会自动恢复历史。\n", len(sessions))
}
