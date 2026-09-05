package agentkit

import (
	"context"

	"github.com/cloudwego/eino/adk"
)

type runConfigContextKey struct{}

// RunConfig 配置一次请求；它只影响携带它的 context，不会修改 Agent 的全局配置。
type RunConfig struct {
	// ModelOptions 会传给本次请求中的每次模型调用。
	ModelOptions []ModelOption
	// ToolOptions 会传给本次请求中的每次工具调用。
	ToolOptions []ToolOption
	// Values 可用于 SystemPrompt 的 {name} 占位符，也可在工具和中间件中通过 RunValue 读取。
	Values map[string]any
}

// WithRunConfig 返回携带请求级配置的 context。
// 配置的切片、map 和常见嵌套值会被复制，调用方可在返回后安全复用原始容器。
func WithRunConfig(ctx context.Context, config RunConfig) context.Context {
	cloned := RunConfig{
		ModelOptions: append([]ModelOption(nil), config.ModelOptions...),
		ToolOptions:  append([]ToolOption(nil), config.ToolOptions...),
		Values:       cloneMap(config.Values),
	}
	return context.WithValue(ctx, runConfigContextKey{}, cloned)
}

// RunValue 读取当前请求中的一个类型匹配的值。
func RunValue[T any](ctx context.Context, key string) (T, bool) {
	var zero T
	if ctx == nil {
		return zero, false
	}
	value, ok := adk.GetSessionValue(ctx, key)
	if !ok {
		if config, exists := runConfigFromContext(ctx); exists {
			value, ok = config.Values[key]
		}
	}
	if !ok {
		return zero, false
	}
	typed, ok := cloneMapValue(value).(T)
	if !ok {
		return zero, false
	}
	return typed, true
}

// RunValues 返回当前请求值的副本。
func RunValues(ctx context.Context) map[string]any {
	if ctx == nil {
		return map[string]any{}
	}
	values := adk.GetSessionValues(ctx)
	if len(values) > 0 {
		return cloneMap(values)
	}
	if config, ok := runConfigFromContext(ctx); ok {
		return cloneMap(config.Values)
	}
	return map[string]any{}
}

// SetRunValue 在工具或中间件执行期间更新当前请求值。
// 该值可被同一次底层运行中的后续工具和中间件读取。
func SetRunValue(ctx context.Context, key string, value any) {
	if ctx != nil {
		adk.AddSessionValue(ctx, key, cloneMapValue(value))
	}
}

func runConfigFromContext(ctx context.Context) (RunConfig, bool) {
	if ctx == nil {
		return RunConfig{}, false
	}
	config, ok := ctx.Value(runConfigContextKey{}).(RunConfig)
	return config, ok
}

func agentRunOptions(ctx context.Context) []adk.AgentRunOption {
	config, ok := runConfigFromContext(ctx)
	if !ok {
		return nil
	}
	options := make([]adk.AgentRunOption, 0, 3)
	if len(config.ModelOptions) > 0 {
		options = append(options, adk.WithChatModelOptions(config.ModelOptions))
	}
	if len(config.ToolOptions) > 0 {
		options = append(options, adk.WithToolOptions(config.ToolOptions))
	}
	if len(config.Values) > 0 {
		options = append(options, adk.WithSessionValues(cloneMap(config.Values)))
	}
	return options
}
