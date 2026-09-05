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

	manager, err := agentkit.NewSessionManager(&agentkit.SessionManagerConfig{
		Store:   store,
		OwnerID: "demo-user",
		AgentConfig: &agentkit.Config{
			Name:         "assistant",
			SystemPrompt: "你是一个记忆清晰的助手，回答请简洁。",
			Model:        chatModel,
		},
	})
	if err != nil {
		log.Fatalln(err)
	}
	defer manager.Close()

	agent, created, err := manager.OpenOrCreate(ctx, agentkit.CreateSessionOptions{
		ID:    "main",
		Title: "演示会话",
		Tags:  []string{"demo"},
	})
	if err != nil {
		log.Fatalln(err)
	}
	demo.SubscribeText(agent)

	if err := demo.Ask(ctx, agent, "记住我最喜欢的颜色是蓝色，然后告诉我你记住了什么。"); err != nil {
		log.Fatalln(err)
	}

	sessions, err := manager.List(ctx, agentkit.SessionQuery{})
	if err != nil {
		log.Fatalln(err)
	}
	fmt.Printf("\n当前用户有 %d 个会话；本次是否新建：%v。再次运行会自动恢复历史。\n", len(sessions.Sessions), created)
}
