package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"

	protocol "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/wsshow/agentkit"
	"github.com/wsshow/agentkit/examples/internal/demo"
)

type wordCountInput struct {
	Text string `json:"text" jsonschema:"the text to count"`
}

func main() {
	ctx := context.Background()
	server := protocol.NewServer(&protocol.Implementation{Name: "demo", Version: "v1.0.0"}, nil)
	protocol.AddTool(server, &protocol.Tool{
		Name:        "word_count",
		Description: "Count words in text",
	}, func(_ context.Context, _ *protocol.CallToolRequest, input wordCountInput) (*protocol.CallToolResult, any, error) {
		return &protocol.CallToolResult{
			Content: []protocol.Content{&protocol.TextContent{Text: fmt.Sprintf("%d", len(strings.Fields(input.Text)))}},
		}, nil, nil
	})
	httpServer := httptest.NewServer(protocol.NewStreamableHTTPHandler(
		func(*http.Request) *protocol.Server { return server },
		nil,
	))
	defer httpServer.Close()

	chatModel, err := demo.NewChatModel(ctx)
	if err != nil {
		log.Fatalln(err)
	}
	agent, err := agentkit.New(ctx, &agentkit.Config{
		Name:         "assistant",
		SystemPrompt: "需要统计字数时必须调用 demo__word_count 工具。",
		Model:        chatModel,
		MCP: &agentkit.MCPConfig{Servers: []agentkit.MCPServerConfig{{
			Name:       "demo",
			Transport:  agentkit.MCPTransportStreamableHTTP,
			URL:        httpServer.URL,
			ToolPrefix: "demo__",
		}}},
	})
	if err != nil {
		log.Fatalln(err)
	}
	defer agent.Close()
	demo.SubscribeText(agent)

	if err := demo.Ask(ctx, agent, "请告诉我这句话有多少个英文单词：AgentKit keeps MCP simple and stable"); err != nil {
		log.Fatalln(err)
	}
}
