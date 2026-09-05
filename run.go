package agentkit

import (
	"context"
	"errors"
	"io"
	"strings"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/schema"
)

// run 外层执行，包含 steering/follow-up 队列循环
func (a *Agent) run(ctx context.Context, inputs []Message) error {
	ctx = a.withRunContext(ctx)

	a.emtr.Emit(Event{Type: EventAgentStart, Agent: a.name})
	a.emitInputMessages(inputs)

	err := a.executeLoop(ctx)
	err = a.processQueues(ctx, err)
	err = a.persistSession(ctx, err)

	a.emtr.Emit(Event{Type: EventAgentEnd, Agent: a.name})
	return err
}

func (a *Agent) persistSession(ctx context.Context, runErr error) error {
	if a.sessionStore == nil {
		return runErr
	}
	persistCtx, cancel := a.persistenceContext(ctx)
	defer cancel()
	if err := a.saveSession(persistCtx, nil, true); err != nil {
		a.emtr.Emit(Event{Type: EventError, Agent: a.name, Error: err})
		return errors.Join(runErr, err)
	}
	return runErr
}

// processQueues 处理 steering/follow-up 队列
func (a *Agent) processQueues(ctx context.Context, err error) error {
	for err == nil && !a.wasInterrupted() {
		if msgs := a.drainSteering(); len(msgs) > 0 {
			for _, m := range msgs {
				a.state.AddMessage(m)
				a.appendHistory(schema.UserMessage(m.Content))
			}
			a.emitInputMessages(msgs)
			err = a.executeLoop(ctx)
			continue
		}
		if msgs := a.drainFollowUp(); len(msgs) > 0 {
			for _, m := range msgs {
				a.state.AddMessage(m)
				a.appendHistory(schema.UserMessage(m.Content))
			}
			a.emitInputMessages(msgs)
			err = a.executeLoop(ctx)
			continue
		}
		break
	}
	return err
}

// executeLoop 执行一次 runner.Run，消费事件流。
func (a *Agent) executeLoop(parentCtx context.Context) error {
	a.mu.Lock()
	history := cloneHistoryMessages(a.contextHistory)
	a.mu.Unlock()

	cancelOpt, cancelAgent := adk.WithCancel()
	runOptions := agentRunOptions(parentCtx)
	runOptions = append(runOptions,
		cancelOpt,
		adk.WithCheckPointID(a.checkPointID),
		adk.WithAfterToolCallsHook(func(ctx context.Context) error {
			if err := a.waitToolBatch(ctx); err != nil {
				return err
			}
			if a.hasSteering() {
				cancelAgent(adk.WithAgentCancelMode(adk.CancelAfterToolCalls))
			}
			return nil
		}),
	)
	iter := a.runner.Run(
		parentCtx,
		history,
		runOptions...,
	)
	return a.consumeIter(parentCtx, iter)
}

// executeResume 执行 HITL 恢复
func (a *Agent) executeResume(parentCtx context.Context, targets map[string]any) error {
	a.mu.Lock()
	waitForTrackedTools := len(a.toolCalls) > 0
	a.mu.Unlock()

	cancelOpt, cancelAgent := adk.WithCancel()
	runOptions := agentRunOptions(parentCtx)
	runOptions = append(runOptions,
		cancelOpt,
		adk.WithAfterToolCallsHook(func(ctx context.Context) error {
			// A recreated Agent has no in-memory batch barrier from the interrupted
			// process. Eino publishes its resumed tool results after this hook.
			if waitForTrackedTools {
				if err := a.waitToolBatch(ctx); err != nil {
					return err
				}
			}
			if a.hasSteering() {
				cancelAgent(adk.WithAgentCancelMode(adk.CancelAfterToolCalls))
			}
			return nil
		}),
	)
	iter, err := a.runner.ResumeWithParams(parentCtx, a.checkPointID, &adk.ResumeParams{
		Targets: targets,
	},
		runOptions...,
	)
	if err != nil {
		return err
	}
	return a.consumeIter(parentCtx, iter)
}

