package main

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/cloudwego/eino/components/tool/utils"
	"github.com/wsshow/agentkit"
	"github.com/wsshow/agentkit/examples/internal/demo"
)

type SearchInput struct {
	Query string `json:"query" jsonschema:"description=搜索关键词"`
}

func newSearchTool() agentkit.Tool {
	tool, err := utils.InferTool("search", "搜索指定关键词",
		func(ctx context.Context, input *SearchInput) (string, error) {
			agentkit.EmitToolUpdate(ctx, "正在搜索："+input.Query)
			time.Sleep(500 * time.Millisecond)
			return "搜索结果：" + input.Query + " 相关内容", nil
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
		SystemPrompt: "你可以使用搜索工具。回答请简洁。",
		Model:        chatModel,
		Tools:        []agentkit.Tool{newSearchTool()},
	})
	if err != nil {
		log.Fatalln(err)
	}
	defer agent.Close()

	demo.SubscribeText(agent)

	fmt.Println("=== FollowUp：当前任务完成后继续处理 ===")
	agent.FollowUp("再总结一下刚才的搜索结论")
	if err := demo.Ask(ctx, agent, "搜索 agentkit 的核心能力"); err != nil {
		log.Fatalln(err)
	}

	fmt.Println("\n=== Steer：当前工具批次完成后改向 ===")
	steered := false
	unsubscribe := agent.Subscribe(func(e agentkit.Event) {
		if e.Type == agentkit.EventToolStart && !steered {
			steered = true
			agent.Steer("不要继续展开搜索结果，改为用一句话说明你能做什么")
		}
	})
	defer unsubscribe()

	if err := demo.Ask(ctx, agent, "搜索北京、上海、广州的天气新闻"); err != nil {
		log.Fatalln(err)
	}
}
