package main

import (
	"context"
	"fmt"
	"log"

	"github.com/cloudwego/eino/components/tool/utils"
	"github.com/wsshow/agentkit"
	"github.com/wsshow/agentkit/examples/internal/demo"
)

type WeatherInput struct {
	City string `json:"city" jsonschema:"description=城市名称，例如：北京、上海、广州"`
}

type WeatherOutput struct {
	City        string `json:"city"`
	Temperature string `json:"temperature"`
	Condition   string `json:"condition"`
}

func newWeatherTool() agentkit.Tool {
	tool, err := utils.InferTool("get_weather", "查询指定城市的当前天气信息",
		func(ctx context.Context, input *WeatherInput) (*WeatherOutput, error) {
			agentkit.EmitToolUpdate(ctx, fmt.Sprintf("正在查询 %s 的天气", input.City))
			db := map[string]*WeatherOutput{
				"北京": {City: "北京", Temperature: "22°C", Condition: "晴"},
				"上海": {City: "上海", Temperature: "26°C", Condition: "多云"},
				"广州": {City: "广州", Temperature: "30°C", Condition: "阵雨"},
			}
			if out, ok := db[input.City]; ok {
				return out, nil
			}
			return &WeatherOutput{City: input.City, Temperature: "25°C", Condition: "晴"}, nil
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
		Name:         "weather-assistant",
		SystemPrompt: "你可以调用工具查询天气。回答请简洁。",
		Model:        chatModel,
		Tools:        []agentkit.Tool{newWeatherTool()},
	})
	if err != nil {
		log.Fatalln(err)
	}
	defer agent.Close()

	demo.SubscribeText(agent)

	if err := demo.Ask(ctx, agent, "同时查一下北京和上海的天气"); err != nil {
		log.Fatalln(err)
	}
}