// consumeIter 消费事件迭代器，处理事件。
func (a *Agent) consumeIter(parentCtx context.Context, iter *adk.AsyncIterator[*adk.AgentEvent]) error {
	var lastErr error
	for {
		select {
		case <-parentCtx.Done():
			a.endTurn()
			a.emtr.Emit(Event{Type: EventError, Agent: a.name, Error: parentCtx.Err()})
			return parentCtx.Err()
		default:
		}

		event, ok := iter.Next()
		if !ok {
			break
		}

		if event.Err != nil {
			if a.isSteeringCancel(event.Err) {
				a.endTurn()
				return nil
			}
			lastErr = event.Err
		}

		if err := a.processEvent(parentCtx, event); err != nil {
			lastErr = err
		}
	}

	a.endTurn()
	return lastErr
}

func (a *Agent) isSteeringCancel(err error) bool {
	if !a.hasSteering() {
		return false
	}
	var cancelErr *adk.CancelError
	return errors.As(err, &cancelErr) && cancelErr.Info != nil && cancelErr.Info.Mode&adk.CancelAfterToolCalls != 0
}

func (a *Agent) hasSteering() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.steeringQueue) > 0
}

// processEvent 将底层事件转换为统一事件。
func (a *Agent) processEvent(ctx context.Context, event *adk.AgentEvent) error {
	if event == nil {
		return nil
	}
	if a.subAgents != nil && a.subAgents.hasAgent(event.AgentName) {
		return a.processSubAgentEvent(ctx, event)
	}
	agentName := event.AgentName

	if event.Err != nil {
		a.emtr.Emit(Event{Type: EventError, Agent: agentName, Error: event.Err})
		return nil
	}

	if event.Action != nil {
		a.processAction(agentName, event.Action)
	}

	if event.Output != nil && event.Output.MessageOutput != nil {
		mo := event.Output.MessageOutput
		if mo.Role == schema.Assistant {
			a.beginTurn(agentName)
		}
		return a.processOutput(ctx, agentName, mo)
	}

	return nil
}

func (a *Agent) processAction(agentName string, action *adk.AgentAction) {
	if action.CustomizedAction != nil {
		a.processCompactionAction(agentName, action.CustomizedAction)
	}
	if action.TransferToAgent != nil {
		a.emtr.Emit(Event{
			Type:    EventTransfer,
			Agent:   agentName,
			Content: action.TransferToAgent.DestAgentName,
		})
	}
	if action.Interrupted != nil {
		var points []InterruptPoint
		for _, ic := range action.Interrupted.InterruptContexts {
			points = append(points, InterruptPoint{ID: ic.ID, Info: ic.Info})
		}
		a.markInterrupted(points)
		event := Event{
			Type:      EventInterrupted,
			Agent:     agentName,
			Interrupt: points,
		}
		if a.subAgents != nil {
			event.Delegation = a.subAgents.onlyActiveDelegation()
		}
		a.emtr.Emit(event)
	}
}

func (a *Agent) processOutput(ctx context.Context, agentName string, output *adk.MessageVariant) error {
	if output.IsStreaming && output.MessageStream != nil {
		return a.processStream(ctx, agentName, output.MessageStream)
	} else if output.Message != nil {
		a.processMessage(agentName, output.Message)
	}
	return nil
}

// processMessage 处理完整消息
func (a *Agent) processMessage(agentName string, msg adk.Message) {
	a.appendHistory(msg)

	if msg.Role == schema.Tool {
		toolName, arguments := a.toolCallInfo(msg.ToolCallID)
		if msg.ToolName != "" {
			toolName = msg.ToolName
		}
		var delegation *DelegationInfo
		var delegationErr error
		if a.subAgents != nil {
			delegation, delegationErr, _ = a.subAgents.finish(msg.ToolCallID)
		}
		a.emtr.Emit(Event{
			Type:          EventToolEnd,
			Agent:         agentName,
			Content:       msg.Content,
			ToolCallID:    msg.ToolCallID,
			ToolName:      toolName,
			ToolArguments: arguments,
			Delegation:    delegation,
		})
		a.emitMessageStart(agentName, RoleTool, msg.Content)
		a.emitMessageEnd(agentName, RoleTool, msg.Content, "", nil)
		if delegation != nil {
			a.emtr.Emit(Event{
				Type:       EventDelegationEnd,
				Agent:      delegation.Agent,
				Delegation: delegation,
				Error:      delegationErr,
			})
		}
		if a.clearToolCall(msg.ToolCallID) {
			a.endTurn()
			a.completeToolBatch()
		}
		return
	}

	a.emitMessageStart(agentName, RoleAssistant, msg.Content)

	if msg.Content != "" || msg.ReasoningContent != "" {
		a.state.AddMessage(Message{
			Role:             RoleType(msg.Role),
			Agent:            agentName,
			Content:          msg.Content,
			ReasoningContent: msg.ReasoningContent,
		})
	}

	a.emitMessageEnd(agentName, RoleAssistant, msg.Content, msg.ReasoningContent, msg.ResponseMeta)
	a.emitToolStart(agentName, msg.ToolCalls)
}

