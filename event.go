package agentkit

import (
	"errors"
	"fmt"
	"sort"
	"sync"
)

// ErrSubscriberPanic 表示事件订阅回调发生 panic；其他订阅者会通过 EventError 收到该错误。
var ErrSubscriberPanic = errors.New("agentkit: event subscriber panicked")

// EventType 事件类型
type EventType string

const (
	EventAgentStart      EventType = "agent_start"      // Agent 开始处理
	EventTurnStart       EventType = "turn_start"       // 新一轮开始，发生在模型请求前
	EventMessageStart    EventType = "message_start"    // 消息开始（流式或非流式）
	EventReasoningDelta  EventType = "reasoning_delta"  // 推理模型思考过程增量（如 DeepSeek-R1、o1）
	EventMessageDelta    EventType = "message_delta"    // 流式增量文本
	EventMessageEnd      EventType = "message_end"      // 消息结束
	EventToolStart       EventType = "tool_start"       // 工具调用请求
	EventToolUpdate      EventType = "tool_update"      // 工具执行进度更新
	EventToolEnd         EventType = "tool_end"         // 工具调用结果
	EventTurnEnd         EventType = "turn_end"         // 助手消息和工具结果处理完成
	EventTransfer        EventType = "transfer"         // Agent 转移
	EventInterrupted     EventType = "interrupted"      // HITL 中断（等待用户输入）
	EventCompactionStart EventType = "compaction_start" // 上下文压缩开始
	EventCompactionEnd   EventType = "compaction_end"   // 上下文压缩完成
	EventGoalUpdate      EventType = "goal_update"      // Goal 状态已持久化
	EventDelegationStart EventType = "delegation_start" // 子 Agent 委派开始
	EventDelegationEnd   EventType = "delegation_end"   // 子 Agent 委派结束
	EventAgentEnd        EventType = "agent_end"        // Agent 处理完成
	EventError           EventType = "error"            // 错误
)

// Event 统一事件
type Event struct {
	Type             EventType
	Agent            string           // 产生事件的 Agent 名称
	SessionID        string           // 产生事件的会话 ID；未启用会话时为空
	Role             RoleType         // 消息角色（message_start / message_end）
	Content          string           // 文本内容（message_end / tool_end）
	Delta            string           // 流式增量内容（message_delta / reasoning_delta）
	ReasoningContent string           // 完整推理内容（message_end，仅推理模型）
	ResponseMeta     *ResponseMeta    // 响应元数据：token 用量、完成原因（message_end）
	ToolCalls        []ToolCall       // 工具调用列表（tool_start）
	ToolCallID       string           // 工具调用 ID（tool_update / tool_end）
	ToolName         string           // 工具名称（tool_update / tool_end）
	ToolArguments    string           // 工具调用参数（tool_update / tool_end）
	Interrupt        []InterruptPoint // 中断点列表（interrupted）
	Compaction       *CompactionInfo  // 上下文压缩信息（compaction_start / compaction_end）
	Goal             *Goal            // 已持久化的目标快照（goal_update）
	Delegation       *DelegationInfo  // 子 Agent 委派信息（delegation_start / delegation_end 及子 Agent 事件）
	Error            error            // 错误信息（error）
}

// InterruptPoint HITL 中断点信息
type InterruptPoint struct {
	ID   string // 中断点唯一标识，Resume 时传入此 ID
	Info any    // 中断原因/上下文信息
}

// Subscriber 事件订阅函数
type Subscriber func(Event)

// emitter 事件发射器，支持多订阅者
type emitter struct {
	mu          sync.RWMutex
	subscribers map[int]Subscriber
	nextID      int
}

func newEmitter() *emitter {
	return &emitter{subscribers: make(map[int]Subscriber)}
}

// Subscribe 订阅事件，返回取消订阅函数。
// 回调按订阅顺序同步执行；nil 回调会被忽略。
func (e *emitter) Subscribe(fn Subscriber) func() {
	if fn == nil {
		return func() {}
	}
	e.mu.Lock()
	id := e.nextID
	e.nextID++
	e.subscribers[id] = fn
	e.mu.Unlock()

	return func() {
		e.mu.Lock()
		delete(e.subscribers, id)
		e.mu.Unlock()
	}
}

// Emit 发射事件到所有订阅者
func (e *emitter) Emit(event Event) {
	e.mu.RLock()
	ids := make([]int, 0, len(e.subscribers))
	for id := range e.subscribers {
		ids = append(ids, id)
	}
	sort.Ints(ids)
	subs := make([]Subscriber, 0, len(ids))
	for _, id := range ids {
		subs = append(subs, e.subscribers[id])
	}
	e.mu.RUnlock()

	failed := make(map[int]struct{})
	var panicErrs []error
	for index, fn := range subs {
		if err := invokeSubscriber(fn, event); err != nil {
			failed[index] = struct{}{}
			panicErrs = append(panicErrs, err)
		}
	}
	if len(panicErrs) == 0 {
		return
	}
	diagnostic := Event{
		Type: EventError, Agent: event.Agent, Delegation: cloneDelegation(event.Delegation), Error: errors.Join(panicErrs...),
	}
	for index, fn := range subs {
		if _, panicked := failed[index]; panicked {
			continue
		}
		_ = invokeSubscriber(fn, diagnostic)
	}
}

func invokeSubscriber(fn Subscriber, event Event) (err error) {
	defer func() {
		if value := recover(); value != nil {
			err = fmt.Errorf("%w: %v", ErrSubscriberPanic, value)
		}
	}()
	fn(cloneEvent(event))
	return nil
}

func cloneEvent(event Event) Event {
	out := event
	out.ResponseMeta = cloneResponseMeta(event.ResponseMeta)
	out.ToolCalls = cloneToolCalls(event.ToolCalls)
	out.Interrupt = cloneInterruptPoints(event.Interrupt)
	if event.Compaction != nil {
		compaction := *event.Compaction
		out.Compaction = &compaction
	}
	out.Goal = cloneGoal(event.Goal)
	out.Delegation = cloneDelegation(event.Delegation)
	return out
}

func cloneDelegation(info *DelegationInfo) *DelegationInfo {
	if info == nil {
		return nil
	}
	cloned := cloneDelegationInfo(*info)
	return &cloned
}

func cloneInterruptPoints(points []InterruptPoint) []InterruptPoint {
	return append([]InterruptPoint(nil), points...)
}
