package agentkit

import "github.com/cloudwego/eino/schema"

// RunResult 汇总一次 Agent 执行新增的消息与最终响应。
// 即使执行返回错误，Result 仍会保留错误发生前已经产生的消息和用量。
type RunResult struct {
	Response         *schema.Message   // 本次执行最后一条 assistant 消息；可能为 nil
	Messages         []*schema.Message // 本次执行新增的完整消息
	Text             string            // Response.Content 的便捷访问
	ReasoningContent string            // Response.ReasoningContent 的便捷访问
	FinishReason     string            // Response.ResponseMeta.FinishReason 的便捷访问
	Usage            *TokenUsage       // 本次执行所有模型调用的累计 token 用量
	ToolCalls        []ToolCall        // 本次执行请求的全部工具调用
	Interrupts       []InterruptPoint  // 当前等待 Resume 的中断点
}

// IsInterrupted 报告本次执行是否正在等待 Resume。
func (r *RunResult) IsInterrupted() bool {
	return r != nil && len(r.Interrupts) > 0
}

func (a *Agent) resultSince(historyOffset int) *RunResult {
	a.mu.Lock()
	if historyOffset < 0 {
		historyOffset = 0
	}
	if historyOffset > len(a.history) {
		historyOffset = len(a.history)
	}
	messages := cloneHistoryMessages(a.history[historyOffset:])
	interrupts := cloneInterruptPoints(a.pendingInterrupts)
	a.mu.Unlock()

	result := &RunResult{
		Messages:   messages,
		Interrupts: interrupts,
	}
	for _, message := range messages {
		if message == nil || message.Role != schema.Assistant {
			continue
		}
		result.Response = cloneHistoryMessage(message)
		result.ToolCalls = append(result.ToolCalls, cloneToolCalls(message.ToolCalls)...)
		if message.ResponseMeta != nil && message.ResponseMeta.Usage != nil {
			result.Usage = addTokenUsage(result.Usage, message.ResponseMeta.Usage)
		}
	}
	if result.Response != nil {
		result.Text = result.Response.Content
		result.ReasoningContent = result.Response.ReasoningContent
		if result.Response.ResponseMeta != nil {
			result.FinishReason = result.Response.ResponseMeta.FinishReason
		}
	}
	return result
}

func addTokenUsage(total, current *TokenUsage) *TokenUsage {
	if current == nil {
		return total
	}
	if total == nil {
		total = &TokenUsage{}
	}
	total.PromptTokens += current.PromptTokens
	total.PromptTokenDetails.CachedTokens += current.PromptTokenDetails.CachedTokens
	total.CompletionTokens += current.CompletionTokens
	total.CompletionTokensDetails.ReasoningTokens += current.CompletionTokensDetails.ReasoningTokens
	total.TotalTokens += current.TotalTokens
	return total
}
