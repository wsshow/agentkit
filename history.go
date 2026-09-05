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
	a.pendingInterrupts = nil
	a.runInterrupted = false
	a.mu.Unlock()

	a.state.replaceMessages(stateMessages)
}

func (a *Agent) replaceContextHistory(history []*schema.Message) {
	a.mu.Lock()
	// Eino prepends the effective Agent instruction as a system message for the
	// current run. Middleware state therefore contains one message that is not
	// part of AgentKit's stored context. Persisting it would prepend the same
	// instruction again on every later run and after every session restore.
	if a.runtimeSystemMessage && len(history) > 0 && history[0] != nil && history[0].Role == schema.System {
		history = history[1:]
	}
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
	//lint:ignore SA1019 Preserve Eino's legacy multimodal field in history snapshots.
	out.MultiContent = append([]schema.ChatMessagePart(nil), msg.MultiContent...)
	out.UserInputMultiContent = cloneMessageInputParts(msg.UserInputMultiContent)
	out.AssistantGenMultiContent = cloneMessageOutputParts(msg.AssistantGenMultiContent)
	out.ToolCalls = cloneToolCalls(msg.ToolCalls)
	out.ResponseMeta = cloneResponseMeta(msg.ResponseMeta)
	out.Extra = cloneMap(msg.Extra)
	return &out
}

func cloneMessageInputParts(parts []schema.MessageInputPart) []schema.MessageInputPart {
	if parts == nil {
		return nil
	}
	out := make([]schema.MessageInputPart, len(parts))
	for i, part := range parts {
		out[i] = part
		out[i].Extra = cloneMap(part.Extra)
		if part.Image != nil {
			image := *part.Image
			image.MessagePartCommon = cloneMessagePartCommon(part.Image.MessagePartCommon)
			out[i].Image = &image
		}
		if part.Audio != nil {
			audio := *part.Audio
			audio.MessagePartCommon = cloneMessagePartCommon(part.Audio.MessagePartCommon)
			out[i].Audio = &audio
		}
		if part.Video != nil {
			video := *part.Video
			video.MessagePartCommon = cloneMessagePartCommon(part.Video.MessagePartCommon)
			out[i].Video = &video
		}
		if part.File != nil {
			file := *part.File
			file.MessagePartCommon = cloneMessagePartCommon(part.File.MessagePartCommon)
			out[i].File = &file
		}
		out[i].ToolSearchResult = cloneToolSearchResult(part.ToolSearchResult)
	}
	return out
}

func cloneMessageOutputParts(parts []schema.MessageOutputPart) []schema.MessageOutputPart {
	if parts == nil {
		return nil
	}
	out := make([]schema.MessageOutputPart, len(parts))
	for i, part := range parts {
		out[i] = part
		out[i].Extra = cloneMap(part.Extra)
		if part.Image != nil {
			image := *part.Image
			image.MessagePartCommon = cloneMessagePartCommon(part.Image.MessagePartCommon)
			out[i].Image = &image
		}
		if part.Audio != nil {
			audio := *part.Audio
			audio.MessagePartCommon = cloneMessagePartCommon(part.Audio.MessagePartCommon)
			out[i].Audio = &audio
		}
		if part.Video != nil {
			video := *part.Video
			video.MessagePartCommon = cloneMessagePartCommon(part.Video.MessagePartCommon)
			out[i].Video = &video
		}
		if part.Reasoning != nil {
			reasoning := *part.Reasoning
			out[i].Reasoning = &reasoning
		}
		if part.StreamingMeta != nil {
			meta := *part.StreamingMeta
			out[i].StreamingMeta = &meta
		}
	}
	return out
}

func cloneMessagePartCommon(part schema.MessagePartCommon) schema.MessagePartCommon {
	out := part
	out.URL = cloneStringPointer(part.URL)
	out.Base64Data = cloneStringPointer(part.Base64Data)
	//lint:ignore SA1019 Preserve legacy per-media metadata when cloning Eino messages.
	out.Extra = cloneMap(part.Extra)
	return out
}

func cloneStringPointer(value *string) *string {
	if value == nil {
		return nil
	}
	out := *value
	return &out
}

func cloneToolSearchResult(result *schema.ToolSearchResult) *schema.ToolSearchResult {
	if result == nil {
		return nil
	}
	out := &schema.ToolSearchResult{Tools: make([]*schema.ToolInfo, len(result.Tools))}
	for i, info := range result.Tools {
		if info == nil {
			continue
		}
		cloned := *info
		cloned.Extra = cloneMap(info.Extra)
		out.Tools[i] = &cloned
	}
	return out
}

func cloneToolCalls(calls []schema.ToolCall) []schema.ToolCall {
	if calls == nil {
		return nil
	}
	out := make([]schema.ToolCall, len(calls))
	for i, call := range calls {
		out[i] = call
		if call.Index != nil {
			index := *call.Index
			out[i].Index = &index
		}
		out[i].Extra = cloneMap(call.Extra)
	}
	return out
}

func cloneResponseMeta(meta *schema.ResponseMeta) *schema.ResponseMeta {
	if meta == nil {
		return nil
	}
	out := *meta
	if meta.Usage != nil {
		usage := *meta.Usage
		out.Usage = &usage
	}
	if meta.LogProbs != nil {
		logProbs := &schema.LogProbs{Content: make([]schema.LogProb, len(meta.LogProbs.Content))}
		for i, item := range meta.LogProbs.Content {
			logProbs.Content[i] = item
			logProbs.Content[i].Bytes = append([]int64(nil), item.Bytes...)
			logProbs.Content[i].TopLogProbs = append([]schema.TopLogProb(nil), item.TopLogProbs...)
			for j := range item.TopLogProbs {
				logProbs.Content[i].TopLogProbs[j].Bytes = append([]int64(nil), item.TopLogProbs[j].Bytes...)
			}
		}
		out.LogProbs = logProbs
	}
	return &out
}

func cloneMap(src map[string]any) map[string]any {
	if src == nil {
		return nil
	}
	out := make(map[string]any, len(src))
	for key, value := range src {
		out[key] = cloneMapValue(value)
	}
	return out
}

func cloneMapValue(value any) any {
	switch value := value.(type) {
	case map[string]any:
		return cloneMap(value)
	case []any:
		out := make([]any, len(value))
		for i, item := range value {
			out[i] = cloneMapValue(item)
		}
		return out
	case []byte:
		return append([]byte(nil), value...)
	case []string:
		return append([]string(nil), value...)
	case map[string]string:
		out := make(map[string]string, len(value))
		for key, item := range value {
			out[key] = item
		}
		return out
	default:
		return value
	}
}
