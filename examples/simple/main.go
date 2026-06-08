// Package main 演示 agentkit 的最简单用法：纯文本对话，无工具。
package main

import (
	"context"
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

	// 创建 Agent
	agent, err := agentkit.New(ctx, &agentkit.Config{
		Name:         "assistant",
		SystemPrompt: "你是一个有用的助手，回答请简洁明了。",
		Model:        chatModel,
	})
	if err != nil {
		log.Fatalln(err)
	}
	defer agent.Close()

	demo.SubscribeText(agent)

	// 多轮对话
	questions := []string{
		"你好！请用一句话介绍 wsshow/agentkit。",
		"它基于哪个框架？",
		"谢谢！",
	}

	for _, q := range questions {
		if err := demo.Ask(ctx, agent, q); err != nil {
			log.Fatalln(err)
		}
	}
}
