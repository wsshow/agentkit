package agentkit

import (
	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
)

// ChatModel 基础聊天模型接口
type ChatModel = model.BaseChatModel

// Tool 基础工具接口
type Tool = tool.BaseTool

// ChatModelAgentMiddleware 是 ChatModelAgent 扩展接口。
type ChatModelAgentMiddleware = adk.ChatModelAgentMiddleware

// BaseChatModelAgentMiddleware 提供 ChatModelAgentMiddleware 的默认空实现。
type BaseChatModelAgentMiddleware = adk.BaseChatModelAgentMiddleware

// ModelRetryConfig 配置 ChatModel 调用重试策略。
type ModelRetryConfig = adk.ModelRetryConfig

// ModelFailoverConfig 配置 ChatModel 失败转移策略。
type ModelFailoverConfig = adk.ModelFailoverConfig[*schema.Message]

// ToolCall 工具调用信息
type ToolCall = schema.ToolCall

// ResponseMeta 聊天响应元数据，包含 token 用量、完成原因、log probabilities 等。
// 通常附着于 EventMessageEnd 事件，在流式场景下来自最后一个 chunk。
type ResponseMeta = schema.ResponseMeta

// TokenUsage 表示一次聊天请求的 token 用量统计。
type TokenUsage = schema.TokenUsage
