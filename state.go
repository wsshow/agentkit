package agentkit

import "sync"

// RoleType 消息角色类型
type RoleType string

const (
	RoleUser      RoleType = "user"
	RoleAssistant RoleType = "assistant"
	RoleTool      RoleType = "tool"
)

// Message 消息记录
type Message struct {
	Role    RoleType
	Agent   string // 产生消息的 Agent 名称
	Content string
}

// State Agent 状态管理（线程安全）
type State struct {
	mu        sync.RWMutex
	messages  []Message
	streaming bool
}

func newState() *State {
	return &State{}
}

// AddMessage 添加消息
func (s *State) AddMessage(msg Message) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.messages = append(s.messages, msg)
}

// Messages 获取所有消息的副本
func (s *State) Messages() []Message {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Message, len(s.messages))
	copy(out, s.messages)
	return out
}

// IsStreaming 是否正在流式输出
func (s *State) IsStreaming() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.streaming
}

func (s *State) setStreaming(v bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.streaming = v
}

// Clear 清空状态
func (s *State) Clear() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.messages = nil
	s.streaming = false
}
