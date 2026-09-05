package agentkit

import (
	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
)

// ChatModel 基础聊天模型接口
type ChatModel = model.BaseChatModel

// ModelOption 是单次模型调用选项，可通过 RunConfig 按请求传入。
type ModelOption = model.Option

// Tool 基础工具接口
type Tool = tool.BaseTool

// ToolOption 是单次工具调用选项，可通过 RunConfig 按请求传入。
type ToolOption = tool.Option

// ToolMiddleware 为工具调用添加自定义执行逻辑。
type ToolMiddleware = compose.ToolMiddleware

// InvokableToolMiddleware 包装非流式工具调用。
type InvokableToolMiddleware = compose.InvokableToolMiddleware

// InvokableToolEndpoint 是非流式工具调用端点。
type InvokableToolEndpoint = compose.InvokableToolEndpoint

// StreamableToolMiddleware 包装流式文本工具调用。
type StreamableToolMiddleware = compose.StreamableToolMiddleware

// StreamableToolEndpoint 是流式文本工具调用端点。
type StreamableToolEndpoint = compose.StreamableToolEndpoint

// EnhancedInvokableToolMiddleware 包装非流式多模态工具调用。
type EnhancedInvokableToolMiddleware = compose.EnhancedInvokableToolMiddleware

// EnhancedInvokableToolEndpoint 是非流式多模态工具调用端点。
type EnhancedInvokableToolEndpoint = compose.EnhancedInvokableToolEndpoint

// EnhancedStreamableToolMiddleware 包装流式多模态工具调用。
type EnhancedStreamableToolMiddleware = compose.EnhancedStreamableToolMiddleware

// EnhancedStreamableToolEndpoint 是流式多模态工具调用端点。
type EnhancedStreamableToolEndpoint = compose.EnhancedStreamableToolEndpoint

// ToolInput 描述一次工具调用输入。
type ToolInput = compose.ToolInput

// ToolOutput 描述一次非流式工具调用输出。
type ToolOutput = compose.ToolOutput

// StreamToolOutput 描述一次流式文本工具调用输出。
type StreamToolOutput = compose.StreamToolOutput

// EnhancedInvokableToolOutput 描述一次非流式多模态工具调用输出。
type EnhancedInvokableToolOutput = compose.EnhancedInvokableToolOutput

// EnhancedStreamableToolOutput 描述一次流式多模态工具调用输出。
type EnhancedStreamableToolOutput = compose.EnhancedStreamableToolOutput

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
