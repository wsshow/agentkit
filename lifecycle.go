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

func (h *agentLifecycleHandler) AfterModelRewriteState(ctx context.Context, state *adk.ChatModelAgentState, mc *adk.ModelContext) (context.Context, *adk.ChatModelAgentState, error) {
	if len(state.Messages) > 0 {
		message := state.Messages[len(state.Messages)-1]
		if message != nil && message.Role == schema.Assistant && len(message.ToolCalls) > 0 {
			h.agent.prepareToolBatch(message.ToolCalls)
		}
	}
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
	var delegations []DelegationInfo
	if a.subAgents != nil {
		delegations = a.subAgents.prepare(calls)
	}
	a.recordToolCalls(calls)
	a.emtr.Emit(Event{Type: EventToolStart, Agent: agentName, ToolCalls: calls})
	for _, info := range delegations {
		info := info
		a.emtr.Emit(Event{Type: EventDelegationStart, Agent: info.Agent, Delegation: &info})
	}
	a.markToolCallsStarted(calls)
}

func (a *Agent) recordToolCalls(calls []schema.ToolCall) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.toolCalls == nil {
		a.toolCalls = make(map[string]toolCallInfo)
	}
	current := make(map[string]struct{}, len(calls))
	for _, call := range calls {
		if call.ID != "" {
			current[call.ID] = struct{}{}
		}
	}
	for callID := range a.toolCalls {
		if _, ok := current[callID]; !ok {
			delete(a.toolCalls, callID)
		}
	}
	for _, call := range calls {
		if call.ID == "" {
			continue
		}
		info := a.toolCalls[call.ID]
		if info.start == nil {
			info.start = make(chan struct{})
		}
		info.name = call.Function.Name
		info.arguments = call.Function.Arguments
		a.toolCalls[call.ID] = info
	}
}

func (a *Agent) prepareToolBatch(calls []schema.ToolCall) {
	a.mu.Lock()
	if a.toolBatchDone == nil || a.toolBatchDoneFlag {
		a.toolBatchDone = make(chan struct{})
		a.toolBatchDoneFlag = false
	}
	a.mu.Unlock()
	if a.subAgents != nil {
		a.subAgents.prepare(calls)
	}
	a.recordToolCalls(calls)
}

func (a *Agent) waitToolBatch(ctx context.Context) error {
	a.mu.Lock()
	for _, info := range a.toolCalls {
		if _, known := a.knownToolNames[info.name]; !known {
			a.mu.Unlock()
			return nil
		}
	}
	if a.toolBatchDone == nil {
		a.toolBatchDone = make(chan struct{})
		a.toolBatchDoneFlag = false
	}
	done := a.toolBatchDone
	a.mu.Unlock()

	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (a *Agent) completeToolBatch() {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.toolBatchDone == nil || a.toolBatchDoneFlag {
		return
	}
	close(a.toolBatchDone)
	a.toolBatchDoneFlag = true
}

func (a *Agent) markToolCallsStarted(calls []schema.ToolCall) {
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, call := range calls {
		info, ok := a.toolCalls[call.ID]
		if !ok || info.started {
			continue
		}
		close(info.start)
		info.started = true
		a.toolCalls[call.ID] = info
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

func (a *Agent) waitToolCallInfo(ctx context.Context, callID string) (string, string, bool) {
	if callID == "" {
		return "", "", true
	}

	a.mu.Lock()
	if a.toolCalls == nil {
		a.toolCalls = make(map[string]toolCallInfo)
	}
	info := a.toolCalls[callID]
	if info.start == nil {
		info.start = make(chan struct{})
		a.toolCalls[callID] = info
	}
	start := info.start
	started := info.started
	a.mu.Unlock()

	if !started {
		select {
		case <-start:
		case <-ctx.Done():
			return "", "", false
		}
	}
	name, arguments := a.toolCallInfo(callID)
	return name, arguments, true
}

func (a *Agent) clearToolCall(callID string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	if callID != "" {
		delete(a.toolCalls, callID)
	}
	return len(a.toolCalls) == 0
}
