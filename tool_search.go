package agentkit

import (
	"context"
	"errors"
	"fmt"

	"github.com/cloudwego/eino/adk/middlewares/dynamictool/toolsearch"
)

const toolSearchToolName = "tool_search"

// ToolSearchConfig 配置按需发现的大型工具集。
// Tools 中的工具默认不进入模型上下文；模型先调用 tool_search，匹配的工具才会变为可见。
type ToolSearchConfig struct {
	Tools []Tool
	// UseModelNative 使用模型提供商原生的工具搜索协议。大多数模型保持 false 即可。
	UseModelNative bool
}

func validateToolSearchConfig(cfg *ToolSearchConfig) error {
	if cfg == nil {
		return nil
	}
	if len(cfg.Tools) == 0 {
		return errors.New("agentkit: tool search requires at least one dynamic tool")
	}
	for index, item := range cfg.Tools {
		if item == nil {
			return fmt.Errorf("agentkit: dynamic tool %d is nil", index)
		}
	}
	return nil
}

func newToolSearchMiddleware(ctx context.Context, cfg *ToolSearchConfig) (ChatModelAgentMiddleware, error) {
	middleware, err := toolsearch.New(ctx, &toolsearch.Config{
		DynamicTools:       append([]Tool(nil), cfg.Tools...),
		UseModelToolSearch: cfg.UseModelNative,
	})
	if err != nil {
		return nil, fmt.Errorf("agentkit: configure tool search: %w", err)
	}
	return middleware, nil
}

func dynamicTools(cfg *ToolSearchConfig) []Tool {
	if cfg == nil {
		return nil
	}
	return cfg.Tools
}

func validateReservedToolNames(ctx context.Context, tools []Tool, cfg *ToolSearchConfig, policy *ToolPolicy) error {
	if cfg == nil {
		return nil
	}
	for index, item := range tools {
		info, err := item.Info(ctx)
		if err != nil {
			return fmt.Errorf("agentkit: inspect tool %d for reserved names: %w", index, err)
		}
		if info.Name == toolSearchToolName {
			return fmt.Errorf("agentkit: tool name %q is reserved when tool search is enabled", toolSearchToolName)
		}
	}
	if policy != nil {
		for canonical, alias := range policy.Aliases {
			if canonical == toolSearchToolName {
				return fmt.Errorf("agentkit: tool name %q is reserved when tool search is enabled", toolSearchToolName)
			}
			for _, name := range alias.Names {
				if name == toolSearchToolName {
					return fmt.Errorf("agentkit: tool alias %q is reserved when tool search is enabled", toolSearchToolName)
				}
			}
		}
	}
	return nil
}
