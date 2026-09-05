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

type KnowledgeInput struct {
	Topic string `json:"topic" jsonschema:"description=需要检索的主题"`
}

func newKnowledgeTool() agentkit.Tool {
	tool, err := utils.InferTool("search_knowledge", "检索内部知识库",
		func(ctx context.Context, input *KnowledgeInput) (string, error) {
			agentkit.EmitToolUpdate(ctx, "正在检索 "+input.Topic)
			return "AgentKit 提供 Session、GoalRunner、HITL 和子智能体委派。", nil
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
		Name:         "coordinator",
		SystemPrompt: "你是协调者。涉及项目能力事实时，先委派 researcher 调研，再综合回答。",
		Model:        chatModel,
		SubAgents: []agentkit.SubAgentConfig{{
			Name:         "researcher",
			Description:  "调研一个明确问题并返回可靠证据",
			SystemPrompt: "使用知识库核对事实，只返回与委派问题有关的结论。",
			Tools:        []agentkit.Tool{newKnowledgeTool()},
		}},
		SubAgentPolicy: &agentkit.SubAgentPolicy{
			MaxDelegations: 4,
			MaxParallel:    2,
			Timeout:        2 * time.Minute,
		},
	})
	if err != nil {
		log.Fatalln(err)
	}
	defer agent.Close()

	agent.Subscribe(func(event agentkit.Event) {
		switch event.Type {
		case agentkit.EventDelegationStart:
			fmt.Printf("\n[delegate %s -> %s]\n", event.Delegation.ParentAgent, event.Delegation.Agent)
		case agentkit.EventDelegationEnd:
			fmt.Printf("[delegate done %s, err=%v]\n", event.Delegation.ID, event.Error)
		}
	})
	demo.SubscribeText(agent)

	if err := demo.Ask(ctx, agent, "AgentKit 是否适合需要中断恢复的长期任务？"); err != nil {
		log.Fatalln(err)
	}
}
