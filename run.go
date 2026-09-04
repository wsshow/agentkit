package agentkit

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/compose"
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
	if err := a.SaveSession(context.WithoutCancel(ctx)); err != nil {
		a.emtr.Emit(Event{Type: EventError, Agent: a.name, Error: err})
		return errors.Join(runErr, err)
	}
	return runErr
}

// processQueues 处理 steering/follow-up 队列
func (a *Agent) processQueues(ctx context.Context, err error) error {
	for err == nil {
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
	iter := a.runner.Run(
		parentCtx,
		history,
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
	return a.consumeIter(parentCtx, iter)
}

// executeResume 执行 HITL 恢复
func (a *Agent) executeResume(parentCtx context.Context, targets map[string]any) error {
	cancelOpt, cancelAgent := adk.WithCancel()
	iter, err := a.runner.ResumeWithParams(parentCtx, a.checkPointID, &adk.ResumeParams{
		Targets: targets,
	},
		cancelOpt,
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
	if err != nil {
		return err
	}
	return a.consumeIter(parentCtx, iter)
}

// consumeIter 消费事件迭代器，处理事件。
func (a *Agent) consumeIter(parentCtx context.Context, iter *adk.AsyncIterator[*adk.AgentEvent]) error {
	ctx, cancel := context.WithCancel(parentCtx)
	a.mu.Lock()
	a.cancelFn = cancel
	a.mu.Unlock()

	defer func() {
		cancel()
		a.mu.Lock()
		a.cancelFn = nil
		a.mu.Unlock()
	}()

	var lastErr error
	for {
		select {
		case <-ctx.Done():
			a.endTurn()
			a.emtr.Emit(Event{Type: EventError, Agent: a.name, Error: ctx.Err()})
			return ctx.Err()
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

		if err := a.processEvent(ctx, event); err != nil {
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
		a.emtr.Emit(Event{
			Type:      EventInterrupted,
			Agent:     agentName,
			Interrupt: points,
		})
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
		a.emtr.Emit(Event{
			Type:          EventToolEnd,
			Agent:         agentName,
			Content:       msg.Content,
			ToolCallID:    msg.ToolCallID,
			ToolName:      toolName,
			ToolArguments: arguments,
		})
		a.emitMessageStart(agentName, RoleTool, msg.Content)
		a.emitMessageEnd(agentName, RoleTool, msg.Content, "", nil)
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

// inMemoryStore 内存 CheckPoint 存储
type inMemoryStore struct {
	mu  sync.Mutex
	mem map[string][]byte
}

func newInMemoryStore() compose.CheckPointStore {
	return &inMemoryStore{mem: map[string][]byte{}}
}

func (s *inMemoryStore) Set(_ context.Context, key string, value []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.mem[key] = value
	return nil
}

func (s *inMemoryStore) Get(_ context.Context, key string) ([]byte, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.mem[key]
	return v, ok, nil
}
