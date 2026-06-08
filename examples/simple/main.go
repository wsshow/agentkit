// Package main 演示 agentkit 的最简单用法：纯文本对话，无工具。
package main

import (
	"context"
	"fmt"
	"log"
	"os"

	"github.com/cloudwego/eino-ext/components/model/openai"
	"github.com/joho/godotenv"
	"github.com/wsshow/agentkit"
)

func main() {
	ctx := context.Background()

	// 加载环境变量
	if err := godotenv.Load(); err != nil {
		log.Fatalln(err)
	}

	// 创建聊天模型
	chatModel, err := openai.NewChatModel(ctx, &openai.ChatModelConfig{
		APIKey:  os.Getenv("FEIKONG_OPENAI_API_KEY"),
		BaseURL: os.Getenv("FEIKONG_OPENAI_BASE_URL"),
		Model:   os.Getenv("FEIKONG_OPENAI_MODEL"),
	})
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

	// 订阅事件：只处理流式输出和错误
	agent.Subscribe(func(e agentkit.Event) {
		switch e.Type {
		case agentkit.EventReasoningDelta:
			fmt.Print(e.Delta)
		case agentkit.EventMessageDelta:
			fmt.Print(e.Delta)
		case agentkit.EventMessageEnd:
			if e.Role == agentkit.RoleAssistant {
				fmt.Println()
			}
		case agentkit.EventError:
			fmt.Printf("[错误] %v\n", e.Error)
		}
	})

	// 多轮对话
	questions := []string{
		"你好！请用一句话介绍 agentkit。",
		"它基于哪个框架？",
		"谢谢！",
	}

	for _, q := range questions {
		fmt.Printf("\n用户: %s\n助手: ", q)
		if err := agent.Prompt(ctx, q); err != nil {
			log.Fatalln(err)
		}
	}
}