// processStream 处理流式消息，返回流式传输过程中的错误
func (a *Agent) processStream(ctx context.Context, agentName string, stream adk.MessageStream) error {
	a.state.setStreaming(true)
	a.emitMessageStart(agentName, RoleAssistant, "")

	defer a.state.setStreaming(false)

	var fullContent strings.Builder
	var reasoningContent strings.Builder
	var toolCalls []schema.ToolCall
	var resMeta *schema.ResponseMeta

	for {
		select {
		case <-ctx.Done():
			a.emtr.Emit(Event{Type: EventError, Agent: agentName, Error: ctx.Err()})
			return ctx.Err()
		default:
		}

		chunk, err := stream.Recv()
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			a.emtr.Emit(Event{Type: EventError, Agent: agentName, Error: err})
			return err
		}

		if chunk == nil {
			continue
		}

		if chunk.ReasoningContent != "" {
			reasoningContent.WriteString(chunk.ReasoningContent)
			a.emtr.Emit(Event{Type: EventReasoningDelta, Agent: agentName, Delta: chunk.ReasoningContent})
		}

		if chunk.Content != "" {
			fullContent.WriteString(chunk.Content)
			a.emtr.Emit(Event{Type: EventMessageDelta, Agent: agentName, Delta: chunk.Content})
		}

		if chunk.ResponseMeta != nil {
			resMeta = chunk.ResponseMeta
		}

		if len(chunk.ToolCalls) > 0 {
			toolCalls = mergeToolCalls(toolCalls, chunk.ToolCalls)
		}
	}

	content := fullContent.String()
	reasoning := reasoningContent.String()

	if content != "" || reasoning != "" {
		a.state.AddMessage(Message{
			Role:             RoleAssistant,
			Agent:            agentName,
			Content:          content,
			ReasoningContent: reasoning,
		})
	}

	a.appendHistory(&schema.Message{
		Role:             schema.Assistant,
		Content:          content,
		ToolCalls:        toolCalls,
		ReasoningContent: reasoning,
		ResponseMeta:     resMeta,
	})

	a.emitMessageEnd(agentName, RoleAssistant, content, reasoning, resMeta)
	a.emitToolStart(agentName, toolCalls)
	return nil
}

// mergeToolCalls 合并流式工具调用分片
func mergeToolCalls(existing []schema.ToolCall, chunks []schema.ToolCall) []schema.ToolCall {
	for _, chunk := range chunks {
		idx := -1
		if chunk.Index != nil {
			idx = *chunk.Index
		}

		found := false
		for i := range existing {
			if existing[i].Index != nil && *existing[i].Index == idx && idx >= 0 {
				existing[i].Function.Arguments += chunk.Function.Arguments
				if chunk.ID != "" {
					existing[i].ID = chunk.ID
				}
				if chunk.Function.Name != "" {
					existing[i].Function.Name = chunk.Function.Name
				}
				found = true
				break
			}
		}

		if !found {
			existing = append(existing, chunk)
		}
	}
	return existing
}

// endTurn 结束当前 turn（如果有）
func (a *Agent) endTurn() {
	if a.inTurn.CompareAndSwap(true, false) {
		a.emtr.Emit(Event{Type: EventTurnEnd, Agent: a.name})
	}
}
