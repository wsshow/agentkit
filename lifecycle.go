package agentkit

import (
	"context"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/schema"
)

type agentLifecycleHandler struct {
	*BaseChatModelAgentMiddleware
	agent *Agent
}

func (h *agentLifecycleHandler) BeforeModelRewriteState(ctx context.Context, state *adk.ChatModelAgentState, mc *adk.ModelContext) (context.Context, *adk.ChatModelAgentState, error) {
	h.agent.beginTurn(h.agent.name)
	return ctx, state, nil
}

func (a *Agent) withRunContext(ctx context.Context) context.Context {
	ctx = context.WithValue(ctx, emitterCtxKey{}, a.emtr)
	ctx = context.WithValue(ctx, agentNameCtxKey{}, a.name)
	return context.WithValue(ctx, agentCtxKey{}, a)
}

func (a *Agent) beginTurn(agentName string) {
	if a.inTurn.CompareAndSwap(false, true) {
		a.emtr.Emit(Event{Type: EventTurnStart, Agent: agentName})
	}
}

func (a *Agent) emitInputMessages(messages []Message) {
	if len(messages) == 0 {
		return
	}
	a.beginTurn(a.name)
	for _, message := range messages {
		a.emitMessageStart(a.name, message.Role, message.Content)
		a.emitMessageEnd(a.name, message.Role, message.Content, message.ReasoningContent, nil)
	}
}

func (a *Agent) emitMessageStart(agentName string, role RoleType, content string) {
	a.emtr.Emit(Event{
		Type:    EventMessageStart,
		Agent:   agentName,
		Role:    role,
		Content: content,
	})
}

func (a *Agent) emitMessageEnd(agentName string, role RoleType, content string, reasoning string, meta *schema.ResponseMeta) {
	a.emtr.Emit(Event{
		Type:             EventMessageEnd,
		Agent:            agentName,
		Role:             role,
		Content:          content,
		ReasoningContent: reasoning,
		ResponseMeta:     meta,
	})
}

func (a *Agent) emitToolStart(agentName string, calls []schema.ToolCall) {
	if len(calls) == 0 {
		return
	}
	a.recordToolCalls(calls)
	a.emtr.Emit(Event{Type: EventToolStart, Agent: agentName, ToolCalls: calls})
}

func (a *Agent) recordToolCalls(calls []schema.ToolCall) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.toolCalls == nil {
		a.toolCalls = make(map[string]toolCallInfo)
	}
	for _, call := range calls {
		if call.ID == "" {
			continue
		}
		a.toolCalls[call.ID] = toolCallInfo{
			name:      call.Function.Name,
			arguments: call.Function.Arguments,
		}
	}
}

func (a *Agent) toolCallInfo(callID string) (string, string) {
	if callID == "" {
		return "", ""
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	info := a.toolCalls[callID]
	return info.name, info.arguments
}

func (a *Agent) clearToolCall(callID string) {
	if callID == "" {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	delete(a.toolCalls, callID)
}
