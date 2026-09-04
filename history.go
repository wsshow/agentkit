package agentkit

import "github.com/cloudwego/eino/schema"

func (a *Agent) replaceHistory(history []*schema.Message) {
	a.restoreHistory(history, history, false)
}

func (a *Agent) restoreHistory(history, contextHistory []*schema.Message, compacted bool) {
	clonedHistory := cloneHistoryMessages(history)
	clonedContext := cloneHistoryMessages(contextHistory)
	stateMessages := stateMessagesFromHistory(a.name, clonedHistory)

	a.mu.Lock()
	a.history = clonedHistory
	a.contextHistory = clonedContext
	a.contextCompacted = compacted
	a.steeringQueue = nil
	a.followUpQueue = nil
	a.toolCalls = make(map[string]toolCallInfo)
	a.toolBatchDone = nil
	a.toolBatchDoneFlag = false
	a.compactionMessagesBefore = 0
	a.mu.Unlock()

	a.state.replaceMessages(stateMessages)
}

func (a *Agent) replaceContextHistory(history []*schema.Message) {
	a.mu.Lock()
	a.contextHistory = cloneHistoryMessages(history)
	a.contextCompacted = true
	a.mu.Unlock()
}

func (a *Agent) sessionContextLocked() []*schema.Message {
	if !a.contextCompacted {
		return nil
	}
	return cloneHistoryMessages(a.contextHistory)
}

func stateMessagesFromHistory(agentName string, history []*schema.Message) []Message {
	messages := make([]Message, 0, len(history))
	for _, msg := range history {
		if msg == nil {
			continue
		}
		switch msg.Role {
		case schema.User:
			messages = append(messages, Message{
				Role:    RoleUser,
				Content: userMessageText(msg),
			})
		case schema.Assistant:
			if msg.Content == "" && msg.ReasoningContent == "" {
				continue
			}
			messages = append(messages, Message{
				Role:             RoleAssistant,
				Agent:            agentName,
				Content:          msg.Content,
				ReasoningContent: msg.ReasoningContent,
			})
		}
	}
	return messages
}

func userMessageText(msg *schema.Message) string {
	if msg.Content != "" || len(msg.UserInputMultiContent) == 0 {
		return msg.Content
	}
	var text string
	for _, part := range msg.UserInputMultiContent {
		if part.Type == schema.ChatMessagePartTypeText {
			text += part.Text
		}
	}
	return text
}

func cloneHistoryMessages(messages []*schema.Message) []*schema.Message {
	if messages == nil {
		return nil
	}
	out := make([]*schema.Message, len(messages))
	for i, msg := range messages {
		out[i] = cloneHistoryMessage(msg)
	}
	return out
}

func cloneHistoryMessage(msg *schema.Message) *schema.Message {
	if msg == nil {
		return nil
	}
	out := *msg
	out.MultiContent = append([]schema.ChatMessagePart(nil), msg.MultiContent...)
	out.UserInputMultiContent = append([]schema.MessageInputPart(nil), msg.UserInputMultiContent...)
	out.AssistantGenMultiContent = append([]schema.MessageOutputPart(nil), msg.AssistantGenMultiContent...)
	out.ToolCalls = append([]schema.ToolCall(nil), msg.ToolCalls...)
	if msg.Extra != nil {
		out.Extra = make(map[string]any, len(msg.Extra))
		for k, v := range msg.Extra {
			out.Extra[k] = v
		}
	}
	return &out
}
