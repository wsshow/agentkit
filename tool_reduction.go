package agentkit

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/adk/filesystem"
	"github.com/cloudwego/eino/adk/middlewares/reduction"
	"github.com/cloudwego/eino/components/tool/utils"
	"github.com/google/uuid"
)

const (
	// ToolResultReadToolName 是启用工具结果压缩后自动注册的只读回取工具名。
	ToolResultReadToolName = "read_tool_result"
	// DefaultToolReductionMaxResultBytes 是单个工具结果触发持久化卸载的默认字节数。
	DefaultToolReductionMaxResultBytes = 50_000
	// DefaultToolReductionMaxContextTokens 是清理旧工具轮次的默认上下文 token 阈值。
	DefaultToolReductionMaxContextTokens int64 = 160_000
	// DefaultToolReductionKeepRecentRounds 是清理时原样保留的最近工具调用轮数。
	DefaultToolReductionKeepRecentRounds = 1
	// DefaultToolResultReadMaxChars 是一次回取最多返回给模型的 Unicode 字符数。
	DefaultToolResultReadMaxChars = 20_000
)

// ToolReductionConfig 配置大型工具结果卸载和旧工具轮次清理。
// 零值使用安全默认值；Store 为空时优先复用 Session 配套存储，否则使用内存存储。
type ToolReductionConfig struct {
	Store ToolResultStore
	// MaxResultBytes 是单个工具结果触发卸载的字节数，默认 50,000。
	MaxResultBytes int
	// MaxContextTokens 是开始清理旧工具轮次的上下文 token 估算值，默认 160,000。
	MaxContextTokens int64
	// KeepRecentToolRounds 是清理时原样保留的最近工具调用轮数，默认 1。
	KeepRecentToolRounds int
}

type toolResultBackend struct {
	store     ToolResultStore
	sessionID string
}

func (b *toolResultBackend) Write(ctx context.Context, request *filesystem.WriteRequest) error {
	if request == nil {
		return errors.New("agentkit: tool result write request is required")
	}
	if err := toolResultStoreSave(ctx, b.store, &StoredToolResult{
		ID: request.FilePath, SessionID: b.sessionID, Content: request.Content,
	}); err != nil {
		return fmt.Errorf("agentkit: save reduced tool result: %w", err)
	}
	return nil
}

type readToolResultInput struct {
	ID     string `json:"id" jsonschema:"required,description=Opaque result ID shown in the reduction notice"`
	Offset int    `json:"offset,omitempty" jsonschema:"description=Zero-based Unicode character offset; defaults to 0"`
	Limit  int    `json:"limit,omitempty" jsonschema:"description=Maximum Unicode characters to return; defaults to and cannot exceed 20000"`
}

func validateToolReductionConfig(cfg *ToolReductionConfig) error {
	if cfg == nil {
		return nil
	}
	if cfg.MaxResultBytes < 0 {
		return fmt.Errorf("agentkit: tool reduction max result bytes must not be negative: %d", cfg.MaxResultBytes)
	}
	if cfg.MaxContextTokens < 0 {
		return fmt.Errorf("agentkit: tool reduction max context tokens must not be negative: %d", cfg.MaxContextTokens)
	}
	if cfg.KeepRecentToolRounds < 0 {
		return fmt.Errorf("agentkit: tool reduction keep recent tool rounds must not be negative: %d", cfg.KeepRecentToolRounds)
	}
	return nil
}

