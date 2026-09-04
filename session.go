package agentkit

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/cloudwego/eino/schema"
)

var (
	// ErrSessionNotFound 表示会话存储中不存在指定会话。
	ErrSessionNotFound = errors.New("agentkit: session not found")
	// ErrSessionDisabled 表示 Agent 未配置会话存储。
	ErrSessionDisabled = errors.New("agentkit: session persistence is not configured")
)

// SessionConfig 配置 Agent 的自动会话恢复与持久化。
type SessionConfig struct {
	ID    string       // 在存储中唯一且稳定的会话标识
	Store SessionStore // 会话存储
}

// Session 是可持久化的完整对话快照。
type Session struct {
	ID        string            `json:"id"`
	CreatedAt time.Time         `json:"created_at"`
	UpdatedAt time.Time         `json:"updated_at"`
	Messages  []*schema.Message `json:"messages"`
}

// SessionInfo 是用于会话列表展示的轻量元数据。
type SessionInfo struct {
	ID           string    `json:"id"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
	MessageCount int       `json:"message_count"`
}

// SessionStore 管理多个持久化会话。
// Load 在会话不存在时必须返回包装 ErrSessionNotFound 的错误；Delete 必须是幂等的。
// 实现还必须可以安全地被多个 goroutine 调用，并且不得保留调用方传入的可变切片。
type SessionStore interface {
	Load(ctx context.Context, id string) (*Session, error)
	Save(ctx context.Context, session *Session) error
	Delete(ctx context.Context, id string) error
	List(ctx context.Context) ([]SessionInfo, error)
}

// MemorySessionStore 是并发安全的内存会话存储，适合测试和单进程服务。
type MemorySessionStore struct {
	mu       sync.RWMutex
	sessions map[string]*Session
}

var _ SessionStore = (*MemorySessionStore)(nil)

// NewMemorySessionStore 创建内存会话存储。
func NewMemorySessionStore() *MemorySessionStore {
	return &MemorySessionStore{sessions: make(map[string]*Session)}
}

// Load 加载会话快照。
func (s *MemorySessionStore) Load(ctx context.Context, id string) (*Session, error) {
	if err := validateSessionContextAndID(ctx, id); err != nil {
		return nil, err
	}
	s.mu.RLock()
	session, ok := s.sessions[id]
	s.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrSessionNotFound, id)
	}
	return cloneSession(session), nil
}

// Save 保存并完全替换会话快照。
func (s *MemorySessionStore) Save(ctx context.Context, session *Session) error {
	if err := validateSession(ctx, session); err != nil {
		return err
	}
	cloned := normalizedSession(session)
	s.mu.Lock()
	if s.sessions == nil {
		s.sessions = make(map[string]*Session)
	}
	s.sessions[session.ID] = cloned
	s.mu.Unlock()
	return nil
}

// Delete 删除会话。会话不存在时也返回 nil。
func (s *MemorySessionStore) Delete(ctx context.Context, id string) error {
	if err := validateSessionContextAndID(ctx, id); err != nil {
		return err
	}
	s.mu.Lock()
	delete(s.sessions, id)
	s.mu.Unlock()
	return nil
}

// List 按更新时间从新到旧列出会话。
func (s *MemorySessionStore) List(ctx context.Context) ([]SessionInfo, error) {
	if ctx == nil {
		return nil, errors.New("agentkit: context is required")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	s.mu.RLock()
	infos := make([]SessionInfo, 0, len(s.sessions))
	for _, session := range s.sessions {
		infos = append(infos, sessionInfo(session))
	}
	s.mu.RUnlock()
	sortSessionInfos(infos)
	return infos, nil
}

func validateSessionContextAndID(ctx context.Context, id string) error {
	if ctx == nil {
		return errors.New("agentkit: context is required")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if id == "" {
		return errors.New("agentkit: session ID is required")
	}
	return nil
}

func validateSession(ctx context.Context, session *Session) error {
	if session == nil {
		return errors.New("agentkit: session is required")
	}
	if err := validateSessionContextAndID(ctx, session.ID); err != nil {
		return err
	}
	return nil
}

func cloneSession(session *Session) *Session {
	if session == nil {
		return nil
	}
	cloned := *session
	cloned.Messages = cloneHistoryMessages(session.Messages)
	return &cloned
}

func normalizedSession(session *Session) *Session {
	cloned := cloneSession(session)
	now := time.Now().UTC()
	if cloned.CreatedAt.IsZero() {
		cloned.CreatedAt = now
	}
	if cloned.UpdatedAt.IsZero() {
		cloned.UpdatedAt = cloned.CreatedAt
	}
	return cloned
}

func sessionInfo(session *Session) SessionInfo {
	return SessionInfo{
		ID:           session.ID,
		CreatedAt:    session.CreatedAt,
		UpdatedAt:    session.UpdatedAt,
		MessageCount: len(session.Messages),
	}
}

func sortSessionInfos(infos []SessionInfo) {
	sort.SliceStable(infos, func(i, j int) bool {
		if infos[i].UpdatedAt.Equal(infos[j].UpdatedAt) {
			return infos[i].ID < infos[j].ID
		}
		return infos[i].UpdatedAt.After(infos[j].UpdatedAt)
	})
}

func sessionStorageKey(id string) string {
	sum := sha256.Sum256([]byte(id))
	return hex.EncodeToString(sum[:])
}
