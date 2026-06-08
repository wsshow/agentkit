package demo

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/cloudwego/eino-ext/components/model/openai"
	"github.com/joho/godotenv"
	"github.com/wsshow/agentkit"
)

func NewChatModel(ctx context.Context) (agentkit.ChatModel, error) {
	if err := godotenv.Load(); err != nil {
		return nil, err
	}
	return openai.NewChatModel(ctx, &openai.ChatModelConfig{
		APIKey:  os.Getenv("FEIKONG_API_KEY"),
		BaseURL: os.Getenv("FEIKONG_BASE_URL"),
		Model:   os.Getenv("FEIKONG_MODEL"),
	})
}

func SubscribeText(agent *agentkit.Agent) {
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
		case agentkit.EventToolStart:
			names := make([]string, 0, len(e.ToolCalls))
			for _, call := range e.ToolCalls {
				names = append(names, call.Function.Name)
			}
			fmt.Printf("\n[tool start] %s\n", strings.Join(names, ", "))
		case agentkit.EventToolUpdate:
			fmt.Printf("[tool update] %s\n", e.Content)
		case agentkit.EventToolEnd:
			fmt.Printf("[tool end] %s: %s\n", e.ToolName, e.Content)
		case agentkit.EventInterrupted:
			if len(e.Interrupt) > 0 {
				fmt.Printf("\n[interrupted] %v\n", e.Interrupt[0].Info)
			}
		case agentkit.EventError:
			fmt.Printf("\n[error] %v\n", e.Error)
		}
	})
}

func Ask(ctx context.Context, agent *agentkit.Agent, input string) error {
	fmt.Printf("\n用户: %s\n助手: ", input)
	return agent.Prompt(ctx, input)
}