func newToolReduction(
	ctx context.Context,
	agent *Agent,
	cfg *ToolReductionConfig,
	session *SessionConfig,
) (ChatModelAgentMiddleware, Tool, ToolResultStore, error) {
	store := cfg.Store
	if store == nil && session != nil {
		if provider, ok := session.Store.(ToolResultStoreProvider); ok {
			var err error
			store, err = providedStore("tool result store provider", provider.ToolResultStore)
			if err != nil {
				return nil, nil, nil, err
			}
		}
	}
	if store == nil {
		store = NewMemoryToolResultStore()
	}

	sessionID := ""
	if session != nil {
		sessionID = session.ID
	}
	reader, err := newToolResultReader(store, sessionID)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("agentkit: configure tool result reader: %w", err)
	}
	maxResultBytes := cfg.MaxResultBytes
	if maxResultBytes == 0 {
		maxResultBytes = DefaultToolReductionMaxResultBytes
	}
	maxContextTokens := cfg.MaxContextTokens
	if maxContextTokens == 0 {
		maxContextTokens = DefaultToolReductionMaxContextTokens
	}
	keepRecentRounds := cfg.KeepRecentToolRounds
	if keepRecentRounds == 0 {
		keepRecentRounds = DefaultToolReductionKeepRecentRounds
	}
	newResultID := func(ctx context.Context, _ *reduction.ToolDetail) (string, error) {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		return uuid.NewString(), nil
	}
	middleware, err := reduction.New(ctx, &reduction.Config{
		Backend:                   &toolResultBackend{store: store, sessionID: sessionID},
		ReadFileToolName:          ToolResultReadToolName,
		GenTruncOffloadFilePath:   newResultID,
		GenClearOffloadFilePath:   newResultID,
		MaxLengthForTrunc:         maxResultBytes,
		MaxTokensForClear:         maxContextTokens,
		ClearRetentionSuffixLimit: keepRecentRounds,
		TruncExcludeTools:         []string{ToolResultReadToolName},
		ClearPostProcess: func(ctx context.Context, state *adk.ChatModelAgentState) context.Context {
			agent.replaceContextHistory(state.Messages)
			return ctx
		},
	})
	if err != nil {
		return nil, nil, nil, fmt.Errorf("agentkit: configure tool reduction: %w", err)
	}
	return middleware, reader, store, nil
}

func newToolResultReader(store ToolResultStore, sessionID string) (Tool, error) {
	return utils.InferTool(
		ToolResultReadToolName,
		"Read a bounded chunk of a complete tool result previously moved out of context. Use the opaque ID from the reduction notice and continue with next_offset when present.",
		func(ctx context.Context, input *readToolResultInput) (string, error) {
			if input == nil {
				return "", errors.New("agentkit: read tool result input is required")
			}
			if strings.TrimSpace(input.ID) == "" {
				return "", errors.New("agentkit: tool result ID is required")
			}
			if input.Offset < 0 {
				return "", errors.New("agentkit: tool result offset must not be negative")
			}
			if input.Limit < 0 {
				return "", errors.New("agentkit: tool result limit must not be negative")
			}
			result, err := toolResultStoreLoad(ctx, store, input.ID)
			if err != nil {
				return "", err
			}
			if result.SessionID != sessionID {
				return "", fmt.Errorf("%w: %s", ErrToolResultAccessDenied, input.ID)
			}
			content := []rune(result.Content)
			if input.Offset > len(content) {
				return "", fmt.Errorf("agentkit: tool result offset %d exceeds total characters %d", input.Offset, len(content))
			}
			limit := input.Limit
			if limit == 0 || limit > DefaultToolResultReadMaxChars {
				limit = DefaultToolResultReadMaxChars
			}
			end := input.Offset + limit
			if end > len(content) {
				end = len(content)
			}
			var output strings.Builder
			output.WriteString(string(content[input.Offset:end]))
			fmt.Fprintf(&output, "\n\n[tool result %q: characters %d-%d of %d", input.ID, input.Offset, end, len(content))
			if end < len(content) {
				fmt.Fprintf(&output, "; next_offset=%d", end)
			}
			output.WriteString("]")
			return output.String(), nil
		},
	)
}

func toolPolicyForReduction(policy *ToolPolicy, enabled bool) *ToolPolicy {
	if !enabled {
		return policy
	}
	if policy == nil {
		return &ToolPolicy{MaxResultChars: -1}
	}
	cloned := *policy
	cloned.MaxResultChars = -1
	return &cloned
}

func mcpConfigForReduction(cfg *MCPConfig, enabled bool) *MCPConfig {
	if !enabled || cfg == nil || cfg.MaxResultChars != 0 {
		return cfg
	}
	cloned := *cfg
	cloned.MaxResultChars = -1
	return &cloned
}
