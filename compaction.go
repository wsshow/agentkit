package agentkit

import (
	"context"
	"fmt"
	"math"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/adk/middlewares/summarization"
	"github.com/cloudwego/eino/schema"
)

// DefaultCompactionMaxTokens 是未指定触发条件时使用的默认上下文 token 上限。
const DefaultCompactionMaxTokens = 100_000

// CompactionConfig 配置基于摘要的自动上下文压缩。
type CompactionConfig struct {
	// Model 用于生成摘要。为空时复用 Agent 的主模型。
	Model ChatModel
	// MaxTokens 在估算的上下文 token 数超过该值时压缩。
	// MaxTokens 和 MaxMessages 都为 0 时默认使用 DefaultCompactionMaxTokens。
	MaxTokens int
	// MaxMessages 在模型上下文消息数超过该值时压缩。
	MaxMessages int
	// KeepRecentTurns 原样保留最近的用户轮次，默认 1。
	KeepRecentTurns int
	// SummaryPrompt 是可选的自定义摘要指令。
	SummaryPrompt string
}

// CompactionInfo 描述一次上下文压缩前后的消息数量。
type CompactionInfo struct {
	MessagesBefore int
	MessagesAfter  int
}

func validateCompactionConfig(cfg *CompactionConfig) error {
	if cfg == nil {
		return nil
	}
	if cfg.MaxTokens < 0 {
		return fmt.Errorf("agentkit: compaction max tokens must not be negative: %d", cfg.MaxTokens)
	}
	if cfg.MaxMessages < 0 {
		return fmt.Errorf("agentkit: compaction max messages must not be negative: %d", cfg.MaxMessages)
	}
	if cfg.KeepRecentTurns < 0 {
		return fmt.Errorf("agentkit: compaction keep recent turns must not be negative: %d", cfg.KeepRecentTurns)
	}
	return nil
}

func newCompactionMiddleware(ctx context.Context, agent *Agent, primaryModel ChatModel, cfg *CompactionConfig) (ChatModelAgentMiddleware, error) {
	model := cfg.Model
	if model == nil {
		model = primaryModel
	}
	maxTokens := cfg.MaxTokens
	if maxTokens == 0 {
		if cfg.MaxMessages == 0 {
			maxTokens = DefaultCompactionMaxTokens
		} else {
			// Eino treats a zero token threshold as "always trigger", so use an
			// unreachable value when only a message threshold was requested.
			maxTokens = math.MaxInt
		}
	}
	keepRecentTurns := cfg.KeepRecentTurns
	if keepRecentTurns == 0 {
		keepRecentTurns = 1
	}

	middleware, err := summarization.New(ctx, &summarization.Config{
		Model: model,
		Trigger: &summarization.TriggerCondition{
			ContextTokens:   maxTokens,
			ContextMessages: cfg.MaxMessages,
		},
		EmitInternalEvents: true,
		UserInstruction:    cfg.SummaryPrompt,
		GenModelInput: func(_ context.Context, systemInstruction, userInstruction *schema.Message, original []*schema.Message) ([]*schema.Message, error) {
			older, _ := splitCompactionHistory(original, keepRecentTurns)
			contextMessages := withoutLeadingSystemMessages(older)
			input := make([]*schema.Message, 0, len(contextMessages)+2)
			input = append(input, systemInstruction)
			input = append(input, contextMessages...)
			input = append(input, userInstruction)
			return input, nil
		},
		Finalize: func(ctx context.Context, original []*schema.Message, summary *schema.Message) ([]*schema.Message, error) {
			older, recent := splitCompactionHistory(original, keepRecentTurns)
			compacted, err := summarization.DefaultFinalize(ctx, older, summary)
			if err != nil {
				return nil, err
			}
			result := make([]*schema.Message, 0, len(compacted)+len(recent))
			result = append(result, compacted...)
			result = append(result, recent...)
			return result, nil
		},
		Callback: func(_ context.Context, _, after adk.ChatModelAgentState) error {
			agent.replaceContextHistory(after.Messages)
			return nil
		},
	})
	if err != nil {
		return nil, fmt.Errorf("agentkit: configure compaction: %w", err)
	}
	return middleware, nil
}

func splitCompactionHistory(messages []*schema.Message, keepRecentTurns int) (older, recent []*schema.Message) {
	cut := len(messages)
	turns := 0
	for i := len(messages) - 1; i >= 0; i-- {
		message := messages[i]
		if message != nil && message.Role == schema.User {
			turns++
			if turns == keepRecentTurns {
				cut = i
				break
			}
		}
	}

	systemCount := len(messages) - len(withoutLeadingSystemMessages(messages))
	if cut <= systemCount {
		// There is no older conversation to summarize. Falling back to the
		// complete context is safer than asking the model to summarize nothing.
		return messages, nil
	}
	return messages[:cut], messages[cut:]
}

func withoutLeadingSystemMessages(messages []*schema.Message) []*schema.Message {
	for i, message := range messages {
		if message == nil || message.Role != schema.System {
			return messages[i:]
		}
	}
	return nil
}

func (a *Agent) processCompactionAction(agentName string, value any) {
	action, ok := value.(*summarization.CustomizedAction)
	if !ok || action == nil {
		return
	}

	switch action.Type {
	case summarization.ActionTypeBeforeSummarize:
		if action.Before == nil {
			return
		}
		before := len(action.Before.Messages)
		a.mu.Lock()
		a.compactionMessagesBefore = before
		a.mu.Unlock()
		a.emtr.Emit(Event{
			Type:       EventCompactionStart,
			Agent:      agentName,
			Compaction: &CompactionInfo{MessagesBefore: before},
		})
	case summarization.ActionTypeAfterSummarize:
		if action.After == nil {
			return
		}
		a.mu.Lock()
		before := a.compactionMessagesBefore
		a.mu.Unlock()
		a.emtr.Emit(Event{
			Type:  EventCompactionEnd,
			Agent: agentName,
			Compaction: &CompactionInfo{
				MessagesBefore: before,
				MessagesAfter:  len(action.After.Messages),
			},
		})
	}
}
