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
