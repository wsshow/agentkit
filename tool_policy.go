package agentkit

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/cloudwego/eino/compose"
)

// ErrToolPolicyPanic 表示 ToolPolicy 的用户回调发生 panic。
var ErrToolPolicyPanic = errors.New("agentkit: tool policy callback panicked")

// ToolAlias 配置一个工具可接受的名称和顶层 JSON 参数别名。
type ToolAlias struct {
	Names     []string            // 工具名称别名
	Arguments map[string][]string // canonical argument -> aliases
}

// ToolPolicy 集中配置工具分发与执行行为。
type ToolPolicy struct {
	Aliases          map[string]ToolAlias
	UnknownTool      func(ctx context.Context, name, arguments string) (string, error)
	RewriteArguments func(ctx context.Context, name, arguments string) (string, error)
	Sequential       bool
	// Timeout 限制单次工具调用（含钩子）的最长执行时间。零值表示不限制。
	Timeout time.Duration
	// MaxResultChars 限制返回给模型的文本字符数。零值使用 DefaultToolResultMaxChars，-1 表示不限制。
	MaxResultChars int
	// BeforeTool 在工具执行前调用；返回错误可拒绝本次调用。
	BeforeTool func(ctx context.Context, call ToolInvocation) error
	// AfterTool 在工具结束后调用，可用于审计和指标记录。
	AfterTool   func(ctx context.Context, call ToolInvocation, outcome ToolOutcome)
	Middlewares []ToolMiddleware
}

func (p *ToolPolicy) toolsNodeConfig(tools []Tool) compose.ToolsNodeConfig {
	config := compose.ToolsNodeConfig{Tools: tools}
	config.ToolCallMiddlewares = []compose.ToolMiddleware{p.executionMiddleware()}
	if p == nil {
		return config
	}
	config.ToolAliases = make(map[string]compose.ToolAliasConfig, len(p.Aliases))
	for name, alias := range p.Aliases {
		arguments := make(map[string][]string, len(alias.Arguments))
		for argument, aliases := range alias.Arguments {
			arguments[argument] = append([]string(nil), aliases...)
		}
		config.ToolAliases[name] = compose.ToolAliasConfig{
			NameAliases:      append([]string(nil), alias.Names...),
			ArgumentsAliases: arguments,
		}
	}
	config.UnknownToolsHandler = guardedToolStringHandler("UnknownTool", p.UnknownTool)
	config.ToolArgumentsHandler = guardedToolStringHandler("RewriteArguments", p.RewriteArguments)
	config.ExecuteSequentially = p.Sequential
	config.ToolCallMiddlewares = append(config.ToolCallMiddlewares, p.Middlewares...)
	return config
}

func guardedToolStringHandler(
	name string,
	handler func(context.Context, string, string) (string, error),
) func(context.Context, string, string) (string, error) {
	if handler == nil {
		return nil
	}
	return func(ctx context.Context, toolName, arguments string) (result string, err error) {
		defer recoverToolPolicyPanic(name, &err)
		return handler(ctx, toolName, arguments)
	}
}

func recoverToolPolicyPanic(name string, err *error) {
	if value := recover(); value != nil {
		*err = fmt.Errorf("%w in %s: %v", ErrToolPolicyPanic, name, value)
	}
}

func validateToolPolicy(ctx context.Context, tools []Tool, skills *SkillsConfig, policy *ToolPolicy) (map[string]struct{}, error) {
	canonicalNames := make(map[string]struct{}, len(tools)+1)
	if skills != nil {
		name := skills.ToolName
		if name == "" {
			name = "skill"
		}
		canonicalNames[name] = struct{}{}
	}
	for index, item := range tools {
		info, err := inspectToolInfo(ctx, item)
		if err != nil {
			return nil, fmt.Errorf("agentkit: inspect tool %d for policy: %w", index, err)
		}
		canonicalNames[info.Name] = struct{}{}
	}
	if policy == nil {
		return canonicalNames, nil
	}
	if policy.Timeout < 0 {
		return nil, fmt.Errorf("agentkit: tool policy timeout must not be negative")
	}
	if policy.MaxResultChars < -1 {
		return nil, fmt.Errorf("agentkit: tool policy max result chars must be -1 or greater")
	}

	allNames := make(map[string]string, len(canonicalNames)+len(policy.Aliases))
	for name := range canonicalNames {
		allNames[name] = name
	}
	for canonical, alias := range policy.Aliases {
		if strings.TrimSpace(canonical) != canonical || canonical == "" {
			return nil, fmt.Errorf("agentkit: tool policy canonical name must be non-empty without surrounding whitespace: %q", canonical)
		}
		if _, ok := canonicalNames[canonical]; !ok {
			return nil, fmt.Errorf("agentkit: tool policy references unknown canonical tool %q", canonical)
		}
		for _, name := range alias.Names {
			if strings.TrimSpace(name) != name || name == "" {
				return nil, fmt.Errorf("agentkit: tool %q alias must be non-empty without surrounding whitespace: %q", canonical, name)
			}
			if owner, exists := allNames[name]; exists {
				return nil, fmt.Errorf("agentkit: tool alias %q for %q conflicts with %q", name, canonical, owner)
			}
			allNames[name] = canonical
		}

		argumentNames := make(map[string]string)
		for argument := range alias.Arguments {
			if strings.TrimSpace(argument) != argument || argument == "" {
				return nil, fmt.Errorf("agentkit: tool %q canonical argument must be non-empty without surrounding whitespace: %q", canonical, argument)
			}
			argumentNames[argument] = argument
		}
		for argument, aliases := range alias.Arguments {
			for _, name := range aliases {
				if strings.TrimSpace(name) != name || name == "" {
					return nil, fmt.Errorf("agentkit: tool %q argument alias must be non-empty without surrounding whitespace: %q", canonical, name)
				}
				if owner, exists := argumentNames[name]; exists {
					return nil, fmt.Errorf("agentkit: tool %q argument alias %q conflicts with %q", canonical, name, owner)
				}
				argumentNames[name] = argument
			}
		}
	}
	knownNames := make(map[string]struct{}, len(allNames))
	for name := range allNames {
		knownNames[name] = struct{}{}
	}
	return knownNames, nil
}
