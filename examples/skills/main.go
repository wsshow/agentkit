package main

import (
	"context"
	"log"
	"path/filepath"
	"runtime"

	"github.com/wsshow/agentkit"
	"github.com/wsshow/agentkit/examples/internal/demo"
)

func main() {
	ctx := context.Background()
	chatModel, err := demo.NewChatModel(ctx)
	if err != nil {
		log.Fatalln(err)
	}
	_, sourceFile, _, ok := runtime.Caller(0)
	if !ok {
		log.Fatalln("无法定位示例技能目录")
	}

	agent, err := agentkit.New(ctx, &agentkit.Config{
		Name:         "assistant",
		SystemPrompt: "回答前先加载最相关的技能。",
		Model:        chatModel,
		Skills: &agentkit.SkillsConfig{
			Paths: []string{filepath.Join(filepath.Dir(sourceFile), "skills")},
		},
	})
	if err != nil {
		log.Fatalln(err)
	}
	defer agent.Close()
	demo.SubscribeText(agent)

	if err := demo.Ask(ctx, agent, "请简要解释为什么 Go 适合构建网络服务。"); err != nil {
		log.Fatalln(err)
	}
}
