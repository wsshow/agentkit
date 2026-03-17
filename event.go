package agentkit

import (
	"sync"
)

// EventType 事件类型
type EventType string

const (
	EventAgentStart   EventType = "agent_start"   // Agent 开始处理
	EventTurnStart    EventType = "turn_start"    // 新一轮开始（一次 LLM 调用 + 工具执行）
	EventMessageStart EventType = "message_start" // 消息开始（流式或非流式）
	EventMessageDelta EventType = "message_delta" // 流式增量文本
	EventMessageEnd   EventType = "message_end"   // 消息结束
	EventToolStart    EventType = "tool_start"    // 工具调用请求
	EventToolUpdate   EventType = "tool_update"   // 工具执行进度更新
	EventToolEnd      EventType = "tool_end"      // 工具调用结果
	EventTurnEnd      EventType = "turn_end"      // 一轮结束
	EventTransfer     EventType = "transfer"      // Agent 转移
	EventInterrupted  EventType = "interrupted"   // HITL 中断（等待用户输入）
	EventAgentEnd     EventType = "agent_end"     // Agent 处理完成
	EventError        EventType = "error"         // 错误
)

// Event 统一事件
type Event struct {
	Type      EventType
	Agent     string           // 产生事件的 Agent 名称
	Content   string           // 文本内容（message_end / tool_end）
	Delta     string           // 流式增量内容（message_delta）
	ToolCalls []ToolCall       // 工具调用列表（tool_start）
	Interrupt []InterruptPoint // 中断点列表（interrupted）
	Error     error            // 错误信息（error）
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

// Subscribe 订阅事件，返回取消订阅函数
func (e *emitter) Subscribe(fn Subscriber) func() {
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
	subs := make([]Subscriber, 0, len(e.subscribers))
	for _, fn := range e.subscribers {
		subs = append(subs, fn)
	}
	e.mu.RUnlock()

	for _, fn := range subs {
		fn(event)
	}
}
