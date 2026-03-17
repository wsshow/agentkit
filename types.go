package agentkit

import (
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
)

// ChatModel 基础聊天模型接口
type ChatModel = model.BaseChatModel

// Tool 基础工具接口
type Tool = tool.BaseTool

// ToolCall 工具调用信息
type ToolCall = schema.ToolCall

// ResponseMeta 聊天响应元数据，包含 token 用量、完成原因、log probabilities 等。
// 通常附着于 EventMessageEnd 事件，在流式场景下来自最后一个 chunk。
type ResponseMeta = schema.ResponseMeta

// TokenUsage 表示一次聊天请求的 token 用量统计。
type TokenUsage = schema.TokenUsage
